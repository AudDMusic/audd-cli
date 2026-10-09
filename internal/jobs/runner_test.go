package jobs

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func inputs(paths []string) []media.Input {
	out := make([]media.Input, len(paths))
	for i, p := range paths {
		out[i] = media.Input{Path: p}
	}
	return out
}

func intp(n int) *int { return &n }

// lines parses JSONL output.
func lines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not JSON: %q", sc.Text())
		}
		if m["schema_version"] != float64(1) || m["type"] == nil {
			t.Fatalf("missing schema_version/type: %q", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func ofType(ls []map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, l := range ls {
		if l["type"] == typ {
			out = append(out, l)
		}
	}
	return out
}

func exitOf(err error) int { return output.ExitCode(err) }

func TestBatchStreamsResultsAndSummary(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-one.mp3", "none-two.mp3", "bad-three.mp3")
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(10), Return: "spotify"})
	if exitOf(err) != output.ExitPartial {
		t.Fatalf("want exit 7 for a failed file, got %v (%d)", err, exitOf(err))
	}
	if sum.Recognized != 1 || sum.NoMatch != 1 || sum.Failed != 1 || sum.RequestsSpent != 3 || len(sum.JobID) != 6 {
		t.Fatalf("summary %+v", sum)
	}
	ls := lines(t, ta.stdout.String())
	results := ofType(ls, "result")
	if len(results) != 3 {
		t.Fatalf("results: %s", ta.stdout)
	}
	byStatus := map[string]map[string]any{}
	for _, r := range results {
		byStatus[r["status"].(string)] = r
		if r["job_id"] != sum.JobID {
			t.Fatalf("job_id %v", r["job_id"])
		}
	}
	m := byStatus["matched"]["result"].(map[string]any)
	if m["title"] != "match-one" || m["extra_field"] != float64(7) {
		t.Fatalf("API fields, unknown ones included, must pass through: %v", m)
	}
	if byStatus["no_match"]["result"] != nil {
		t.Fatalf("no match is result null: %v", byStatus["no_match"])
	}
	e := byStatus["failed"]["error"].(map[string]any)
	if e["api_code"] != float64(300) || e["code"] != "invalid_audio" {
		t.Fatalf("error %v", e)
	}
	s := ofType(ls, "summary")
	if len(s) != 1 || s[0]["recognized"] != float64(1) || s[0]["status"] != StatusPartial {
		t.Fatalf("summary line %v", s)
	}
	if len(ofType(ls, "event")) != 1 {
		t.Fatalf("job_started event missing")
	}
	if !strings.Contains(f.params["match-one.mp3"], "return=spotify") {
		t.Fatalf("--return not sent: %q", f.params["match-one.mp3"])
	}
	if !strings.Contains(ta.stderr.String(), "Plan: 3 files, 3 requests ($0.015 ") {
		t.Fatalf("plan should be shown on stderr: %q", ta.stderr)
	}
}

func TestInterruptThenResumeDoesNotResend(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	var names []string
	for i := 0; i < 10; i++ {
		names = append(names, "match-"+string(rune('a'+i))+".mp3")
	}
	paths := mkfiles(t, names...)
	ctx, cancel := context.WithCancel(context.Background())
	f.onReq = func(n int) {
		if n == 3 {
			cancel() // Ctrl-C while requests are in flight
		}
	}
	sum, err := RunBatch(ctx, ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: nil, Concurrency: 2})
	if exitOf(err) != ExitInterrupted {
		t.Fatalf("want exit 130, got %v", err)
	}
	var oe *output.Error
	errors.As(err, &oe)
	if oe.Hint != "audd jobs resume "+sum.JobID {
		t.Fatalf("hint %q", oe.Hint)
	}
	first := f.totalRequests()
	if first < 3 || first >= 10 {
		t.Fatalf("requests before stop: %d", first)
	}
	st, _ := Open()
	job, _ := st.Get(sum.JobID)
	st.Close()
	// Every request that was sent finished and was saved.
	if job.Done != first || job.Status != StatusInterrupted {
		t.Fatalf("job after interrupt %+v (requests %d)", job, first)
	}

	f.onReq = nil
	ta.stdout.Reset()
	sum2, err := RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if sum2.JobID != sum.JobID || sum2.Recognized != 10 || sum2.RequestsSpent != 10-first {
		t.Fatalf("resume summary %+v", sum2)
	}
	for _, n := range names {
		if c := f.count(n); c != 1 {
			t.Fatalf("%s sent %d times", n, c)
		}
	}
	if got := len(ofType(lines(t, ta.stdout.String()), "result")); got != 10-first {
		t.Fatalf("resume printed %d results, want %d", got, 10-first)
	}
	// The summary counts the whole job, like audd jobs list, and says
	// what this run used.
	summary := ofType(lines(t, ta.stdout.String()), "summary")
	if len(summary) != 1 || summary[0]["recognized"] != float64(10) || summary[0]["requests_spent"] != float64(10) ||
		summary[0]["requests_spent_this_run"] != float64(10-first) {
		t.Fatalf("resume summary line %v", summary)
	}
	st, _ = Open()
	job, _ = st.Get(sum.JobID)
	st.Close()
	if job.Requests != 10 {
		t.Fatalf("jobs list counts %d requests", job.Requests)
	}
}

func TestIdenticalRunResumesStoppedJob(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3", "match-3.mp3", "match-4.mp3")
	ta.Flags.MaxRequests = 2
	opts := app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(4), Concurrency: 1}
	sum, err := RunBatch(context.Background(), ta.App, opts)
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "max_requests_reached" || oe.Exit != output.ExitSafety {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(oe.Hint, "audd jobs resume "+sum.JobID) || !strings.Contains(oe.Message, "2 of 4 files not done") {
		t.Fatalf("message %q hint %q", oe.Message, oe.Hint)
	}
	if f.totalRequests() != 2 {
		t.Fatalf("budget allowed %d requests", f.totalRequests())
	}

	// Without a terminal, an identical re-run must choose.
	ta.Flags.MaxRequests = 0
	_, err = RunBatch(context.Background(), ta.App, opts)
	if !errors.As(err, &oe) || oe.Code != "resume_available" || oe.Exit != output.ExitSafety {
		t.Fatalf("want resume_available, got %#v", err)
	}
	if !strings.Contains(oe.Hint, "audd jobs resume "+sum.JobID) || !strings.Contains(oe.Hint, "--resume") || !strings.Contains(oe.Hint, "--new") ||
		!strings.Contains(oe.Message, "2 of 4 files left") {
		t.Fatalf("message %q hint %q", oe.Message, oe.Hint)
	}
	if f.totalRequests() != 2 {
		t.Fatalf("refusing must send nothing: %d", f.totalRequests())
	}
	// --yes is not a choice between the two.
	opts.Yes = true
	if _, err = RunBatch(context.Background(), ta.App, opts); output.AsError(err).Code != "resume_available" {
		t.Fatalf("--yes alone: %v", err)
	}

	ta.stdout.Reset()
	opts.Resume = true
	sum2, err := RunBatch(context.Background(), ta.App, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.JobID != sum.JobID || sum2.Recognized != 4 {
		t.Fatalf("--resume should continue %s: %+v", sum.JobID, sum2)
	}
	if f.totalRequests() != 4 {
		t.Fatalf("total requests %d", f.totalRequests())
	}
	ls := lines(t, ta.stdout.String())
	var resumed map[string]any
	for _, e := range ofType(ls, "event") {
		switch e["event"] {
		case "job_resumed":
			resumed = e
		case "job_started":
			t.Fatalf("a resumed job is not a new start: %v", e)
		}
	}
	if resumed == nil || resumed["job_id"] != sum.JobID || resumed["done"] != float64(2) || resumed["total"] != float64(4) || resumed["to_run"] != float64(2) {
		t.Fatalf("job_resumed event: %v", ofType(ls, "event"))
	}
	if len(ofType(ls, "result")) != 2 {
		t.Fatalf("results %s", ta.stdout)
	}
	if !strings.Contains(ta.stderr.String(), "Resuming job "+sum.JobID+": 2 of 4 files left") {
		t.Fatalf("stderr %q", ta.stderr)
	}
	// A finished job is not offered again: the next identical run is a new
	// job, with or without --resume.
	sum3, err := RunBatch(context.Background(), ta.App, opts)
	if err != nil || sum3.JobID == sum.JobID {
		t.Fatalf("finished jobs are not resumed: %+v %v", sum3, err)
	}
}

func TestNewStartsOverAndConflictsWithResume(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3")
	ta.Flags.MaxRequests = 1
	opts := app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(4), Concurrency: 1}
	sum, _ := RunBatch(context.Background(), ta.App, opts)

	ta.Flags.MaxRequests = 0
	bad := opts
	bad.Resume, bad.New = true, true
	if _, err := RunBatch(context.Background(), ta.App, bad); exitOf(err) != output.ExitUsage {
		t.Fatalf("--resume with --new: %v", err)
	}
	opts.New = true
	sum2, err := RunBatch(context.Background(), ta.App, opts)
	if err != nil || sum2.JobID == sum.JobID || sum2.Recognized != 2 {
		t.Fatalf("--new: %+v %v", sum2, err)
	}
	if f.totalRequests() != 3 {
		t.Fatalf("requests %d", f.totalRequests())
	}
}

func TestStdinInputsAreNotMatchedOrResumed(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3")
	ins := []media.Input{{Path: paths[0]}, {Path: paths[1], IsStdin: true}}
	ta.Flags.MaxRequests = 1
	opts := app.BatchOptions{Inputs: ins, MaxFiles: intp(4), Concurrency: 1}
	sum, _ := RunBatch(context.Background(), ta.App, opts)

	// The same command again does not find the old job...
	ta.Flags.MaxRequests = 0
	sum2, err := RunBatch(context.Background(), ta.App, opts)
	if err != nil || sum2.JobID == sum.JobID {
		t.Fatalf("stdin batches are not matched: %+v %v", sum2, err)
	}
	// ...and resuming it after the spooled audio is gone says why.
	os.Remove(paths[1])
	ta.stdout.Reset()
	_, err = RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID})
	if exitOf(err) != output.ExitPartial {
		t.Fatalf("resume: %v", err)
	}
	r := ofType(lines(t, ta.stdout.String()), "result")
	if len(r) != 1 || r[0]["error"].(map[string]any)["code"] != "stdin_unavailable" {
		t.Fatalf("results %s", ta.stdout)
	}
}

func TestEnterpriseBudgetIsAHardCeiling(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	urls := []media.Input{{URL: "https://example.com/a.mp3"}, {URL: "https://example.com/b.mp3"}, {URL: "https://example.com/c.mp3"}}

	// --limit 4 with --max-requests 10: 4, 4, then the last 2.
	ta.Flags.MaxRequests = 10
	opts := app.BatchOptions{Inputs: urls, MaxFiles: intp(3), Enterprise: true, Limit: intp(4), Concurrency: 1}
	sum, err := RunBatch(context.Background(), ta.App, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"https://example.com/a.mp3": "limit=4", "https://example.com/b.mp3": "limit=4", "https://example.com/c.mp3": "limit=2"}
	for u, l := range want {
		if !strings.Contains(f.params[u], l) {
			t.Fatalf("%s sent %q, want %s", u, f.params[u], l)
		}
	}
	// A URL's length is not known: the limits are what the calls may have
	// used, not what they used.
	if sum.RequestsSpent != 0 || sum.RequestsReserved != 10 {
		t.Fatalf("spent %d reserved %d", sum.RequestsSpent, sum.RequestsReserved)
	}
	st, _ := Open()
	job, _ := st.Get(sum.JobID)
	items, _ := st.Items(sum.JobID)
	st.Close()
	if job.Requests != 0 || job.RequestsReserved != 10 || items[0].Requests != 0 || items[0].RequestsReserved != 4 {
		t.Fatalf("job %+v item %+v", job, items[0])
	}

	// No --limit: the first URL may use the whole budget, and nothing more
	// is sent.
	f2 := newFakeAPI(t)
	ta2 := newTestApp(t, f2, output.FormatJSONL)
	ta2.Flags.MaxRequests = 10
	opts.Limit, opts.New = nil, true
	_, err = RunBatch(context.Background(), ta2.App, opts)
	oe := output.AsError(err)
	if oe.Code != "max_requests_reached" || !strings.Contains(oe.Hint, "--limit N") || strings.Contains(oe.Hint, "ffprobe") ||
		!strings.Contains(oe.Message, "10 are reserved") {
		t.Fatalf("got %q hint %q", oe.Message, oe.Hint)
	}
	if f2.totalRequests() != 1 || !strings.Contains(f2.params["https://example.com/a.mp3"], "limit=10") {
		t.Fatalf("requests %d params %v", f2.totalRequests(), f2.params)
	}
}

func TestEnterpriseKnownLengthStopsInsteadOfCutting(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	old := Durations
	Durations = func(path string) (time.Duration, bool) { return 30 * time.Second, true } // 3 chunks
	t.Cleanup(func() { Durations = old })
	paths := mkfiles(t, "a.mp3", "b.mp3")
	ta.Flags.MaxRequests = 5
	_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Enterprise: true, Concurrency: 1})
	if output.AsError(err).Code != "max_requests_reached" {
		t.Fatalf("got %v", err)
	}
	if f.totalRequests() != 1 || !strings.Contains(f.params["a.mp3"], "limit=3") {
		t.Fatalf("requests %d params %v", f.totalRequests(), f.params)
	}
}

func TestRateLimitBackoffStopsOnInterrupt(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	ctx, cancel := context.WithCancel(context.Background())
	old := sleep
	sleep = func(c context.Context, d time.Duration) error {
		cancel() // Ctrl-C during the first backoff
		<-c.Done()
		return c.Err()
	}
	t.Cleanup(func() { sleep = old })
	paths := mkfiles(t, "rate-1.mp3")
	sum, err := RunBatch(ctx, ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(1)})
	if exitOf(err) != ExitInterrupted {
		t.Fatalf("want 130, got %v", err)
	}
	if f.count("rate-1.mp3") != 1 {
		t.Fatalf("retried after Ctrl-C: %d", f.count("rate-1.mp3"))
	}
	st, _ := Open()
	defer st.Close()
	job, _ := st.Get(sum.JobID)
	if job.Remaining != 1 {
		t.Fatalf("the item should be left for resume: %+v", job)
	}
}

func TestResumeOfferDeclinedInTerminalStartsNewJob(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3")
	ta.Flags.MaxRequests = 1
	opts := app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(4), Concurrency: 1}
	sum, _ := RunBatch(context.Background(), ta.App, opts)

	ta.Flags.MaxRequests, ta.Flags.Yes = 0, false
	ta.Out = output.NewPrinter(ta.stdout, ta.stderr, output.PrinterOptions{Format: output.FormatJSONL, StdinTTY: true, Stdin: strings.NewReader("n\ny\n")})
	sum2, err := RunBatch(context.Background(), ta.App, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.JobID == sum.JobID {
		t.Fatal("declining the resume offer should start a new job")
	}
	if !strings.Contains(ta.stderr.String(), "Resume it? [y/N]") || !strings.Contains(ta.stderr.String(), "Recognize 2 files, using 2 requests? [y/N]") {
		t.Fatalf("prompts: %q", ta.stderr)
	}
}

func TestPostUploadFailureIsNotRetriedAutomatically(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "drop-1.mp3", "match-2.mp3")
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2)})
	if exitOf(err) != output.ExitPartial {
		t.Fatalf("got %v", err)
	}
	if f.count("drop-1.mp3") != 1 {
		t.Fatalf("a dropped upload must not be retried by the SDK or the runner: %d", f.count("drop-1.mp3"))
	}
	st, _ := Open()
	items, _ := st.Items(sum.JobID)
	st.Close()
	if items[0].State != StateFailed || items[0].SafeRetry || items[0].ErrCode != "network" || items[0].Requests != 1 {
		t.Fatalf("item %+v", items[0])
	}

	// A plain resume leaves it alone...
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID}); exitOf(err) != output.ExitPartial {
		t.Fatalf("got %v", err)
	}
	if f.count("drop-1.mp3") != 1 {
		t.Fatal("resume without --retry-failed re-sent a possibly billed file")
	}
	// ...--retry-failed sends it again.
	RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID, RetryFailed: true})
	if f.count("drop-1.mp3") != 2 || f.count("match-2.mp3") != 1 {
		t.Fatalf("retry-failed: drop %d, match %d", f.count("drop-1.mp3"), f.count("match-2.mp3"))
	}
}

func TestPreUploadFailureIsRetriedOnResume(t *testing.T) {
	ta := newTestApp(t, nil, output.FormatJSONL)
	paths := mkfiles(t, "a.mp3", "b.mp3")
	var calls sync.Map
	fail := true
	old := Recognize
	Recognize = func(ctx context.Context, a *app.App, in media.Input, p Params) (json.RawMessage, error) {
		n, _ := calls.LoadOrStore(in.Path, new(int))
		*(n.(*int))++
		if fail && filepath.Base(in.Path) == "a.mp3" {
			return nil, &audd.AudDConnectionError{Cause: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
		}
		return json.RawMessage(`{"artist":"A","title":"T"}`), nil
	}
	t.Cleanup(func() { Recognize = old })

	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Concurrency: 1})
	if exitOf(err) != output.ExitPartial || sum.RequestsSpent != 1 {
		t.Fatalf("got %v %+v", err, sum)
	}
	st, _ := Open()
	items, _ := st.Items(sum.JobID)
	st.Close()
	if !items[0].SafeRetry || items[0].Requests != 0 {
		t.Fatalf("a dial failure never reached AudD: %+v", items[0])
	}
	fail = false
	sum2, err := RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID})
	if err != nil || sum2.Recognized != 2 {
		t.Fatalf("resume should retry it: %+v %v", sum2, err)
	}
	if n, _ := calls.Load(paths[1]); *(n.(*int)) != 1 {
		t.Fatal("b.mp3 sent twice")
	}
}

func TestAuthErrorStopsTheBatch(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "auth-1.mp3", "match-2.mp3", "match-3.mp3")
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(3), Concurrency: 1})
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Exit != output.ExitAuth || oe.APICode != 900 {
		t.Fatalf("got %#v", err)
	}
	if f.totalRequests() != 1 {
		t.Fatalf("should stop after the rejected token, sent %d", f.totalRequests())
	}
	st, _ := Open()
	job, _ := st.Get(sum.JobID)
	st.Close()
	if job.Pending != 3 || job.Status != StatusStopped {
		t.Fatalf("job %+v", job)
	}
}

func TestRateLimitBacksOffAndRetries(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	waits := noSleep(t)
	paths := mkfiles(t, "rate-1.mp3")
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(1)})
	if err != nil || sum.Recognized != 1 {
		t.Fatalf("%+v %v", sum, err)
	}
	if f.count("rate-1.mp3") != 3 || len(*waits) != 2 || (*waits)[0] != 2*time.Second || (*waits)[1] != 4*time.Second {
		t.Fatalf("requests %d waits %v", f.count("rate-1.mp3"), *waits)
	}
}

func TestCacheHitsAreFreeAndMarked(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	dir := t.TempDir()
	a := filepath.Join(dir, "match-a.mp3")
	b := filepath.Join(dir, "match-renamed.mp3")
	os.WriteFile(a, []byte("same bytes"), 0o644)
	os.WriteFile(b, []byte("same bytes"), 0o644)

	cache := map[[32]byte]json.RawMessage{}
	var mu sync.Mutex
	key := func(in media.Input) [32]byte {
		data, _ := os.ReadFile(in.Path)
		return sha256.Sum256(data)
	}
	oldGet, oldPut := CacheGet, CachePut
	CacheGet = func(_ *app.App, in media.Input, _ Params) (json.RawMessage, bool) {
		mu.Lock()
		defer mu.Unlock()
		r, ok := cache[key(in)]
		return r, ok
	}
	CachePut = func(_ *app.App, in media.Input, _ Params, r json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		cache[key(in)] = r
	}
	t.Cleanup(func() { CacheGet, CachePut = oldGet, oldPut })

	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs([]string{a}), MaxFiles: intp(1)})
	if err != nil || sum.Cached != 0 {
		t.Fatal(err)
	}
	ta.stdout.Reset()
	sum, err = RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs([]string{a, b}), MaxFiles: intp(2)})
	if err != nil || sum.Cached != 2 || sum.RequestsSpent != 0 {
		t.Fatalf("%+v %v", sum, err)
	}
	if f.totalRequests() != 1 {
		t.Fatalf("renamed copy should come from the cache, requests %d", f.totalRequests())
	}
	for _, r := range ofType(lines(t, ta.stdout.String()), "result") {
		if r["cached"] != true {
			t.Fatalf("not marked cached: %v", r)
		}
	}
	if !strings.Contains(ta.stderr.String(), "2 already cached") {
		t.Fatalf("plan should count cached files: %q", ta.stderr)
	}

	// --no-cache sends it again.
	RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs([]string{b}), MaxFiles: intp(1), NoCache: true})
	if f.totalRequests() != 2 {
		t.Fatalf("--no-cache: requests %d", f.totalRequests())
	}
	// ...and still records the fresh result.
	delete(cache, key(inputs([]string{b})[0]))
	RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs([]string{b}), MaxFiles: intp(1), NoCache: true})
	if _, ok := cache[key(inputs([]string{b})[0])]; !ok {
		t.Fatal("--no-cache results are recorded in the cache")
	}
}

func TestMaxFilesRefusesBeforeAnything(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3", "match-3.mp3")
	_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2)})
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "max_files_exceeded" || oe.Exit != output.ExitSafety || !strings.Contains(oe.Hint, "--max-files 3") {
		t.Fatalf("got %#v", err)
	}
	st, _ := Open()
	defer st.Close()
	if jobs, _ := List(st); len(jobs) != 0 || f.totalRequests() != 0 {
		t.Fatal("nothing should be created or sent")
	}
}

func TestConfirmationRequiredWithoutYes(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	ta.Flags.Yes = false
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3")
	_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2)})
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "confirmation_required" || oe.Exit != output.ExitSafety {
		t.Fatalf("got %#v", err)
	}
	st, _ := Open()
	defer st.Close()
	if jobs, _ := List(st); len(jobs) != 0 || f.totalRequests() != 0 {
		t.Fatal("a declined run leaves no job and sends nothing")
	}
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Yes: true}); err != nil {
		t.Fatalf("BatchOptions.Yes confirms: %v", err)
	}
}

func TestDryRunShowsPlanOnly(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSON)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3", "match-3.mp3")
	_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(5), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "dry_run_json", ta.stdout.Bytes())
	st, _ := Open()
	defer st.Close()
	if jobs, _ := List(st); len(jobs) != 0 || f.totalRequests() != 0 {
		t.Fatal("dry run must not create jobs or send requests")
	}

	ta2 := newTestApp(t, f, output.FormatTable)
	RunBatch(context.Background(), ta2.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(5), DryRun: true})
	testutil.Golden(t, "dry_run_table", ta2.stdout.Bytes())
}

// In JSON lines the plan is an event, never a "result" line that a
// consumer would count as a file.
func TestDryRunJSONLIsAnEvent(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3")
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(5), DryRun: true}); err != nil {
		t.Fatal(err)
	}
	ls := lines(t, ta.stdout.String())
	if len(ls) != 1 || ls[0]["type"] != "event" || ls[0]["event"] != "dry_run" || ls[0]["dry_run"] != true || ls[0]["endpoint"] != "standard" {
		t.Fatalf("%s", ta.stdout.String())
	}
	if plan, ok := ls[0]["plan"].(map[string]any); !ok || plan["requests"] != float64(2) {
		t.Fatalf("plan: %s", ta.stdout.String())
	}
}

// Enterprise URLs without --limit have no known length: the plan says the
// requests are unknown instead of counting one per URL.
func TestDryRunUnboundedURLs(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSON)
	in := []media.Input{{URL: "https://example.com/a.mp3"}, {URL: "https://example.com/b.mp3"}}
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: in, MaxFiles: intp(5), Enterprise: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Plan map[string]any `json:"plan"`
	}
	if err := json.Unmarshal(ta.stdout.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if v, ok := doc.Plan["requests"]; !ok || v != nil || doc.Plan["unbounded"] != true || doc.Plan["unknown_length_files"] != float64(2) {
		t.Fatalf("%s", ta.stdout.String())
	}
}

func TestEnterpriseBatch(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	old := Durations
	Durations = func(path string) (time.Duration, bool) { return 61 * time.Second, true }
	t.Cleanup(func() { Durations = old })
	paths := mkfiles(t, "mix.mp3")
	opts := app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(1), Enterprise: true, Limit: intp(20), Return: "spotify",
		EnterpriseOpts: map[string]string{"skip": "0", "use_timecode": "true", "custom": "x"}}
	sum, err := RunBatch(context.Background(), ta.App, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sum.RequestsSpent != 6 { // ceil(61 s / 12 s)
		t.Fatalf("requests %d", sum.RequestsSpent)
	}
	if f.hosts["enterprise.audd.io"] != 1 {
		t.Fatalf("hosts %v", f.hosts)
	}
	p := f.params["mix.mp3"]
	for _, want := range []string{"limit=20", "skip=0", "use_timecode=true", "custom=x", "accurate_offsets=true"} {
		if !strings.Contains(p, want) {
			t.Fatalf("enterprise params %q missing %s", p, want)
		}
	}
	if strings.Contains(p, "return=") {
		t.Fatalf("enterprise must not send return: %q", p)
	}
	r := ofType(lines(t, ta.stdout.String()), "result")[0]
	ms := r["result"].([]any)
	second := ms[1].(map[string]any)
	if len(ms) != 2 || second["start_seconds"] != float64(63) || second["end_seconds"] != float64(69) || second["title"] != "mix two" {
		t.Fatalf("matches %v", ms)
	}

	// The limit caps the estimate.
	ta.stdout.Reset()
	opts.DryRun, opts.Limit = true, intp(3)
	RunBatch(context.Background(), ta.App, opts)
	if !strings.Contains(ta.stdout.String(), `"requests":3`) {
		t.Fatalf("dry run %s", ta.stdout)
	}
}

func TestCSVAndTableOutput(t *testing.T) {
	f := newFakeAPI(t)
	paths := mkfiles(t, "match-1.mp3", "none-2.mp3", "bad-3.mp3")
	dir := filepath.Dir(paths[0])
	norm := func(s, jobID string) []byte {
		s = strings.ReplaceAll(s, dir+string(filepath.Separator), "DIR/")
		return []byte(strings.ReplaceAll(s, jobID, "JOBID"))
	}

	ta := newTestApp(t, f, output.FormatCSV)
	sum, _ := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(3), Concurrency: 1})
	testutil.Golden(t, "batch_csv", norm(ta.stdout.String(), sum.JobID))

	ta2 := newTestApp(t, f, output.FormatTable)
	sum, err := RunBatch(context.Background(), ta2.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(3), Concurrency: 1, NoCache: true})
	if exitOf(err) != output.ExitPartial {
		t.Fatalf("got %v", err)
	}
	testutil.Golden(t, "batch_table", norm(ta2.stdout.String(), sum.JobID))
	if !strings.Contains(ta2.stderr.String(), "1 recognized, 1 no match, 1 failed, 0 cached; 3 requests used in this run.") {
		t.Fatalf("human summary: %q", ta2.stderr)
	}
}

func TestStandardFileTooLargeFailsWithoutRequest(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	dir := t.TempDir()
	big := filepath.Join(dir, "match-big.wav")
	fh, _ := os.Create(big)
	fh.Truncate(11 << 20)
	fh.Close()
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs([]string{big}), MaxFiles: intp(1)})
	if exitOf(err) != output.ExitPartial || f.totalRequests() != 0 || sum.RequestsSpent != 0 {
		t.Fatalf("%v %d", err, f.totalRequests())
	}
	r := ofType(lines(t, ta.stdout.String()), "result")[0]
	if r["error"].(map[string]any)["code"] != "file_too_large" {
		t.Fatalf("%v", r)
	}
	if e := output.AsError(err); strings.Contains(e.Hint, "--retry-failed") || !strings.Contains(e.Hint, "--enterprise") {
		t.Fatalf("hint: %q", e.Hint)
	}

	// The plan leaves the large file out of the request estimate.
	small := mkfiles(t, "match-1.mp3")[0]
	ta = newTestApp(t, f, output.FormatTable)
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs([]string{big, small}), MaxFiles: intp(2), DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "2 files, 1 request (") || !strings.Contains(ta.stdout.String(), "1 file over 10 MB will not be sent") {
		t.Fatalf("plan: %s", ta.stdout.String())
	}
}

func TestResumeUnknownJob(t *testing.T) {
	ta := newTestApp(t, nil, output.FormatJSONL)
	_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: "zzzzzz"})
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "job_not_found" {
		t.Fatalf("got %#v", err)
	}
}

func TestRunBatchHookIsAssigned(t *testing.T) {
	ta := newTestApp(t, nil, output.FormatJSONL)
	_, err := app.RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: "zzzzzz"})
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "job_not_found" {
		t.Fatalf("app.RunBatch should be the jobs runner: %#v", err)
	}
}

func TestEnterpriseUnknownLengthWithoutFFprobe(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	old := Durations
	Durations = func(path string) (time.Duration, bool) { return 0, false } // no ffprobe
	t.Cleanup(func() { Durations = old })
	paths := mkfiles(t, "a.mp3", "b.mp3", "c.mp3")
	ta.Flags.MaxRequests = 500

	// --limit none: the first file's limit is the whole budget, so nothing
	// is left for the next one. Nothing is reported as used for certain.
	opts := app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(20), Enterprise: true, Concurrency: 1}
	sum, err := RunBatch(context.Background(), ta.App, opts)
	oe := output.AsError(err)
	if oe.Code != "max_requests_reached" || f.totalRequests() != 1 || !strings.Contains(f.params["a.mp3"], "limit=500") {
		t.Fatalf("got %v, %d requests, params %v", err, f.totalRequests(), f.params)
	}
	for _, want := range []string{"install ffprobe", "--limit N", "audd jobs resume " + sum.JobID + " --max-requests N"} {
		if !strings.Contains(oe.Hint, want) {
			t.Fatalf("hint %q lacks %q", oe.Hint, want)
		}
	}
	if !strings.Contains(oe.Message, "500 are reserved") || !strings.Contains(oe.Message, "2 of 3 files not done") {
		t.Fatalf("message %q", oe.Message)
	}
	if sum.RequestsSpent != 0 || sum.RequestsReserved != 500 {
		t.Fatalf("summary %+v", sum)
	}
	summary := ofType(lines(t, ta.stdout.String()), "summary")
	if len(summary) != 1 || summary[0]["requests_spent"] != float64(0) || summary[0]["requests_reserved"] != float64(500) {
		t.Fatalf("summary event %v", summary)
	}
	st, _ := Open()
	job, _ := st.Get(sum.JobID)
	st.Close()
	if job.Requests != 0 || job.RequestsReserved != 500 {
		t.Fatalf("job %+v", job)
	}
	if got := RequestsUsed(job.Requests, job.RequestsReserved, job.RequestsUnknownFiles); got != "up to 500 requests used (file lengths were not known)" {
		t.Fatalf("jobs show line %q", got)
	}

	// --limit 5 caps each file's reservation, so all three fit.
	f2 := newFakeAPI(t)
	ta2 := newTestApp(t, f2, output.FormatJSONL)
	ta2.Flags.MaxRequests = 500
	opts.Limit, opts.New = intp(5), true
	sum2, err := RunBatch(context.Background(), ta2.App, opts)
	if err != nil || f2.totalRequests() != 3 || sum2.RequestsReserved != 15 || sum2.RequestsSpent != 0 {
		t.Fatalf("%+v %v, %d requests", sum2, err, f2.totalRequests())
	}
}

func TestEnterpriseWithoutLimitOrBudgetIsUnknown(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatTable)
	urls := []media.Input{{URL: "https://example.com/a.mp3"}}
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: urls, MaxFiles: intp(1), Enterprise: true})
	if err != nil || sum.RequestsSpent != 0 || sum.RequestsReserved != 0 {
		t.Fatalf("%+v %v", sum, err)
	}
	if strings.Contains(f.params["https://example.com/a.mp3"], "limit=") {
		t.Fatalf("sent a limit: %q", f.params["https://example.com/a.mp3"])
	}
	if !strings.Contains(ta.stderr.String(), "0 requests used, plus an unknown number for 1 file of unknown length sent without --limit in this run") {
		t.Fatalf("stderr %q", ta.stderr)
	}
	st, _ := Open()
	job, _ := st.Get(sum.JobID)
	st.Close()
	if job.RequestsUnknownFiles != 1 {
		t.Fatalf("job %+v", job)
	}
}

func TestRequestsUsed(t *testing.T) {
	for _, c := range []struct {
		spent, reserved, unknown int
		want                     string
	}{
		{1, 0, 0, "1 request used"},
		{1200, 0, 0, "1,200 requests used"},
		{0, 500, 0, "up to 500 requests used (file lengths were not known)"},
		{12, 1500, 0, "12 requests used, plus up to 1,500 for files of unknown length"},
		{12, 0, 2, "12 requests used, plus an unknown number for 2 files of unknown length sent without --limit"},
	} {
		if got := RequestsUsed(c.spent, c.reserved, c.unknown); got != c.want {
			t.Errorf("RequestsUsed(%d, %d, %d) = %q, want %q", c.spent, c.reserved, c.unknown, got, c.want)
		}
	}
}

func TestDailyRateLimitStopsTheBatch(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	waits := noSleep(t)
	paths := mkfiles(t, "daily-1.mp3", "match-2.mp3")
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Concurrency: 1})
	oe := output.AsError(err)
	if oe.Code != "rate_limited" || oe.APICode != 611 || oe.Exit != output.ExitQuota || !strings.Contains(oe.Hint, "audd jobs resume "+sum.JobID) {
		t.Fatalf("got %#v", err)
	}
	if len(*waits) != 0 || f.count("daily-1.mp3") != 1 || f.count("match-2.mp3") != 0 {
		t.Fatalf("waits %v, requests %d/%d", *waits, f.count("daily-1.mp3"), f.count("match-2.mp3"))
	}
	st, _ := Open()
	job, _ := st.Get(sum.JobID)
	st.Close()
	if job.Pending != 2 || job.Failed != 0 {
		t.Fatalf("job %+v", job)
	}
}

func TestMissingFileIsRetriedOnResume(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3")
	moved := paths[0] + ".moved"
	if err := os.Rename(paths[0], moved); err != nil {
		t.Fatal(err)
	}
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Concurrency: 1})
	if exitOf(err) != output.ExitPartial {
		t.Fatalf("got %v", err)
	}
	st, _ := Open()
	items, _ := st.Items(sum.JobID)
	st.Close()
	if items[0].ErrCode != "file_not_found" || !items[0].SafeRetry || items[0].Requests != 0 {
		t.Fatalf("item %+v", items[0])
	}
	if err := os.Rename(moved, paths[0]); err != nil {
		t.Fatal(err)
	}
	sum2, err := RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID})
	if err != nil || sum2.Recognized != 2 || f.count("match-1.mp3") != 1 || f.count("match-2.mp3") != 1 {
		t.Fatalf("%+v %v", sum2, err)
	}
}

func TestClassifyMappedErrors(t *testing.T) {
	dial := &audd.AudDConnectionError{Cause: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	mappedNet := &output.Error{Code: "network", Message: "could not reach AudD", Hint: "check your connection", Retryable: true, Exit: output.ExitNetwork}
	f := classify(errors.Join(mappedNet, dial))
	if !f.safe || f.err.Hint != "check your connection" || f.err.Message != "could not reach AudD" {
		t.Fatalf("joined dial error: %+v %+v", f, f.err)
	}
	dropped := &audd.AudDConnectionError{Cause: errors.New("connection reset")}
	f = classify(errors.Join(mappedNet, dropped))
	if f.safe || f.err.Retryable || mappedNet.Retryable != true {
		t.Fatalf("joined post-upload error: %+v %+v", f, f.err)
	}
	// A mapped error alone cannot tell when the connection failed.
	if f := classify(mappedNet); f.safe || f.fatal || f.rateLimit {
		t.Fatalf("mapped network error: %+v", f)
	}
	if f := classify(&output.Error{Code: "rate_limited", APICode: 611, Exit: output.ExitQuota}); !f.fatal || f.rateLimit {
		t.Fatalf("611: %+v", f)
	}
	if f := classify(&output.Error{Code: "rate_limited", Exit: output.ExitQuota}); !f.rateLimit || f.fatal {
		t.Fatalf("throttled: %+v", f)
	}
	if f := classify(&output.Error{Code: "token_rejected", APICode: 900, Exit: output.ExitAuth}); !f.fatal {
		t.Fatalf("auth: %+v", f)
	}
	if f := classify(&output.Error{Code: "interrupted", Exit: 130}); f.safe || f.err.Code != "interrupted" {
		t.Fatalf("interrupted: %+v", f)
	}
	if f := classify(&audd.AudDAPIError{HTTPStatus: 429}); !f.rateLimit || f.fatal || f.err.Code != "rate_limited" {
		t.Fatalf("429: %+v", f)
	}
}

func TestBudgetLimitedResultIsMarkedAndNotReusedWithoutBudget(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	cache := map[string]json.RawMessage{}
	var mu sync.Mutex
	key := func(in media.Input, p Params) string {
		return in.URL + "|" + fingerprint(nil, p.Map())
	}
	oldGet, oldPut := CacheGet, CachePut
	CacheGet = func(_ *app.App, in media.Input, p Params) (json.RawMessage, bool) {
		mu.Lock()
		defer mu.Unlock()
		r, ok := cache[key(in, p)]
		return r, ok
	}
	CachePut = func(_ *app.App, in media.Input, p Params, r json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		cache[key(in, p)] = r
	}
	t.Cleanup(func() { CacheGet, CachePut = oldGet, oldPut })

	urls := []media.Input{{URL: "https://example.com/long.mp3"}}
	ta.Flags.MaxRequests = 3
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: urls, MaxFiles: intp(1), Enterprise: true})
	if err != nil || !strings.Contains(f.params["https://example.com/long.mp3"], "limit=3") {
		t.Fatalf("%v params %v", err, f.params)
	}
	res := ofType(lines(t, ta.stdout.String()), "result")
	if len(res) != 1 || res[0]["budget_limit"] != float64(3) {
		t.Fatalf("result should be marked: %v", res)
	}
	summary := ofType(lines(t, ta.stdout.String()), "summary")
	if len(summary) != 1 || summary[0]["budget_limited_files"] != float64(1) {
		t.Fatalf("summary %v", summary)
	}
	if !strings.Contains(ta.stderr.String(), "1 file may be only partly recognized") {
		t.Fatalf("stderr %q", ta.stderr)
	}
	st, _ := Open()
	job, _ := st.Get(sum.JobID)
	items, _ := st.Items(sum.JobID)
	st.Close()
	if job.BudgetLimitedFiles != 1 || items[0].BudgetLimit != 3 {
		t.Fatalf("job %+v item %+v", job, items[0])
	}

	// Without a budget the full file is wanted: the partial result must not
	// come from the cache.
	ta2 := newTestApp(t, f, output.FormatJSONL)
	ta2.Flags.MaxRequests = 0
	sum2, err := RunBatch(context.Background(), ta2.App, app.BatchOptions{Inputs: urls, MaxFiles: intp(1), Enterprise: true, New: true})
	if err != nil || sum2.Cached != 0 || f.totalRequests() != 2 {
		t.Fatalf("%+v %v requests %d", sum2, err, f.totalRequests())
	}
	if strings.Contains(f.params["https://example.com/long.mp3"], "limit=") {
		t.Fatalf("second run sent a limit: %q", f.params["https://example.com/long.mp3"])
	}
	st, _ = Open()
	items, _ = st.Items(sum2.JobID)
	st.Close()
	if items[0].BudgetLimit != 0 {
		t.Fatalf("full run marked as limited: %+v", items[0])
	}
}

func TestNoTokenStopsBeforeAnyJobIsStored(t *testing.T) {
	ta := newTestApp(t, nil, output.FormatJSONL)
	ta.Flags.Yes = false // the token check comes before the confirmation
	ta.APIClient = func() (*audd.Client, error) {
		return nil, &output.Error{Code: "no_token", Message: "no AudD API token is set", Exit: output.ExitAuth}
	}
	paths := mkfiles(t, "a.mp3", "b.mp3")
	_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Concurrency: 1})
	if e := output.AsError(err); e.Code != "no_token" || e.Exit != output.ExitAuth {
		t.Fatalf("got %v", err)
	}
	st, _ := Open()
	defer st.Close()
	if jobs, _ := List(st); len(jobs) != 0 {
		t.Fatalf("no job is stored: %+v", jobs)
	}
}

func TestBatchStoppedBeforeAnyFileIsNotKept(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	old := Durations
	Durations = func(path string) (time.Duration, bool) { return 60 * time.Second, true } // 5 chunks
	t.Cleanup(func() { Durations = old })
	paths := mkfiles(t, "a.mp3", "b.mp3")
	ta.Flags.MaxRequests = 3
	_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Enterprise: true, Concurrency: 1})
	if e := output.AsError(err); e.Code != "max_requests_reached" || strings.Contains(e.Hint+e.Message, "job") {
		t.Fatalf("got %#v", err)
	}
	if f.totalRequests() != 0 {
		t.Fatalf("sent %d", f.totalRequests())
	}
	st, _ := Open()
	defer st.Close()
	if jobs, _ := List(st); len(jobs) != 0 {
		t.Fatalf("nothing was sent, so no job is kept to resume: %+v", jobs)
	}
	// The same command with a larger budget simply runs.
	ta.Flags.MaxRequests = 0
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Enterprise: true, Concurrency: 1}); err != nil && output.AsError(err).Code == "resume_available" {
		t.Fatalf("no resume prompt: %v", err)
	}
}

// An item is saved as in flight before its request goes out, so a process
// that dies mid-request (a crash, SIGKILL) leaves it for --retry-failed
// instead of a plain resume sending it again.
func TestItemInFlightIsNotResentByAPlainResume(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3")
	var seen []Item
	f.onReq = func(n int) {
		if n != 1 {
			return
		}
		st, err := Open()
		if err != nil {
			t.Error(err)
			return
		}
		defer st.Close()
		jobs, _ := List(st)
		if len(jobs) == 1 {
			seen, _ = st.Items(jobs[0].ID)
		}
	}
	sum, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(2), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].State != StateFailed || seen[0].ErrCode != ErrCodeInFlight || seen[0].SafeRetry {
		t.Fatalf("while the first request was out: %+v", seen)
	}

	// Simulate a process that died at that moment.
	st, _ := Open()
	if err := st.SaveItem(sum.JobID, seen[0]); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID}); exitOf(err) != output.ExitPartial {
		t.Fatalf("got %v", err)
	}
	if f.count("match-1.mp3") != 1 {
		t.Fatal("a plain resume re-sent a file that may have been counted")
	}
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID, RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	if f.count("match-1.mp3") != 2 || f.count("match-2.mp3") != 1 {
		t.Fatalf("retry-failed: %d %d", f.count("match-1.mp3"), f.count("match-2.mp3"))
	}
}

// readHook runs fn the first time the prompt reads its answer.
type readHook struct {
	once sync.Once
	fn   func()
	r    *strings.Reader
}

func (h *readHook) Read(p []byte) (int, error) {
	h.once.Do(h.fn)
	return h.r.Read(p)
}

func TestResumeWaitingAtPromptDoesNotResendWhatAnotherProcessFinished(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3", "match-3.mp3", "match-4.mp3")
	ta.Flags.MaxRequests = 1
	sum, _ := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(4), Concurrency: 1})
	if f.totalRequests() != 1 {
		t.Fatalf("first run sent %d", f.totalRequests())
	}
	ta.Flags.MaxRequests = 0

	// Process A waits at the prompt while process B resumes and finishes
	// the same job.
	other := *ta.App
	var bOut, bErr strings.Builder
	other.Out = output.NewPrinter(&bOut, &bErr, output.PrinterOptions{Format: output.FormatJSONL, NoColor: true, Stdin: strings.NewReader("")})
	hook := &readHook{r: strings.NewReader("y\n"), fn: func() {
		if _, err := RunBatch(context.Background(), &other, app.BatchOptions{ResumeID: sum.JobID, Yes: true}); err != nil {
			t.Errorf("process B: %v", err)
		}
	}}
	ta.Flags.Yes = false
	ta.Out = output.NewPrinter(ta.stdout, ta.stderr, output.PrinterOptions{Format: output.FormatJSONL, NoColor: true, StdinTTY: true, Stdin: hook})
	if _, err := RunBatch(context.Background(), ta.App, app.BatchOptions{ResumeID: sum.JobID}); err != nil {
		t.Fatal(err)
	}
	if f.totalRequests() != 4 {
		t.Fatalf("each file must be sent once, got %d requests", f.totalRequests())
	}
	if !strings.Contains(ta.stderr.String(), "another audd process finished it") {
		t.Fatalf("stderr %q", ta.stderr)
	}
}

func TestBatchFieldsAreCheckedBeforeAnythingIsSent(t *testing.T) {
	cases := []struct {
		format output.Format
		fields []string
		ok     bool
	}{
		{output.FormatCSV, []string{"nosuch"}, false},
		{output.FormatCSV, []string{"result.artist"}, false},
		{output.FormatCSV, []string{"input", "artist"}, true},
		{output.FormatJSONL, []string{"nosuch"}, false},
		{output.FormatJSONL, []string{"input", "result.artist"}, true},
		// Real fields a match lacks print as null instead of failing at the end.
		{output.FormatJSONL, []string{"input", "result.isrc", "result.apple_music.url"}, true},
		{output.FormatJSONL, []string{"result.artst"}, false},
		{output.FormatTable, []string{"nosuch"}, false},
		{output.FormatTable, []string{"result.title"}, true},
	}
	for _, c := range cases {
		f := newFakeAPI(t)
		ta := newTestApp(t, f, c.format)
		ta.Out = output.NewPrinter(ta.stdout, ta.stderr, output.PrinterOptions{Format: c.format, NoColor: true, Fields: c.fields, Stdin: strings.NewReader("")})
		paths := mkfiles(t, "match-one.mp3", "none-two.mp3")
		_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(9)})
		if !c.ok {
			if exitOf(err) != output.ExitUsage || !strings.Contains(output.AsError(err).Message, "available fields") {
				t.Fatalf("%s %v: want exit 2 listing the fields, got %v", c.format, c.fields, err)
			}
			if f.totalRequests() != 0 {
				t.Fatalf("%s %v: sent %d requests", c.format, c.fields, f.totalRequests())
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s %v: %v", c.format, c.fields, err)
		}
		if !strings.Contains(ta.stdout.String(), "match-one") {
			t.Fatalf("%s %v: results missing: %q", c.format, c.fields, ta.stdout)
		}
		if c.format == output.FormatTable && strings.Contains(ta.stdout.String(), "none-two") {
			t.Fatalf("table --fields result.title should trim the line: %q", ta.stdout)
		}
	}
}

// The plan and the question say what --max-requests lets the run spend.
func TestPlanSaysWhereMaxRequestsStops(t *testing.T) {
	f := newFakeAPI(t)
	ta := newTestApp(t, f, output.FormatJSONL)
	ta.Flags.Yes = false
	ta.Flags.MaxRequests = 2
	paths := mkfiles(t, "match-1.mp3", "match-2.mp3", "match-3.mp3", "match-4.mp3")
	_, err := RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(4), Concurrency: 1})
	e := output.AsError(err)
	if e.Code != "confirmation_required" || !strings.Contains(e.Message, "Recognize 4 files, stopping after 2 requests (--max-requests 2)") {
		t.Fatalf("question: %#v", err)
	}
	if !strings.Contains(ta.stderr.String(), "Plan: 4 files, stops after 2 of 4 requests (--max-requests 2), at most $0.01 at the pay-as-you-go price") {
		t.Fatalf("plan: %s", ta.stderr.String())
	}
	if f.totalRequests() != 0 {
		t.Fatal("nothing should be sent")
	}
	// A ceiling the plan stays under is not mentioned.
	ta.stderr.Reset()
	ta.Flags.MaxRequests = 10
	_, _ = RunBatch(context.Background(), ta.App, app.BatchOptions{Inputs: inputs(paths), MaxFiles: intp(4), Concurrency: 1})
	if strings.Contains(ta.stderr.String(), "stops after") || !strings.Contains(ta.stderr.String(), "Plan: 4 files, 4 requests") {
		t.Fatalf("plan: %s", ta.stderr.String())
	}
}
