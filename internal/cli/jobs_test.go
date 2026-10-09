package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cli"

	"github.com/AudDMusic/audd-cli/internal/jobs"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// seedJob stores a finished job with a match, a no-match, and a failure.
func seedJob(t *testing.T) string {
	t.Helper()
	st, err := jobs.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := []media.Input{{Path: "/music/one.mp3"}, {Path: "/music/two.mp3"}, {URL: "https://example.com/three.mp3"}}
	j, err := st.Create([]string{"recognize", "/music/one.mp3", "/music/two.mp3", "https://example.com/three.mp3"}, in, map[string]string{"endpoint": "standard"})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := st.Items(j.ID)
	items[0].State, items[0].Result, items[0].Requests = jobs.StateDone, json.RawMessage(`{"artist":"Artist","title":"One","isrc":"XX0000000001"}`), 1
	items[1].State, items[1].Result, items[1].Requests = jobs.StateDone, json.RawMessage(`null`), 1
	items[2].State, items[2].Err, items[2].ErrCode, items[2].APICode = jobs.StateFailed, "Recognition failed", "invalid_audio", 300
	for _, it := range items {
		if err := st.SaveItem(j.ID, it); err != nil {
			t.Fatal(err)
		}
	}
	return j.ID
}

func TestJobsListEmptyAndSeeded(t *testing.T) {
	testutil.Isolate(t)
	r := run(t, "jobs", "list", "--format", "table")
	if r.Code != 0 || !strings.Contains(r.Stdout, "No jobs yet") {
		t.Fatalf("%d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	r = run(t, "jobs", "list")
	if r.Code != 0 || strings.TrimSpace(r.Stdout) != `{"schema_version":1,"items":[]}` {
		t.Fatalf("empty JSON list: %q", r.Stdout)
	}

	id := seedJob(t)
	r = run(t, "jobs", "list", "--format", "table")
	if r.Code != 0 {
		t.Fatalf("%d %s", r.Code, r.Stderr)
	}
	for _, want := range []string{"ID", id, "partial", "2/3", "standard", "one.mp3 and 2 more"} {
		if !strings.Contains(r.Stdout, want) {
			t.Fatalf("missing %q in\n%s", want, r.Stdout)
		}
	}
	r = run(t, "jobs", "list")
	var doc struct {
		SchemaVersion int        `json:"schema_version"`
		Items         []jobs.Job `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil || doc.SchemaVersion != 1 || len(doc.Items) != 1 || doc.Items[0].Failed != 1 {
		t.Fatalf("%v %q", err, r.Stdout)
	}
}

func TestJobsShowFormats(t *testing.T) {
	testutil.Isolate(t)
	id := seedJob(t)

	r := run(t, "jobs", "show", id)
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil {
		t.Fatalf("%v %q", err, r.Stdout)
	}
	items := doc["items"].([]any)
	if doc["schema_version"] != float64(1) || doc["id"] != id || len(items) != 3 {
		t.Fatalf("doc %v", doc)
	}
	first := items[0].(map[string]any)
	if first["status"] != "matched" || first["result"].(map[string]any)["isrc"] != "XX0000000001" {
		t.Fatalf("item %v", first)
	}

	r = run(t, "jobs", "show", id, "--format", "jsonl", "--failed")
	ls := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(ls) != 1 || !strings.Contains(ls[0], `"type":"result"`) || !strings.Contains(ls[0], `"api_code":300`) {
		t.Fatalf("jsonl --failed: %q", r.Stdout)
	}

	r = run(t, "jobs", "show", id, "--format", "csv")
	rows := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(rows) != 4 || !strings.HasPrefix(rows[0], "job_id,index,input,status") || !strings.Contains(rows[1], "Artist,One") {
		t.Fatalf("csv: %q", r.Stdout)
	}

	r = run(t, "jobs", "show", id, "--format", "table")
	for _, want := range []string{"Job " + id, "1 recognized, 1 no match, 1 failed, 0 cached; 2 requests used.", "✓ /music/one.mp3  Artist — One", "· /music/two.mp3  no match",
		"✗ https://example.com/three.mp3  failed: Recognition failed", "audd jobs resume " + id + " --retry-failed"} {
		if !strings.Contains(r.Stdout, want) {
			t.Fatalf("missing %q in\n%s", want, r.Stdout)
		}
	}

	r = run(t, "jobs", "show", "nosuch")
	if r.Code != 2 || !strings.Contains(r.Stderr, `"code":"job_not_found"`) {
		t.Fatalf("%d %q", r.Code, r.Stderr)
	}
	// Without a terminal, browse prints the job.
	r = run(t, "jobs", "browse", id)
	if r.Code != 0 || !strings.Contains(r.Stdout, `"items"`) {
		t.Fatalf("browse: %d %q", r.Code, r.Stdout)
	}
}

func TestJobsBrowseOpensTheExplorerOnTheJob(t *testing.T) {
	testutil.Isolate(t)
	id := seedJob(t)
	var tab string
	old := app.RunExplorer
	app.RunExplorer = func(_ context.Context, a *app.App, t string) error {
		tab = t
		return nil
	}
	t.Cleanup(func() { app.RunExplorer = old })
	var out, errb bytes.Buffer
	code := cli.Run(context.Background(), []string{"jobs", "browse", id}, cli.IO{In: strings.NewReader(""), Out: &out, Err: &errb, StdoutTTY: true, StderrTTY: true})
	if code != 0 || tab != "jobs/"+id {
		t.Fatalf("exit %d tab %q %q", code, tab, errb.String())
	}
}

func TestJobsResumeFinishedJobSendsNothing(t *testing.T) {
	testutil.Isolate(t)
	id := seedJob(t)
	// The failure may have been billed, so a plain resume has nothing to do.
	r := run(t, "jobs", "resume", id)
	if r.Code != 7 {
		t.Fatalf("exit %d, stderr %q", r.Code, r.Stderr)
	}
	if !strings.Contains(r.Stderr, "Nothing left to do in job "+id) {
		t.Fatalf("stderr %q", r.Stderr)
	}
	if !strings.Contains(r.Stdout, `"type":"summary"`) {
		t.Fatalf("piped resume streams JSONL: %q", r.Stdout)
	}
	r = run(t, "jobs", "resume", id, "--retry-failed", "--dry-run")
	if r.Code != 0 || !strings.Contains(r.Stdout, `"requests":1`) || !strings.Contains(r.Stdout, `"job_id":"`+id+`"`) {
		t.Fatalf("dry run: %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
}

func TestJobsClean(t *testing.T) {
	testutil.Isolate(t)
	id := seedJob(t)
	r := run(t, "jobs", "clean")
	if r.Code != 0 || !strings.Contains(r.Stdout, `"deleted":0`) {
		t.Fatalf("fresh jobs are kept by default: %q", r.Stdout)
	}
	r = run(t, "jobs", "clean", "--older-than", "soon")
	if r.Code != 2 {
		t.Fatalf("bad duration: %d", r.Code)
	}
	r = run(t, "jobs", "clean", id, "--all")
	if r.Code != 2 {
		t.Fatalf("ids with --all: %d", r.Code)
	}
	r = run(t, "jobs", "clean", id, "--format", "table")
	if r.Code != 0 || r.Stdout != "Deleted 1 job.\n" {
		t.Fatalf("%d %q", r.Code, r.Stdout)
	}
	seedJob(t)
	seedJob(t)
	r = run(t, "jobs", "clean", "--all")
	if r.Code != 0 || !strings.Contains(r.Stdout, `"deleted":2`) {
		t.Fatalf("%q", r.Stdout)
	}
}

func TestListenWithoutToolsIsUsageError(t *testing.T) {
	testutil.Isolate(t)
	t.Setenv("PATH", t.TempDir())
	r := run(t, "listen")
	if r.Code != 2 {
		t.Fatalf("exit %d %q", r.Code, r.Stderr)
	}
	var e struct {
		Error struct{ Code, Message, Hint string }
	}
	if err := json.Unmarshal([]byte(r.Stderr), &e); err != nil || e.Error.Code != "missing_tool" || e.Error.Hint == "" || !strings.Contains(e.Error.Message, "sox") {
		t.Fatalf("%v %q", err, r.Stderr)
	}
	r = run(t, "listen", "--seconds", "0")
	if r.Code != 2 {
		t.Fatalf("--seconds 0: %d", r.Code)
	}
	// Checked before recording: longer recordings pass the 10 MB limit.
	r = run(t, "listen", "--seconds", "61")
	if r.Code != 2 || !strings.Contains(r.Stderr, `"invalid_argument"`) || !strings.Contains(r.Stderr, "at most 60") {
		t.Fatalf("--seconds 61: %d %q", r.Code, r.Stderr)
	}
}
