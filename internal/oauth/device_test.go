package oauth

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func TestMain(m *testing.M) {
	// A device sign-in's "seconds" are milliseconds in tests.
	PollUnit = time.Millisecond
	os.Exit(m.Run())
}

// sleeps records the waits between device polls, in polling seconds.
type sleeps struct {
	mu sync.Mutex
	d  []int
}

func (s *sleeps) record(c *Client) {
	c.sleep = func(ctx context.Context, d time.Duration) error {
		s.mu.Lock()
		s.d = append(s.d, int(d/PollUnit))
		s.mu.Unlock()
		return ctx.Err()
	}
}

func (s *sleeps) get() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.d...)
}

// deviceClient is an agent's client: no terminal, a headless machine, so
// the method is the device flow.
func deviceClient(e *env, opts ...Option) *Client {
	return e.client(false, nil, func(string) error { return nil }, opts...)
}

func firstLine(t *testing.T, s string) map[string]any {
	t.Helper()
	var ev map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(s, "\n", 2)[0]), &ev); err != nil {
		t.Fatalf("not a JSON line: %q", s)
	}
	return ev
}

func TestDeviceLoginHappyPath(t *testing.T) {
	e := newEnv(t)
	c := deviceClient(e)
	var s sleeps
	s.record(c)
	tok, err := c.Login(ctx(t), LoginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tok.ClientID != FirstPartyClientID || tok.RefreshToken == "" {
		t.Fatalf("tokens: %+v", tok)
	}
	// A default login asks for everything the CLI uses, never api:request.
	want := "account:read billing:pay billing:read openid profile:read token:read token:write usage:read"
	if strings.Join(tok.Scopes, " ") != want || strings.Join(tok.Requested, " ") != want {
		t.Fatalf("scopes %v, requested %v", tok.Scopes, tok.Requested)
	}
	if got := e.as.RequestedScopes(); len(got) != 1 || got[0] != want || strings.Contains(got[0], "api:request") {
		t.Fatalf("requested: %q", got)
	}
	n := e.as.Snapshot()
	if n.DeviceAuthorize != 1 || n.DevicePoll != 1 || n.DeviceSuccess != 1 || n.Register != 0 || n.Authorize != 0 {
		t.Fatalf("counters: %+v", n)
	}
	if got := s.get(); !slices.Equal(got, []int{5}) {
		t.Fatalf("waited %v; the first poll comes after one interval", got)
	}
	ev := firstLine(t, e.stdout.String())
	if ev["type"] != "login_pending" || ev["method"] != "device" || ev["user_code"] == "" ||
		ev["expires_in_seconds"].(float64) != 900 || ev["interval"].(float64) != 5 ||
		!strings.HasSuffix(ev["verification_uri"].(string), "/device") ||
		!strings.Contains(ev["verification_uri_complete"].(string), "user_code="+ev["user_code"].(string)) {
		t.Fatalf("login_pending: %v", ev)
	}
	if e.profile.OAuthClientID != FirstPartyClientID || e.profile.OAuthIssuer != e.as.URL() {
		t.Fatalf("profile: %+v", e.profile)
	}
}

func TestDevicePendingRecordGolden(t *testing.T) {
	e := newEnv(t)
	c := deviceClient(e)
	if _, err := c.Login(ctx(t), LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	line := strings.SplitN(e.stdout.String(), "\n", 2)[0] + "\n"
	line = strings.ReplaceAll(line, e.as.URL(), "https://dashboard.example")
	testutil.Golden(t, "login_pending_device", []byte(line))
}

func TestDeviceHumanOutput(t *testing.T) {
	e := newEnv(t)
	var opened []string
	var mu sync.Mutex
	// A person at a terminal on a desktop who asked for --device: the page
	// opens on this machine too.
	c := e.client(true, nil, func(u string) error { mu.Lock(); opened = append(opened, u); mu.Unlock(); return nil },
		WithEnvironment(Environment{GOOS: "darwin", StdinTTY: true, StdoutTTY: true}))
	if _, err := c.Login(ctx(t), LoginOptions{Method: MethodDevice}); err != nil {
		t.Fatal(err)
	}
	errOut := e.stderr.String()
	for _, want := range []string{"open this page", "/device?user_code=", "Check that it shows this code", "expires in 15 minutes"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("missing %q in %q", want, errOut)
		}
	}
	if e.stdout.String() != "" {
		t.Fatalf("people get no login_pending record: %q", e.stdout.String())
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 1 || !strings.Contains(opened[0], "/device?user_code=") {
		t.Fatalf("opened %v", opened)
	}
}

func TestDeviceNoBrowserOnHeadlessMachine(t *testing.T) {
	e := newEnv(t)
	opened := false
	c := e.client(true, nil, func(string) error { opened = true; return nil },
		WithEnvironment(Environment{GOOS: "linux", Getenv: func(k string) string {
			if k == "SSH_CONNECTION" {
				return "10.0.0.1 22 10.0.0.2 22"
			}
			return ""
		}, StdinTTY: true, StdoutTTY: true}))
	if _, err := c.Login(ctx(t), LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	if opened || e.as.Snapshot().DeviceSuccess != 1 {
		t.Fatalf("over SSH the device flow runs and no browser opens: opened=%v %+v", opened, e.as.Snapshot())
	}
}

func TestDeviceSlowDownIncreasesInterval(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) {
		f.DeviceScript = []string{"pending", "slow_down", "slow_down_429", "pending", "approve"}
	})
	c := deviceClient(e)
	var s sleeps
	s.record(c)
	if _, err := c.Login(ctx(t), LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	// 400 slow_down carries interval 10; 429 carries none, so old + 5.
	if got := s.get(); !slices.Equal(got, []int{5, 5, 10, 15, 15}) {
		t.Fatalf("intervals %v", got)
	}
	if n := e.as.Snapshot(); n.DevicePoll != 5 || n.DeviceSuccess != 1 {
		t.Fatalf("counters: %+v", n)
	}
}

func TestDeviceStopsAfterFirstSuccess(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.DeviceScript = []string{"pending", "pending"} })
	c := deviceClient(e)
	if _, err := c.Login(ctx(t), LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	n := e.as.Snapshot()
	if n.DevicePoll != 3 || n.DeviceSuccess != 1 || n.DevicePollAfterSuccess != 0 {
		t.Fatalf("exactly one 200 poll and nothing after it: %+v", n)
	}
	// The tokens are still valid: a replay would have revoked them.
	tok, _ := c.Stored()
	if !e.as.HasRefresh(tok.RefreshToken) {
		t.Fatal("the session was revoked")
	}
}

func TestDeviceErrors(t *testing.T) {
	for _, tt := range []struct {
		action, code, hint string
	}{
		{"deny", "login_denied", "audd login"},
		{"expire", "login_expired", "run audd login again"},
		{"invalid_grant", "login_failed", "run audd login again"},
	} {
		t.Run(tt.action, func(t *testing.T) {
			e := newEnv(t)
			e.as.Set(func(f *testutil.FakeOAuth) { f.DeviceScript = []string{"pending", tt.action} })
			c := deviceClient(e)
			_, err := c.Login(ctx(t), LoginOptions{})
			wantCode(t, err, tt.code, output.ExitAuth)
			if !strings.Contains(output.AsError(err).Hint, tt.hint) {
				t.Fatalf("hint: %q", output.AsError(err).Hint)
			}
			if n := e.as.Snapshot().DevicePoll; n != 2 {
				t.Fatalf("polling must stop at the error: %d polls", n)
			}
			if c.LoggedIn() {
				t.Fatal("no session")
			}
		})
	}
}

func TestDeviceExpiresLocally(t *testing.T) {
	e := newEnv(t)
	script := make([]string, 100)
	for i := range script {
		script[i] = "pending"
	}
	e.as.Set(func(f *testutil.FakeOAuth) { f.DeviceScript = script; f.DeviceExpiresIn = 10; f.DeviceInterval = 5 })
	c := deviceClient(e)
	_, err := c.Login(ctx(t), LoginOptions{})
	wantCode(t, err, "login_expired", output.ExitAuth)
	// expires_in 10 plus 60 seconds of grace, at 5 seconds a poll.
	if n := e.as.Snapshot().DevicePoll; n != 14 {
		t.Fatalf("polls: %d", n)
	}
}

func TestDeviceAuthorizationRateLimited(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.DeviceRetryAfter = 1800 })
	_, err := deviceClient(e).Login(ctx(t), LoginOptions{})
	wantCode(t, err, "rate_limited", output.ExitNetwork)
	if oe := output.AsError(err); !strings.Contains(oe.Message, "30 minutes") || !strings.Contains(oe.Hint, "30 minutes") {
		t.Fatalf("must say how long to wait: %#v", oe)
	}
	if e.as.Snapshot().DevicePoll != 0 {
		t.Fatal("no polling without a code")
	}
}

func TestDeviceLockRefusesASecondPoller(t *testing.T) {
	path := t.TempDir() + "/oauth-device-x.lock"
	unlock, ok, err := tryLockFile(path)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, ok2, err := tryLockFile(path); err != nil || ok2 {
		t.Fatalf("a second holder must be refused: %v %v", ok2, err)
	}
	unlock()
	unlock2, ok, err := tryLockFile(path)
	if err != nil || !ok {
		t.Fatalf("free again after unlock: %v", err)
	}
	unlock2()
}

func TestMethodSelection(t *testing.T) {
	env := func(goos string, vars map[string]string, in, out bool) Environment {
		return Environment{GOOS: goos, Getenv: func(k string) string { return vars[k] }, StdinTTY: in, StdoutTTY: out}
	}
	x11 := map[string]string{"DISPLAY": ":0"}
	wl := map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
	ssh := map[string]string{"SSH_CONNECTION": "1 2 3 4"}
	sshTTY := map[string]string{"SSH_TTY": "/dev/pts/1", "DISPLAY": "localhost:10.0"}
	for _, tt := range []struct {
		name    string
		env     Environment
		browser bool
		method  Method
	}{
		{"macOS terminal", env("darwin", nil, true, true), true, MethodBrowser},
		{"Windows terminal", env("windows", nil, true, true), true, MethodBrowser},
		{"Linux X11", env("linux", x11, true, true), true, MethodBrowser},
		{"Linux Wayland", env("linux", wl, true, true), true, MethodBrowser},
		{"Linux console", env("linux", nil, true, true), false, MethodDevice},
		{"FreeBSD X11", env("freebsd", x11, true, true), true, MethodBrowser},
		{"macOS over SSH", env("darwin", ssh, true, true), false, MethodDevice},
		{"Linux over SSH with X forwarding", env("linux", sshTTY, true, true), false, MethodDevice},
		{"container without display", Environment{GOOS: "linux", InContainer: true, StdinTTY: true, StdoutTTY: true}, false, MethodDevice},
		{"container with display", Environment{GOOS: "linux", InContainer: true, Getenv: func(k string) string { return x11[k] }, StdinTTY: true, StdoutTTY: true}, true, MethodBrowser},
		{"agent on macOS (no TTY)", env("darwin", nil, false, false), true, MethodDevice},
		{"stdout piped", env("linux", x11, true, false), true, MethodDevice},
		{"stdin piped", env("windows", nil, false, true), true, MethodDevice},
	} {
		if got := tt.env.BrowserAvailable(); got != tt.browser {
			t.Errorf("%s: browser available %v, want %v", tt.name, got, tt.browser)
		}
		if got := tt.env.Method(); got != tt.method {
			t.Errorf("%s: method %q, want %q", tt.name, got, tt.method)
		}
	}
}

func TestStepUpRequestsGrantedPlusNew(t *testing.T) {
	e := newEnv(t)
	// The user unticks billing:read and token:write at the first sign-in.
	e.as.Set(func(f *testutil.FakeOAuth) { f.Untick = []string{"billing:read", "token:write"} })
	c := deviceClient(e)
	if _, err := c.Login(ctx(t), LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	e.as.Set(func(f *testutil.FakeOAuth) { f.Untick = nil })
	tok, err := deviceClient(e).EnsureScopes(ctx(t), "token:write")
	if err != nil {
		t.Fatal(err)
	}
	req := e.as.RequestedScopes()
	if got := req[len(req)-1]; got != "account:read billing:pay openid profile:read token:read token:write usage:read" {
		t.Fatalf("step-up must ask for what is granted plus the new scope: %q", got)
	}
	if !tok.HasScopes("token:write", "usage:read") || tok.HasScopes("billing:read") {
		t.Fatalf("scopes: %v", tok.Scopes)
	}
}

func TestGrantedScopesNarrowed(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.Untick = []string{"token:read"} })
	c := deviceClient(e)
	tok, err := c.Login(ctx(t), LoginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tok.HasScopes("token:read") || !slices.Contains(tok.Requested, "token:read") {
		t.Fatalf("granted %v, requested %v", tok.Scopes, tok.Requested)
	}
	stored, _ := c.Stored()
	if stored.HasScopes("token:read") || slices.Contains(e.profile.OAuthScopes, "token:read") {
		t.Fatalf("the narrower grant must be stored: %v / %v", stored.Scopes, e.profile.OAuthScopes)
	}
	// A command that needs it explains which permission was unticked; the
	// user unticks it again, and the command stops with a hint.
	_, err = deviceClient(e).EnsureScopes(ctx(t), "token:read")
	wantCode(t, err, "scope_not_granted", output.ExitAuth)
	if !strings.Contains(e.stderr.String(), "read your API token (token:read)") || !strings.Contains(e.stderr.String(), "unticked") {
		t.Fatalf("prompt: %q", e.stderr.String())
	}
	if !strings.Contains(output.AsError(err).Hint, "--scopes token:read") {
		t.Fatalf("hint: %q", output.AsError(err).Hint)
	}
}

func TestRefreshKeepsGrantedScope(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.Untick = []string{"billing:read"} })
	c := deviceClient(e)
	if _, err := c.Login(ctx(t), LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	tok, err := c.Refresh(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if tok.HasScopes("billing:read") || !slices.Contains(tok.Requested, "billing:read") || tok.ClientID != FirstPartyClientID {
		t.Fatalf("after refresh: %+v", tok)
	}
}

func TestMigrationRevokesOldClientSession(t *testing.T) {
	e := newEnv(t)
	old := e.as.RegisterClient("openid", "email", "profile:read", "billing:pay", "api:request")
	_, oldRefresh := e.as.IssueSession(old, "openid", "email", "profile:read", "billing:pay", "api:request")
	// A profile from an earlier version: a self-registered client.
	e.profile.OAuthClientID = old
	b, _ := json.Marshal(Tokens{AccessToken: "old-access", RefreshToken: oldRefresh, ClientID: old,
		Scopes: []string{"api:request", "billing:pay", "email", "openid", "profile:read"}, Expiry: time.Now().Add(time.Hour)})
	_ = e.sec.Set("default", "oauth", string(b))

	c := deviceClient(e)
	// audd login keeps earlier grants only from the same client.
	tok, err := c.Login(ctx(t), LoginOptions{KeepGranted: true})
	if err != nil {
		t.Fatal(err)
	}
	if tok.ClientID != FirstPartyClientID || e.profile.OAuthClientID != FirstPartyClientID {
		t.Fatalf("switched to audd-cli: %+v %+v", tok, e.profile)
	}
	if ids := e.as.RequestedClientIDs(); !slices.Equal(ids, []string{FirstPartyClientID}) {
		t.Fatalf("client IDs: %v", ids)
	}
	// The new sign-in asks for the default set; the old client's over-grant
	// (email, api:request) is not carried over.
	if got := e.as.RequestedScopes()[0]; got != strings.Join(union(DefaultScopes, nil), " ") ||
		got != "account:read billing:pay billing:read openid profile:read token:read token:write usage:read" ||
		strings.Contains(got, "email") || strings.Contains(got, "api:request") {
		t.Fatalf("the old client's grant is not carried over: %q", got)
	}
	revs := e.as.RevocationRequests()
	if len(revs) != 1 || revs[0].Token != oldRefresh || revs[0].ClientID != old {
		t.Fatalf("revocations: %+v", revs)
	}
	if e.as.HasRefresh(oldRefresh) {
		t.Fatal("the old sign-in is still valid")
	}
	if e.as.Snapshot().Register != 0 {
		t.Fatal("no registration")
	}
	// From now on refresh and revoke use audd-cli.
	if _, err := c.Refresh(ctx(t)); err != nil {
		t.Fatal(err)
	}
	cur, _ := c.Stored()
	if err := c.Logout(ctx(t)); err != nil {
		t.Fatal(err)
	}
	revs = e.as.RevocationRequests()
	if last := revs[len(revs)-1]; last.Token != cur.RefreshToken || last.ClientID != FirstPartyClientID {
		t.Fatalf("logout revocation: %+v", last)
	}
}

func TestMigrationRevokeFailureIsNoted(t *testing.T) {
	e := newEnv(t)
	b, _ := json.Marshal(Tokens{AccessToken: "old-access", RefreshToken: "old-refresh", ClientID: "client-old", Expiry: time.Now().Add(time.Hour)})
	_ = e.sec.Set("default", "oauth", string(b))
	c := deviceClient(e)
	// Discover first, then take the revocation endpoint away.
	m := mustDiscover(t, c)
	m.RevocationEndpoint = ""
	if _, err := c.Login(ctx(t), LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stderr.String(), "could not end the previous sign-in") {
		t.Fatalf("note: %q", e.stderr.String())
	}
}

func TestAudDServerNeverRegisters(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.NoFirstPartyClient = true })
	_, err := deviceClient(e).Login(ctx(t), LoginOptions{})
	wantCode(t, err, "client_unavailable", output.ExitAuth)
	if n := e.as.Snapshot(); n.Register != 0 || n.DeviceAuthorize != 1 {
		t.Fatalf("counters: %+v", n)
	}
	e.as.Set(func(f *testutil.FakeOAuth) { f.NoFirstPartyClient = false })
	// The browser method does not register either.
	if _, err := e.client(false, nil, testutil.FollowAuthorize).Login(ctx(t), viaBrowser); err != nil {
		t.Fatal(err)
	}
	if n := e.as.Snapshot().Register; n != 0 {
		t.Fatalf("registered %d times", n)
	}
}

// otherServer is a client for an authorization server that is not AudD's
// (AUDD_MCP_URL) and does not know audd-cli.
func otherServer(t *testing.T, e *env, opener func(string) error) *Client {
	t.Helper()
	e.as.Set(func(f *testutil.FakeOAuth) { f.NoFirstPartyClient = true })
	c := e.client(false, nil, opener)
	c.firstParty = func(string) bool { return false }
	return c
}

func TestRegistrationFallbackOnOtherServer(t *testing.T) {
	t.Run("device", func(t *testing.T) {
		e := newEnv(t)
		c := otherServer(t, e, nil)
		_, err := c.Login(ctx(t), LoginOptions{})
		// The registered client may not use device sign-in on this server.
		wantCode(t, err, "device_unsupported", output.ExitAuth)
		ids := e.as.RequestedClientIDs()
		if e.as.Snapshot().Register != 1 || len(ids) != 2 || ids[0] != FirstPartyClientID || ids[1] == FirstPartyClientID {
			t.Fatalf("register once, then retry with the new client: %v %+v", ids, e.as.Snapshot())
		}
		if !strings.Contains(output.AsError(err).Hint, "--browser") {
			t.Fatalf("hint: %q", output.AsError(err).Hint)
		}
	})
	t.Run("browser without device sign-in", func(t *testing.T) {
		e := newEnv(t)
		e.as.Set(func(f *testutil.FakeOAuth) { f.NoDeviceEndpoint = true })
		c := otherServer(t, e, testutil.FollowAuthorize)
		tok, err := c.Login(ctx(t), LoginOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if tok.ClientID == FirstPartyClientID || e.profile.OAuthClientID != tok.ClientID || e.profile.OAuthIssuer != e.as.URL() {
			t.Fatalf("registered client: %+v %+v", tok, e.profile)
		}
		// The registration is reused.
		if _, err := c.Login(ctx(t), LoginOptions{}); err != nil {
			t.Fatal(err)
		}
		if n := e.as.Snapshot().Register; n != 1 {
			t.Fatalf("registered %d times", n)
		}
	})
	t.Run("registration returns the refused client", func(t *testing.T) {
		e := newEnv(t)
		e.as.Set(func(f *testutil.FakeOAuth) { f.RegisterReturns = FirstPartyClientID })
		_, err := otherServer(t, e, nil).Login(ctx(t), LoginOptions{})
		wantCode(t, err, "client_unavailable", output.ExitAuth)
		if n := e.as.Snapshot(); n.Register != 1 || n.DeviceAuthorize != 1 {
			t.Fatalf("no loop: %+v", n)
		}
	})
	t.Run("registration refused", func(t *testing.T) {
		e := newEnv(t)
		e.as.Set(func(f *testutil.FakeOAuth) {
			f.RegisterError, f.RegisterErrorDescription = "invalid_client_metadata", "the AudD CLI client is unavailable right now"
		})
		_, err := otherServer(t, e, nil).Login(ctx(t), LoginOptions{})
		wantCode(t, err, "registration_refused", output.ExitAuth)
		if !strings.Contains(output.AsError(err).Message, "the AudD CLI client is unavailable right now") {
			t.Fatalf("message: %q", output.AsError(err).Message)
		}
		if n := e.as.Snapshot(); n.Register != 1 || n.DeviceAuthorize != 1 {
			t.Fatalf("no retry: %+v", n)
		}
	})
}

func TestAPIRequestNeverSent(t *testing.T) {
	e := newEnv(t)
	c := deviceClient(e)
	if _, err := c.Login(ctx(t), LoginOptions{Scopes: []string{"openid", "api:request", "usage:read"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stderr.String(), "Left out api:request") {
		t.Fatalf("note: %q", e.stderr.String())
	}
	// A saved grant that somehow holds it is not asked for again either.
	tok, _ := c.Stored()
	tok.Scopes = append(tok.Scopes, "api:request")
	b, _ := json.Marshal(tok)
	_ = e.sec.Set("default", "oauth", string(b))
	if _, err := deviceClient(e).EnsureScopes(ctx(t), "billing:pay"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client(false, nil, testutil.FollowAuthorize).Login(ctx(t), LoginOptions{Method: MethodBrowser, KeepGranted: true, Scopes: []string{"api:request"}}); err != nil {
		t.Fatal(err)
	}
	for _, s := range e.as.RequestedScopes() {
		if strings.Contains(s, "api:request") {
			t.Fatalf("api:request was sent: %q", e.as.RequestedScopes())
		}
	}
	if len(e.as.RequestedScopes()) != 3 {
		t.Fatalf("requests: %q", e.as.RequestedScopes())
	}
}

func TestForcedDeviceWithoutServerSupport(t *testing.T) {
	e := newEnv(t)
	e.as.Set(func(f *testutil.FakeOAuth) { f.NoDeviceEndpoint = true })
	_, err := deviceClient(e).Login(ctx(t), LoginOptions{Method: MethodDevice})
	wantCode(t, err, "device_unsupported", output.ExitAuth)
	// Chosen automatically, the browser method (with paste-back) is used.
	if _, err := e.client(false, nil, testutil.FollowAuthorize).Login(ctx(t), LoginOptions{}); err != nil {
		t.Fatal(err)
	}
}
