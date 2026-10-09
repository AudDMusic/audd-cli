package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

type env struct {
	as      *testutil.FakeOAuth
	mcp     *testutil.FakeMCP
	sec     *secrets.Memory
	profile *config.Profile
	lockDir string
	stdout  *syncBuf
	stderr  *syncBuf
	saves   atomic.Int32
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	as := testutil.NewFakeOAuth(t)
	return &env{
		as:      as,
		mcp:     testutil.NewFakeMCP(t, as),
		sec:     secrets.NewMemory(),
		profile: &config.Profile{Name: "default"},
		lockDir: t.TempDir(),
		stdout:  &syncBuf{},
		stderr:  &syncBuf{},
	}
}

// client builds a Client; stdinTTY enables the paste path, format "" is auto (json when not a TTY).
func (e *env) client(stdinTTY bool, stdin io.Reader, opener func(string) error, opts ...Option) *Client {
	out := output.NewPrinter(e.stdout, e.stderr, output.PrinterOptions{StdinTTY: stdinTTY, Stdin: stdin, NoColor: true})
	base := []Option{
		WithResourceURL(e.mcp.URL()),
		WithBrowserOpener(opener),
		WithLockDir(e.lockDir),
		WithSaveConfig(func() error { e.saves.Add(1); return nil }),
		WithLoginTimeout(5 * time.Second),
	}
	env := headless
	env.StdinTTY = stdinTTY
	base = append(base, WithEnvironment(env))
	c := New(e.profile, e.sec, out, append(base, opts...)...)
	c.poll = 20 * time.Millisecond
	// The fake stands in for AudD's server.
	c.firstParty = func(iss string) bool { return iss == e.as.URL() }
	return c
}

// viaBrowser forces the browser method.
var viaBrowser = LoginOptions{Method: MethodBrowser}

// headless is a Linux machine with no display, as on a server.
var headless = Environment{GOOS: "linux", Getenv: func(string) string { return "" }}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func wantCode(t *testing.T, err error, code string, exit int) {
	t.Helper()
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != code || oe.Exit != exit {
		t.Fatalf("want %s (exit %d), got %#v", code, exit, err)
	}
}

func TestLoginLoopback(t *testing.T) {
	e := newEnv(t)
	var opened string
	c := e.client(false, nil, func(u string) error { opened = u; return testutil.FollowAuthorize(u) })
	tok, err := c.Login(ctx(t), viaBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" || tok.ClientID != FirstPartyClientID || tok.Issuer != e.as.URL() {
		t.Fatalf("tokens: %+v", tok)
	}
	if strings.Join(tok.Scopes, " ") != "account:read billing:pay billing:read openid profile:read token:read token:write usage:read" {
		t.Fatalf("scopes: %v", tok.Scopes)
	}
	u, _ := url.Parse(opened)
	q := u.Query()
	if sc := q.Get("scope"); sc != "account:read billing:pay billing:read openid profile:read token:read token:write usage:read" || strings.Contains(sc, "api:request") {
		t.Fatalf("authorize scope: %q", sc)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("resource") != e.mcp.URL() || !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
		t.Fatalf("authorize URL: %s", opened)
	}
	if e.profile.OAuthClientID != FirstPartyClientID || len(e.profile.OAuthScopes) == 0 || e.saves.Load() == 0 {
		t.Fatalf("profile not updated: %+v", e.profile)
	}
	stored, _ := c.Stored()
	if stored == nil || stored.AccessToken != tok.AccessToken {
		t.Fatal("session not stored")
	}
	if p, _ := c.loadPending(); p != nil {
		t.Fatal("pending login left behind")
	}
	// The pending record (non-TTY, json) went to stdout as one JSONL line.
	var ev map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(e.stdout.String(), "\n", 2)[0]), &ev); err != nil || ev["type"] != "login_pending" || ev["schema_version"].(float64) != 1 {
		t.Fatalf("login_pending: %q", e.stdout.String())
	}
	if !strings.Contains(ev["complete_with"].(string), "audd auth login --complete") {
		t.Fatalf("complete_with: %v", ev)
	}
	if ev["method"] != "browser" {
		t.Fatalf("method: %v", ev)
	}
	// AudD's server knows the CLI: no registration, ever.
	if _, err := c.Login(ctx(t), viaBrowser); err != nil {
		t.Fatal(err)
	}
	if n := e.as.Snapshot().Register; n != 0 {
		t.Fatalf("registered %d times", n)
	}
}

// pasteLogin runs a TTY login where the "browser" is elsewhere: the opener
// fetches the redirect without following it and the user pastes transform(redirect).
func pasteLogin(t *testing.T, e *env, transform func(redirect string) string) (*Tokens, error) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	c := e.client(true, pr, func(u string) error {
		red, err := testutil.AuthorizeRedirect(u)
		if err != nil {
			return err
		}
		_, err = io.WriteString(pw, "\n"+transform(red)+"\n")
		return err
	})
	return c.Login(ctx(t), LoginOptions{Method: MethodBrowser, In: pr})
}

func TestLoginPasteForms(t *testing.T) {
	forms := map[string]func(string) string{
		"full URL": func(r string) string { return "'" + r + "'" },
		"query": func(r string) string {
			u, _ := url.Parse(r)
			return "?" + u.RawQuery
		},
		"bare code": func(r string) string {
			u, _ := url.Parse(r)
			return u.Query().Get("code")
		},
	}
	for name, tf := range forms {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			tok, err := pasteLogin(t, e, tf)
			if err != nil {
				t.Fatal(err)
			}
			if tok.AccessToken == "" {
				t.Fatal("no token")
			}
			if !strings.Contains(e.stderr.String(), "paste it here") {
				t.Fatalf("instructions: %q", e.stderr.String())
			}
		})
	}
}

func TestLoginPasteGarbageThenURL(t *testing.T) {
	e := newEnv(t)
	tok, err := pasteLogin(t, e, func(r string) string { return "hello there\n" + r })
	if err != nil || tok == nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stderr.String(), "not a sign-in address") {
		t.Fatalf("garbage paste should be reported: %q", e.stderr.String())
	}
}

func TestLoginIgnoresLoopbackStateMismatch(t *testing.T) {
	e := newEnv(t)
	var forgedErr error
	c := e.client(false, nil, func(u string) error {
		// Something else on this machine hits the loopback first with a
		// response for another login; the real browser follows.
		e.as.Set(func(f *testutil.FakeOAuth) { f.StateOverride = "forged" })
		forgedErr = testutil.FollowAuthorize(u)
		e.as.Set(func(f *testutil.FakeOAuth) { f.StateOverride = "" })
		return testutil.FollowAuthorize(u)
	})
	tok, err := c.Login(ctx(t), viaBrowser)
	if err != nil || tok == nil {
		t.Fatalf("login should survive a stray loopback request: %v", err)
	}
	if forgedErr == nil {
		t.Fatal("the stray request should get the failure page")
	}
	if n := e.as.Snapshot().CodeExchange; n != 1 {
		t.Fatalf("exchanged %d codes, want only the real one", n)
	}
}

func TestLoginLoopbackStateMismatchTimesOut(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.StateOverride = "forged" })
	c := e.client(false, nil, testutil.FollowAuthorize, WithLoginTimeout(300*time.Millisecond))
	_, err := c.Login(ctx(t), viaBrowser)
	wantCode(t, err, "login_timeout", output.ExitAuth)
	if e.as.Snapshot().CodeExchange != 0 {
		t.Fatal("code must not be exchanged")
	}
}

func TestLoginPastedStateMismatch(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.StateOverride = "forged" })
	_, err := pasteLogin(t, e, func(r string) string { return r })
	wantCode(t, err, "state_mismatch", output.ExitAuth)
	if e.as.Snapshot().CodeExchange != 0 {
		t.Fatal("code must not be exchanged")
	}
}

func TestLoginLeavesLaterInputForTheNextReader(t *testing.T) {
	e := newEnv(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	c := e.client(true, pr, testutil.FollowAuthorize)
	if _, err := c.Login(ctx(t), LoginOptions{Method: MethodBrowser, In: pr}); err != nil {
		t.Fatal(err)
	}
	// A confirmation prompt after the login must get the answer typed for it.
	go func() { _, _ = io.WriteString(pw, "y\n") }()
	if err := c.out.Confirm("Continue?", false); err != nil {
		t.Fatalf("the answer was swallowed: %v", err)
	}
}

func TestLoginIssuerMismatch(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.IssOverride = "https://evil.example" })
	_, err := e.client(false, nil, testutil.FollowAuthorize).Login(ctx(t), viaBrowser)
	wantCode(t, err, "issuer_mismatch", output.ExitAuth)

	e = newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.OmitIss = true })
	_, err = e.client(false, nil, testutil.FollowAuthorize).Login(ctx(t), viaBrowser)
	wantCode(t, err, "issuer_mismatch", output.ExitAuth)
}

func TestLoginPastedResponseWithoutIssuer(t *testing.T) {
	// A full redirect URL or query string from a server that sends iss
	// always carries it; only a bare code legitimately lacks it.
	for name, tf := range map[string]func(string) string{
		"full URL": func(r string) string { return r },
		"query":    func(r string) string { u, _ := url.Parse(r); return "?" + u.RawQuery },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.as.Set(func(f *testutil.FakeOAuth) { f.OmitIss = true })
			_, err := pasteLogin(t, e, tf)
			wantCode(t, err, "issuer_mismatch", output.ExitAuth)
			if e.as.Snapshot().CodeExchange != 0 {
				t.Fatal("code must not be exchanged")
			}
		})
	}
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.OmitIss = true })
	tok, err := pasteLogin(t, e, func(r string) string { u, _ := url.Parse(r); return u.Query().Get("code") })
	if err != nil || tok == nil {
		t.Fatalf("a bare code has no issuer to check: %v", err)
	}
}

func TestLoginPendingWhenFormatCannotCarryIt(t *testing.T) {
	for _, f := range []output.Format{output.FormatCSV, output.FormatJSON} {
		t.Run(string(f), func(t *testing.T) {
			e := newEnv(t)
			out := output.NewPrinter(e.stdout, e.stderr, output.PrinterOptions{Format: f, NoColor: true})
			c := New(e.profile, e.sec, out, WithResourceURL(e.mcp.URL()), WithBrowserOpener(testutil.FollowAuthorize),
				WithLockDir(e.lockDir), WithSaveConfig(func() error { return nil }), WithLoginTimeout(5*time.Second), WithEnvironment(headless))
			c.poll = 20 * time.Millisecond
			if _, err := c.Login(ctx(t), viaBrowser); err != nil {
				t.Fatal(err)
			}
			if e.stdout.String() != "" {
				t.Fatalf("stdout must stay free for the command's own output: %q", e.stdout.String())
			}
			var ev map[string]any
			line := strings.SplitN(e.stderr.String(), "\n", 2)[0]
			if err := json.Unmarshal([]byte(line), &ev); err != nil || ev["type"] != "login_pending" ||
				!strings.HasPrefix(ev["url"].(string), e.as.URL()+"/authorize?") || ev["complete_with"] == nil {
				t.Fatalf("the sign-in URL must be printed on stderr: %q", e.stderr.String())
			}
		})
	}
}

func TestLoginTimeout(t *testing.T) {
	e := newEnv(t)
	c := e.client(false, nil, func(string) error { return nil }, WithLoginTimeout(100*time.Millisecond))
	_, err := c.Login(ctx(t), viaBrowser)
	wantCode(t, err, "login_timeout", output.ExitAuth)
	if p, _ := c.loadPending(); p == nil {
		t.Fatal("pending login must survive a timeout so --complete can finish it")
	}
}

func TestLoginInterrupted(t *testing.T) {
	e := newEnv(t)
	c := e.client(false, nil, func(string) error { return nil })
	cctx, cancel := context.WithCancel(ctx(t))
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := c.Login(cctx, viaBrowser)
	wantCode(t, err, "interrupted", 130)
}

func TestNonTTYPendingCompletedElsewhere(t *testing.T) {
	e := newEnv(t)
	urls := make(chan string, 1)
	waiting := e.client(false, nil, func(u string) error { urls <- u; return nil })
	type res struct {
		tok *Tokens
		err error
	}
	done := make(chan res, 1)
	go func() {
		tok, err := waiting.Login(ctx(t), viaBrowser)
		done <- res{tok, err}
	}()
	redirect, err := testutil.AuthorizeRedirect(<-urls)
	if err != nil {
		t.Fatal(err)
	}
	// A separate invocation: fresh Client and its own copy of the profile
	// (as loaded from the config file), same store.
	other := e.client(false, nil, nil)
	cp := *e.profile
	other.profile = &cp
	tok, err := other.CompletePending(ctx(t), redirect)
	if err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.tok.AccessToken != tok.AccessToken {
		t.Fatalf("waiting login: %v %+v", r.err, r.tok)
	}
	// Completing again fails cleanly.
	_, err = other.CompletePending(ctx(t), redirect)
	wantCode(t, err, "no_pending_login", output.ExitAuth)
}

func TestCompletePendingRejectsWrongState(t *testing.T) {
	e := newEnv(t)
	urls := make(chan string, 1)
	c := e.client(false, nil, func(u string) error { urls <- u; return nil }, WithLoginTimeout(300*time.Millisecond))
	go c.Login(ctx(t), viaBrowser)
	redirect, _ := testutil.AuthorizeRedirect(<-urls)
	forged := strings.Replace(redirect, "state=", "state=x", 1)
	_, err := e.client(false, nil, nil).CompletePending(ctx(t), forged)
	wantCode(t, err, "state_mismatch", output.ExitAuth)
}

func loggedIn(t *testing.T, e *env) *Client {
	t.Helper()
	c := e.client(false, nil, testutil.FollowAuthorize)
	if _, err := c.Login(ctx(t), viaBrowser); err != nil {
		t.Fatal(err)
	}
	return c
}

func expire(t *testing.T, e *env) {
	t.Helper()
	s, _ := e.sec.Get("default", "oauth")
	var tok Tokens
	_ = json.Unmarshal([]byte(s), &tok)
	tok.Expiry = time.Now().Add(-time.Minute)
	b, _ := json.Marshal(tok)
	_ = e.sec.Set("default", "oauth", string(b))
}

func TestTokenRefreshesOnceUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	loggedIn(t, e)
	expire(t, e)
	e.as.Set(func(f *testutil.FakeOAuth) { f.RefreshDelay = 100 * time.Millisecond })
	// Two clients stand in for two processes: separate in-process locks,
	// shared store and lock file.
	a, b := e.client(false, nil, nil), e.client(false, nil, nil)
	var wg sync.WaitGroup
	toks := make([]*Tokens, 2)
	errs := make([]error, 2)
	for i, c := range []*Client{a, b} {
		wg.Add(1)
		go func(i int, c *Client) {
			defer wg.Done()
			toks[i], errs[i] = c.Token(ctx(t))
		}(i, c)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if toks[0].AccessToken != toks[1].AccessToken {
		t.Fatal("both callers should end with the same session")
	}
	s := e.as.Snapshot()
	if s.Refresh != 1 || s.RefreshRejected != 0 {
		t.Fatalf("want exactly one refresh, got %+v", s)
	}
	// A fresh token is not refreshed again.
	if _, err := a.Token(ctx(t)); err != nil || e.as.Snapshot().Refresh != 1 {
		t.Fatalf("unexpected refresh: %v %+v", err, e.as.Snapshot())
	}
}

func TestRefreshRevokedSession(t *testing.T) {
	e := newEnv(t)
	c := loggedIn(t, e)
	e.as.RevokeAll()
	expire(t, e)
	_, err := c.Token(ctx(t))
	wantCode(t, err, "login_required", output.ExitAuth)
}

func TestTokenWithoutLogin(t *testing.T) {
	e := newEnv(t)
	_, err := e.client(false, nil, nil).Token(ctx(t))
	wantCode(t, err, "login_required", output.ExitAuth)
}

func TestStepUpAddsScope(t *testing.T) {
	e := newEnv(t)
	// The user unticks billing:pay at the first sign-in.
	e.as.Set(func(f *testutil.FakeOAuth) { f.Untick = []string{"billing:pay"} })
	c := loggedIn(t, e)
	if stored, _ := c.Stored(); stored.HasScopes("billing:pay") {
		t.Fatalf("billing:pay was unticked: %v", stored.Scopes)
	}
	e.as.Set(func(f *testutil.FakeOAuth) { f.Untick = nil })
	var opened string
	c = e.client(false, nil, func(u string) error { opened = u; return testutil.FollowAuthorize(u) }, WithMethod(MethodBrowser))
	tok, err := c.EnsureScopes(ctx(t), "billing:pay")
	if err != nil {
		t.Fatal(err)
	}
	if !tok.HasScopes("billing:pay", "usage:read") {
		t.Fatalf("scopes: %v", tok.Scopes)
	}
	u, _ := url.Parse(opened)
	if !strings.Contains(u.Query().Get("scope"), "billing:pay") || !strings.Contains(u.Query().Get("scope"), "usage:read") {
		t.Fatalf("step-up must keep existing scopes: %s", u.Query().Get("scope"))
	}
	if !strings.Contains(e.stderr.String(), "create payment links") {
		t.Fatalf("step-up note: %q", e.stderr.String())
	}
	// Already granted: no second consent.
	before := e.as.Snapshot().Authorize
	if _, err := c.EnsureScopes(ctx(t), "billing:pay"); err != nil || e.as.Snapshot().Authorize != before {
		t.Fatal("no consent needed when the scope is granted")
	}
}

func TestStepUpRefused(t *testing.T) {
	e := newEnv(t)
	// The user unticks token:write at the first sign-in; the server then
	// refuses it.
	e.as.Set(func(f *testutil.FakeOAuth) { f.Untick = []string{"token:write"} })
	loggedIn(t, e)
	e.as.Set(func(f *testutil.FakeOAuth) { f.Untick = nil; f.DenyScopes = []string{"token:write"} })
	_, err := e.client(false, nil, testutil.FollowAuthorize).EnsureScopes(ctx(t), "token:write")
	wantCode(t, err, "scope_not_allowed", output.ExitAuth)
}

func TestLogoutRevokesAndKeepsUserToken(t *testing.T) {
	e := newEnv(t)
	c := loggedIn(t, e)
	tok, _ := c.Stored()
	_ = e.sec.Set("default", "api_token", testutil.PlaceholderToken)
	_ = e.sec.Set("default", "login_api_token", testutil.FakeAPIToken)
	e.profile.Account = "user@example.com"
	if err := c.Logout(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if r := e.as.RevokedTokens(); len(r) != 1 || r[0] != tok.RefreshToken {
		t.Fatalf("revoked: %v", r)
	}
	if _, err := e.sec.Get("default", "oauth"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("session kept")
	}
	if _, err := e.sec.Get("default", "login_api_token"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("login token kept")
	}
	if v, _ := e.sec.Get("default", "api_token"); v != testutil.PlaceholderToken {
		t.Fatal("user-set token must stay")
	}
	if e.profile.Account != "" || e.profile.OAuthScopes != nil {
		t.Fatalf("profile: %+v", e.profile)
	}
}

func TestLogoutWhenServerUnreachable(t *testing.T) {
	e := newEnv(t)
	c := loggedIn(t, e)
	e.as.Server.Close()
	c = e.client(false, nil, nil) // no cached metadata
	if err := c.Logout(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stderr.String(), "Could not revoke") {
		t.Fatalf("warning: %q", e.stderr.String())
	}
	if c.LoggedIn() {
		t.Fatal("session kept")
	}
}

func TestParseCallback(t *testing.T) {
	p := &pending{State: "s1"}
	tests := []struct {
		in       string
		code     string
		bare     bool
		wantErr  bool // input not understood
		respCode string
	}{
		{"http://127.0.0.1:5555/callback?code=abc&state=s1&iss=x", "abc", false, false, ""},
		{"  \"http://127.0.0.1:5555/callback?code=abc&state=s1\"  ", "abc", false, false, ""},
		{"?code=abc&state=s1", "abc", false, false, ""},
		{"code=abc&state=s1", "abc", false, false, ""},
		{"abc", "abc", true, false, ""},
		{"code=abc&state=zz", "abc", false, false, "state_mismatch"},
		{"error=access_denied&state=s1", "", false, false, "login_denied"},
		{"error=invalid_scope&state=s1", "", false, false, "scope_not_allowed"},
		{"state=s1", "", false, false, "login_failed"},
		{"", "", false, true, ""},
		{"not a code", "", false, true, ""},
	}
	for _, tt := range tests {
		cb, err := parseCallback(tt.in, p)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("%q: want error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tt.in, err)
		}
		if tt.respCode != "" {
			wantCode(t, cb.err, tt.respCode, output.ExitAuth)
			continue
		}
		if cb.err != nil || cb.code != tt.code || cb.bare != tt.bare {
			t.Fatalf("%q: %+v", tt.in, cb)
		}
	}
}

func TestWellKnown(t *testing.T) {
	for in, want := range map[string]string{
		"https://mcp.audd.io":        "https://mcp.audd.io/.well-known/oauth-protected-resource",
		"https://mcp.audd.io/":       "https://mcp.audd.io/.well-known/oauth-protected-resource",
		"https://example.com/v1/mcp": "https://example.com/.well-known/oauth-protected-resource/v1/mcp",
	} {
		got, err := wellKnown(in, "oauth-protected-resource")
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", in, got, err)
		}
	}
}

func TestCompleteAfterTimeoutKeepsTheClient(t *testing.T) {
	e := newEnv(t)
	urls := make(chan string, 1)
	c := e.client(false, nil, func(u string) error { urls <- u; return nil }, WithLoginTimeout(200*time.Millisecond))
	_, err := c.Login(ctx(t), viaBrowser)
	wantCode(t, err, "login_timeout", output.ExitAuth)
	redirect, err := testutil.AuthorizeRedirect(<-urls)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := e.client(false, nil, nil).CompletePending(ctx(t), redirect)
	if err != nil {
		t.Fatal(err)
	}
	if e.profile.OAuthClientID != tok.ClientID {
		t.Fatalf("completing the sign-in must keep its client: %q vs %q", e.profile.OAuthClientID, tok.ClientID)
	}
}

func mustDiscover(t *testing.T, c *Client) *metadata {
	t.Helper()
	m, err := c.discover(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// stuckStore refuses to delete, like a locked system keychain.
type stuckStore struct{ *secrets.Memory }

func (stuckStore) Delete(profile, key string) error {
	return fmt.Errorf("%w: keychain is locked", secrets.ErrKeyringDelete)
}

func TestLogoutFailsWhenTheKeyringKeepsTheSignIn(t *testing.T) {
	e := newEnv(t)
	loggedIn(t, e)
	out := output.NewPrinter(e.stdout, e.stderr, output.PrinterOptions{NoColor: true})
	c := New(e.profile, stuckStore{e.sec}, out, WithResourceURL(e.mcp.URL()), WithLockDir(e.lockDir),
		WithSaveConfig(func() error { e.saves.Add(1); return nil }))
	err := c.Logout(ctx(t))
	if output.AsError(err).Code != "logout_incomplete" {
		t.Fatalf("want logout_incomplete, got %v", err)
	}
}
