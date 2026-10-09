package cli_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cli"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// mcpClient starts `audd mcp` in-process with args and connects a client.
func mcpClient(t *testing.T, args ...string) *mcp.ClientSession {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		code := cli.Run(ctx, append([]string{"mcp"}, args...), cli.IO{In: inR, Out: outW, Err: &stderr})
		outW.Close()
		done <- code
	}()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		inW.Close()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("audd mcp exited %d: %s", code, stderr.String())
			}
		case <-time.After(5 * time.Second):
			t.Error("audd mcp did not stop when the client disconnected")
		}
		cancel()
	})
	return cs
}

func toolText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMCPServerEndToEnd(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	song := audio(t, dir, "song.mp3", "audio bytes")
	cs := mcpClient(t)
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) < 10 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	if cs.InitializeResult().ServerInfo.Version != app.Version {
		t.Fatalf("server version %q", cs.InitializeResult().ServerInfo.Version)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "recognize", Arguments: map[string]any{"path": song}})
	if err != nil || res.IsError {
		t.Fatalf("recognize: %v %s", err, toolText(res))
	}
	doc := decode(t, toolText(res))
	result, _ := doc["result"].(map[string]any)
	if doc["schema_version"] != float64(1) || result == nil || result["artist"] == nil || doc["cached"] != false {
		t.Fatalf("recognize result: %v", doc)
	}
	if len(f.Requests()) != 1 {
		t.Fatalf("requests: %d", len(f.Requests()))
	}
	// Same file again: the shared cache answers.
	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "recognize", Arguments: map[string]any{"path": song}})
	if decode(t, toolText(res))["cached"] != true || len(f.Requests()) != 1 {
		t.Fatalf("second call should be cached: %s", toolText(res))
	}

	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "recognize_enterprise", Arguments: map[string]any{"path": song}})
	if !res.IsError || !strings.Contains(toolText(res), "limit_required") || len(f.Requests()) != 1 {
		t.Fatalf("enterprise without limit: %s", toolText(res))
	}

	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "docs", Arguments: map[string]any{"topic": "cli"}})
	if res.IsError || toolText(res) != agent.CLIDoc {
		t.Fatal("docs cli")
	}
}

func TestMCPServerReportsCommandErrors(t *testing.T) {
	testutil.Isolate(t)
	testutil.NewFakeAPI(t)
	cs := mcpClient(t, "--max-requests", "3")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "recognize", Arguments: map[string]any{"url": "https://audd.tech/example.mp3"}})
	if err != nil || !res.IsError || !strings.Contains(toolText(res), "no_token") || !strings.Contains(toolText(res), "exit code 3") {
		t.Fatalf("no token: %v %s", err, toolText(res))
	}
	// Account tools never start a browser sign-in from the server.
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "usage"})
	if !res.IsError || !strings.Contains(toolText(res), "audd login") {
		t.Fatalf("usage: %s", toolText(res))
	}
}

func TestMCPPaymentLinkNeverPromptsForConsent(t *testing.T) {
	as, fm := accountEnv(t)
	// The user unticked payment links at sign-in.
	loginUnticked(t, as, "billing:pay")
	before := as.Snapshot()
	r := run(t, "billing", "renew", "--no-consent-prompt")
	wantExit(t, r, output.ExitAuth, "approval_required")
	after := as.Snapshot()
	if !strings.Contains(r.Stderr, `"hint":"audd billing renew"`) || after.Authorize != before.Authorize || after.DeviceAuthorize != before.DeviceAuthorize || len(fm.CallsTo("create_renewal_payment")) != 0 {
		t.Fatalf("no consent: %s", r.Stderr)
	}
	// Once payment access is approved, the link comes back.
	mustRun(t, "billing", "renew")
	m := decode(t, mustRun(t, "billing", "renew", "--no-consent-prompt").Stdout)
	if m["url"] == nil || m["charged"] != false {
		t.Fatalf("%v", m)
	}
}

func TestMCPMaxRequestsCoversTheSession(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	a := audio(t, dir, "a.mp3", "a")
	b := audio(t, dir, "b.mp3", "b")
	c := audio(t, dir, "c.mp3", "c")
	cs := mcpClient(t, "--max-requests", "2")
	ctx := context.Background()
	call := func(path string) *mcp.CallToolResult {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "recognize", Arguments: map[string]any{"path": path}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, p := range []string{a, a, b} { // the second a is cached and free
		if res := call(p); res.IsError {
			t.Fatalf("%s: %s", p, toolText(res))
		}
	}
	res := call(c)
	if !res.IsError || !strings.Contains(toolText(res), "max_requests_reached") || len(f.Requests()) != 2 {
		t.Fatalf("third billed call: %s (%d requests)", toolText(res), len(f.Requests()))
	}
}
