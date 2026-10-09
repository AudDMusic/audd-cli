package mcpclient

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

var readScopes = []string{"profile:read", "account:read", "usage:read", "billing:read", "token:read"}

func setup(t *testing.T, scopes ...string) (*testutil.FakeMCP, func(cache string) *Client) {
	t.Helper()
	as := testutil.NewFakeOAuth(t)
	srv := testutil.NewFakeMCP(t, as)
	tok := as.IssueAccess(scopes...)
	return srv, func(cache string) *Client {
		return New(srv.URL(), func(context.Context) (string, error) { return tok, nil }, WithSessionCache(cache), WithUserAgent("audd-cli/test"))
	}
}

func wantErr(t *testing.T, err error, code string, exit int) *output.Error {
	t.Helper()
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != code || oe.Exit != exit {
		t.Fatalf("want %s/%d, got %#v", code, exit, err)
	}
	return oe
}

func TestCallToolStructured(t *testing.T) {
	srv, mk := setup(t, readScopes...)
	c := mk(filepath.Join(t.TempDir(), "s"))
	st, text, err := c.CallTool(context.Background(), "get_profile", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st["email"] != "user@example.com" || !strings.Contains(text, "user@example.com") {
		t.Fatalf("result: %v %q", st, text)
	}
	if inits, _ := srv.Stats(); inits != 1 {
		t.Fatalf("inits %d", inits)
	}
	// Same client: no new handshake.
	if _, _, err := c.CallTool(context.Background(), "get_account_status", nil); err != nil {
		t.Fatal(err)
	}
	if inits, _ := srv.Stats(); inits != 1 {
		t.Fatalf("inits %d", inits)
	}
}

func TestSessionCachedAcrossClients(t *testing.T) {
	srv, mk := setup(t, readScopes...)
	cache := filepath.Join(t.TempDir(), "mcp-session-default")
	if _, _, err := mk(cache).CallTool(context.Background(), "get_profile", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mk(cache).CallTool(context.Background(), "get_profile", nil); err != nil {
		t.Fatal(err)
	}
	if inits, _ := srv.Stats(); inits != 1 {
		t.Fatalf("second process should reuse the cached session; inits=%d", inits)
	}
	// An expired cache entry is not used.
	c := mk(cache)
	c.now = func() time.Time { return time.Now().Add(SessionTTL + time.Minute) }
	if _, _, err := c.CallTool(context.Background(), "get_profile", nil); err != nil {
		t.Fatal(err)
	}
	if inits, _ := srv.Stats(); inits != 2 {
		t.Fatalf("stale cache must not be used; inits=%d", inits)
	}
}

func TestSessionExpiryRetriesOnce(t *testing.T) {
	srv, mk := setup(t, readScopes...)
	c := mk(filepath.Join(t.TempDir(), "s"))
	if _, _, err := c.CallTool(context.Background(), "get_profile", nil); err != nil {
		t.Fatal(err)
	}
	srv.ExpireSessions()
	if _, _, err := c.CallTool(context.Background(), "get_profile", nil); err != nil {
		t.Fatal(err)
	}
	if inits, _ := srv.Stats(); inits != 2 {
		t.Fatalf("inits %d", inits)
	}
	if n := len(srv.CallsTo("get_profile")); n != 2 {
		t.Fatalf("calls %d", n)
	}
}

func TestSSEResponses(t *testing.T) {
	srv, mk := setup(t, readScopes...)
	srv.SetSSE(true)
	c := mk("")
	st, _, err := c.CallTool(context.Background(), "get_usage_stats", map[string]any{"days": 2})
	if err != nil {
		t.Fatal(err)
	}
	if days, _ := st["daily"].([]any); len(days) != 2 {
		t.Fatalf("usage: %v", st)
	}
	tools, err := c.ListTools(context.Background())
	if err != nil || len(tools) == 0 {
		t.Fatal(err)
	}
}

func TestToolErrors(t *testing.T) {
	_, mk := setup(t, append(readScopes, "billing:pay")...)
	c := mk("")
	_, _, err := c.CallTool(context.Background(), "subscribe_to_plan", map[string]any{"plan": "nope"})
	oe := wantErr(t, err, "invalid_argument", output.ExitUsage)
	if !strings.Contains(oe.Message, "Unknown plan: nope") {
		t.Fatal(oe.Message)
	}
	// Tools outside the granted scopes are hidden; calling one names the scope.
	_, _, err = c.CallTool(context.Background(), "rotate_api_token", nil)
	oe = wantErr(t, err, "scope_missing", output.ExitAuth)
	if oe.Hint != "audd auth refresh --scopes token:write" {
		t.Fatalf("hint %q", oe.Hint)
	}
}

func TestUnauthorized(t *testing.T) {
	srv, _ := setup(t)
	c := New(srv.URL(), func(context.Context) (string, error) { return "bogus", nil })
	_, _, err := c.CallTool(context.Background(), "get_profile", nil)
	wantErr(t, err, "login_required", output.ExitAuth)
}

func TestUnauthorizedRefreshesOnceAndRetries(t *testing.T) {
	as := testutil.NewFakeOAuth(t)
	srv := testutil.NewFakeMCP(t, as)
	cur := "revoked-early"
	refreshes := 0
	c := New(srv.URL(), func(context.Context) (string, error) { return cur, nil },
		WithRefresh(func(context.Context) (string, error) {
			refreshes++
			cur = as.IssueAccess(readScopes...)
			return cur, nil
		}))
	st, _, err := c.CallTool(context.Background(), "get_profile", nil)
	if err != nil || st["email"] != "user@example.com" || refreshes != 1 {
		t.Fatalf("%v %v refreshes=%d", st, err, refreshes)
	}
	// Still rejected after a refresh: sign in again, without looping.
	refreshes = 0
	c = New(srv.URL(), func(context.Context) (string, error) { return "bogus", nil },
		WithRefresh(func(context.Context) (string, error) { refreshes++; return "still-bogus", nil }))
	_, _, err = c.CallTool(context.Background(), "get_profile", nil)
	wantErr(t, err, "login_required", output.ExitAuth)
	if refreshes != 1 {
		t.Fatalf("refreshed %d times", refreshes)
	}
	// A failed refresh is reported as is.
	boom := output.Errf(output.ExitAuth, "login_required", "audd login", "your AudD sign-in has expired or was revoked")
	c = New(srv.URL(), func(context.Context) (string, error) { return "bogus", nil },
		WithRefresh(func(context.Context) (string, error) { return "", boom }))
	if _, _, err = c.CallTool(context.Background(), "get_profile", nil); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

func TestTokenErrorPassesThrough(t *testing.T) {
	srv, _ := setup(t)
	boom := output.Errf(output.ExitAuth, "login_required", "audd login", "not signed in")
	c := New(srv.URL(), func(context.Context) (string, error) { return "", boom })
	_, _, err := c.CallTool(context.Background(), "get_profile", nil)
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

func TestListToolsScopedAndCached(t *testing.T) {
	srv, mk := setup(t, "usage:read")
	cache := filepath.Join(t.TempDir(), "s")
	tools, err := mk(cache).ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "get_usage_stats,get_api_docs" {
		t.Fatalf("tools: %v", names)
	}
	if tools[0].InputSchema["properties"] == nil || tools[0].OutputSchema == nil {
		t.Fatalf("schemas: %+v", tools[0])
	}
	if _, err := mk(cache).ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, lists := srv.Stats(); lists != 1 {
		t.Fatalf("tool list should be cached with the session; lists=%d", lists)
	}
}

func TestReadSSE(t *testing.T) {
	stream := ": ping\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":3,\n" + "data: \"result\":{\"ok\":true}}\n\n"
	r, err := readSSE(strings.NewReader(stream), 3)
	if err != nil || string(r.Result) != `{"ok":true}` {
		t.Fatalf("%v %s", err, r.Result)
	}
	if _, err := readSSE(strings.NewReader("data: {}\n\n"), 1); err == nil {
		t.Fatal("want error for a stream without the response")
	}
}

func TestToolErrorMapping(t *testing.T) {
	for text, want := range map[string]string{
		"missing scope billing:pay":           "scope_missing",
		"Unauthorized":                        "login_required",
		"Invalid token":                       "login_required",
		"invalid_token: access token expired": "login_required",
		"invalid plan":                        "invalid_argument",
		"rate limit exceeded":                 "server",
		"requests must be a multiple of 1000": "invalid_argument",
		"something broke":                     "account_error",
	} {
		if got := ToolError("buy_bonus_requests", text).Code; got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
}
