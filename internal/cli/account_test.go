package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/cli"
	"github.com/AudDMusic/audd-cli/internal/oauth"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// accountEnv isolates the CLI and points it at fake sign-in and account servers.
func accountEnv(t *testing.T) (*testutil.FakeOAuth, *testutil.FakeMCP) {
	t.Helper()
	testutil.Isolate(t)
	as := testutil.NewFakeOAuth(t)
	mcp := testutil.NewFakeMCP(t, as)
	t.Setenv("AUDD_MCP_URL", mcp.URL())
	setBrowser(t, testutil.FollowAuthorize)
	return as, mcp
}

var browserMu sync.Mutex

func setBrowser(t *testing.T, f func(string) error) {
	t.Helper()
	browserMu.Lock()
	prev := oauth.OpenBrowser
	oauth.OpenBrowser = f
	browserMu.Unlock()
	t.Cleanup(func() {
		browserMu.Lock()
		oauth.OpenBrowser = prev
		browserMu.Unlock()
	})
}

func mustRun(t *testing.T, args ...string) testutil.Result {
	t.Helper()
	r := run(t, args...)
	if r.Code != 0 {
		t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, r.Code, r.Stdout, r.Stderr)
	}
	return r
}

func login(t *testing.T) {
	t.Helper()
	mustRun(t, "login")
}

// loginUnticked signs in with the given permissions unticked on the
// approval screen, so commands that need them must ask again (step-up).
func loginUnticked(t *testing.T, as *testutil.FakeOAuth, scopes ...string) {
	t.Helper()
	as.Set(func(f *testutil.FakeOAuth) { f.Untick = scopes })
	login(t)
	as.Set(func(f *testutil.FakeOAuth) { f.Untick = nil })
}

func wantExit(t *testing.T, r testutil.Result, exit int, code string) {
	t.Helper()
	if r.Code != exit || !strings.Contains(r.Stderr, `"code":"`+code+`"`) {
		t.Fatalf("want exit %d %s, got %d\nstdout: %s\nstderr: %s", exit, code, r.Code, r.Stdout, r.Stderr)
	}
}

func TestLoginStatusTokenLogout(t *testing.T) {
	as, _ := accountEnv(t)
	r := mustRun(t, "login")
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("piped login prints login_pending then the result as JSONL: %q", r.Stdout)
	}
	var pending, result map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &pending)
	_ = json.Unmarshal([]byte(lines[1]), &result)
	if pending["type"] != "login_pending" || pending["method"] != "device" || pending["verification_uri"] != as.URL()+"/device" ||
		pending["user_code"] == "" || pending["expires_in_seconds"].(float64) != 900 {
		t.Fatalf("pending: %s", lines[0])
	}
	if n := as.Snapshot(); n.Register != 0 || n.DeviceSuccess != 1 {
		t.Fatalf("signs in as audd-cli with a code: %+v", n)
	}
	if result["type"] != "result" || result["account"] != "user@example.com" || result["api_token_saved"] != true || result["token_source"] != "login" {
		t.Fatalf("result: %s", lines[1])
	}

	r = mustRun(t, "auth", "status")
	var st map[string]any
	_ = json.Unmarshal([]byte(r.Stdout), &st)
	if st["signed_in"] != true || st["account"] != "user@example.com" || st["token_source"] != "login" || st["api_token"] != "0123…cdef" {
		t.Fatalf("status: %s", r.Stdout)
	}
	if strings.Contains(r.Stdout, testutil.FakeAPIToken) {
		t.Fatal("status leaked the token")
	}
	r = mustRun(t, "whoami", "--format", "table")
	if !strings.Contains(r.Stdout, "user@example.com") || !strings.Contains(r.Stdout, "0123…cdef (fetched by audd login)") ||
		!strings.Contains(r.Stdout, "Access expires") || !strings.Contains(r.Stdout, "renewed automatically") {
		t.Fatalf("whoami: %q", r.Stdout)
	}

	r = mustRun(t, "token", "show")
	if !strings.Contains(r.Stdout, `"token":"0123…cdef"`) || !strings.Contains(r.Stdout, `"masked":true`) || !strings.Contains(r.Stdout, `"source":"login"`) {
		t.Fatalf("token show: %s", r.Stdout)
	}
	r = mustRun(t, "token", "show", "--reveal", "--format", "table", "--quiet")
	if r.Stdout != testutil.FakeAPIToken+"\n" {
		t.Fatalf("reveal: %q", r.Stdout)
	}
	// A user-set token wins and survives logout.
	mustRun(t, "config", "set", "token", "your-api-token")
	r = mustRun(t, "token", "show")
	if !strings.Contains(r.Stdout, `"source":"config"`) {
		t.Fatalf("config token should take precedence: %s", r.Stdout)
	}
	if r = mustRun(t, "token", "show", "--format", "table"); !strings.Contains(r.Stderr, "Set with audd config set token.") {
		t.Fatalf("source sentence: %q", r.Stderr)
	}

	r = mustRun(t, "logout")
	if !strings.Contains(r.Stdout, `"was_signed_in":true`) || !strings.Contains(r.Stdout, `"kept_api_token":true`) {
		t.Fatalf("logout: %s", r.Stdout)
	}
	if n := len(as.RevokedTokens()); n != 1 {
		t.Fatalf("revoked %d tokens", n)
	}
	r = mustRun(t, "token", "show", "--reveal")
	if !strings.Contains(r.Stdout, `"token":"your-api-token"`) {
		t.Fatalf("user token kept: %s", r.Stdout)
	}
	mustRun(t, "config", "unset", "token")
	wantExit(t, run(t, "token", "show"), output.ExitAuth, "no_token")
	wantExit(t, run(t, "auth", "status"), output.ExitAuth, "no_token")
}

func TestAccountCommandsNeedLogin(t *testing.T) {
	accountEnv(t)
	for _, args := range [][]string{{"account"}, {"usage"}, {"billing", "plans"}, {"billing", "renew"}, {"token", "rotate", "--yes"}, {"token", "refresh-local"}} {
		r := run(t, args...)
		wantExit(t, r, output.ExitAuth, "login_required")
		if !strings.Contains(r.Stderr, `"hint":"audd login"`) {
			t.Fatalf("%v: %s", args, r.Stderr)
		}
	}
}

func TestAccountGoldens(t *testing.T) {
	accountEnv(t)
	login(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"account", []string{"account"}},
		{"usage", []string{"usage"}},
		{"billing_plans", []string{"billing", "plans"}},
		{"billing_history", []string{"billing", "history"}},
		{"billing_owed", []string{"billing", "owed"}},
	} {
		for _, f := range []string{"table", "json"} {
			r := mustRun(t, append(tc.args, "--format", f)...)
			testutil.Golden(t, tc.name+"_"+f, []byte(r.Stdout))
		}
	}
}

func TestUsageCheck(t *testing.T) {
	_, mcp := accountEnv(t)
	login(t)
	mustRun(t, "usage", "--check", "--min-remaining", "1000", "--days", "2")
	if c := mcp.CallsTo("get_usage_stats"); c[len(c)-1].Args["days"] != float64(2) {
		t.Fatalf("days not passed: %v", c)
	}
	r := run(t, "usage", "--check", "--min-remaining", "100000", "--quiet", "--format", "table")
	if r.Code != output.ExitThreshold || r.Stdout != "4,000 remaining\n" || !strings.Contains(r.Stderr, "below the threshold of 100,000") {
		t.Fatalf("threshold: %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	r = mustRun(t, "usage", "--debug")
	if !strings.Contains(r.Stderr, "debug: POST "+mcp.URL()) || strings.Contains(r.Stderr, "access-") {
		t.Fatalf("--debug logs requests without tokens: %q", r.Stderr)
	}
	wantExit(t, run(t, "usage", "--check"), output.ExitUsage, "invalid_argument")
	wantExit(t, run(t, "usage", "--min-remaining", "100000"), output.ExitUsage, "invalid_argument")
	wantExit(t, run(t, "usage", "--days", "0"), output.ExitUsage, "invalid_argument")
}

func TestRejectedAccessTokenIsRefreshed(t *testing.T) {
	as, _ := accountEnv(t)
	login(t)
	as.DropAccess()
	r := mustRun(t, "account", "--format", "table")
	if !strings.Contains(r.Stdout, "user@example.com") {
		t.Fatalf("account: %q", r.Stdout)
	}
	as.RevokeAll()
	wantExit(t, run(t, "account"), output.ExitAuth, "login_required")
}

func TestUsageCheckWithUnknownRemaining(t *testing.T) {
	_, mcp := accountEnv(t)
	login(t)
	mcp.SetTool(testutil.FakeTool{Name: "get_usage_stats", Scope: "usage:read",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"days": map[string]any{"type": "integer"}}},
		OutputSchema: testutil.FakeOutputSchemas["get_usage_stats"],
		Handle: func(map[string]any) (map[string]any, string, bool) {
			return map[string]any{"used_this_cycle": 10, "daily": []any{map[string]any{"date": "2026-10-08", "requests": 10}}}, "", false
		}})
	r := run(t, "usage", "--check", "--min-remaining", "1", "--quiet", "--format", "table")
	if r.Code != output.ExitUnexpected || r.Stdout != "unknown remaining\n" || !strings.Contains(r.Stderr, "did not report how many requests remain") {
		t.Fatalf("unknown remaining: %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	r = mustRun(t, "usage", "--format", "table")
	if !strings.Contains(r.Stdout, "Remaining  unknown") {
		t.Fatalf("human view: %q", r.Stdout)
	}
}

func TestBillingPaymentLinks(t *testing.T) {
	as, mcp := accountEnv(t)
	loginUnticked(t, as, "billing:pay")
	wantExit(t, run(t, "billing", "buy", "1500"), output.ExitUsage, "invalid_argument")
	if len(mcp.CallsTo("buy_bonus_requests")) != 0 {
		t.Fatal("invalid amounts must not reach the server")
	}
	before := as.Snapshot().DeviceAuthorize
	r := mustRun(t, "billing", "subscribe", "pro_plan")
	// The permission prompt makes the output a stream: login_pending, then
	// the payment link as a typed result line.
	lines := jsonLines(t, r.Stdout)
	if len(lines) != 2 || lines[0]["type"] != "login_pending" || lines[1]["type"] != "result" {
		t.Fatalf("subscribe with a permission prompt: %s", r.Stdout)
	}
	doc := lines[1]
	if doc["url"] != "https://checkout.stripe.com/c/pay/cs_test_example" || doc["charged"] != false || doc["schema_version"] != float64(1) {
		t.Fatalf("subscribe: %s", r.Stdout)
	}
	// Without a prompt the output is one JSON document.
	r = mustRun(t, "billing", "renew")
	if lines := jsonLines(t, r.Stdout); len(lines) != 1 || lines[0]["type"] != nil || lines[0]["url"] == nil {
		t.Fatalf("renew: %s", r.Stdout)
	}
	if as.Snapshot().DeviceAuthorize != before+1 {
		t.Fatal("first payment link should ask for billing:pay")
	}
	r = mustRun(t, "billing", "buy", "10,000", "--format", "table")
	if !strings.Contains(r.Stdout, "Amount: $50.00 for 10,000 requests") || !strings.Contains(r.Stdout, "nothing is charged") {
		t.Fatalf("buy: %q", r.Stdout)
	}
	if as.Snapshot().DeviceAuthorize != before+1 {
		t.Fatal("billing:pay is granted now; no second consent")
	}
	if c := mcp.CallsTo("buy_bonus_requests"); len(c) != 1 || c[0].Args["requests"] != float64(10000) {
		t.Fatalf("buy args: %v", c)
	}
	var opened []string
	setBrowser(t, func(u string) error { opened = append(opened, u); return nil })
	r = mustRun(t, "billing", "renew", "--open", "--quiet", "--format", "table")
	if r.Stdout != "https://checkout.stripe.com/c/pay/cs_test_example\n" || len(opened) != 1 || opened[0] != "https://checkout.stripe.com/c/pay/cs_test_example" {
		t.Fatalf("renew --open: %q %v", r.Stdout, opened)
	}
	r = run(t, "billing", "subscribe", "nope")
	wantExit(t, r, output.ExitUsage, "invalid_argument")
	if !strings.Contains(r.Stderr, "indie_plan_outside, startup_plan, pro_plan") {
		t.Fatalf("unknown plan lists the valid ones: %s", r.Stderr)
	}
	if c := mcp.CallsTo("subscribe_to_plan"); len(c) != 1 || c[0].Args["plan"] != "pro_plan" {
		t.Fatalf("subscribe calls: %v", c)
	}
	// Plan keys match regardless of case.
	mustRun(t, "billing", "subscribe", "Startup_Plan")
	if c := mcp.CallsTo("subscribe_to_plan"); len(c) != 2 || c[1].Args["plan"] != "startup_plan" {
		t.Fatalf("subscribe calls: %v", c)
	}
}

func TestDefaultLoginCoversRotateAndPaymentLinks(t *testing.T) {
	as, mcp := accountEnv(t)
	login(t)
	if got := as.RequestedScopes(); len(got) != 1 || !strings.Contains(got[0], "billing:pay") || !strings.Contains(got[0], "token:write") || strings.Contains(got[0], "api:request") {
		t.Fatalf("default login requested: %q", got)
	}
	before := as.Snapshot()
	t.Setenv("AUDD_API_TOKEN", "")
	r := mustRun(t, "token", "rotate", "--yes")
	if lines := jsonLines(t, r.Stdout); len(lines) != 1 || lines[0]["type"] != nil || lines[0]["token"] == nil {
		t.Fatalf("rotate without a permission prompt is one document: %q", r.Stdout)
	}
	r = mustRun(t, "billing", "subscribe", "pro_plan")
	if lines := jsonLines(t, r.Stdout); len(lines) != 1 || lines[0]["type"] != nil || lines[0]["url"] == nil {
		t.Fatalf("subscribe without a permission prompt is one document: %q", r.Stdout)
	}
	if strings.Contains(r.Stderr, "login_pending") {
		t.Fatalf("no second approval: %q", r.Stderr)
	}
	after := as.Snapshot()
	if after.DeviceAuthorize != before.DeviceAuthorize || after.Authorize != before.Authorize || len(as.RequestedScopes()) != 1 {
		t.Fatalf("everything was granted at login; no step-up: %+v -> %+v", before, after)
	}
	if len(mcp.CallsTo("rotate_api_token")) != 1 || len(mcp.CallsTo("subscribe_to_plan")) != 1 {
		t.Fatal("both commands reach the server")
	}
}

func ioPipe(t *testing.T) (*io.PipeReader, *io.PipeWriter) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	return pr, pw
}

// jsonLines parses stdout as one JSON value per line.
func jsonLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not one JSON value per line: %q", s)
		}
		out = append(out, m)
	}
	return out
}

func TestTokenRotateUnknownOutcome(t *testing.T) {
	_, mcp := accountEnv(t)
	login(t)
	mcp.SetTool(testutil.FakeTool{Name: "rotate_api_token", Scope: "token:write",
		InputSchema: map[string]any{"type": "object"}, OutputSchema: testutil.FakeOutputSchemas["rotate_api_token"],
		Handle: func(map[string]any) (map[string]any, string, bool) {
			return nil, "the service is temporarily unavailable", true
		}})
	r := run(t, "token", "rotate", "--yes")
	wantExit(t, r, output.ExitNetwork, "server")
	if !strings.Contains(r.Stderr, "may already have been rotated") || !strings.Contains(r.Stderr, `"hint":"audd token refresh-local"`) || !strings.Contains(r.Stderr, `"retryable":false`) {
		t.Fatalf("rotate failure should explain recovery: %s", r.Stderr)
	}
}

func TestTokenRotate(t *testing.T) {
	as, mcp := accountEnv(t)
	loginUnticked(t, as, "token:write")
	wantExit(t, run(t, "token", "rotate"), output.ExitSafety, "confirmation_required")
	if len(mcp.CallsTo("rotate_api_token")) != 0 {
		t.Fatal("rotated without confirmation")
	}
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	r := mustRun(t, "token", "rotate", "--yes")
	lines := jsonLines(t, r.Stdout)
	if len(lines) != 2 || lines[0]["type"] != "login_pending" || lines[1]["type"] != "result" ||
		lines[1]["token"] != "your…oken" || lines[1]["masked"] != true {
		t.Fatalf("rotate: %q", r.Stdout)
	}
	if !strings.Contains(r.Stderr, "AUDD_API_TOKEN environment variable still holds the old token") {
		t.Fatalf("rotate: %q", r.Stderr)
	}
	t.Setenv("AUDD_API_TOKEN", "")
	r = mustRun(t, "token", "show", "--reveal")
	if !strings.Contains(r.Stdout, testutil.FakeRotatedToken) {
		t.Fatalf("new token saved: %s", r.Stdout)
	}
	r = mustRun(t, "token", "refresh-local")
	if !strings.Contains(r.Stdout, `"token":"0123…cdef"`) {
		t.Fatalf("refresh-local: %s", r.Stdout)
	}
}

func TestLoginCompleteFromAnotherInvocation(t *testing.T) {
	accountEnv(t)
	urls := make(chan string, 1)
	setBrowser(t, func(u string) error { urls <- u; return nil })
	done := make(chan testutil.Result, 1)
	go func() { done <- run(t, "auth", "login", "--browser") }()
	var authURL string
	select {
	case authURL = <-urls:
	case <-time.After(10 * time.Second):
		t.Fatal("no sign-in URL")
	}
	redirect, err := testutil.AuthorizeRedirect(authURL)
	if err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, "auth", "login", "--complete", redirect)
	if !strings.Contains(r.Stdout, `"account":"user@example.com"`) {
		t.Fatalf("complete: %s", r.Stdout)
	}
	select {
	case first := <-done:
		if first.Code != 0 || !strings.Contains(first.Stdout, `"type":"login_pending"`) {
			t.Fatalf("waiting login: %d %s %s", first.Code, first.Stdout, first.Stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting login did not notice the completed sign-in")
	}
	wantExit(t, run(t, "auth", "login", "--complete", redirect), output.ExitAuth, "no_pending_login")
}

func TestLoginPasteOnTerminal(t *testing.T) {
	accountEnv(t)
	pr, pw := ioPipe(t)
	setBrowser(t, func(u string) error {
		red, err := testutil.AuthorizeRedirect(u)
		if err != nil {
			return err
		}
		_, err = pw.Write([]byte(red + "\n"))
		return err
	})
	var out, errb bytes.Buffer
	code := cli.Run(context.Background(), []string{"login", "--browser"}, cli.IO{In: pr, Out: &out, Err: &errb, StdinTTY: true, StdoutTTY: true, StderrTTY: true})
	if code != 0 || !strings.Contains(out.String(), "Signed in as user@example.com (profile default).") || !strings.Contains(errb.String(), "paste it here") {
		t.Fatalf("%d\n%s\n%s", code, out.String(), errb.String())
	}
}

func TestAuthSwitchAndProfiles(t *testing.T) {
	accountEnv(t)
	mustRun(t, "login", "--profile", "work")
	// The default profile is not signed in and has no token: exit 3.
	r := run(t, "auth", "status")
	if r.Code != output.ExitAuth || !strings.Contains(r.Stdout, `"signed_in":false`) {
		t.Fatalf("default profile: %d %s", r.Code, r.Stdout)
	}
	if r := mustRun(t, "auth", "switch", "work"); !strings.Contains(r.Stdout, `"existing":true`) {
		t.Fatalf("a signed-in profile exists: %s", r.Stdout)
	}
	if r := mustRun(t, "auth", "switch", "default"); !strings.Contains(r.Stdout, `"existing":true`) {
		t.Fatalf("the default profile exists: %s", r.Stdout)
	}
	if r := mustRun(t, "auth", "switch", "brand-new"); !strings.Contains(r.Stdout, `"existing":false`) {
		t.Fatalf("an unused profile is new: %s", r.Stdout)
	}
	mustRun(t, "auth", "switch", "work")
	r = mustRun(t, "auth", "status")
	if !strings.Contains(r.Stdout, `"profile":"work"`) || !strings.Contains(r.Stdout, `"signed_in":true`) {
		t.Fatalf("after switch: %s", r.Stdout)
	}
	r = mustRun(t, "auth", "logout", "--all")
	if !strings.Contains(r.Stdout, `"profile":"work","was_signed_in":true`) {
		t.Fatalf("logout --all: %s", r.Stdout)
	}
	wantExit(t, run(t, "auth", "switch", "bad/name"), output.ExitUsage, "invalid_argument")
	if r := run(t, "login", "--device", "--browser"); r.Code != output.ExitUsage {
		t.Fatalf("--device and --browser together: exit %d %s", r.Code, r.Stderr)
	}
}

func TestLoginLeavesOutAPIRequest(t *testing.T) {
	as, _ := accountEnv(t)
	r := mustRun(t, "login", "--scopes", "api:request,billing:pay")
	if !strings.Contains(r.Stderr, "Left out api:request") {
		t.Fatalf("note: %q", r.Stderr)
	}
	for _, s := range as.RequestedScopes() {
		if strings.Contains(s, "api:request") || !strings.Contains(s, "billing:pay") {
			t.Fatalf("requested: %q", s)
		}
	}
}

func TestStatusShowsUntickedScopes(t *testing.T) {
	as, _ := accountEnv(t)
	as.Set(func(f *testutil.FakeOAuth) { f.Untick = []string{"billing:read"} })
	mustRun(t, "login")
	r := mustRun(t, "auth", "status")
	var st map[string]any
	_ = json.Unmarshal([]byte(r.Stdout), &st)
	if strings.Contains(fmt.Sprint(st["scopes"]), "billing:read") || fmt.Sprint(st["scopes_not_granted"]) != "[billing:read]" {
		t.Fatalf("status: %s", r.Stdout)
	}
	r = mustRun(t, "auth", "status", "--format", "table")
	if !strings.Contains(r.Stdout, "Not approved") || !strings.Contains(r.Stdout, "audd auth refresh --scopes billing:read") {
		t.Fatalf("table: %q", r.Stdout)
	}
}

func TestStepUpExplainsUntickedScope(t *testing.T) {
	as, _ := accountEnv(t)
	as.Set(func(f *testutil.FakeOAuth) { f.Untick = []string{"billing:pay"} })
	mustRun(t, "login", "--scopes", "billing:pay")
	r := run(t, "billing", "subscribe", "pro_plan")
	if !strings.Contains(r.Stderr, "create payment links (billing:pay)") || !strings.Contains(r.Stderr, "unticked") {
		t.Fatalf("prompt: %q", r.Stderr)
	}
	wantExit(t, r, output.ExitAuth, "scope_not_granted")
}

func TestLoginWithCSVFormatPrintsURL(t *testing.T) {
	as, _ := accountEnv(t)
	r := mustRun(t, "login", "--format", "csv")
	if !strings.Contains(r.Stderr, `"type":"login_pending"`) || !strings.Contains(r.Stderr, `"verification_uri":"`+as.URL()+`/device"`) {
		t.Fatalf("the sign-in URL must be printed: %q", r.Stderr)
	}
	if !strings.HasPrefix(r.Stdout, "profile,") || strings.Contains(r.Stdout, "login_pending") {
		t.Fatalf("stdout carries only the CSV result: %q", r.Stdout)
	}
}

func TestStepUpWithExplicitJSONKeepsOneDocument(t *testing.T) {
	as, _ := accountEnv(t)
	loginUnticked(t, as, "billing:pay")
	r := mustRun(t, "billing", "subscribe", "pro_plan", "--format", "json")
	if lines := jsonLines(t, r.Stdout); len(lines) != 1 || lines[0]["url"] == nil {
		t.Fatalf("explicit json gives one document: %q", r.Stdout)
	}
	if !strings.Contains(r.Stderr, `"type":"login_pending"`) {
		t.Fatalf("the sign-in URL goes to stderr: %q", r.Stderr)
	}
}

func TestLogoutStopsTheRecorderThatUsedTheLoginToken(t *testing.T) {
	accountEnv(t)
	mustRun(t, "login")
	held, err := streams.Acquire("default")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(held.Release)
	if err := held.MarkBackground(); err != nil {
		t.Fatal(err)
	}
	terminated := 0
	defer streams.SetTerminateForTesting(func(pid int) error {
		terminated++
		held.Release()
		return nil
	})()
	r := mustRun(t, "logout", "--format", "table")
	if terminated != 1 || !strings.Contains(r.Stderr, "Stopped the background stream recorder for profile default") {
		t.Fatalf("recorder stopped %d times; stderr %q", terminated, r.Stderr)
	}
	if running, _, _, _ := streams.Status("default"); running {
		t.Fatal("recorder still running")
	}
}

func TestLogoutKeepsTheRecorderWithAConfigToken(t *testing.T) {
	accountEnv(t)
	mustRun(t, "login")
	mustRun(t, "config", "set", "token", "your-api-token")
	held, err := streams.Acquire("default")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(held.Release)
	if err := held.MarkBackground(); err != nil {
		t.Fatal(err)
	}
	defer streams.SetTerminateForTesting(func(pid int) error {
		t.Error("the recorder uses the config token and keeps running")
		return nil
	})()
	r := mustRun(t, "logout")
	if !strings.Contains(r.Stdout, `"stopped_recorder":false`) {
		t.Fatalf("logout: %s", r.Stdout)
	}
}
