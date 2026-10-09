package testutil

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// FakeTool is one tool served by FakeMCP.
type FakeTool struct {
	Name         string
	Scope        string // tool is listed and callable only with this scope
	InputSchema  map[string]any
	OutputSchema map[string]any
	// Handle returns structuredContent (nil for text-only), text, and isError.
	Handle func(args map[string]any) (structured map[string]any, text string, isError bool)
}

// FakeMCPCall records one tools/call.
type FakeMCPCall struct {
	Tool string
	Args map[string]any
}

// FakeMCP is a Streamable HTTP MCP server for tests. It checks bearer
// tokens against a FakeOAuth, issues Mcp-Session-Id on initialize, rejects
// unknown sessions with 404, scopes tools like the AudD MCP server, and can
// answer as JSON or as an SSE stream.
type FakeMCP struct {
	Server *httptest.Server
	OAuth  *FakeOAuth

	mu       sync.Mutex
	tools    map[string]*FakeTool
	order    []string
	sessions map[string]bool
	seq      int
	SSE      bool // answer requests as text/event-stream
	Calls    []FakeMCPCall
	Inits    int
	Lists    int
}

// NewFakeMCP starts a fake MCP server using oauth for tokens, with the
// default AudD account tools (see DefaultFakeTools).
func NewFakeMCP(t testing.TB, oauth *FakeOAuth) *FakeMCP {
	f := &FakeMCP{OAuth: oauth, tools: map[string]*FakeTool{}, sessions: map[string]bool{}}
	for _, tool := range DefaultFakeTools() {
		f.SetTool(tool)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"resource":              f.Server.URL,
			"authorization_servers": []string{oauth.URL()},
		})
	})
	mux.HandleFunc("/", f.handle)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the MCP endpoint (and protected resource identifier).
func (f *FakeMCP) URL() string { return f.Server.URL }

// SetTool adds or replaces a tool.
func (f *FakeMCP) SetTool(tool FakeTool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tools[tool.Name]; !ok {
		f.order = append(f.order, tool.Name)
	}
	t := tool
	f.tools[tool.Name] = &t
}

// ExpireSessions forgets every session, as a server restart would.
func (f *FakeMCP) ExpireSessions() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = map[string]bool{}
}

// SetSSE switches between JSON and SSE responses.
func (f *FakeMCP) SetSSE(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SSE = on
}

// CallsTo returns the recorded calls to a tool.
func (f *FakeMCP) CallsTo(name string) []FakeMCPCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []FakeMCPCall
	for _, c := range f.Calls {
		if c.Tool == name {
			out = append(out, c)
		}
	}
	return out
}

// Stats returns the number of initialize and tools/list requests.
func (f *FakeMCP) Stats() (inits, lists int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Inits, f.Lists
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (f *FakeMCP) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	auth := r.Header.Get("Authorization")
	scopes, ok := f.OAuth.AccessScopes(strings.TrimPrefix(auth, "Bearer "))
	if !strings.HasPrefix(auth, "Bearer ") || !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="AudD", resource_metadata="`+f.Server.URL+`/.well-known/oauth-protected-resource"`)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var req rpcReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32700, "message": "parse error"}})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Method == "initialize" {
		f.Inits++
		f.seq++
		sid := fmt.Sprintf("session-%d", f.seq)
		f.sessions[sid] = true
		w.Header().Set("Mcp-Session-Id", sid)
		f.reply(w, req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake-audd", "version": "1"},
		}, nil)
		return
	}
	if !f.sessions[r.Header.Get("Mcp-Session-Id")] {
		writeJSON(w, http.StatusNotFound, map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32001, "message": "session not found"}})
		return
	}
	has := func(s string) bool {
		for _, x := range scopes {
			if x == s {
				return true
			}
		}
		return false
	}
	switch req.Method {
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		f.Lists++
		var list []map[string]any
		for _, n := range f.order {
			t := f.tools[n]
			if t.Scope != "" && !has(t.Scope) {
				continue
			}
			m := map[string]any{"name": t.Name, "description": t.Name, "inputSchema": t.InputSchema}
			if t.OutputSchema != nil {
				m["outputSchema"] = t.OutputSchema
			}
			list = append(list, m)
		}
		f.reply(w, req.ID, map[string]any{"tools": list}, nil)
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		t, ok := f.tools[p.Name]
		if !ok || (t.Scope != "" && !has(t.Scope)) {
			f.reply(w, req.ID, nil, map[string]any{"code": -32602, "message": "Unknown tool: " + p.Name})
			return
		}
		f.Calls = append(f.Calls, FakeMCPCall{Tool: p.Name, Args: p.Arguments})
		structured, text, isErr := t.Handle(p.Arguments)
		if text == "" && structured != nil {
			b, _ := json.Marshal(structured)
			text = string(b)
		}
		res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		if structured != nil {
			res["structuredContent"] = structured
		}
		if isErr {
			res["isError"] = true
		}
		f.reply(w, req.ID, res, nil)
	default:
		f.reply(w, req.ID, nil, map[string]any{"code": -32601, "message": "method not found"})
	}
}

func (f *FakeMCP) reply(w http.ResponseWriter, id json.RawMessage, result any, rpcErr map[string]any) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	if f.SSE {
		b, _ := json.Marshal(msg)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, ": keep-alive\n\nevent: message\ndata: %s\n\n", b)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

// FakeAPIToken is the account API token the fake MCP server hands out.
const FakeAPIToken = PlaceholderToken

// FakeRotatedToken is the token rotate_api_token returns: the other allowed
// placeholder, so tests can tell the old and new tokens apart.
const FakeRotatedToken = "your-api-token"

// The fixtures in testdata/mcp are responses recorded from the live AudD MCP
// server (personal details replaced): tools_list.json is its tools/list,
// and <tool>.json is one tools/call response per read-only tool.
//
//go:embed testdata/mcp/*.json
var liveFixtures embed.FS

// LiveFixture returns the structuredContent of a recorded tools/call
// response, freshly decoded (callers may change it).
func LiveFixture(tool string) map[string]any {
	b, err := liveFixtures.ReadFile("testdata/mcp/" + tool + ".json")
	if err != nil {
		panic(err)
	}
	var resp struct {
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		panic(err)
	}
	return resp.Result.StructuredContent
}

type liveTool struct {
	Name         string         `json:"name"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
}

func liveTools() map[string]liveTool {
	b, err := liveFixtures.ReadFile("testdata/mcp/tools_list.json")
	if err != nil {
		panic(err)
	}
	var list []liveTool
	if err := json.Unmarshal(b, &list); err != nil {
		panic(err)
	}
	out := map[string]liveTool{}
	for _, t := range list {
		out[t.Name] = t
	}
	return out
}

// LiveInputSchemas and FakeOutputSchemas are the account tools' schemas as
// the live server publishes them. rotate_api_token was not in the recording
// (it is listed only with token:write); it is assumed to answer like
// get_api_token.
var LiveInputSchemas, FakeOutputSchemas = func() (in, out map[string]map[string]any) {
	in, out = map[string]map[string]any{}, map[string]map[string]any{}
	for name, t := range liveTools() {
		in[name], out[name] = t.InputSchema, t.OutputSchema
	}
	in["rotate_api_token"] = in["get_api_token"]
	out["rotate_api_token"] = out["get_api_token"]
	return in, out
}()

func static(m map[string]any) func(map[string]any) (map[string]any, string, bool) {
	return func(map[string]any) (map[string]any, string, bool) { return m, "", false }
}

func recorded(tool string) func(map[string]any) (map[string]any, string, bool) {
	return func(map[string]any) (map[string]any, string, bool) { return LiveFixture(tool), "", false }
}

// FakePaymentURL is the link the fake payment tools return.
const FakePaymentURL = "https://checkout.stripe.com/c/pay/cs_test_example"

// DefaultFakeTools returns the AudD account tools with the live schemas and
// the recorded results.
func DefaultFakeTools() []FakeTool {
	in, out := LiveInputSchemas, FakeOutputSchemas
	tool := func(name, scope string, h func(map[string]any) (map[string]any, string, bool)) FakeTool {
		return FakeTool{Name: name, Scope: scope, InputSchema: in[name], OutputSchema: out[name], Handle: h}
	}
	link := func(cents int, extra map[string]any) map[string]any {
		m := map[string]any{"payment_url": FakePaymentURL, "amount_cents": cents, "human_approval_required": true}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	return []FakeTool{
		tool("get_profile", "profile:read", recorded("get_profile")),
		tool("get_account_status", "account:read", recorded("get_account_status")),
		tool("get_usage_stats", "usage:read", func(args map[string]any) (map[string]any, string, bool) {
			m := LiveFixture("get_usage_stats")
			days, _ := m["daily"].([]any)
			if n, ok := args["days"].(float64); ok && int(n) < len(days) {
				m["daily"] = days[len(days)-int(n):]
			}
			return m, "", false
		}),
		tool("get_api_token", "token:read", static(map[string]any{"api_token": FakeAPIToken})),
		tool("rotate_api_token", "token:write", static(map[string]any{"api_token": FakeRotatedToken})),
		tool("list_plans", "billing:read", recorded("list_plans")),
		tool("get_billing_history", "billing:read", recorded("get_billing_history")),
		tool("get_amount_owed", "billing:read", recorded("get_amount_owed")),
		tool("subscribe_to_plan", "billing:pay", func(args map[string]any) (map[string]any, string, bool) {
			for _, p := range LiveFixture("list_plans")["plans"].([]any) {
				p := p.(map[string]any)
				if p["plan"] == args["plan"] {
					return link(int(p["monthly_price_cents"].(float64)), nil), "", false
				}
			}
			return nil, fmt.Sprintf("Unknown plan: %v", args["plan"]), true
		}),
		tool("create_renewal_payment", "billing:pay", static(link(500, map[string]any{"includes_extras_cents": 0}))),
		tool("buy_bonus_requests", "billing:pay", func(args map[string]any) (map[string]any, string, bool) {
			n, _ := args["requests"].(float64)
			return link(int(n)/2, map[string]any{"requests": int(n)}), "", false
		}),
		tool("get_api_docs", "", func(args map[string]any) (map[string]any, string, bool) {
			topic, _ := args["topic"].(string)
			if topic == "" {
				topic = "api"
			}
			return map[string]any{"topic": topic, "documentation_markdown": "# " + topic + "\n\nDocs."}, "", false
		}),
	}
}
