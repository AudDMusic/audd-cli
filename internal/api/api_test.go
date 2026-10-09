package api_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

const (
	oldToken = "0123456789abcdef0123456789abcdef"
	newToken = "fedcba9876543210fedcba9876543210"
)

// fakeAccount is the account backend from the login: it hands out the
// current API token.
type fakeAccount struct {
	account.Backend
	mu    sync.Mutex
	token string
	err   error
	calls int
}

func (f *fakeAccount) APIToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.token, f.err
}

func newApp(t *testing.T, acct *fakeAccount) (*app.App, *bytes.Buffer) {
	t.Helper()
	testutil.Isolate(t)
	a := app.New()
	a.Profile = &config.Profile{Name: "default"}
	a.Secrets = secrets.NewMemory()
	var errb bytes.Buffer
	a.Out = output.NewPrinter(nil, &errb, output.PrinterOptions{})
	a.APIClient = api.NewClientFactory(a)
	if acct != nil {
		a.Account = func() (account.Backend, error) { return acct, nil }
	}
	return a, &errb
}

func recognize(ctx context.Context, a *app.App) (*audd.Recognition, error) {
	return api.Do(ctx, a, func(c *audd.Client) (*audd.Recognition, error) {
		return c.RecognizeContext(ctx, "https://example.com/a.mp3", nil)
	})
}

func asErr(t *testing.T, err error) *output.Error {
	t.Helper()
	var oe *output.Error
	if !errors.As(err, &oe) {
		t.Fatalf("want *output.Error, got %T %v", err, err)
	}
	return oe
}

func TestNoToken(t *testing.T) {
	a, _ := newApp(t, nil)
	_, err := recognize(context.Background(), a)
	e := asErr(t, err)
	if e.Exit != output.ExitAuth || e.Code != "no_token" || e.Hint != api.NoTokenHint || !strings.Contains(e.Hint, "<your-api-token>") || !strings.Contains(e.Hint, "https://dashboard.audd.io") {
		t.Fatalf("%+v", e)
	}
}

func TestTokenPrecedenceReachesTheAPI(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(nil))
	a, _ := newApp(t, nil)
	_ = a.Secrets.Set("default", "login_api_token", "login-token")
	_ = a.Secrets.Set("default", "api_token", "config-token")
	t.Setenv("AUDD_API_TOKEN", "env-token")
	a.Flags.Token = "flag-token"
	for _, want := range []string{"flag-token", "env-token", "config-token", "login-token"} {
		if _, err := recognize(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		reqs := f.Requests()
		if got := reqs[len(reqs)-1].Token; got != want {
			t.Fatalf("token %q, want %q", got, want)
		}
		switch want {
		case "flag-token":
			a.Flags.Token = ""
		case "env-token":
			t.Setenv("AUDD_API_TOKEN", "")
		case "config-token":
			_ = a.Secrets.Delete("default", "api_token")
		}
	}
}

func TestUserAgentAndRewrite(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	var ua string
	f.On(testutil.EndpointEnterprise, func(r testutil.FakeRequest) (int, any) {
		return http.StatusOK, testutil.Success([]any{})
	})
	a, _ := newApp(t, nil)
	a.Flags.Token = oldToken
	hc := api.HTTPClient(a)
	req, _ := http.NewRequest("GET", "https://api.audd.io/getStreams/?api_token=secret", nil)
	req.Header.Set("User-Agent", "audd-go/1.5.20")
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Write([]byte(`{}`))
	})
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ua != "audd-cli/"+app.Version+" audd-go/1.5.20" {
		t.Fatalf("user agent %q", ua)
	}
}

func TestDebugLogRedactsToken(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	f.Reply("getStreams", testutil.Success([]any{}))
	a, errb := newApp(t, nil)
	a.Flags.Token = oldToken
	a.Flags.Debug = true
	hc := api.HTTPClient(a)
	resp, err := hc.Get("https://api.audd.io/getStreams/?api_token=" + oldToken)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	log := errb.String()
	if !strings.Contains(log, "debug: → GET") || !strings.Contains(log, "api_token=REDACTED") || strings.Contains(log, oldToken) {
		t.Fatalf("debug log: %q", log)
	}
}

func TestSelfHealLoginToken(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	f.AcceptTokens(newToken)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	acct := &fakeAccount{token: newToken}
	a, errb := newApp(t, acct)
	_ = a.Secrets.Set("default", "login_api_token", oldToken)

	rec, err := recognize(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Artist != "Imagine Dragons" {
		t.Fatalf("%+v", rec)
	}
	if got, _ := a.Secrets.Get("default", "login_api_token"); got != newToken {
		t.Fatalf("stored token %q", got)
	}
	if acct.calls != 1 || f.Count(testutil.EndpointRecognize) != 2 {
		t.Fatalf("account calls %d, API calls %d", acct.calls, f.Count(testutil.EndpointRecognize))
	}
	if !strings.Contains(errb.String(), "fetched the current one") {
		t.Fatalf("note: %q", errb.String())
	}
	// Next run uses the stored token directly.
	if _, err := recognize(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if acct.calls != 1 {
		t.Fatal("should not heal again")
	}
}

func TestSelfHealStopsAfterSecondRejection(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	f.AcceptTokens("only-this-one-works")
	acct := &fakeAccount{token: newToken}
	a, _ := newApp(t, acct)
	_ = a.Secrets.Set("default", "login_api_token", oldToken)
	_, err := recognize(context.Background(), a)
	e := asErr(t, err)
	if e.Exit != output.ExitAuth || e.Code != "token_rejected" || e.APICode != 900 || e.Hint != "audd login" {
		t.Fatalf("%+v", e)
	}
	if acct.calls != 1 || len(f.Requests()) != 2 {
		t.Fatalf("account calls %d, API calls %d", acct.calls, len(f.Requests()))
	}
}

func TestSelfHealNeverReplacesUserTokens(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	f.AcceptTokens(newToken)
	acct := &fakeAccount{token: newToken}
	a, _ := newApp(t, acct)
	t.Setenv("AUDD_API_TOKEN", oldToken)
	_, err := recognize(context.Background(), a)
	e := asErr(t, err)
	if e.Exit != output.ExitAuth || !strings.Contains(e.Hint, "AUDD_API_TOKEN") {
		t.Fatalf("%+v", e)
	}
	if acct.calls != 0 || len(f.Requests()) != 1 {
		t.Fatal("env tokens must not be healed")
	}
}

func TestSelfHealRevokedLogin(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	f.AcceptTokens(newToken)
	acct := &fakeAccount{err: output.Errf(output.ExitAuth, "login_required", "audd login", "your login has expired")}
	a, _ := newApp(t, acct)
	_ = a.Secrets.Set("default", "login_api_token", oldToken)
	_, err := recognize(context.Background(), a)
	e := asErr(t, err)
	if e.Code != "login_required" || e.Exit != output.ExitAuth {
		t.Fatalf("%+v", e)
	}
	if len(f.Requests()) != 1 {
		t.Fatal("no retry without a new token")
	}
}

func TestConcurrentHealFetchesOnce(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	f.AcceptTokens(newToken)
	f.Reply(testutil.EndpointRecognize, testutil.Success(nil))
	acct := &fakeAccount{token: newToken}
	a, _ := newApp(t, acct)
	_ = a.Secrets.Set("default", "login_api_token", oldToken)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := recognize(context.Background(), a)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if acct.calls != 1 {
		t.Fatalf("account asked %d times", acct.calls)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		code int
		exit int
		name string
	}{
		{900, output.ExitAuth, "token_rejected"},
		{901, output.ExitAuth, "token_rejected"},
		{902, output.ExitQuota, "quota_exceeded"},
		{903, output.ExitAuth, "token_rejected"},
		{904, output.ExitQuota, "not_enabled"},
		{611, output.ExitQuota, "rate_limited"},
		{19, output.ExitQuota, "blocked"},
		{31337, output.ExitQuota, "blocked"},
		{300, output.ExitUsage, "invalid_audio"},
		{400, output.ExitUsage, "invalid_audio"},
		{700, output.ExitUsage, "invalid_request"},
		{1000, output.ExitUsage, "invalid_request"},
		{100, output.ExitNetwork, "server"},
	}
	for _, tt := range tests {
		f := testutil.NewFakeAPI(t)
		f.Reply(testutil.EndpointRecognize, testutil.APIError(tt.code, "server says no"))
		a, _ := newApp(t, nil)
		a.Flags.Token = oldToken
		_, err := recognize(context.Background(), a)
		e := asErr(t, err)
		if e.Exit != tt.exit || e.Code != tt.name || e.APICode != tt.code || e.Message != "server says no" {
			t.Errorf("code %d: %+v", tt.code, e)
		}
		if e.Retryable != (tt.name == "server" || tt.name == "rate_limited") {
			t.Errorf("code %d: retryable=%v", tt.code, e.Retryable)
		}
		if tt.code == 900 && !strings.Contains(e.Hint, "--token") {
			t.Errorf("flag token hint: %q", e.Hint)
		}
	}
	// Connection failures are network errors.
	a, _ := newApp(t, nil)
	a.Flags.Token = oldToken
	t.Setenv("AUDD_API_BASE_URL", "http://127.0.0.1:1")
	_, err := recognize(context.Background(), a)
	if e := asErr(t, err); e.Exit != output.ExitNetwork || e.Code != "network" || !e.Retryable {
		t.Fatalf("%+v", e)
	}
	// Cancelled runs report interruption.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = recognize(ctx, a)
	if e := asErr(t, err); e.Code != "interrupted" {
		t.Fatalf("%+v", e)
	}
}

func TestCustomCatalogAccessError(t *testing.T) {
	f := testutil.NewFakeAPI(t)
	f.Reply(testutil.EndpointUpload, testutil.APIError(904, "not allowed"))
	a, _ := newApp(t, nil)
	a.Flags.Token = oldToken
	_, err := api.Do(context.Background(), a, func(c *audd.Client) (struct{}, error) {
		return struct{}{}, c.CustomCatalog().AddContext(context.Background(), 1, "https://example.com/a.mp3")
	})
	e := asErr(t, err)
	if e.Exit != output.ExitQuota || e.Code != "not_enabled" || !strings.Contains(e.Hint, "audd recognize") {
		t.Fatalf("%+v", e)
	}
	if f.Count(testutil.EndpointUpload) != 1 {
		t.Fatal("custom catalog uploads are never retried")
	}
}

func TestPreUploadUnreachableProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	proxy, _ := url.Parse("http://" + closed)
	hc := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	_, err = hc.Get("https://api.audd.io/")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !api.PreUpload(err) {
		t.Fatalf("an unreachable proxy means nothing was sent: %#v", err)
	}
}
