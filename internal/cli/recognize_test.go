package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cli"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

const tok = testutil.PlaceholderToken

// setup isolates state, starts a fake API, and sets the token in the env.
func setup(t *testing.T) (*testutil.FakeAPI, string) {
	t.Helper()
	testutil.Isolate(t)
	f := testutil.NewFakeAPI(t)
	t.Setenv("AUDD_API_TOKEN", tok)
	return f, t.TempDir()
}

func audio(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %q (%v)", s, err)
	}
	return m
}

func errCode(t *testing.T, r testutil.Result) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(r.Stderr), "\n")
	m := decode(t, lines[len(lines)-1])
	return m["error"].(map[string]any)["code"].(string)
}

func TestRecognizeFileJSONAndCache(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	song := audio(t, dir, "song.mp3", "audio bytes")

	r := run(t, "recognize", song)
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	doc := decode(t, r.Stdout)
	res := doc["result"].(map[string]any)
	if doc["schema_version"].(float64) != 1 || doc["input"] != song || doc["cached"] != false ||
		res["artist"] != "Imagine Dragons" || res["new_field"] != "kept as is" {
		t.Fatalf("doc: %s", r.Stdout)
	}
	if !strings.HasPrefix(r.Stdout, `{"schema_version":1,"input":`) {
		t.Fatalf("key order: %s", r.Stdout)
	}
	req := f.Requests()[0]
	if req.Token != tok || req.FileName != "song.mp3" || req.FileSize != len("audio bytes") {
		t.Fatalf("request: %+v", req)
	}

	// Same bytes under another name: served from the cache, nothing sent.
	copyPath := audio(t, dir, "copy of song.flac", "audio bytes")
	r = run(t, "recognize", copyPath)
	doc = decode(t, r.Stdout)
	if r.Code != 0 || doc["cached"] != true || doc["result"].(map[string]any)["title"] != "Warriors" {
		t.Fatalf("cached: %d %s", r.Code, r.Stdout)
	}
	if n := f.Count(testutil.EndpointRecognize); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
	// Different parameters are a different result.
	r = run(t, "recognize", song, "--return", "spotify,apple_music")
	if r.Code != 0 || f.Count(testutil.EndpointRecognize) != 2 {
		t.Fatalf("--return should miss the cache: %d %s", r.Code, r.Stderr)
	}
	if got := f.Requests()[1].Form["return"]; got != "apple_music,spotify" {
		t.Fatalf("return param %q", got)
	}
	// --no-cache sends again.
	r = run(t, "recognize", song, "--no-cache")
	if r.Code != 0 || f.Count(testutil.EndpointRecognize) != 3 {
		t.Fatalf("--no-cache: %d", f.Count(testutil.EndpointRecognize))
	}
}

func TestRecognizeURL(t *testing.T) {
	f, _ := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	r := run(t, "recognize", "https://audd.tech/example.mp3")
	if r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	req := f.Requests()[0]
	if req.Form["url"] != "https://audd.tech/example.mp3" || req.FileSize != -1 {
		t.Fatalf("%+v", req)
	}
	run(t, "recognize", "https://audd.tech/example.mp3")
	if f.Count(testutil.EndpointRecognize) != 1 {
		t.Fatal("URL results are cached")
	}
}

func TestRecognizeNoMatch(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(nil))
	song := audio(t, dir, "quiet.wav", "silence")
	r := run(t, "recognize", song)
	if r.Code != 0 || decode(t, r.Stdout)["result"] != nil || !strings.Contains(r.Stdout, `"result":null`) {
		t.Fatalf("no match: %d %s", r.Code, r.Stdout)
	}
	r = run(t, "recognize", song, "--format", "table")
	if r.Code != 0 || r.Stdout != "No match.\n" {
		t.Fatalf("human no match: %q", r.Stdout)
	}
	r = run(t, "recognize", song, "--fail-on-no-match")
	if r.Code != output.ExitUnexpected || errCode(t, r) != "no_match" || !strings.Contains(r.Stdout, `"result":null`) || !strings.Contains(r.Stderr, "--at 1:00") {
		t.Fatalf("--fail-on-no-match: %d %s %s", r.Code, r.Stdout, r.Stderr)
	}
}

func TestRecognizeHumanAndQuiet(t *testing.T) {
	old := app.RenderResult
	app.RenderResult = app.PlainResult
	t.Cleanup(func() { app.RenderResult = old })
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	song := audio(t, dir, "song.mp3", "x")
	r := run(t, "recognize", song, "--format", "table", "--details")
	want := "Imagine Dragons — Warriors\nWarriors · Universal Music · 2014-09-18\nhttps://lis.tn/Warriors\nISRC:     USUM71409990\nUPC:      00602547058929\nTimecode: 00:40\n"
	if r.Code != 0 || r.Stdout != want {
		t.Fatalf("human:\n%s", r.Stdout)
	}
	r = run(t, "recognize", song, "--format", "table", "--quiet")
	if r.Stdout != "Imagine Dragons — Warriors\n" {
		t.Fatalf("quiet: %q", r.Stdout)
	}
	if !strings.Contains(run(t, "recognize", song, "--format", "table").Stdout, "(cached result, no request used)") {
		t.Fatal("cached results are marked for people")
	}
}

func TestRecognizeNoArtReachesRenderer(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	song := audio(t, dir, "song.mp3", "x")
	var got []app.ResultView
	old := app.RenderResult
	app.RenderResult = func(w io.Writer, a *app.App, r app.ResultView, details bool) { got = append(got, r) }
	t.Cleanup(func() { app.RenderResult = old })
	run(t, "recognize", song, "--format", "table")
	run(t, "recognize", song, "--format", "table", "--no-art")
	if len(got) != 2 || got[0].NoArt || !got[1].NoArt {
		t.Fatalf("%+v", got)
	}
	if _, ok := got[1].Extra["no_art"]; ok {
		t.Fatal("Extra holds only API fields")
	}
}

func TestRecognizeFieldsCSV(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	song := audio(t, dir, "song.mp3", "x")
	// CSV columns are flat, the same for one file as for a batch.
	r := run(t, "recognize", song, "--format", "csv", "--fields", "input,artist,title,cached")
	if r.Code != 0 || r.Stdout != "input,artist,title,cached\n"+song+",Imagine Dragons,Warriors,false\n" {
		t.Fatalf("csv: %q %s", r.Stdout, r.Stderr)
	}
	r = run(t, "recognize", song, "--format", "csv", "--fields", "result.isrc")
	if r.Code != output.ExitUsage || !strings.Contains(r.Stderr, "use isrc") {
		t.Fatalf("dotted CSV field: %d %s", r.Code, r.Stderr)
	}
	batch := run(t, "recognize", dir, "--format", "csv", "--max-files", "5", "--yes")
	one := run(t, "recognize", song, "--format", "csv")
	if h1, h2 := strings.SplitN(batch.Stdout, "\n", 2)[0], strings.SplitN(one.Stdout, "\n", 2)[0]; h1 != h2 {
		t.Fatalf("CSV columns differ:\nbatch %s\none   %s", h1, h2)
	}
}

func TestRecognizeFieldsMissingInResult(t *testing.T) {
	f, dir := setup(t)
	m := testutil.MatchResult()
	delete(m, "isrc")
	f.Reply(testutil.EndpointRecognize, testutil.Success(m))
	song := audio(t, dir, "song.mp3", "x")
	r := run(t, "recognize", song, "--format", "json", "--fields", "result.artist,result.isrc")
	if r.Code != 0 || !strings.Contains(r.Stdout, `"result":{"artist":"Imagine Dragons","isrc":null}`) {
		t.Fatalf("%d %q %s", r.Code, r.Stdout, r.Stderr)
	}
	audio(t, dir, "b.mp3", "b")
	r = run(t, "recognize", dir, "--max-files", "5", "--yes", "--fields", "input,result.isrc")
	if r.Code != 0 || strings.Count(r.Stdout, `"result":{"isrc":null}`) != 2 {
		t.Fatalf("batch: %d %q %s", r.Code, r.Stdout, r.Stderr)
	}
}

func TestRecognizeUnknownFields(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	song := audio(t, dir, "song.mp3", "x")
	r := run(t, "recognize", song, "--format", "json", "--fields", "result.artst")
	if r.Code != output.ExitUsage || r.Stdout != "" || !strings.Contains(r.Stderr, `result.artst`) || !strings.Contains(r.Stderr, "result.artist") {
		t.Fatalf("%d %q %s", r.Code, r.Stdout, r.Stderr)
	}
	// Nothing was sent; the corrected command sends one request.
	if len(f.Requests()) != 0 {
		t.Fatalf("a wrong --fields sent %d requests", len(f.Requests()))
	}
	r = run(t, "recognize", song, "--format", "json", "--fields", "result.artist")
	if r.Code != 0 || !strings.Contains(r.Stdout, `"result":{"artist":"Imagine Dragons"}`) || len(f.Requests()) != 1 {
		t.Fatalf("%d %q %s (%d requests)", r.Code, r.Stdout, r.Stderr, len(f.Requests()))
	}
}

func TestRecognizeAPIErrors(t *testing.T) {
	tests := []struct {
		code int
		exit int
		name string
	}{
		{900, output.ExitAuth, "token_rejected"},
		{902, output.ExitQuota, "quota_exceeded"},
		{904, output.ExitQuota, "not_enabled"},
		{19, output.ExitQuota, "blocked"},
		{100, output.ExitNetwork, "server"},
		{700, output.ExitUsage, "invalid_request"},
	}
	for _, tt := range tests {
		f, dir := setup(t)
		f.Reply(testutil.EndpointRecognize, testutil.APIError(tt.code, "nope"))
		r := run(t, "recognize", audio(t, dir, "a.mp3", "x"))
		if r.Code != tt.exit || errCode(t, r) != tt.name {
			t.Errorf("%d: exit %d %s", tt.code, r.Code, r.Stderr)
		}
		e := decode(t, strings.TrimSpace(r.Stderr))["error"].(map[string]any)
		if e["api_code"].(float64) != float64(tt.code) {
			t.Errorf("%d: api_code %v", tt.code, e["api_code"])
		}
	}
	// Errors are not cached.
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.APIError(902, "limit"))
	song := audio(t, dir, "a.mp3", "x")
	run(t, "recognize", song)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	if r := run(t, "recognize", song); r.Code != 0 || f.Count(testutil.EndpointRecognize) != 2 {
		t.Fatalf("after an error the file is sent again: %d %s", r.Code, r.Stderr)
	}
}

func TestRecognizeNoToken(t *testing.T) {
	testutil.Isolate(t)
	dir := t.TempDir()
	r := run(t, "recognize", audio(t, dir, "a.mp3", "x"))
	if r.Code != output.ExitAuth || errCode(t, r) != "no_token" {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
}

func TestRecognizeTokenFlagBeatsEnv(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(nil))
	run(t, "recognize", audio(t, dir, "a.mp3", "x"), "--token", "your-api-token")
	if f.Requests()[0].Token != "your-api-token" {
		t.Fatal("--token should win")
	}
}

func TestRecognizePreChecks(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	big := filepath.Join(dir, "big.wav")
	fh, _ := os.Create(big)
	fh.Truncate(11 << 20)
	fh.Close()
	r := run(t, "recognize", big)
	if r.Code != output.ExitUsage || errCode(t, r) != "file_too_large" || !strings.Contains(r.Stderr, "--at") {
		t.Fatalf("big file: %d %s", r.Code, r.Stderr)
	}
	if len(f.Requests()) != 0 {
		t.Fatal("nothing should be sent")
	}

	restore := cli.SetMediaHelpers(nil, func(string) (time.Duration, bool) { return 3 * time.Minute, true })
	defer restore()
	r = run(t, "recognize", audio(t, dir, "long.mp3", "x"))
	if r.Code != 0 || !strings.Contains(r.Stderr, "Only up to the first 12 seconds are analyzed") {
		t.Fatalf("long file note: %d %q", r.Code, r.Stderr)
	}
	r = run(t, "recognize", audio(t, dir, "long2.mp3", "y"), "--quiet")
	if strings.Contains(r.Stderr, "12 seconds") {
		t.Fatal("--quiet hides notes")
	}
	if strings.Contains(r.Stderr, "\n\n") {
		t.Fatalf("no empty line in machine output: %q", r.Stderr)
	}

	// For people, an empty line separates the note from the result card.
	// The progress line ("Recognizing <name>") is cut to the terminal
	// width, so even a long name leaves nothing behind.
	scr := newScreen(100)
	t.Setenv("NO_COLOR", "1")
	code := cli.Run(context.Background(), []string{"recognize", audio(t, dir, strings.Repeat("long", 30)+".mp3", "z"), "--no-art"},
		cli.IO{In: strings.NewReader(""), Out: scr, Err: scr, StdoutTTY: true, StderrTTY: true, StderrWidth: 100})
	if got := scr.text(); code != 0 || !strings.HasPrefix(got, "Only up to the first 12 seconds are analyzed. Use --at or --enterprise to recognize later parts.\n\n") ||
		strings.Contains(got, "\n\n\n") {
		t.Fatalf("exit %d, screen:\n%s", code, got)
	}
}

func TestRecognizeAt(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	song := audio(t, dir, "mix.mp3", "the whole mix")
	var gotAt, gotDur time.Duration
	var gotSrc string
	clip := filepath.Join(dir, "clip.mp3")
	cleaned := false
	restore := cli.SetMediaHelpers(func(ctx context.Context, src string, at, dur time.Duration) (string, func(), error) {
		gotSrc, gotAt, gotDur = src, at, dur
		os.WriteFile(clip, []byte("clip"), 0o644)
		return clip, func() { cleaned = true; os.Remove(clip) }, nil
	}, nil)
	defer restore()
	r := run(t, "recognize", song, "--at", "1:30")
	if r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	if gotSrc != song || gotAt != 90*time.Second || gotDur != 12*time.Second || !cleaned {
		t.Fatalf("trim %s %v %v cleaned=%v", gotSrc, gotAt, gotDur, cleaned)
	}
	doc := decode(t, r.Stdout)
	if doc["input"] != song || doc["clip"].(map[string]any)["start_seconds"].(float64) != 90 {
		t.Fatalf("doc %s", r.Stdout)
	}
	if f.Requests()[0].FileSize != len("clip") {
		t.Fatal("the clip is sent, not the whole file")
	}
	run(t, "recognize", song, "--at", "95", "--duration", "20s")
	if gotAt != 95*time.Second || gotDur != 20*time.Second {
		t.Fatalf("%v %v", gotAt, gotDur)
	}
	for _, bad := range [][]string{{"--at", "1:75"}, {"--at", "soon"}, {"--duration", "5"}} {
		r := run(t, append([]string{"recognize", song}, bad...)...)
		if r.Code != output.ExitUsage {
			t.Errorf("%v: exit %d", bad, r.Code)
		}
	}
}

func TestRecognizeAtWithoutFFmpeg(t *testing.T) {
	_, dir := setup(t)
	r := run(t, "recognize", audio(t, dir, "a.mp3", "x"), "--at", "10")
	if r.Code == 0 {
		t.Fatal("trimming without the media tools must fail")
	}
}

func enterpriseBody() map[string]any {
	return testutil.Success([]any{
		testutil.EnterpriseChunk("00:00", testutil.EnterpriseSong("A", "One", 500, 12000)),
		testutil.EnterpriseChunk("00:12", testutil.EnterpriseSong("A", "One", 0, 12000)),
		testutil.EnterpriseChunk("00:24", testutil.EnterpriseSong("B", "Two", 2000, 10000)),
	})
}

func TestRecognizeEnterpriseNeedsLimit(t *testing.T) {
	f, dir := setup(t)
	song := audio(t, dir, "mix.mp3", "x")
	r := run(t, "recognize", song, "--enterprise")
	if r.Code != output.ExitSafety || errCode(t, r) != "limit_required" || !strings.Contains(r.Stderr, "--dry-run") {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	// --dry-run sends nothing, so it plans without --limit.
	r = run(t, "recognize", song, "--enterprise", "--dry-run", "--format", "json")
	if r.Code != 0 || decode(t, r.Stdout)["dry_run"] != true || len(f.Requests()) != 0 {
		t.Fatalf("dry run without --limit: %d %s %s", r.Code, r.Stdout, r.Stderr)
	}
	// --limit none spends without a bound: it needs a confirmation.
	f.Reply(testutil.EndpointEnterprise, enterpriseBody())
	r = run(t, "recognize", song, "--enterprise", "--limit", "none")
	if r.Code != output.ExitSafety || errCode(t, r) != "confirmation_required" {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	if len(f.Requests()) != 0 {
		t.Fatal("nothing sent before confirmation")
	}
	r = run(t, "recognize", song, "--enterprise", "--limit", "none", "--yes")
	if r.Code != 0 || f.Requests()[0].Form["limit"] != "" {
		t.Fatalf("%d %s %+v", r.Code, r.Stderr, f.Requests())
	}
	// Flags that only make sense with --enterprise.
	for _, args := range [][]string{{"--limit", "3"}, {"--skip", "2"}, {"--tracklist"}} {
		r := run(t, append([]string{"recognize", song}, args...)...)
		if r.Code != output.ExitUsage {
			t.Errorf("%v: %d", args, r.Code)
		}
	}
	r = run(t, "recognize", song, "--enterprise", "--limit", "3", "--return", "spotify")
	if r.Code != output.ExitUsage || !strings.Contains(r.Stderr, "enterprise endpoint doesn't return") {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	r = run(t, "recognize", song, "--return", "napster")
	if r.Code != output.ExitUsage {
		t.Fatal("napster is not a metadata source")
	}
}

func TestRecognizeEnterprise(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointEnterprise, enterpriseBody())
	song := audio(t, dir, "mix.mp3", "x")
	r := run(t, "recognize", song, "--enterprise", "--limit", "5", "--skip", "0", "--every", "1", "--yes")
	if r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	form := f.Requests()[0].Form
	if form["limit"] != "5" || form["accurate_offsets"] != "true" || form["skip"] != "0" || form["every"] != "1" {
		t.Fatalf("form %v", form)
	}
	if !strings.Contains(r.Stderr, "1 file, ") {
		t.Fatalf("plan on stderr: %q", r.Stderr)
	}
	doc := decode(t, r.Stdout)
	ms := doc["matches"].([]any)
	m0 := ms[0].(map[string]any)
	if doc["enterprise"] != true || len(ms) != 3 || m0["start_seconds"].(float64) != 0.5 || m0["end_seconds"].(float64) != 12 || m0["score"].(float64) != 100 {
		t.Fatalf("doc %s", r.Stdout)
	}
	if _, ok := doc["tracks"]; ok {
		t.Fatal("tracks only with --tracklist")
	}

	r = run(t, "recognize", song, "--enterprise", "--limit", "5", "--skip", "0", "--every", "1", "--tracklist")
	doc = decode(t, r.Stdout)
	if doc["cached"] != true {
		t.Fatal("same request is cached")
	}
	tracks := doc["tracks"].([]any)
	t0 := tracks[0].(map[string]any)
	if len(tracks) != 2 || t0["start"] != "0:00" || t0["end"] != "0:24" || t0["matches"].(float64) != 2 {
		t.Fatalf("tracks %s", r.Stdout)
	}
	r = run(t, "recognize", song, "--enterprise", "--limit", "5", "--skip", "0", "--every", "1", "--tracklist", "--format", "table")
	want := "0:00 – 0:24        A — One\n0:26 – 0:34        B — Two\n(cached result, no requests used)\n"
	if r.Stdout != want {
		t.Fatalf("human tracklist:\n%q", r.Stdout)
	}
	r = run(t, "recognize", song, "--enterprise", "--limit", "5", "--skip", "0", "--every", "1", "--format", "jsonl")
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], `{"schema_version":1,"type":"result","input":"`) {
		t.Fatalf("jsonl rows: %s", r.Stdout)
	}
	for _, l := range lines {
		row := decode(t, l)
		if row["cached"] != true || row["artist"] == nil {
			t.Fatalf("jsonl rows are marked cached: %s", l)
		}
	}
	songJSON, _ := json.Marshal(song) // backslashes are escaped on Windows
	if !strings.Contains(lines[0], `"input":`+string(songJSON)+`,"cached":true,`) {
		t.Fatalf("input and cached lead each row: %s", lines[0])
	}
	r = run(t, "recognize", song, "--enterprise", "--limit", "5", "--skip", "0", "--every", "1", "--tracklist", "--format", "csv", "--fields", "start,end,artist,title")
	if r.Stdout != "start,end,artist,title\n0:00,0:24,A,One\n0:26,0:34,B,Two\n" {
		t.Fatalf("csv tracks: %q", r.Stdout)
	}
	r = run(t, "recognize", song, "--enterprise", "--limit", "5", "--skip", "0", "--every", "1", "--tracklist", "--format", "csv", "--fields", "cached,artist")
	if r.Stdout != "cached,artist\ntrue,A\ntrue,B\n" {
		t.Fatalf("csv tracks are marked cached: %q", r.Stdout)
	}
}

func TestRecognizeEnterpriseConfirmsMultiRequestRuns(t *testing.T) {
	f, dir := setup(t)
	song := audio(t, dir, "mix.mp3", strings.Repeat("x", 16000*12*30)) // ≈ 30 chunks
	r := run(t, "recognize", song, "--enterprise", "--limit", "20")
	if r.Code != output.ExitSafety || errCode(t, r) != "confirmation_required" {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stderr, "1 file, ") || len(f.Requests()) != 0 {
		t.Fatalf("plan shown, nothing sent: %q %v", r.Stderr, f.Requests())
	}
	f.Reply(testutil.EndpointEnterprise, testutil.Success([]any{}))
	r = run(t, "recognize", song, "--enterprise", "--limit", "20", "--yes")
	if r.Code != 0 || f.Requests()[0].Form["limit"] != "20" {
		t.Fatalf("%d %s %v", r.Code, r.Stderr, f.Requests())
	}
	// A single chunk costs one request, like standard recognition.
	f.Reply(testutil.EndpointEnterprise, testutil.Success([]any{}))
	r = run(t, "recognize", song, "--enterprise", "--limit", "1")
	if r.Code != 0 || len(f.Requests()) != 2 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
}

func TestRecognizeEnterpriseConfirmsWhenLengthIsGuessed(t *testing.T) {
	f, dir := setup(t)
	// A small file looks like one chunk by size, but its real length is
	// unknown, so the limit is what the call can spend.
	song := audio(t, dir, "short.mp3", strings.Repeat("x", 150*1024))
	r := run(t, "recognize", song, "--enterprise", "--limit", "20")
	if r.Code != output.ExitSafety || errCode(t, r) != "confirmation_required" {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	if len(f.Requests()) != 0 || !strings.Contains(r.Stderr, "up to 20 chunks") {
		t.Fatalf("nothing sent, worst case asked: %q %v", r.Stderr, f.Requests())
	}
	// With an exact length from ffprobe, one chunk needs no confirmation.
	defer cli.SetMediaHelpers(nil, func(string) (time.Duration, bool) { return 5 * time.Second, true })()
	f.Reply(testutil.EndpointEnterprise, testutil.Success([]any{}))
	r = run(t, "recognize", song, "--enterprise", "--limit", "20")
	if r.Code != 0 || len(f.Requests()) != 1 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
}

func TestRecognizeEnterpriseMaxRequestsCapsLimit(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointEnterprise, testutil.Success([]any{}))
	// About two minutes of audio at 128 kbit/s: 10 chunks.
	r := run(t, "recognize", audio(t, dir, "mix.mp3", strings.Repeat("x", 16000*120)), "--enterprise", "--limit", "none", "--max-requests", "4", "--yes")
	if r.Code != 0 || f.Requests()[0].Form["limit"] != "4" || !strings.Contains(r.Stderr, "Plan: 1 file, stops after 4 of ≈ 10 requests (--max-requests 4), at most $0.02 ") {
		t.Fatalf("%d %s %v", r.Code, r.Stderr, f.Requests())
	}
	if doc := decode(t, r.Stdout); len(doc["matches"].([]any)) != 0 {
		t.Fatal("no matches")
	}
	// The dry run says the same.
	r = run(t, "recognize", audio(t, dir, "mix2.mp3", strings.Repeat("y", 16000*120)), "--enterprise", "--limit", "20", "--max-requests", "4", "--dry-run", "--format", "json")
	if doc := decode(t, r.Stdout); r.Code != 0 || doc["plan"].(map[string]any)["max_requests"] != float64(4) {
		t.Fatalf("%d %s %s", r.Code, r.Stdout, r.Stderr)
	}
}

func TestRecognizeEnterpriseAtShiftsPositions(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointEnterprise, enterpriseBody())
	clip := filepath.Join(dir, "clip.mp3")
	restore := cli.SetMediaHelpers(func(ctx context.Context, src string, at, dur time.Duration) (string, func(), error) {
		os.WriteFile(clip, []byte("clip"), 0o644)
		return clip, func() {}, nil
	}, nil)
	defer restore()
	r := run(t, "recognize", audio(t, dir, "mix.mp3", "x"), "--enterprise", "--limit", "10", "--at", "1:00", "--duration", "2m", "--yes")
	if r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	m0 := decode(t, r.Stdout)["matches"].([]any)[0].(map[string]any)
	if m0["start_seconds"].(float64) != 60.5 {
		t.Fatalf("positions are in the original file: %v", m0)
	}
}

func TestRecognizeDryRun(t *testing.T) {
	f, dir := setup(t)
	song := audio(t, dir, "song.mp3", "x")
	restore := cli.SetMediaHelpers(nil, func(string) (time.Duration, bool) { return 61 * time.Second, true })
	defer restore()
	r := run(t, "recognize", song, "--enterprise", "--limit", "none", "--dry-run")
	if r.Code != 0 || len(f.Requests()) != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	esc, _ := json.Marshal(song)
	got := strings.Replace(r.Stdout, string(esc), `"SONG"`, 1)
	testutil.Golden(t, "recognize_dry_run_json", []byte(got))
	r = run(t, "recognize", song, "--dry-run", "--format", "table")
	testutil.Golden(t, "recognize_dry_run_human", []byte(r.Stdout))

	// In JSON lines a single input's plan is an event line, as for a batch.
	r = run(t, "recognize", song, "--dry-run", "--format", "jsonl")
	if doc := decode(t, r.Stdout); doc["type"] != "event" || doc["event"] != "dry_run" || doc["input"] == nil || doc["job_id"] != nil {
		t.Fatalf("jsonl dry run: %s", r.Stdout)
	}

	// Piped, a batch prints the same document as one input, and --fields
	// picks from it.
	audio(t, dir, "other.mp3", "y")
	r = run(t, "recognize", dir, "--max-files", "5", "--dry-run")
	if doc := decode(t, r.Stdout); r.Code != 0 || doc["type"] != nil || doc["event"] != nil || doc["dry_run"] != true || doc["input"] != nil {
		t.Fatalf("piped batch dry run: %d %s %s", r.Code, r.Stdout, r.Stderr)
	}
	r = run(t, "recognize", dir, "--max-files", "5", "--dry-run", "--fields", "plan.requests")
	if doc := decode(t, r.Stdout); r.Code != 0 || doc["plan"] == nil || doc["endpoint"] != nil {
		t.Fatalf("batch dry run --fields: %d %s %s", r.Code, r.Stdout, r.Stderr)
	}
	if r = run(t, "recognize", song, "--dry-run", "--fields", "event"); r.Code != output.ExitUsage {
		t.Fatalf("event is not a dry-run field: %d %s", r.Code, r.Stdout)
	}
}

func TestRecognizeBatchDelegates(t *testing.T) {
	_, dir := setup(t)
	audio(t, dir, "a.mp3", "a")
	audio(t, dir, "b.mp3", "b")
	r := run(t, "recognize", dir)
	if r.Code != output.ExitSafety || errCode(t, r) != "limit_required" || !strings.Contains(r.Stderr, "--max-files") {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	var got app.BatchOptions
	old := app.RunBatch
	defer func() { app.RunBatch = old }()
	app.RunBatch = func(ctx context.Context, a *app.App, opts app.BatchOptions) (app.BatchSummary, error) {
		got = opts
		return app.BatchSummary{JobID: "j1", Failed: 1}, nil
	}
	// --dry-run plans without --max-files.
	r = run(t, "recognize", dir, "--dry-run")
	if r.Code == output.ExitSafety || got.MaxFiles != nil || !got.DryRun || len(got.Inputs) != 2 {
		t.Fatalf("dry run without --max-files: %d %s %+v", r.Code, r.Stderr, got)
	}
	r = run(t, "recognize", dir, "--max-files", "10", "--enterprise", "--limit", "3", "--skip", "2", "--concurrency", "2", "--dry-run")
	if r.Code != output.ExitPartial || !strings.Contains(r.Stderr, "audd jobs resume j1") {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	if len(got.Inputs) != 2 || *got.MaxFiles != 10 || *got.Limit != 3 || !got.Enterprise || got.EnterpriseOpts["skip"] != "2" || got.Concurrency != 2 || !got.DryRun {
		t.Fatalf("%+v", got)
	}
	r = run(t, "recognize", dir, "--max-files", "none", "--at", "10")
	if r.Code != output.ExitUsage {
		t.Fatal("--at is for one input")
	}
}

func TestCatalogAdd(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointUpload, map[string]any{"status": "success", "result": nil})
	song := audio(t, dir, "mine.mp3", "my song")
	r := run(t, "catalog", "add", song, "--id", "42")
	if r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	doc := decode(t, r.Stdout)
	if doc["audio_id"].(float64) != 42 || doc["added"] != true {
		t.Fatal(r.Stdout)
	}
	req := f.Requests()[0]
	if req.Form["audio_id"] != "42" || req.FileSize != len("my song") {
		t.Fatalf("%+v", req)
	}
	if r := run(t, "catalog", "add", song); r.Code != output.ExitUsage {
		t.Fatal("--id is required")
	}
	// Server errors are reported once, never retried.
	f.On(testutil.EndpointUpload, func(testutil.FakeRequest) (int, any) { return http.StatusBadGateway, "bad gateway" })
	r = run(t, "catalog", "add", song, "--id", "42")
	if r.Code != output.ExitNetwork || f.Count(testutil.EndpointUpload) != 2 {
		t.Fatalf("%d %s, %d uploads", r.Code, r.Stderr, f.Count(testutil.EndpointUpload))
	}
	f.Reply(testutil.EndpointUpload, testutil.APIError(904, "no access"))
	r = run(t, "catalog", "add", song, "--id", "1")
	if r.Code != output.ExitQuota || !strings.Contains(r.Stderr, "custom catalog access") {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
}

func TestAPICommand(t *testing.T) {
	f, _ := setup(t)
	f.Reply("getStreams", testutil.Success([]any{map[string]any{"radio_id": 1, "url": "https://example.com/stream"}}))
	r := run(t, "api", "getStreams")
	doc := decode(t, r.Stdout)
	if r.Code != 0 || doc["schema_version"].(float64) != 1 || doc["status"] != "success" {
		t.Fatalf("%d %s %s", r.Code, r.Stdout, r.Stderr)
	}
	f.Reply("setCallbackUrl", testutil.APIError(602, "bad callback url"))
	r = run(t, "api", "setCallbackUrl", "url=notaurl")
	if r.Code != output.ExitUsage || decode(t, r.Stdout)["status"] != "error" || errCode(t, r) != "invalid_request" {
		t.Fatalf("%d %s %s", r.Code, r.Stdout, r.Stderr)
	}
	f.Reply("setCallbackUrl", map[string]any{"status": "error", "error": map[string]any{"error_code": " 602 ", "error_message": "bad callback url"}})
	if r := run(t, "api", "setCallbackUrl", "url=notaurl"); r.Code != output.ExitUsage || errCode(t, r) != "invalid_request" {
		t.Fatalf("string error_code: %d %s %s", r.Code, r.Stdout, r.Stderr)
	}
	if f.Requests()[1].Form["url"] != "notaurl" || f.Requests()[2].Form["url"] != "notaurl" {
		t.Fatal("params are sent")
	}
	for _, args := range [][]string{{"api", "get/../x"}, {"api", "getStreams", "noequals"}, {"api", "getStreams", "api_token=x"}} {
		if r := run(t, args...); r.Code != output.ExitUsage {
			t.Errorf("%v: %d", args, r.Code)
		}
	}
	r = run(t, "api", "--help")
	if strings.Contains(strings.ToLower(r.Stdout), "lyrics") {
		t.Fatal("no lyrics in help")
	}
}

func TestCacheClear(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	song := audio(t, dir, "a.mp3", "x")
	run(t, "recognize", song)
	r := run(t, "cache", "clear")
	if r.Code != 0 || decode(t, r.Stdout)["cleared"].(float64) != 1 {
		t.Fatalf("%s %s", r.Stdout, r.Stderr)
	}
	run(t, "recognize", song)
	if f.Count(testutil.EndpointRecognize) != 2 {
		t.Fatal("cleared results are fetched again")
	}
	if r := run(t, "cache", "clear", "--format", "table"); r.Stdout != "Cleared 1 cached result.\n" {
		t.Fatalf("%q", r.Stdout)
	}
}

func TestRecognizeBatchFieldsKeepEventsAndSummary(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	audio(t, dir, "a.mp3", "a")
	audio(t, dir, "b.mp3", "b")
	r := run(t, "recognize", dir, "--max-files", "5", "--yes", "--fields", "input,result.title")
	if r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	var results int
	var summary map[string]any
	for _, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		m := decode(t, line)
		switch m["type"] {
		case "result":
			results++
			if _, ok := m["cached"]; ok || m["input"] == nil || m["result"].(map[string]any)["title"] != "Warriors" {
				t.Fatalf("result lines keep only --fields: %s", line)
			}
		case "event":
			if m["event"] == nil || m["job_id"] == nil {
				t.Fatalf("event lines are printed in full: %s", line)
			}
		case "summary":
			summary = m
		}
	}
	if results != 2 || summary == nil || summary["job_id"] == nil || summary["recognized"] != float64(2) || summary["status"] == nil {
		t.Fatalf("summary %v\n%s", summary, r.Stdout)
	}
}

func TestNoTokenIsReportedFirst(t *testing.T) {
	testutil.Isolate(t)
	dir := t.TempDir()
	restore := cli.SetMediaHelpers(nil, func(string) (time.Duration, bool) { return 3 * time.Minute, true })
	defer restore()
	song := audio(t, dir, "song.mp3", "x")

	// Before the enterprise confirmation, and before the 12-second note.
	for _, args := range [][]string{
		{"recognize", song, "--enterprise", "--limit", "20"},
		{"recognize", song},
		{"catalog", "add", song, "--id", "1"},
	} {
		r := run(t, args...)
		if r.Code != output.ExitAuth || errCode(t, r) != "no_token" || strings.Contains(r.Stderr, "12 seconds") {
			t.Fatalf("%v: %d %s", args, r.Code, r.Stderr)
		}
	}
	// A batch stores no job, so setting the token and running the same
	// command again simply works.
	audio(t, dir, "other.mp3", "y")
	r := run(t, "recognize", dir, "--max-files", "5", "--yes")
	if r.Code != output.ExitAuth || errCode(t, r) != "no_token" {
		t.Fatalf("batch: %d %s", r.Code, r.Stderr)
	}
	if r := run(t, "jobs", "list", "--format", "json"); len(decode(t, r.Stdout)["items"].([]any)) != 0 {
		t.Fatalf("no job was stored: %s", r.Stdout)
	}
	// --dry-run needs no token.
	if r := run(t, "recognize", dir, "--max-files", "5", "--dry-run"); r.Code != 0 {
		t.Fatalf("dry run: %d %s", r.Code, r.Stderr)
	}
	f := testutil.NewFakeAPI(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	t.Setenv("AUDD_API_TOKEN", tok)
	if r := run(t, "recognize", dir, "--max-files", "5", "--yes"); r.Code != 0 {
		t.Fatalf("after setting the token: %d %s", r.Code, r.Stderr)
	}
}

func TestBilledCallsAreNotRetriedOnServerErrors(t *testing.T) {
	f, dir := setup(t)
	badGateway := func(testutil.FakeRequest) (int, any) { return http.StatusBadGateway, "bad gateway" }
	f.On(testutil.EndpointEnterprise, badGateway)
	f.On(testutil.EndpointUpload, badGateway)
	song := audio(t, dir, "mix.mp3", "x")

	r := run(t, "recognize", song, "--enterprise", "--limit", "5", "--yes")
	if r.Code != output.ExitNetwork || f.Count(testutil.EndpointEnterprise) != 1 {
		t.Fatalf("enterprise: exit %d, %d requests: %s", r.Code, f.Count(testutil.EndpointEnterprise), r.Stderr)
	}
	audio(t, dir, "mix2.mp3", "y")
	r = run(t, "recognize", dir, "--enterprise", "--limit", "5", "--max-files", "5", "--yes", "--concurrency", "1")
	if n := f.Count(testutil.EndpointEnterprise); n != 3 {
		t.Fatalf("batch enterprise: %d requests for 2 files (plus 1 before): %s", n, r.Stderr)
	}
	r = run(t, "api", "upload", "audio_id=1", "url=https://example.com/song.mp3")
	if r.Code != output.ExitNetwork || f.Count(testutil.EndpointUpload) != 1 {
		t.Fatalf("api upload: exit %d, %d requests: %s", r.Code, f.Count(testutil.EndpointUpload), r.Stderr)
	}
}

// A 5xx comes back only after the upload, so the file may have been
// counted: it is sent once, never again on its own.
func TestRecognizeServerErrorIsSentOnce(t *testing.T) {
	f, dir := setup(t)
	f.On(testutil.EndpointRecognize, func(testutil.FakeRequest) (int, any) { return http.StatusBadGateway, "bad gateway" })
	song := audio(t, dir, "song.mp3", "audio bytes")
	r := run(t, "recognize", song)
	if r.Code != output.ExitNetwork {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if n := f.Count(testutil.EndpointRecognize); n != 1 {
		t.Fatalf("sent %d times", n)
	}

	batch := filepath.Join(dir, "batch")
	if err := os.Mkdir(batch, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.mp3", "b.mp3", "c.mp3", "d.mp3"} {
		audio(t, batch, n, "audio "+n)
	}
	r = run(t, "recognize", batch, "--max-files", "5", "--yes")
	if r.Code != output.ExitPartial && r.Code != output.ExitNetwork {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	if n := f.Count(testutil.EndpointRecognize); n != 1+4 {
		t.Fatalf("batch sent %d requests for 4 files", n-1)
	}
}

// An enterprise URL without --limit has no known length, so the dry run
// does not claim it costs one request.
func TestRecognizeDryRunURLWithoutLimitIsUnbounded(t *testing.T) {
	f, _ := setup(t)
	r := run(t, "recognize", "https://example.com/3hour-mix.mp3", "--enterprise", "--limit", "none", "--dry-run")
	if r.Code != 0 || len(f.Requests()) != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	plan := decode(t, r.Stdout)["plan"].(map[string]any)
	if v, ok := plan["requests"]; !ok || v != nil || plan["unbounded"] != true {
		t.Fatalf("plan %v", plan)
	}
	r = run(t, "recognize", "https://example.com/3hour-mix.mp3", "--enterprise", "--limit", "none", "--dry-run", "--format", "table")
	if !strings.Contains(r.Stdout, "requests unknown") || strings.Contains(r.Stdout+r.Stderr, "ffprobe") || strings.Contains(r.Stdout, "1 request") {
		t.Fatalf("human: %q %q", r.Stdout, r.Stderr)
	}
}

func TestRecognizeBatchFailOnNoMatch(t *testing.T) {
	f, dir := setup(t)
	f.On(testutil.EndpointRecognize, func(r testutil.FakeRequest) (int, any) {
		if r.FileName == "quiet.mp3" {
			return http.StatusOK, testutil.Success(nil)
		}
		return http.StatusOK, testutil.Success(testutil.MatchResult())
	})
	audio(t, dir, "song.mp3", "a")
	audio(t, dir, "quiet.mp3", "b")
	if r := run(t, "recognize", dir, "--max-files", "none", "--yes"); r.Code != 0 {
		t.Fatalf("no match alone is not an error: %d %s", r.Code, r.Stderr)
	}
	r := run(t, "recognize", dir, "--max-files", "none", "--yes", "--fail-on-no-match")
	if r.Code != output.ExitUnexpected || errCode(t, r) != "no_match" || !strings.Contains(r.Stderr, "1 of the 2 files") || !strings.Contains(r.Stderr, "audd jobs show") {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	// Every file matched: success.
	_ = os.Remove(filepath.Join(dir, "quiet.mp3"))
	if r := run(t, "recognize", dir, "--max-files", "none", "--yes", "--fail-on-no-match"); r.Code != 0 {
		t.Fatalf("all matched: %d %s", r.Code, r.Stderr)
	}
}

// The explorer's Recent tab lists batch results, also with --no-cache and
// after audd cache clear (the jobs store still holds them), once each.
func TestRecentListsBatchResults(t *testing.T) {
	f, dir := setup(t)
	f.Reply(testutil.EndpointRecognize, testutil.Success(testutil.MatchResult()))
	a := audio(t, dir, "a.mp3", "a")
	b := audio(t, dir, "b.mp3", "b")
	if r := run(t, "recognize", dir, "--max-files", "none", "--yes", "--no-cache"); r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	check := func(when string) {
		t.Helper()
		got, err := cli.RecentSources(10)
		if err != nil {
			t.Fatal(err)
		}
		has := map[string]int{}
		for _, s := range got {
			has[s]++
		}
		if len(got) != 2 || has[a] != 1 || has[b] != 1 {
			t.Fatalf("%s: recent %v", when, got)
		}
	}
	check("after a --no-cache batch")
	if r := run(t, "cache", "clear"); r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	check("after cache clear")
}

func TestRecognizeBatchNamesTheConfigCeiling(t *testing.T) {
	_, dir := setup(t)
	audio(t, dir, "a.mp3", "a")
	audio(t, dir, "b.mp3", "b")
	if r := run(t, "config", "set", "max_requests", "1"); r.Code != 0 {
		t.Fatal(r.Stderr)
	}
	r := run(t, "recognize", dir, "--max-files", "5", "--yes")
	if r.Code != output.ExitSafety || errCode(t, r) != "max_requests_reached" ||
		!strings.Contains(r.Stderr, "the max_requests setting (1)") || !strings.Contains(r.Stderr, "audd config unset max_requests") {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
}
