package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-go"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/streams/streamstest"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// streamsFake, when set, is the API every command in this test binary talks to.
var streamsFake *streamstest.Server

// streamsAccount, when set with streamsFake, makes the API token resolve
// as usual (flag, env, config, login) and the account backend hand out
// its token, so token healing can be tested.
var streamsAccount *fakeTokenAccount

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		srv := streamsFake
		if srv == nil {
			return
		}
		a.APIClient = func() (*audd.Client, error) { return srv.Client(), nil }
		streams.LongpollHTTPClient = srv.HTTPClient()
		if acct := streamsAccount; acct != nil {
			a.APIClient = func() (*audd.Client, error) {
				tok, _, err := config.ResolveToken(a.Flags.Token, a.Profile.Name, a.Secrets)
				if err != nil {
					return nil, err
				}
				return srv.ClientWithToken(tok), nil
			}
			a.Account = func() (account.Backend, error) { return acct, nil }
		}
	})
}

type fakeTokenAccount struct {
	account.Backend
	mu    sync.Mutex
	token string
	calls int
}

func (f *fakeTokenAccount) APIToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.token, nil
}

// setupStreamsLogin is setupStreams with the token stored by audd login
// set to old, and an account whose current token is the fake's.
func setupStreamsLogin(t *testing.T, old string) (*streamstest.Server, *fakeTokenAccount) {
	t.Helper()
	srv := setupStreams(t)
	t.Setenv("AUDD_API_TOKEN", "")
	acct := &fakeTokenAccount{token: streamstest.Token}
	streamsAccount = acct
	t.Cleanup(func() { streamsAccount = nil })
	if err := secrets.Default().Set("default", "login_api_token", old); err != nil {
		t.Fatal(err)
	}
	return srv, acct
}

func TestStreamsListHealsALoginToken(t *testing.T) {
	srv, acct := setupStreamsLogin(t, "fedcba9876543210fedcba9876543210")
	srv.SetStreams(streamstest.Stream{RadioID: 1, URL: "https://radio.example/1", Running: true})
	r := runStreams(t, "streams", "list")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stderr, "fetched the current one") {
		t.Fatalf("heal note: %q", r.Stderr)
	}
	if got, _ := secrets.Default().Get("default", "login_api_token"); got != streamstest.Token {
		t.Fatalf("stored token %q", got)
	}
	if acct.calls != 1 {
		t.Fatalf("account asked %d times", acct.calls)
	}
}

func TestStreamsNeverReplaceFlagOrEnvTokens(t *testing.T) {
	srv, acct := setupStreamsLogin(t, streamstest.Token)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	r := runStreams(t, "streams", "list", "--token", "wrongtoken")
	if r.Code != output.ExitAuth || !strings.Contains(r.Stderr, "check the --token value") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	t.Setenv("AUDD_API_TOKEN", "wrongtoken")
	r = runStreams(t, "streams", "list")
	if r.Code != output.ExitAuth || !strings.Contains(r.Stderr, "check AUDD_API_TOKEN") {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if acct.calls != 0 {
		t.Fatalf("account asked %d times", acct.calls)
	}
	if got, _ := secrets.Default().Get("default", "login_api_token"); got != streamstest.Token {
		t.Fatalf("stored token changed to %q", got)
	}
}

func setupStreams(t *testing.T) *streamstest.Server {
	t.Helper()
	testutil.Isolate(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	srv := streamstest.New(t)
	streamsFake = srv
	oldURL := streams.RecentResultsURL
	streams.RecentResultsURL = srv.URL + "/lastSong/getChannelById/"
	oldLP := streams.LongpollHTTPClient
	t.Cleanup(func() {
		streamsFake = nil
		streams.RecentResultsURL = oldURL
		streams.LongpollHTTPClient = oldLP
	})
	return srv
}

func runStreams(t *testing.T, args ...string) testutil.Result {
	t.Helper()
	return testutil.Exec(t, Main, "", args...)
}

func seedStore(t *testing.T, plays ...streamstore.Play) {
	t.Helper()
	st, err := streamstore.Open("default")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, p := range plays {
		if _, err := st.AddPlay(p); err != nil {
			t.Fatal(err)
		}
	}
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %q (%v)", s, err)
	}
	if m["schema_version"] != float64(1) {
		t.Fatalf("schema_version missing: %s", s)
	}
	return m
}

func TestStreamsListJSONAndTable(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, URL: "https://radio.example/1", Running: true},
		streamstest.Stream{RadioID: 2, URL: "https://radio.example/2", Running: false})
	seedStore(t, streamstore.Play{RadioID: 1, Timestamp: time.Now().Add(-3 * time.Minute), Artist: "Artist", Title: "Song", Score: 100})

	r := runStreams(t, "streams", "list")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	items := doc["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items %v", items)
	}
	first := items[0].(map[string]any)
	if first["radio_id"] != float64(1) || first["stream_running"] != true || first["longpoll_category"] != streamstest.Category(1) {
		t.Fatalf("first %v", first)
	}
	if lp, ok := first["last_play"].(map[string]any); !ok || lp["title"] != "Song" {
		t.Fatalf("last play from the store: %v", first["last_play"])
	}
	if items[1].(map[string]any)["last_play"] != nil {
		t.Fatal("no plays → null")
	}

	r = runStreams(t, "streams", "list", "--format", "table")
	for _, want := range []string{"ID", "STATUS", "running", "stopped", "Artist — Song (started 3 min ago)", "https://radio.example/2"} {
		if !strings.Contains(r.Stdout, want) {
			t.Fatalf("table lacks %q:\n%s", want, r.Stdout)
		}
	}
	srv.SetStreams()
	r = runStreams(t, "streams", "list", "--format", "table")
	if !strings.Contains(r.Stdout, "audd streams add <url> --id 1") {
		t.Fatalf("empty list hint: %q", r.Stdout)
	}
}

func TestStreamsAddRemoveSetURL(t *testing.T) {
	srv := setupStreams(t)
	if r := runStreams(t, "streams", "add", "https://radio.example/a"); r.Code != output.ExitUsage {
		t.Fatalf("--id is required: %d %s", r.Code, r.Stderr)
	}
	srv.SetNoCallbackURL()
	r := runStreams(t, "streams", "add", "https://radio.example/a", "--id", "5", "--start")
	if r.Code != 0 {
		t.Fatalf("add: %d %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	if doc["radio_id"] != float64(5) || doc["added"] != true {
		t.Fatalf("add result %v", doc)
	}
	if !strings.Contains(r.Stderr, "audd streams callback set https://audd.tech/empty/") {
		t.Fatalf("a missing callback URL is pointed out: %q", r.Stderr)
	}
	var addCall streamstest.Call
	for _, c := range srv.Calls() {
		if c.Method == "addStream" {
			addCall = c
		}
	}
	if addCall.Params.Get("radio_id") != "5" || addCall.Params.Get("callbacks") != "before" {
		t.Fatalf("addStream params %v", addCall.Params)
	}

	if r := runStreams(t, "streams", "set-url", "5", "https://radio.example/b"); r.Code != 0 || srv.Streams()[0].URL != "https://radio.example/b" {
		t.Fatalf("set-url: %d %s", r.Code, r.Stderr)
	}
	if r := runStreams(t, "streams", "set-url", "five", "x"); r.Code != output.ExitUsage {
		t.Fatalf("bad id: %d", r.Code)
	}

	r = runStreams(t, "streams", "remove", "5")
	if r.Code != output.ExitSafety || !strings.Contains(r.Stderr, "confirmation_required") || len(srv.Streams()) != 1 {
		t.Fatalf("remove needs confirmation without a terminal: %d %s", r.Code, r.Stderr)
	}
	if r := runStreams(t, "streams", "remove", "5", "--yes"); r.Code != 0 || len(srv.Streams()) != 0 {
		t.Fatalf("remove --yes: %d %s", r.Code, r.Stderr)
	}
	r = runStreams(t, "streams", "remove", "5", "--yes")
	if r.Code != output.ExitUsage || !strings.Contains(r.Stderr, `"api_code":700`) {
		t.Fatalf("API errors map to exit codes: %d %s", r.Code, r.Stderr)
	}
}

func TestStreamsCallback(t *testing.T) {
	srv := setupStreams(t)
	r := runStreams(t, "streams", "callback", "get")
	if doc := decode(t, r.Stdout); doc["url"] != "https://example.com/callback" {
		t.Fatalf("get %v", doc)
	}
	srv.SetNoCallbackURL()
	r = runStreams(t, "streams", "callback", "get")
	if r.Code != 0 || !strings.Contains(r.Stdout, `"url":null`) {
		t.Fatalf("no callback is not an error: %d %s %s", r.Code, r.Stdout, r.Stderr)
	}
	r = runStreams(t, "streams", "callback", "get", "--format", "table")
	if !strings.Contains(r.Stdout, "No callback URL is set.") {
		t.Fatalf("human: %q", r.Stdout)
	}
	if r := runStreams(t, "streams", "callback", "set", "https://example.com/hook", "--return", "napster"); r.Code != output.ExitUsage {
		t.Fatalf("napster is not a provider: %d", r.Code)
	}
	r = runStreams(t, "streams", "callback", "set", "https://example.com/hook", "--return", "apple_music,spotify")
	if r.Code != 0 || srv.CallbackURL() != "https://example.com/hook?return=apple_music%2Cspotify" {
		t.Fatalf("set: %d %s %q", r.Code, r.Stderr, srv.CallbackURL())
	}
}

// syncBuffer is a bytes.Buffer safe for a writer and a reader goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runLive runs a long-running command until until() holds, then cancels it.
func runLive(t *testing.T, until func(stdout string) bool, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runLiveIO(t, false, until, args...)
}

// runLiveIO is runLive with stdout optionally reported as a terminal.
func runLiveIO(t *testing.T, tty bool, until func(stdout string) bool, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, args, IO{In: strings.NewReader(""), Out: &out, Err: &errb, StdoutTTY: tty})
	}()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case code := <-done:
			return code, out.String(), errb.String()
		case <-deadline:
			cancel()
			t.Fatalf("timed out; stdout %q stderr %q", out.String(), errb.String())
		case <-time.After(20 * time.Millisecond):
			if until(out.String()) {
				cancel()
				code := <-done
				return code, out.String(), errb.String()
			}
		}
	}
}

func TestStreamsWatchPipedJSONL(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 3, Running: true})
	go func() {
		for srv.Count("longpoll") == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		srv.PushMatch(3, "2026-10-08 14:02:00", "Live Artist", "Live Song", 200)
		srv.PushNotification(3, 651, "no music", true, 1791457320)
	}()
	code, stdout, stderr := runLive(t, func(s string) bool { return strings.Count(s, "\n") >= 2 }, "streams", "watch", "3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	sc := bufio.NewScanner(strings.NewReader(stdout))
	var types []string
	for sc.Scan() {
		m := decode(t, sc.Text())
		types = append(types, m["type"].(string))
		if m["type"] == "result" && (m["title"] != "Live Song" || m["radio_id"] != float64(3)) {
			t.Fatalf("result line %v", m)
		}
		if m["type"] == "event" && (m["event"] != "health" || m["code"] != float64(651)) {
			t.Fatalf("event line %v", m)
		}
	}
	if strings.Join(types, ",") != "result,event" {
		t.Fatalf("types %v", types)
	}
	// What watch saw is in the store.
	st, _ := streamstore.Open("default")
	defer st.Close()
	if p, _ := st.Latest(3); p == nil || p.Title != "Live Song" {
		t.Fatalf("stored %+v", p)
	}
}

func TestStreamsWatchInTerminalWithoutExplorer(t *testing.T) {
	old := app.RunExplorer
	app.RunExplorer = func(_ context.Context, a *app.App, tab string) error {
		return output.Errf(output.ExitUnexpected, "not_implemented", "", "the explorer is not available in this build")
	}
	t.Cleanup(func() { app.RunExplorer = old })
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 3, Running: true})
	go func() {
		for srv.Count("longpoll") == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		srv.PushMatch(3, "2026-10-08 14:02:00", "Live Artist", "Live Song", 200)
	}()
	code, stdout, stderr := runLiveIO(t, true, func(s string) bool { return strings.Contains(s, "Live Song") }, "streams", "watch", "3")
	if code != 0 || !strings.Contains(stdout, "[3] Live Artist — Live Song") {
		t.Fatalf("falls back to lines: %d %q %s", code, stdout, stderr)
	}
	// Only the recorder that prints lines made requests.
	if n := srv.Count("getStreams"); n != 1 {
		t.Fatalf("stream list read %d times", n)
	}
	if n := srv.Count("getChannelById"); n != 1 {
		t.Fatalf("recent results read %d times", n)
	}
}

func TestStreamsWatchOpensTheExplorerOnItsStreams(t *testing.T) {
	var tabs []string
	old := app.RunExplorer
	app.RunExplorer = func(_ context.Context, a *app.App, tab string) error {
		tabs = append(tabs, tab)
		return nil
	}
	t.Cleanup(func() { app.RunExplorer = old })
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 3, Running: true}, streamstest.Stream{RadioID: 5, Running: true})
	never := func(string) bool { return false }
	if code, _, stderr := runLiveIO(t, true, never, "streams", "watch", "3", "5"); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	if code, _, stderr := runLiveIO(t, true, never, "streams", "watch"); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	if strings.Join(tabs, " ") != "streams/3,5 streams" {
		t.Fatalf("explorer tabs %q", tabs)
	}
}

func TestStreamsWatchNeedsCallbackURL(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 3, Running: true})
	srv.SetNoCallbackURL()
	r := runStreams(t, "streams", "watch")
	if r.Code != output.ExitSafety || !strings.Contains(r.Stderr, "callback_url_required") || srv.CallbackURL() != "" {
		t.Fatalf("never set silently: %d %s", r.Code, r.Stderr)
	}
	code, _, stderr := runLive(t, func(string) bool { return srv.Count("longpoll") > 0 }, "streams", "watch", "--yes")
	if code != 0 || srv.CallbackURL() != streams.EmptyCallbackURL {
		t.Fatalf("--yes sets the placeholder: %d %s %q", code, stderr, srv.CallbackURL())
	}
}

func TestStreamsRecordForegroundAndLock(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	srv.SetRecent(1, streamstest.RecentBody(30, map[string]any{"artist": "Old", "title": "Backfilled", "timestamp": "2026-10-08 13:00:00"}))
	go func() {
		for srv.Count("longpoll") == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		srv.PushMatch(1, "2026-10-08 14:02:00", "New", "Live", 200)
	}()
	code, stdout, stderr := runLive(t, func(s string) bool { return strings.Contains(s, "Live") }, "streams", "record", "--format", "table")
	if code != 0 || !strings.Contains(stdout, "[1] New — Live") || !strings.Contains(stderr, "Recording all streams") {
		t.Fatalf("record: %d %q %q", code, stdout, stderr)
	}
	st, _ := streamstore.Open("default")
	n, _ := st.CountPlays()
	st.Close()
	if n != 2 {
		t.Fatalf("backfill + live = 2 plays, got %d", n)
	}

	lock, err := streams.Acquire("default")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	r := runStreams(t, "streams", "record")
	if r.Code != output.ExitUsage || !strings.Contains(r.Stderr, "recorder_running") {
		t.Fatalf("one recorder per profile: %d %s", r.Code, r.Stderr)
	}
}

func TestStreamsRecordBackgroundChildLogs(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	srv.SetNoCallbackURL()
	code, stdout, _ := runLive(t, func(string) bool { return srv.Count("longpoll") > 0 }, "streams", "record", "--background-child")
	if code != 0 || stdout != "" {
		t.Fatalf("background child prints nothing: %d %q", code, stdout)
	}
	b, _ := os.ReadFile(streams.LogPath("default"))
	if !strings.Contains(string(b), "recorder started") || !strings.Contains(string(b), "no callback URL is set") {
		t.Fatalf("log: %s", b)
	}
	if srv.CallbackURL() != "" {
		t.Fatal("the background recorder never changes the callback URL")
	}
	r := runStreams(t, "streams", "recorder", "status")
	doc := decode(t, r.Stdout)
	if doc["running"] != false || !strings.Contains(doc["last_error"].(string), "no callback URL") {
		t.Fatalf("status shows the problem: %v", doc)
	}
}

func TestStreamsRecorderStartStatusStop(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	var held *streams.Lock
	var spawned [][]string
	restore := streams.SetSpawnForTesting(func(exe string, args, env []string, logPath string) (int, error) {
		spawned = append(spawned, args)
		l, err := streams.Acquire("default") // stands in for the child process
		held = l
		return os.Getpid(), err
	})
	defer restore()
	defer func() { held.Release() }()

	r := runStreams(t, "streams", "recorder", "status")
	if doc := decode(t, r.Stdout); doc["running"] != false || doc["background_recorder"] != true {
		t.Fatalf("status before: %v", doc)
	}
	r = runStreams(t, "streams", "recorder", "start")
	if r.Code != 0 {
		t.Fatalf("start: %d %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	if doc["started"] != true || doc["running"] != true || doc["pid"] != float64(os.Getpid()) || len(spawned) != 1 {
		t.Fatalf("start result %v", doc)
	}
	if strings.Join(spawned[0], " ") != "streams record --profile default --background-child" {
		t.Fatalf("spawn args %v", spawned[0])
	}
	r = runStreams(t, "streams", "recorder", "start")
	if doc := decode(t, r.Stdout); doc["started"] != false || len(spawned) != 1 {
		t.Fatalf("second start: %v", doc)
	}
	r = runStreams(t, "streams", "recorder", "status", "--format", "table")
	if !strings.Contains(r.Stdout, "The stream recorder is running (pid") {
		t.Fatalf("status table: %q", r.Stdout)
	}
	// Using a streams command while it runs does not start another.
	runStreams(t, "streams", "list")
	if len(spawned) != 1 {
		t.Fatal("no second recorder")
	}
	held.Release()
	r = runStreams(t, "streams", "recorder", "stop")
	if doc := decode(t, r.Stdout); doc["stopped"] != false {
		t.Fatalf("stop with nothing running: %v", doc)
	}
}

func TestStreamsCommandsStartTheRecorderOnce(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	var held []*streams.Lock
	restore := streams.SetSpawnForTesting(func(exe string, args, env []string, logPath string) (int, error) {
		l, err := streams.Acquire("default")
		held = append(held, l)
		return 1, err
	})
	defer restore()
	defer func() {
		for _, l := range held {
			l.Release()
		}
	}()
	r := runStreams(t, "streams", "list")
	if !strings.Contains(r.Stderr, "Recording stream results in the background") {
		t.Fatalf("first use says so: %q", r.Stderr)
	}
	r = runStreams(t, "streams", "list")
	if strings.Contains(r.Stderr, "Recording stream results") || len(held) != 1 {
		t.Fatalf("only once: %q", r.Stderr)
	}
	// After the recorder stops, the next command starts it again quietly.
	held[0].Release()
	r = runStreams(t, "streams", "list")
	if len(held) != 2 || strings.Contains(r.Stderr, "Recording stream results") {
		t.Fatalf("restarted quietly: %d %q", len(held), r.Stderr)
	}
	// Turned off: never started.
	for _, l := range held {
		l.Release()
	}
	held = nil
	runStreams(t, "config", "set", "streams.background_recorder", "false")
	r = runStreams(t, "streams", "list")
	if len(held) != 0 || strings.Contains(r.Stderr, "Recording stream results") {
		t.Fatal("respects streams.background_recorder false")
	}
}

func TestStreamsRecorderInstallService(t *testing.T) {
	setupStreams(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	svc, err := streams.ServiceFor("default")
	if err != nil {
		t.Fatal(err)
	}
	var held *streams.Lock
	spawned := 0
	restore := streams.SetSpawnForTesting(func(string, []string, []string, string) (int, error) {
		spawned++
		l, err := streams.Acquire("default")
		held = l
		return 1, err
	})
	defer restore()
	defer func() {
		if held != nil {
			held.Release()
		}
	}()
	// The service runs without this shell's environment, so a token that
	// only comes from AUDD_API_TOKEN would leave it failing at every login.
	r := runStreams(t, "streams", "recorder", "start", "--install-service")
	if r.Code != output.ExitAuth || !strings.Contains(r.Stderr, "audd config set token") || spawned != 0 {
		t.Fatalf("env-only token: %d %q", r.Code, r.Stderr)
	}
	if _, err := os.Stat(svc.Path); err == nil {
		t.Fatal("no service file without a stored token")
	}
	runStreams(t, "config", "set", "token", testutil.PlaceholderToken)
	r = runStreams(t, "streams", "recorder", "start", "--install-service")
	if r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	path, _ := doc["service_file"].(string)
	if _, err := os.Stat(path); err != nil || doc["activate"] != svc.Activate {
		t.Fatalf("service file %v: %v", doc, err)
	}
	// Where turning the service on starts the recorder, no recorder is
	// started here: it would hold the lock and make the service fail.
	if svc.StartsNow && (spawned != 0 || doc["started"] != false) {
		t.Fatalf("started a recorder next to the service: %v", doc)
	}
	if !svc.StartsNow && spawned != 1 {
		t.Fatalf("the recorder runs until the next logon: %v", doc)
	}
	r = runStreams(t, "streams", "recorder", "start", "--install-service", "--format", "table")
	if r.Code != 0 || !strings.Contains(r.Stdout, svc.Activate) {
		t.Fatalf("%d %q %s", r.Code, r.Stdout, r.Stderr)
	}
}

func TestStreamsHistory(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true}, streamstest.Stream{RadioID: 2, Running: true})
	now := time.Now()
	srv.SetRecent(2, streamstest.RecentBody(30, map[string]any{"artist": "Recent", "title": "From the endpoint", "timestamp": streams.FormatTimestamp(now.Add(-10 * time.Minute))}))
	seedStore(t,
		streamstore.Play{RadioID: 1, Timestamp: now.Add(-2 * time.Hour), Artist: "A", Title: "Older", PlayLength: 200},
		streamstore.Play{RadioID: 1, Timestamp: now.Add(-30 * time.Minute), Artist: "B", Title: "Newer", PlayLength: 185},
		streamstore.Play{RadioID: 1, Timestamp: now.Add(-30 * 24 * time.Hour), Artist: "C", Title: "Too old"},
	)
	r := runStreams(t, "streams", "history", "--since", "24h")
	if r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	plays := doc["plays"].([]any)
	var titles []string
	for _, p := range plays {
		titles = append(titles, p.(map[string]any)["title"].(string))
	}
	if strings.Join(titles, ",") != "From the endpoint,Newer,Older" {
		t.Fatalf("newest first, refreshed from recent results: %v", titles)
	}
	if gaps := doc["gaps"].([]any); len(gaps) == 0 {
		t.Fatal("nothing recorded 24h ago: the gap is reported")
	}

	r = runStreams(t, "streams", "history", "--since", "24h", "--id", "1", "--format", "table")
	if !strings.Contains(r.Stdout, "B — Newer") || strings.Contains(r.Stdout, "From the endpoint") || !strings.Contains(r.Stdout, "not recorded") || !strings.Contains(r.Stdout, "3:05") {
		t.Fatalf("table:\n%s", r.Stdout)
	}
	r = runStreams(t, "streams", "history", "--since", "24h", "--format", "jsonl")
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) < 4 || !strings.Contains(lines[0], `"type":"result"`) || !strings.Contains(lines[len(lines)-1], `"event":"gap"`) {
		t.Fatalf("jsonl:\n%s", r.Stdout)
	}
	if r := runStreams(t, "streams", "history", "--since", "yesterday-ish"); r.Code != output.ExitUsage {
		t.Fatalf("bad --since: %d", r.Code)
	}
}

func TestStreamsReport(t *testing.T) {
	setupStreams(t)
	now := time.Now()
	seedStore(t,
		streamstore.Play{RadioID: 1, Timestamp: now.Add(-3 * time.Hour), Artist: "A", Title: "One", Label: "L1", PlayLength: 200},
		streamstore.Play{RadioID: 2, Timestamp: now.Add(-2 * time.Hour), Artist: "A", Title: "One", Label: "L1", PlayLength: 200},
		streamstore.Play{RadioID: 1, Timestamp: now.Add(-1 * time.Hour), Artist: "B", Title: "Two", PlayLength: 100},
	)
	r := runStreams(t, "streams", "report", "--by", "artist", "--since", "7d")
	if r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	rows := doc["rows"].([]any)
	first := rows[0].(map[string]any)
	if doc["by"] != "artist" || first["key"] != "A" || first["plays"] != float64(2) || first["airtime_seconds"] != float64(400) || first["stations"] != float64(2) {
		t.Fatalf("report %v", doc)
	}
	if doc["complete"] != false {
		t.Fatal("nothing recorded most of the week: incomplete")
	}
	r = runStreams(t, "streams", "report", "--by", "label", "--format", "table")
	if !strings.Contains(r.Stdout, "LABEL") || !strings.Contains(r.Stdout, "(unknown)") || !strings.Contains(r.Stdout, "Note: Some streams were not recorded for ") {
		t.Fatalf("table:\n%s", r.Stdout)
	}
	r = runStreams(t, "streams", "report", "--by", "song", "--format", "csv")
	if !strings.HasPrefix(r.Stdout, "key,plays,airtime_seconds,stations\nA — One,2,400,2\n") {
		t.Fatalf("csv:\n%s", r.Stdout)
	}
	if r := runStreams(t, "streams", "report", "--by", "genre"); r.Code != output.ExitUsage {
		t.Fatalf("bad --by: %d", r.Code)
	}
}

func TestStreamsExportEmptyCSVHasAHeader(t *testing.T) {
	setupStreams(t)
	r := runStreams(t, "streams", "export", "--format", "csv")
	if r.Code != 0 || !strings.HasPrefix(r.Stdout, "radio_id,") || strings.Count(r.Stdout, "\n") != 1 {
		t.Fatalf("%d %q %s", r.Code, r.Stdout, r.Stderr)
	}
}

func TestStreamsReportSinceAllMarksHoles(t *testing.T) {
	setupStreams(t)
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	seedStore(t,
		streamstore.Play{RadioID: 1, Timestamp: now.Add(-10 * day), Artist: "A", Title: "One", PlayLength: 200},
		streamstore.Play{RadioID: 1, Timestamp: now.Add(-time.Hour), Artist: "B", Title: "Two", PlayLength: 100},
	)
	st, err := streamstore.Open("default")
	if err != nil {
		t.Fatal(err)
	}
	// Recorded around the first play, then nothing for days, then recorded
	// again up to now.
	st.Cover(now.Add(-10*day), now.Add(-9*day))
	st.Cover(now.Add(-2*day), now)
	st.Close()

	r := runStreams(t, "streams", "report", "--since", "all", "--format", "json")
	if r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	if doc["complete"] != false || len(doc["gaps"].([]any)) != 1 {
		t.Fatalf("the days with no recorder are a gap even for all time: %v", doc)
	}
	r = runStreams(t, "streams", "history", "--since", "all", "--format", "json")
	if doc := decode(t, r.Stdout); len(doc["gaps"].([]any)) != 1 {
		t.Fatalf("history all time: %v", doc)
	}
	r = runStreams(t, "streams", "export", "--format", "csv")
	if !strings.Contains(r.Stderr, "of this range (1 gap), so this export misses plays") {
		t.Fatalf("export notes the gap: %q", r.Stderr)
	}
}

func TestStreamsHistorySkipsRecentResultsWhenStoreIsCurrent(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	srv.SetRecent(1, streamstest.RecentBody(30, map[string]any{"artist": "Recent", "title": "Song", "timestamp": streams.FormatTimestamp(time.Now().Add(-10 * time.Minute))}))
	runStreams(t, "config", "set", "streams.background_recorder", "false")
	if r := runStreams(t, "streams", "history"); r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	first := srv.Count("getStreams")
	if first != 1 {
		t.Fatalf("first history fetches recent results: %d", first)
	}
	runStreams(t, "streams", "history")
	if n := srv.Count("getStreams"); n != first {
		t.Fatalf("store was just brought up to date, no second fetch: %d", n)
	}
}

func TestStreamsExport(t *testing.T) {
	setupStreams(t)
	t1 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	seedStore(t,
		streamstore.Play{RadioID: 1, Timestamp: t1.Add(time.Hour), Artist: "B", Title: "Second", ISRC: "X2"},
		streamstore.Play{RadioID: 1, Timestamp: t1, Artist: "A", Title: "First", ISRC: "X1"},
	)
	r := runStreams(t, "streams", "export", "--format", "csv")
	want := "radio_id,timestamp,play_length,artist,title,album,label,release_date,isrc,upc,song_link,score\n" +
		"1,2026-10-01T10:00:00Z,0,A,First,,,,X1,,,0\n" +
		"1,2026-10-01T11:00:00Z,0,B,Second,,,,X2,,,0\n"
	if r.Stdout != want {
		t.Fatalf("csv:\n%s", r.Stdout)
	}
	r = runStreams(t, "streams", "export")
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], `{"schema_version":1,"type":"result","radio_id":1,"timestamp":"2026-10-01T10:00:00Z"`) {
		t.Fatalf("piped export is JSONL, oldest first:\n%s", r.Stdout)
	}
}

func TestStreamsExportFetchesRecentResults(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	srv.SetRecent(1, streamstest.RecentBody(30, map[string]any{"artist": "Recent", "title": "From the endpoint", "timestamp": streams.FormatTimestamp(time.Now().Add(-10 * time.Minute))}))
	runStreams(t, "config", "set", "streams.background_recorder", "false")
	r := runStreams(t, "streams", "export", "--since", "1d")
	if r.Code != 0 || !strings.Contains(r.Stdout, "From the endpoint") {
		t.Fatalf("no recorder ran: export reads the recent results: %d %s %s", r.Code, r.Stdout, r.Stderr)
	}
}

func TestStreamsHelp(t *testing.T) {
	testutil.Isolate(t)
	for _, args := range [][]string{{"streams", "--help"}, {"streams", "recorder", "--help"}, {"streams", "watch", "--help"}, {"--help"}} {
		r := runStreams(t, args...)
		if strings.Contains(strings.ToLower(r.Stdout), "lyrics") {
			t.Fatalf("%v mentions lyrics", args)
		}
		if strings.Contains(r.Stdout, "background-child") {
			t.Fatal("internal flag is hidden")
		}
	}
	r := runStreams(t, "--help")
	if !strings.Contains(r.Stdout, "Streams:") || !strings.Contains(r.Stdout, "streams") {
		t.Fatalf("root help lists the streams group:\n%s", r.Stdout)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"24h": now.Add(-24 * time.Hour), "7d": now.Add(-7 * 24 * time.Hour), "2w": now.Add(-14 * 24 * time.Hour),
		"90m": now.Add(-90 * time.Minute), "1h30m": now.Add(-90 * time.Minute), "all": {}, "": {},
		"2026-10-01T00:00:00Z": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	} {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if got, err := parseSince("2026-10-01", now); err != nil || got.Format("2006-01-02") != "2026-10-01" {
		t.Errorf("date: %v %v", got, err)
	}
	if _, err := parseSince("soon", now); err == nil {
		t.Error("soon")
	}
}

func TestStreamsWatchPassesThroughSongFields(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 3, Running: true})
	go func() {
		for srv.Count("longpoll") == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		// Metadata turned on with `streams callback set --return apple_music`.
		srv.Push(3, json.RawMessage(`{"status":"success","result":{"radio_id":3,"timestamp":"2026-10-08 14:02:00","play_length":200,"results":[{"artist":"Live Artist","title":"Live Song","score":100,"apple_music":{"url":"https://music.apple.com/x","artwork":{"url":"https://is1-ssl.mzstatic.com/{w}x{h}bb.jpg"}},"new_field":"kept"}]}}`))
	}()
	code, stdout, stderr := runLive(t, func(s string) bool { return strings.Count(s, "\n") >= 1 }, "streams", "watch", "3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, strings.SplitN(stdout, "\n", 2)[0])
	am, _ := m["apple_music"].(map[string]any)
	if m["type"] != "result" || m["title"] != "Live Song" || am["url"] != "https://music.apple.com/x" || m["new_field"] != "kept" {
		t.Fatalf("result line %v", m)
	}

	r := runStreams(t, "streams", "history", "--format", "json")
	doc := decode(t, r.Stdout)
	plays, _ := doc["plays"].([]any)
	if len(plays) != 1 {
		t.Fatalf("history %s", r.Stdout)
	}
	if p := plays[0].(map[string]any); p["apple_music"] == nil || p["timestamp"] != "2026-10-08T11:02:00Z" {
		t.Fatalf("history keeps the stored song fields: %v", p)
	}
}

func TestStreamsWatchInTerminalOpensExplorer(t *testing.T) {
	old := app.RunExplorer
	var tab string
	app.RunExplorer = func(_ context.Context, a *app.App, t string) error { tab = t; return nil }
	t.Cleanup(func() { app.RunExplorer = old })
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 3, Running: true})
	code, stdout, stderr := runLiveIO(t, true, func(string) bool { return false }, "streams", "watch", "3")
	if code != 0 || tab != "streams/3" {
		t.Fatalf("explorer on the streams tab: %d tab=%q %q %s", code, tab, stdout, stderr)
	}
}

func TestStreamsReportShowsStreamsARecorderSkipped(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true}, streamstest.Stream{RadioID: 2, Running: true})
	srv.SetRecent(1, streamstest.RecentBody(30, map[string]any{"artist": "R", "title": "Recent", "timestamp": streams.FormatTimestamp(time.Now().Add(-10 * time.Minute))}))
	runStreams(t, "config", "set", "streams.background_recorder", "false")
	// A recorder ran for stream 2 only (audd streams record 2) for the
	// last hour, up to now.
	now := time.Now()
	st, err := streamstore.Open("default")
	if err != nil {
		t.Fatal(err)
	}
	st.SetAccountStreams([]int{1, 2}, now.Add(-time.Hour))
	for at := now.Add(-time.Hour); !at.After(now); at = at.Add(30 * time.Second) {
		st.Heartbeat(at, 2)
	}
	st.Close()

	r := runStreams(t, "streams", "report", "--since", "1h", "--format", "json")
	if r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	if doc["complete"] != false {
		t.Fatalf("stream 1 was not recorded for most of the hour: %v", doc)
	}
	r = runStreams(t, "streams", "history", "--id", "1", "--since", "1h", "--format", "json")
	doc = decode(t, r.Stdout)
	gaps := doc["gaps"].([]any)
	if len(gaps) != 1 {
		t.Fatalf("stream 1 history: %v", doc)
	}
	// The recent results reach back 10 minutes, so the gap ends there.
	to, _ := time.Parse(time.RFC3339, gaps[0].(map[string]any)["to"].(string))
	if d := now.Add(-10 * time.Minute).Sub(to); d > time.Minute || d < -time.Minute {
		t.Fatalf("gap ends at %v", to)
	}
	r = runStreams(t, "streams", "history", "--id", "2", "--since", "1h", "--format", "json")
	if doc := decode(t, r.Stdout); len(doc["gaps"].([]any)) != 0 {
		t.Fatalf("stream 2 was recorded: %v", doc)
	}
	// Stream 2 is current, so only stream 1's recent results were fetched.
	for _, c := range srv.Calls() {
		if c.Method == "getChannelById" && strings.TrimPrefix(c.Params.Get("ch_id"), "-") == streamstest.Category(2) {
			t.Fatal("stream 2 is kept current by the recorder; its recent results are not fetched")
		}
	}
}

func TestStreamsDataWithNoStreams(t *testing.T) {
	setupStreams(t)
	r := runStreams(t, "streams", "history", "--since", "24h", "--format", "table")
	if r.Code != 0 || !strings.Contains(r.Stdout, "No streams yet. Add one with: audd streams add <url> --id 1") || strings.Contains(r.Stdout, "not recorded") {
		t.Fatalf("history: %d %q %s", r.Code, r.Stdout, r.Stderr)
	}
	r = runStreams(t, "streams", "report")
	doc := decode(t, r.Stdout)
	if r.Code != 0 || doc["complete"] != true || len(doc["gaps"].([]any)) != 0 || !strings.Contains(r.Stderr, "No streams yet") {
		t.Fatalf("report: %d %s %s", r.Code, r.Stdout, r.Stderr)
	}
}

// On a narrow terminal, streams list cuts long URLs with "…" instead of
// letting rows wrap; piped output keeps them whole.
func TestStreamsListFitsTheTerminal(t *testing.T) {
	srv := setupStreams(t)
	long := "https://radio.example/" + strings.Repeat("very-long-path/", 8) + "stream.mp3"
	srv.SetStreams(streamstest.Stream{RadioID: 1, URL: long, Running: true}, streamstest.Stream{RadioID: 2, URL: "https://radio.example/2", Running: true})
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"streams", "list"}, IO{In: strings.NewReader(""), Out: &out, Err: &errb, StdoutTTY: true, StdoutWidth: 70})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if w := runewidth.StringWidth(l); w > 70 {
			t.Fatalf("a row is %d columns wide:\n%s", w, out.String())
		}
	}
	if !strings.Contains(out.String(), "very-l") || !strings.Contains(out.String(), "…") || !strings.Contains(out.String(), "https://radio.example/2") {
		t.Fatalf("URLs:\n%s", out.String())
	}
	r := runStreams(t, "streams", "list", "--format", "table")
	if !strings.Contains(r.Stdout, long) {
		t.Fatalf("not a terminal: the URL should be whole:\n%s", r.Stdout)
	}
}

// A default stream reports a song when it ends: streams list says when it
// ended (start + play_length), not when it started.
func TestStreamsListSaysWhenAReportedSongEnded(t *testing.T) {
	srv := setupStreams(t)
	srv.SetStreams(streamstest.Stream{RadioID: 1, URL: "https://radio.example/1", Running: true})
	seedStore(t, streamstore.Play{RadioID: 1, Timestamp: time.Now().Add(-6 * time.Minute), PlayLength: 240, Artist: "Artist", Title: "Song", Score: 100})
	r := runStreams(t, "streams", "list", "--format", "table")
	if !strings.Contains(r.Stdout, "Artist — Song (ended 2 min ago)") {
		t.Fatalf("want the end time:\n%s", r.Stdout)
	}
}
