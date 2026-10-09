package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeRunner records the commands the tools run and answers from a table.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	reply func(args []string) Result
}

func (f *fakeRunner) run(ctx context.Context, args []string) Result {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
	if f.reply != nil {
		return f.reply(args)
	}
	return Result{Stdout: []byte(`{"schema_version":1,"ok":true}`)}
}

func (f *fakeRunner) last() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

func connect(t *testing.T, f *fakeRunner) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	srv := New(Options{Run: f.run, Version: "1.2.3"})
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestListTools(t *testing.T) {
	cs := connect(t, &fakeRunner{})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
		if tl.Description == "" || tl.InputSchema == nil {
			t.Errorf("%s: missing description or schema", tl.Name)
		}
		if strings.Contains(strings.ToLower(tl.Description), "lyric") {
			t.Errorf("%s mentions lyrics", tl.Name)
		}
		// Billed tools are never idempotent: a client must not retry them
		// on its own after a failure.
		if (tl.Name == "recognize" || tl.Name == "recognize_enterprise" || tl.Name == "catalog_add") && tl.Annotations != nil && tl.Annotations.IdempotentHint {
			t.Errorf("%s is billed but marked idempotent", tl.Name)
		}
	}
	slices.Sort(names)
	want := []string{"account_status", "billing_plans", "catalog_add", "docs", "now_playing", "payment_link", "recognize", "recognize_enterprise", "streams_add", "streams_history", "streams_list", "streams_remove", "token_rotate", "usage"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools:\n got %v\nwant %v", names, want)
	}
	// The recognize input schema lists path and url (embedded fields).
	for _, tl := range res.Tools {
		if tl.Name != "recognize" {
			continue
		}
		b, _ := json.Marshal(tl.InputSchema)
		for _, k := range []string{`"path"`, `"url"`, `"return"`, `"at"`} {
			if !strings.Contains(string(b), k) {
				t.Errorf("recognize schema lacks %s: %s", k, b)
			}
		}
	}
}

func TestRecognizeArgs(t *testing.T) {
	f := &fakeRunner{}
	cs := connect(t, f)
	file := filepath.Join(t.TempDir(), "-song.mp3")
	os.WriteFile(file, []byte("x"), 0o644)

	r := call(t, cs, "recognize", map[string]any{"path": file, "return": []string{"spotify", "apple_music"}, "at": "1:30"})
	if r.IsError {
		t.Fatal(text(r))
	}
	want := []string{"recognize", "--no-art", "--return", "spotify,apple_music", "--at", "1:30", "--format", "json", "--", file}
	if !slices.Equal(f.last(), want) {
		t.Fatalf("args:\n got %q\nwant %q", f.last(), want)
	}
	if r.StructuredContent == nil || !strings.Contains(text(r), `"ok":true`) {
		t.Fatalf("result: %+v %s", r.StructuredContent, text(r))
	}

	r = call(t, cs, "recognize", map[string]any{"url": "https://audd.tech/example.mp3"})
	if r.IsError || f.last()[len(f.last())-1] != "https://audd.tech/example.mp3" {
		t.Fatalf("%v %s", f.last(), text(r))
	}

	n := len(f.calls)
	for _, bad := range []map[string]any{
		{},
		{"path": file, "url": "https://audd.tech/example.mp3"},
		{"url": "ftp://x"},
		{"path": filepath.Join(t.TempDir(), "missing.mp3")},
		{"path": t.TempDir()},
		{"path": file, "return": []string{"napster"}},
	} {
		r := call(t, cs, "recognize", bad)
		if !r.IsError {
			t.Errorf("%v: want a tool error", bad)
		}
	}
	if len(f.calls) != n {
		t.Fatal("invalid input ran a command")
	}
}

func TestEnterpriseRequiresLimit(t *testing.T) {
	f := &fakeRunner{}
	cs := connect(t, f)
	r := call(t, cs, "recognize_enterprise", map[string]any{"url": "https://audd.tech/mix.mp3"})
	if !r.IsError || !strings.Contains(text(r), "limit is required") || !strings.Contains(text(r), "limit_required") {
		t.Fatalf("want limit_required tool error, got %s", text(r))
	}
	r = call(t, cs, "recognize_enterprise", map[string]any{"url": "https://audd.tech/mix.mp3", "limit": 0})
	if !r.IsError {
		t.Fatal("limit 0 accepted")
	}
	if len(f.calls) != 0 {
		t.Fatal("ran without a limit")
	}
	r = call(t, cs, "recognize_enterprise", map[string]any{"url": "https://audd.tech/mix.mp3", "limit": 5, "tracklist": true, "dry_run": true, "skip": 2, "every": 1})
	if r.IsError {
		t.Fatal(text(r))
	}
	want := []string{"recognize", "--enterprise", "--limit", "5", "--no-art", "--yes", "--tracklist", "--dry-run", "--skip", "2", "--every", "1", "--format", "json", "--", "https://audd.tech/mix.mp3"}
	if !slices.Equal(f.last(), want) {
		t.Fatalf("args:\n got %q\nwant %q", f.last(), want)
	}
}

func TestEnterpriseDryRunWithoutLimit(t *testing.T) {
	f := &fakeRunner{}
	cs := connect(t, f)
	// The limit_required hint says to call with dry_run first; that call
	// must work without a limit and send nothing.
	r := call(t, cs, "recognize_enterprise", map[string]any{"url": "https://audd.tech/mix.mp3"})
	if !strings.Contains(text(r), "dry_run") {
		t.Fatalf("hint should point at dry_run, got %s", text(r))
	}
	r = call(t, cs, "recognize_enterprise", map[string]any{"url": "https://audd.tech/mix.mp3", "dry_run": true})
	if r.IsError {
		t.Fatal(text(r))
	}
	want := []string{"recognize", "--enterprise", "--limit", "none", "--no-art", "--yes", "--dry-run", "--format", "json", "--", "https://audd.tech/mix.mp3"}
	if !slices.Equal(f.last(), want) {
		t.Fatalf("args:\n got %q\nwant %q", f.last(), want)
	}
	r = call(t, cs, "recognize_enterprise", map[string]any{"url": "https://audd.tech/mix.mp3", "dry_run": true, "limit": 0})
	if !r.IsError {
		t.Fatal("limit 0 accepted on a dry run")
	}
}

func TestCommandErrorsBecomeToolErrors(t *testing.T) {
	f := &fakeRunner{reply: func(args []string) Result {
		return Result{Exit: 3, Stderr: []byte("note: something\n" + `{"schema_version":1,"error":{"code":"no_token","api_code":0,"message":"no API token","hint":"audd login","retryable":false}}` + "\n")}
	}}
	cs := connect(t, f)
	r := call(t, cs, "streams_list", nil)
	if !r.IsError {
		t.Fatal("want error")
	}
	got := text(r)
	for _, w := range []string{"no API token", "Next step: audd login", "no_token", "exit code 3"} {
		if !strings.Contains(got, w) {
			t.Errorf("error text lacks %q: %s", w, got)
		}
	}
	// CLI-only hints are left out.
	f.reply = func([]string) Result {
		return Result{Exit: 1, Stderr: []byte(`{"schema_version":1,"error":{"code":"unexpected","message":"boom","hint":"run again with --debug; report persistent problems at https://github.com/AudDMusic/audd-cli/issues","retryable":false}}` + "\n")}
	}
	if r := call(t, cs, "now_playing", nil); !r.IsError || !strings.Contains(text(r), "boom") || strings.Contains(text(r), "--debug") {
		t.Fatal(text(r))
	}
	f.reply = func([]string) Result { return Result{Exit: 1, Stderr: []byte("panic: boom")} }
	if r := call(t, cs, "account_status", nil); !r.IsError || !strings.Contains(text(r), "boom") {
		t.Fatal(text(r))
	}
	f.reply = func([]string) Result { return Result{Stdout: []byte("not json")} }
	if r := call(t, cs, "usage", nil); !r.IsError {
		t.Fatal("non-JSON output accepted")
	}
}

func TestStreamsAndAccountArgs(t *testing.T) {
	f := &fakeRunner{}
	cs := connect(t, f)
	for _, c := range []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"streams_list", nil, []string{"streams", "list", "--format", "json"}},
		{"streams_add", map[string]any{"url": "https://radio.example/s.mp3", "radio_id": 3, "start": true}, []string{"streams", "add", "--id", "3", "--start", "--format", "json", "--", "https://radio.example/s.mp3"}},
		{"streams_remove", map[string]any{"radio_id": 3}, []string{"streams", "remove", "--yes", "3", "--format", "json"}},
		{"streams_history", map[string]any{"radio_id": 3, "since": "24h", "limit": 10}, []string{"streams", "history", "--id", "3", "--since", "24h", "--limit", "10", "--format", "json"}},
		{"now_playing", map[string]any{"radio_ids": []int{1, 2}}, []string{"now-playing", "--once", "--no-art", "1", "2", "--format", "json"}},
		{"usage", map[string]any{"days": 7}, []string{"usage", "--days", "7", "--format", "json"}},
		{"account_status", nil, []string{"account", "--format", "json"}},
		{"billing_plans", nil, []string{"billing", "plans", "--format", "json"}},
		{"payment_link", map[string]any{"action": "subscribe", "plan": "startup_plan"}, []string{"billing", "subscribe", NoConsentFlag, "--format", "json", "--", "startup_plan"}},
		{"payment_link", map[string]any{"action": "renew"}, []string{"billing", "renew", NoConsentFlag, "--format", "json"}},
		{"payment_link", map[string]any{"action": "buy", "requests": 5000}, []string{"billing", "buy", NoConsentFlag, "5000", "--format", "json"}},
	} {
		r := call(t, cs, c.tool, c.args)
		if r.IsError {
			t.Fatalf("%s: %s", c.tool, text(r))
		}
		if !slices.Equal(f.last(), c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.tool, f.last(), c.want)
		}
	}
	n := len(f.calls)
	for _, bad := range []map[string]any{{"action": "charge"}, {"action": "subscribe"}, {"action": "buy", "requests": 1500}} {
		if r := call(t, cs, "payment_link", bad); !r.IsError {
			t.Errorf("%v accepted", bad)
		}
	}
	if r := call(t, cs, "catalog_add", map[string]any{"url": "https://x.example/a.mp3"}); !r.IsError {
		t.Error("catalog_add without audio_id accepted")
	}
	if len(f.calls) != n {
		t.Fatal("invalid input ran a command")
	}
	r := call(t, cs, "catalog_add", map[string]any{"url": "https://x.example/a.mp3", "audio_id": 0})
	if r.IsError || !slices.Equal(f.last(), []string{"catalog", "add", "--id", "0", "--format", "json", "--", "https://x.example/a.mp3"}) {
		t.Fatalf("%q %s", f.last(), text(r))
	}
}

func TestTokenRotateNeverRuns(t *testing.T) {
	f := &fakeRunner{}
	cs := connect(t, f)
	r := call(t, cs, "token_rotate", nil)
	if r.IsError || !strings.Contains(text(r), "audd token rotate") || len(f.calls) != 0 {
		t.Fatalf("%s %v", text(r), f.calls)
	}
}

func TestDocsReturnsMarkdown(t *testing.T) {
	f := &fakeRunner{reply: func(args []string) Result {
		return Result{Stdout: []byte(`{"schema_version":1,"topic":"api","source":"https://docs.audd.io/.md","markdown":"# API\n"}`)}
	}}
	cs := connect(t, f)
	r := call(t, cs, "docs", map[string]any{"topic": "api"})
	if r.IsError || text(r) != "# API\n" || r.StructuredContent == nil {
		t.Fatalf("%q %v", text(r), r.StructuredContent)
	}
	if !slices.Equal(f.last(), []string{"docs", "--format", "json", "--", "api"}) {
		t.Fatal(f.last())
	}
	// A topic that looks like a flag stays a topic.
	call(t, cs, "docs", map[string]any{"topic": "--debug"})
	if !slices.Equal(f.last(), []string{"docs", "--format", "json", "--", "--debug"}) {
		t.Fatal(f.last())
	}
}
