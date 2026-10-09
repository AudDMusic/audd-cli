// Package e2e runs audd end to end against fake AudD servers: the API
// (recognition, enterprise, streams, longpoll, recent results), the
// sign-in server, and the account (MCP) server. Commands run in-process
// through the real command tree, and through the built binary where a real
// process matters (the background recorder).
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/cli"
	"github.com/AudDMusic/audd-cli/internal/oauth"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/streams/streamstest"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// binary is the audd binary built for this test run ("" when the build
// failed; tests that need it are skipped with the reason).
var (
	binary   string
	buildErr error
)

func TestMain(m *testing.M) {
	// Device sign-ins poll in milliseconds, and every test runs on the
	// same kind of machine whatever the host: Linux without a display.
	oauth.PollUnit = time.Millisecond
	oauth.DetectEnvironment = func(stdinTTY, stdoutTTY bool) oauth.Environment {
		return oauth.Environment{GOOS: "linux", Getenv: func(string) string { return "" }, StdinTTY: stdinTTY, StdoutTTY: stdoutTTY}
	}
	dir, err := os.MkdirTemp("", "audd-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := "audd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary = filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", binary, "github.com/AudDMusic/audd-cli/cmd/audd")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		binary = ""
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is one isolated audd installation talking to fake servers.
type env struct {
	t       *testing.T
	api     *testutil.FakeAPI
	streams *streamstest.Server
	oauth   *testutil.FakeOAuth
	mcp     *testutil.FakeMCP
	dir     string // working files

	mu     *sync.Mutex
	onCall *func(n int) // called with the count of standard recognitions
}

// with returns the environment for a subtest, so failures stop that subtest.
func (e *env) with(t *testing.T) *env {
	c := *e
	c.t = t
	return &c
}

func newEnv(t *testing.T) *env {
	t.Helper()
	testutil.Isolate(t)
	e := &env{t: t, dir: t.TempDir(), mu: &sync.Mutex{}, onCall: new(func(int))}
	e.api = testutil.NewFakeAPI(t)
	e.api.AcceptTokens(testutil.PlaceholderToken)
	e.streams = streamstest.New(t)
	e.oauth = testutil.NewFakeOAuth(t)
	e.mcp = testutil.NewFakeMCP(t, e.oauth)

	// One front server for api.audd.io and enterprise.audd.io: recognition
	// goes to the recognition fake, everything else (stream methods,
	// longpoll, recent results) to the streams fake.
	apiURL, _ := url.Parse(e.api.URL)
	streamsURL, _ := url.Parse(e.streams.URL)
	toAPI := httputil.NewSingleHostReverseProxy(apiURL)
	toStreams := httputil.NewSingleHostReverseProxy(streamsURL)
	// Longpoll requests are cut off when a command ends; that is expected.
	toAPI.ErrorLog = log.New(io.Discard, "", 0)
	toStreams.ErrorLog = log.New(io.Discard, "", 0)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.Trim(r.URL.Path, "/") {
		case "", "_enterprise", "upload":
			toAPI.ServeHTTP(w, r)
		default:
			toStreams.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(front.Close)
	t.Setenv("AUDD_API_BASE_URL", front.URL)
	t.Setenv("AUDD_ENTERPRISE_BASE_URL", front.URL+"/_enterprise")
	t.Setenv("AUDD_MCP_URL", e.mcp.URL())
	// Only `audd streams recorder start` starts a recorder in these tests.
	t.Setenv("AUDD_NO_BACKGROUND_RECORDER", "1")

	e.api.On(testutil.EndpointRecognize, func(r testutil.FakeRequest) (int, any) {
		n := e.api.Count(testutil.EndpointRecognize)
		e.mu.Lock()
		f := *e.onCall
		e.mu.Unlock()
		if f != nil {
			f(n)
		}
		return http.StatusOK, testutil.Success(testutil.MatchResult())
	})
	e.api.Reply(testutil.EndpointEnterprise, testutil.Success([]any{
		testutil.EnterpriseChunk("00:00", testutil.EnterpriseSong("A", "One", 500, 12000)),
		testutil.EnterpriseChunk("00:12", testutil.EnterpriseSong("A", "One", 0, 12000)),
		testutil.EnterpriseChunk("00:24", testutil.EnterpriseSong("B", "Two", 2000, 10000)),
	}))
	return e
}

// run runs audd in-process with no terminal.
func (e *env) run(args ...string) testutil.Result {
	e.t.Helper()
	return testutil.Exec(e.t, cli.Main, "", args...)
}

// runCtx runs audd in-process with a context (Ctrl-C is a cancel).
func (e *env) runCtx(ctx context.Context, args ...string) testutil.Result {
	var out, errb bytes.Buffer
	code := cli.Run(ctx, args, cli.IO{In: strings.NewReader(""), Out: &out, Err: &errb})
	return testutil.Result{Code: code, Stdout: out.String(), Stderr: errb.String()}
}

func (e *env) ok(args ...string) testutil.Result {
	e.t.Helper()
	r := e.run(args...)
	if r.Code != 0 {
		e.t.Fatalf("audd %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.Code, r.Stdout, r.Stderr)
	}
	return r
}

// bin runs the built binary.
func (e *env) bin(args ...string) testutil.Result {
	e.t.Helper()
	if binary == "" {
		e.t.Skipf("audd binary not built: %v", buildErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = e.dir
	cmd.Env = os.Environ()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("running audd %s: %v", strings.Join(args, " "), err)
	}
	return testutil.Result{Code: code, Stdout: out.String(), Stderr: errb.String()}
}

func (e *env) binOK(args ...string) testutil.Result {
	e.t.Helper()
	r := e.bin(args...)
	if r.Code != 0 {
		e.t.Fatalf("audd %s (binary): exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.Code, r.Stdout, r.Stderr)
	}
	return r
}

func (e *env) file(name, content string) string {
	e.t.Helper()
	p := filepath.Join(e.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func doc(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, s)
	}
	if m["schema_version"] != float64(1) {
		t.Fatalf("schema_version missing: %s", s)
	}
	return m
}

func lines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		m := doc(t, sc.Text())
		if m["type"] == nil {
			t.Fatalf("JSONL line without type: %s", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func wantCode(t *testing.T, r testutil.Result, exit int, code string) {
	t.Helper()
	if r.Code != exit || (code != "" && !strings.Contains(r.Stderr, `"code":"`+code+`"`)) {
		t.Fatalf("want exit %d %s, got %d\nstdout: %s\nstderr: %s", exit, code, r.Code, r.Stdout, r.Stderr)
	}
}

// loginByPaste signs in the way a person on a terminal does when the
// browser is on another machine: the CLI prints the URL, the person
// approves, and pastes the redirect URL back.
func (e *env) loginByPaste() {
	t := e.t
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	prev := oauth.OpenBrowser
	oauth.OpenBrowser = func(u string) error {
		redirect, err := testutil.AuthorizeRedirect(u)
		if err != nil {
			return err
		}
		go pw.Write([]byte(redirect + "\n"))
		return nil
	}
	defer func() { oauth.OpenBrowser = prev }()
	var out, errb bytes.Buffer
	code := cli.Run(context.Background(), []string{"login", "--browser"}, cli.IO{In: pr, Out: &out, Err: &errb, StdinTTY: true, StdoutTTY: true, StderrTTY: true})
	if code != 0 || !strings.Contains(out.String(), "Signed in as user@example.com") || !strings.Contains(errb.String(), "paste") {
		t.Fatalf("login: exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	if len(e.mcp.CallsTo("get_api_token")) != 1 {
		t.Fatal("login should fetch the API token once")
	}
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)

	// Signed out: API commands say how to get a token.
	wantCode(t, e.run("recognize", e.file("first.mp3", "first song")), 3, "no_token")

	t.Run("login and token", func(t *testing.T) {
		e := e.with(t)
		e.loginByPaste()
		r := e.ok("token", "show")
		d := doc(t, r.Stdout)
		if d["token"] != "0123…cdef" || d["masked"] != true || d["source"] != "login" {
			t.Fatalf("token show: %s", r.Stdout)
		}
		if strings.Contains(r.Stdout+r.Stderr, testutil.PlaceholderToken) {
			t.Fatal("token show printed the whole token")
		}
		st := doc(t, e.ok("auth", "status").Stdout)
		if st["account"] != "user@example.com" || st["token_source"] != "login" {
			t.Fatalf("auth status: %v", st)
		}
	})

	t.Run("recognize a file and a URL", func(t *testing.T) {
		e := e.with(t)
		r := e.ok("recognize", filepath.Join(e.dir, "first.mp3"))
		d := doc(t, r.Stdout)
		res, _ := d["result"].(map[string]any)
		if res["title"] != "Warriors" || res["new_field"] != "kept as is" || d["cached"] != false {
			t.Fatalf("file: %s", r.Stdout)
		}
		r = e.ok("recognize", "https://example.com/song.mp3", "--return", "spotify,apple_music")
		if d := doc(t, r.Stdout); d["input"] != "https://example.com/song.mp3" {
			t.Fatalf("url: %s", r.Stdout)
		}
		reqs := e.api.Requests()
		last := reqs[len(reqs)-1]
		if last.Form["url"] != "https://example.com/song.mp3" || last.Form["return"] != "apple_music,spotify" || last.Token != testutil.PlaceholderToken {
			t.Fatalf("url request: %+v", last)
		}
		// The same audio again is free.
		before := e.api.Count(testutil.EndpointRecognize)
		r = e.ok("recognize", filepath.Join(e.dir, "first.mp3"), "--format", "table", "--no-color")
		if !strings.Contains(r.Stdout, "Imagine Dragons") || !strings.Contains(r.Stdout, "Cached result, no request used") {
			t.Fatalf("cached card: %q", r.Stdout)
		}
		if e.api.Count(testutil.EndpointRecognize) != before {
			t.Fatal("a cached result sent a request")
		}
		r = e.ok("recognize", filepath.Join(e.dir, "first.mp3"), "--quiet", "--format", "table")
		if r.Stdout != "Imagine Dragons — Warriors\n" {
			t.Fatalf("quiet: %q", r.Stdout)
		}
	})

	t.Run("enterprise tracklist", func(t *testing.T) {
		e := e.with(t)
		mix := e.file("mix.mp3", strings.Repeat("x", 4096))
		wantCode(t, e.run("recognize", mix, "--enterprise"), 6, "limit_required")
		wantCode(t, e.run("recognize", mix, "--enterprise", "--limit", "3"), 6, "confirmation_required")
		if n := e.api.Count(testutil.EndpointEnterprise); n != 0 {
			t.Fatalf("refused runs sent %d requests", n)
		}
		r := e.ok("recognize", mix, "--enterprise", "--limit", "3", "--tracklist", "--yes")
		d := doc(t, r.Stdout)
		tracks, _ := d["tracks"].([]any)
		if len(tracks) != 2 {
			t.Fatalf("tracks: %s", r.Stdout)
		}
		first := tracks[0].(map[string]any)
		if first["title"] != "One" || first["artist"] != "A" {
			t.Fatalf("first track: %v", first)
		}
		reqs := e.api.Requests()
		if last := reqs[len(reqs)-1]; last.Endpoint != testutil.EndpointEnterprise || last.Form["limit"] != "3" {
			t.Fatalf("enterprise request: %+v", last)
		}
	})

	t.Run("batch interrupted and resumed", func(t *testing.T) {
		e := e.with(t)
		folder := filepath.Join(e.dir, "batch")
		for i := 0; i < 6; i++ {
			e.file(filepath.Join("batch", fmt.Sprintf("song%d.mp3", i)), fmt.Sprintf("batch song %d", i))
		}
		start := e.api.Count(testutil.EndpointRecognize)

		wantCode(t, e.run("recognize", folder), 6, "limit_required")
		wantCode(t, e.run("recognize", folder, "--max-files", "3", "--yes"), 6, "max_files_exceeded")
		r := e.ok("recognize", folder, "--max-files", "10", "--dry-run")
		if !strings.Contains(r.Stdout, `"requests":6`) && !strings.Contains(r.Stdout, `"requests": 6`) {
			t.Fatalf("dry run: %s", r.Stdout)
		}
		if n := e.api.Count(testutil.EndpointRecognize) - start; n != 0 {
			t.Fatalf("refused and dry runs sent %d requests", n)
		}

		// Ctrl-C while the second file is being recognized.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.mu.Lock()
		*e.onCall = func(n int) {
			if n-start == 2 {
				cancel()
			}
		}
		e.mu.Unlock()
		r = e.runCtx(ctx, "recognize", folder, "--max-files", "10", "--concurrency", "1", "--yes")
		e.mu.Lock()
		*e.onCall = nil
		e.mu.Unlock()
		if r.Code != 130 {
			t.Fatalf("interrupted batch: exit %d\n%s\n%s", r.Code, r.Stdout, r.Stderr)
		}
		sent := e.api.Count(testutil.EndpointRecognize) - start
		if sent != 2 {
			t.Fatalf("sent %d requests before stopping, want 2", sent)
		}
		done := 0
		for _, l := range lines(t, r.Stdout) {
			if l["type"] == "result" {
				done++
			}
		}
		if done != 2 {
			t.Fatalf("the request in progress should finish and be saved: %d results\n%s", done, r.Stdout)
		}
		if !strings.Contains(r.Stderr, "audd jobs resume") {
			t.Fatalf("no resume hint: %s", r.Stderr)
		}

		// Running it again without a terminal must choose.
		wantCode(t, e.run("recognize", folder, "--max-files", "10", "--yes"), 6, "resume_available")
		r = e.ok("recognize", folder, "--max-files", "10", "--yes", "--resume")
		if total := e.api.Count(testutil.EndpointRecognize) - start; total != 6 {
			t.Fatalf("resume re-billed finished files: %d requests for 6 files", total)
		}
		ls := lines(t, r.Stdout)
		sum := ls[len(ls)-1]
		if sum["type"] != "summary" {
			t.Fatalf("last line: %v", sum)
		}

		jobsList := e.ok("jobs", "list", "--format", "json")
		if !strings.Contains(jobsList.Stdout, `"status":"done"`) {
			t.Fatalf("jobs list: %s", jobsList.Stdout)
		}

		// Batch results are in the same cache as single recognitions.
		before := e.api.Count(testutil.EndpointRecognize)
		r = e.ok("recognize", filepath.Join(folder, "song4.mp3"))
		if doc(t, r.Stdout)["cached"] != true || e.api.Count(testutil.EndpointRecognize) != before {
			t.Fatalf("batch result not reused: %s", r.Stdout)
		}
	})

	t.Run("streams and the background recorder", func(t *testing.T) {
		e := e.with(t)
		e.ok("streams", "add", "https://radio.example/1", "--id", "1")
		if s := e.streams.Streams(); len(s) != 1 || s[0].RadioID != 1 {
			t.Fatalf("streams: %+v", s)
		}
		now := time.Now()
		e.streams.SetRecent(1, streamstest.RecentBody(30,
			map[string]any{"artist": "Earlier Artist", "title": "Earlier Song", "timestamp": streams.FormatTimestamp(now.Add(-20 * time.Minute)), "play_length": 200},
		))

		t.Cleanup(func() { e.bin("streams", "recorder", "stop") })
		r := e.binOK("streams", "recorder", "start", "--format", "json")
		if d := doc(t, r.Stdout); d["started"] != true || d["running"] != true {
			t.Fatalf("recorder start: %s", r.Stdout)
		}
		st := doc(t, e.binOK("streams", "recorder", "status", "--format", "json").Stdout)
		if st["running"] != true || st["pid"] == nil {
			t.Fatalf("recorder status: %v", st)
		}
		// A second recorder is refused.
		if r := e.bin("streams", "record", "--format", "json"); r.Code != 2 || !strings.Contains(r.Stderr, "recorder_running") {
			t.Fatalf("second recorder: %d %s", r.Code, r.Stderr)
		}

		e.streams.PushMatch(1, streams.FormatTimestamp(now.Add(-30*time.Second)), "Live Artist", "Live Song", 30)
		deadline := time.Now().Add(20 * time.Second)
		for {
			st = doc(t, e.ok("streams", "recorder", "status", "--format", "json").Stdout)
			if st["plays"] == float64(2) {
				break
			}
			if time.Now().After(deadline) {
				log, _ := os.ReadFile(streams.LogPath("default"))
				t.Fatalf("the recorder did not save the plays: %v\nlog:\n%s", st, log)
			}
			time.Sleep(200 * time.Millisecond)
		}

		r = e.binOK("streams", "recorder", "stop", "--format", "json")
		if d := doc(t, r.Stdout); d["stopped"] != true {
			t.Fatalf("recorder stop: %s", r.Stdout)
		}
		if st := doc(t, e.ok("streams", "recorder", "status").Stdout); st["running"] != false {
			t.Fatalf("still running: %v", st)
		}
		log, _ := os.ReadFile(streams.LogPath("default"))
		if !strings.Contains(string(log), "recorder started") || !strings.Contains(string(log), "recorder stopped") {
			t.Fatalf("recorder log:\n%s", log)
		}

		h := doc(t, e.ok("streams", "history", "--since", "1h").Stdout)
		var titles []string
		for _, p := range h["plays"].([]any) {
			titles = append(titles, p.(map[string]any)["title"].(string))
		}
		if strings.Join(titles, ",") != "Live Song,Earlier Song" {
			t.Fatalf("history: %v", titles)
		}
		rep := e.ok("streams", "report", "--by", "artist", "--since", "1h", "--format", "csv").Stdout
		if !strings.Contains(rep, "Live Artist") || !strings.Contains(rep, "Earlier Artist") {
			t.Fatalf("report:\n%s", rep)
		}

		np := doc(t, e.ok("now-playing", "--once").Stdout)
		stations := np["stations"].([]any)
		if len(stations) != 1 || !strings.Contains(fmt.Sprint(stations[0]), "Live Song") {
			t.Fatalf("now-playing --once: %v", np)
		}
		r = e.ok("now-playing", "1", "--once", "--format", "{{.Artist}} - {{.Title}}")
		if r.Stdout != "Live Artist - Live Song\n" {
			t.Fatalf("now-playing --format: %q", r.Stdout)
		}
		wantCode(t, e.run("now-playing", "x"), 2, "invalid_argument")
	})

	t.Run("browse without a terminal", func(t *testing.T) {
		e := e.with(t)
		for tab, want := range map[string]string{
			"recent":  "Warriors",
			"jobs":    `"status":"done"`,
			"streams": "Live Song",
			"usage":   `"remaining":4000`,
		} {
			r := e.ok("browse", "--tab", tab)
			doc(t, r.Stdout)
			if !strings.Contains(r.Stdout, want) {
				t.Fatalf("browse --tab %s lacks %q:\n%s", tab, want, r.Stdout)
			}
		}
	})

	t.Run("usage check", func(t *testing.T) {
		e := e.with(t)
		e.ok("usage", "--check", "--min-remaining", "1000")
		r := e.run("usage", "--check", "--min-remaining", "100000")
		if r.Code != 8 {
			t.Fatalf("usage --check: exit %d\n%s\n%s", r.Code, r.Stdout, r.Stderr)
		}
	})

	t.Run("logout", func(t *testing.T) {
		e := e.with(t)
		e.ok("config", "set", "token", "your-api-token")
		r := e.ok("logout")
		d := doc(t, r.Stdout)
		if d["was_signed_in"] != true || d["kept_api_token"] != true {
			t.Fatalf("logout: %s", r.Stdout)
		}
		if n := len(e.oauth.RevokedTokens()); n != 1 {
			t.Fatalf("logout revoked %d tokens, want 1", n)
		}
		r = e.ok("token", "show", "--reveal")
		if d := doc(t, r.Stdout); d["token"] != "your-api-token" || d["source"] != "config" {
			t.Fatalf("user token: %s", r.Stdout)
		}
		wantCode(t, e.run("usage"), 3, "login_required")
		if len(e.mcp.CallsTo("rotate_api_token")) != 0 {
			t.Fatal("logout must not rotate the API token")
		}
	})
}

// With ffmpeg installed, --at sends a clip, long files get the 12-second
// note, and enterprise plans use the real length.
func TestMediaTools(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	e := newEnv(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	song := filepath.Join(e.dir, "tone.wav")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=40", song).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}

	r := e.ok("recognize", song)
	if !strings.Contains(r.Stderr, "first 12 seconds") {
		t.Fatalf("no 12-second note for a 40-second file: %q", r.Stderr)
	}
	r = e.ok("recognize", song, "--at", "0:20", "--duration", "10")
	d := doc(t, r.Stdout)
	if d["input"] != song || fmt.Sprint(d["clip"]) != "map[length_seconds:10 start_seconds:20]" {
		t.Fatalf("clip: %s", r.Stdout)
	}
	reqs := e.api.Requests()
	clip := reqs[len(reqs)-1]
	if clip.FileSize <= 0 || !strings.HasSuffix(clip.FileName, ".flac") {
		t.Fatalf("the clip was not sent: %+v", clip)
	}
	wantCode(t, e.run("recognize", song, "--at", "5:00"), 2, "invalid_argument")

	// 40 seconds is 4 chunks of 12 seconds; the plan knows it exactly.
	r = e.ok("recognize", song, "--enterprise", "--limit", "none", "--dry-run")
	plan := doc(t, r.Stdout)["plan"].(map[string]any)
	if plan["requests"] != float64(4) || plan["approximate"] == true {
		t.Fatalf("enterprise plan: %v", plan)
	}
}

// Parallel batch workers share the results cache and the jobs database.
func TestConcurrentBatch(t *testing.T) {
	e := newEnv(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	for i := 0; i < 60; i++ {
		e.file(filepath.Join("many", fmt.Sprintf("s%02d.mp3", i)), fmt.Sprintf("parallel %d", i))
	}
	folder := filepath.Join(e.dir, "many")
	r := e.ok("recognize", folder, "--max-files", "none", "--yes", "--concurrency", "12")
	ls := lines(t, r.Stdout)
	results := 0
	for _, l := range ls {
		if l["type"] == "result" {
			results++
		}
	}
	if sum := ls[len(ls)-1]; results != 60 || sum["type"] != "summary" || sum["recognized"] != float64(60) {
		t.Fatalf("%d results, summary %v", results, sum)
	}
	// A new job over the same files is answered from the cache.
	e.ok("recognize", folder, "--max-files", "none", "--yes", "--concurrency", "12", "--new")
	if n := e.api.Count(testutil.EndpointRecognize); n != 60 {
		t.Fatalf("%d requests; the second run should be all cached", n)
	}
	r = e.bin("recognize", folder, "--max-files", "none", "--yes", "--new", "--no-cache", "--concurrency", "12")
	if r.Code != 0 || e.api.Count(testutil.EndpointRecognize) != 120 {
		t.Fatalf("binary: exit %d, %d requests\n%s", r.Code, e.api.Count(testutil.EndpointRecognize), r.Stderr)
	}
}

// The binary works on its own: version, help, the command reference, and a
// recognition through the environment's API server.
func TestBinary(t *testing.T) {
	e := newEnv(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	v := doc(t, e.binOK("version", "--format", "json").Stdout)
	if v["version"] == nil {
		t.Fatalf("version: %v", v)
	}
	help := e.binOK("--help").Stdout
	for _, want := range []string{"Recognition:", "Streams:", "Account:", "recognize", "now-playing"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help lacks %q:\n%s", want, help)
		}
	}
	a, b := e.binOK("commands", "--json").Stdout, e.binOK("commands", "--json").Stdout
	if a != b {
		t.Fatal("audd commands --json is not stable")
	}
	r := e.binOK("recognize", e.file("song.mp3", "binary song"))
	if d := doc(t, r.Stdout); d["result"].(map[string]any)["title"] != "Warriors" {
		t.Fatalf("recognize: %s", r.Stdout)
	}
	if r := e.bin("recognize", "--enterprise", e.file("long.mp3", "x")); r.Code != 6 {
		t.Fatalf("enterprise without --limit: exit %d", r.Code)
	}
}

// A login-fetched token that the API stops accepting is replaced once with
// the account's current token, even when parallel batch workers all get
// error 900, and the batch finishes.
func TestBatchHealsLoginToken(t *testing.T) {
	e := newEnv(t)
	e.loginByPaste()
	// The account's token changed elsewhere: the API now accepts only the
	// new one, and the account server hands it out.
	e.api.AcceptTokens(testutil.FakeRotatedToken)
	for _, tool := range testutil.DefaultFakeTools() {
		if tool.Name == "get_api_token" {
			tool.Handle = func(map[string]any) (map[string]any, string, bool) {
				return map[string]any{"api_token": testutil.FakeRotatedToken}, "", false
			}
			e.mcp.SetTool(tool)
		}
	}
	e.file(filepath.Join("pair", "a.mp3"), "pair a")
	e.file(filepath.Join("pair", "b.mp3"), "pair b")
	r := e.ok("recognize", filepath.Join(e.dir, "pair"), "--max-files", "2", "--yes", "--concurrency", "2")
	ls := lines(t, r.Stdout)
	if sum := ls[len(ls)-1]; sum["type"] != "summary" || sum["recognized"] != float64(2) {
		t.Fatalf("batch: %s\n%s", r.Stdout, r.Stderr)
	}
	rejected := 0
	for _, req := range e.api.Requests() {
		if req.Token == testutil.FakeAPIToken {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("the batch never sent the old token, so nothing was healed")
	}
	if n := len(e.mcp.CallsTo("get_api_token")); n != 2 {
		t.Fatalf("get_api_token called %d times; want once at login and once to heal", n)
	}
	d := doc(t, e.ok("token", "show", "--reveal").Stdout)
	if d["token"] != testutil.FakeRotatedToken || d["source"] != "login" {
		t.Fatalf("token after healing: %v", d)
	}
}
