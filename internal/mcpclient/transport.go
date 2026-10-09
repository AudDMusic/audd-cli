package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/output"
)

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

var scopeRe = regexp.MustCompile(`scope="([^"]*)"`)

// post sends one JSON-RPC message. id < 0 sends a notification.
func (c *Client) post(ctx context.Context, token, sid, proto string, id int, method string, params any) (*rpcResponse, http.Header, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	if id >= 0 {
		msg["id"] = id
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", c.ua)
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	if proto != "" {
		req.Header.Set("MCP-Protocol-Version", proto)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, &output.Error{Code: "network", Message: "cannot reach the AudD account service: " + err.Error(),
			Hint: "check your connection and try again", Exit: output.ExitNetwork, Retryable: true}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, nil, errUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		scope := ""
		if m := scopeRe.FindStringSubmatch(resp.Header.Get("WWW-Authenticate")); m != nil {
			scope = m[1]
		}
		return nil, nil, scopeMissing(toolName(method, params), scope)
	case resp.StatusCode == http.StatusNotFound && sid != "":
		return nil, nil, errSessionGone
	case resp.StatusCode == http.StatusAccepted && id < 0:
		return nil, resp.Header, nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return nil, nil, &output.Error{Code: "server", Message: fmt.Sprintf("the AudD account service returned HTTP %d", resp.StatusCode),
			Hint: "try again in a minute", Exit: output.ExitNetwork, Retryable: true}
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var r rpcResponse
		if json.Unmarshal(b, &r) == nil && r.Error != nil && sid != "" && isSessionError(r.Error) {
			return nil, nil, errSessionGone
		}
		return nil, nil, unexpected("the AudD account service returned HTTP %d", resp.StatusCode)
	}
	if id < 0 {
		return nil, resp.Header, nil
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	var r *rpcResponse
	if ct == "text/event-stream" {
		r, err = readSSE(resp.Body, id)
	} else {
		r = &rpcResponse{}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(r)
	}
	if err != nil {
		return nil, nil, unexpected("cannot read the AudD account service response: %v", err)
	}
	return r, resp.Header, nil
}

func toolName(method string, params any) string {
	if method != "tools/call" {
		return ""
	}
	if m, ok := params.(map[string]any); ok {
		if s, ok := m["name"].(string); ok {
			return s
		}
	}
	return ""
}

func isSessionError(e *rpcError) bool {
	return strings.Contains(strings.ToLower(e.Message), "session")
}

// readSSE reads server-sent events until the response with id arrives.
func readSSE(r io.Reader, id int) (*rpcResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var data []string
	want := fmt.Sprint(id)
	flush := func() (*rpcResponse, bool) {
		if len(data) == 0 {
			return nil, false
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		var m rpcResponse
		if json.Unmarshal([]byte(payload), &m) != nil {
			return nil, false
		}
		if strings.TrimSpace(string(m.ID)) == want && (m.Result != nil || m.Error != nil) {
			return &m, true
		}
		return nil, false
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if m, ok := flush(); ok {
				return m, nil
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	if m, ok := flush(); ok {
		return m, nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("the event stream ended without a response")
}
