package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

type fakeStreamsAPI struct {
	mu      sync.Mutex
	added   []string
	removed []int
	setURL  []string
}

func fakeExplorerData(api *fakeStreamsAPI) ExplorerData {
	recent := []RecentItem{
		{Source: "/music/warriors.mp3", At: testNow.Add(-time.Hour), Result: json.RawMessage(`{"artist":"Imagine Dragons","title":"Warriors","album":"Warriors","label":"KIDinaKORNER","release_date":"2014-09-18","song_link":"https://lis.tn/Warriors","isrc":"USUM71414186","upc":"00602547034463"}`)},
		{Source: "https://audd.tech/example.mp3", At: testNow.Add(-2 * time.Hour), Result: json.RawMessage(`null`)},
		{Source: "/music/mix.mp3", At: testNow.Add(-3 * time.Hour), Result: json.RawMessage(`[{"offset":"00:00","songs":[{"artist":"Daft Punk","title":"One More Time","timecode":"00:05"}]},{"offset":"00:12","songs":[{"artist":"Daft Punk","title":"Aerodynamic","timecode":"00:01"}]}]`)},
	}
	jobs := []JobRow{
		{ID: "k3x9", Command: []string{"audd", "recognize", "./music", "--max-files", "10"}, Created: testNow.Add(-30 * time.Minute), Total: 3, Done: 2, Failed: 1, Status: "partial"},
	}
	items := []JobItem{
		{Index: 0, Input: "/music/a.mp3", State: "done", Result: json.RawMessage(`{"artist":"Massive Attack","title":"Teardrop","song_link":"https://lis.tn/Teardrop"}`)},
		{Index: 1, Input: "/music/mix.mp3", State: "done", Result: json.RawMessage(`[{"artist":"Daft Punk","title":"One More Time","start_seconds":0,"end_seconds":36},{"artist":"Daft Punk","title":"Aerodynamic","start_seconds":36,"end_seconds":72}]`)},
		{Index: 2, Input: "/music/c.mp3", State: "failed", Err: "the connection was reset after upload"},
	}
	return ExplorerData{
		Recent:   func(ctx context.Context, limit int) ([]RecentItem, error) { return recent, nil },
		Jobs:     func(ctx context.Context) ([]JobRow, error) { return jobs, nil },
		JobItems: func(ctx context.Context, id string) ([]JobItem, error) { return items, nil },
		Feed:     sampleFeed(),
		Usage: func(ctx context.Context, days int) (*account.Usage, error) {
			return &account.Usage{UsedThisCycle: 1234, Allowance: 5000, Remaining: 3766, Days: []account.DayUsage{{Date: "2026-10-06", Requests: 400}, {Date: "2026-10-07", Requests: 800}, {Date: "2026-10-08", Requests: 34}}}, nil
		},
		AddStream: func(ctx context.Context, url string, id int) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			api.added = append(api.added, url+"#"+itoa(id))
			return nil
		},
		RemoveStream: func(ctx context.Context, id int) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			api.removed = append(api.removed, id)
			return nil
		},
		SetStreamURL: func(ctx context.Context, id int, url string) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			api.setURL = append(api.setURL, itoa(id)+"="+url)
			return nil
		},
	}
}

func itoa(n int) string { return fmtInt(n) }

func explorerModel(t *testing.T, tab string, api *fakeStreamsAPI) *explorer {
	t.Helper()
	d := fakeExplorerData(api)
	old := NewExplorerData
	NewExplorerData = func(a *app.App) (ExplorerData, error) { return d, nil }
	t.Cleanup(func() { NewExplorerData = old })
	oldZone := localZone
	localZone = time.UTC
	t.Cleanup(func() { localZone = oldZone })
	var out, errb bytes.Buffer
	a := npApp(&out, &errb, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, StdinTTY: true, NoColor: true})
	tb, arg, err := parseTab(tab)
	if err != nil {
		t.Fatal(err)
	}
	return newExplorer(context.Background(), a, d, tb, arg)
}

func TestParseTab(t *testing.T) {
	cases := map[string]struct {
		tab tabID
		arg string
		ok  bool
	}{
		"":            {tabRecent, "", true},
		"Streams":     {tabStreams, "", true},
		"usage":       {tabUsage, "", true},
		"jobs/k3x9":   {tabJobs, "k3x9", true},
		"jobs:k3x9":   {tabJobs, "k3x9", true},
		"recent/x":    {0, "", false},
		"streams/3,5": {tabStreams, "3,5", true},
		"streams/x":   {0, "", false},
		"history":     {0, "", false},
	}
	for in, c := range cases {
		tb, arg, err := parseTab(in)
		if (err == nil) != c.ok || (c.ok && (tb != c.tab || arg != c.arg)) {
			t.Errorf("parseTab(%q) = %v %q %v", in, tb, arg, err)
		}
		if err != nil && output.ExitCode(err) != output.ExitUsage {
			t.Errorf("parseTab(%q) should be a usage error", in)
		}
	}
}

func TestExplorerRecentTab(t *testing.T) {
	m := explorerModel(t, "recent", &fakeStreamsAPI{})
	view := runModel(t, m, "Warriors")
	testutil.Golden(t, "explorer_recent", []byte(view))

	m = explorerModel(t, "recent", &fakeStreamsAPI{})
	view = runModelUntil(t, m, "Warriors", "Aerodynamic", key("down"), key("down"), key("enter"))
	if !strings.Contains(view, "00:00 +00:05") || !strings.Contains(view, "Daft Punk — Aerodynamic") {
		t.Fatalf("enterprise detail:\n%s", view)
	}
}

func TestExplorerFilterAndCopy(t *testing.T) {
	m := explorerModel(t, "recent", &fakeStreamsAPI{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(m.load(tabRecent, m.top())())
	m.key(key("/"))
	for _, r := range "kidina" {
		m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.key(key("enter"))
	if n := len(m.top().visible()); n != 1 {
		t.Fatalf("filter should match the label: %d rows", n)
	}
	m.key(key("i"))
	if !strings.HasPrefix(m.View(), "\x1b]52;c;") || !strings.Contains(m.View(), "Copied ISRC USUM71414186") {
		t.Fatalf("copy ISRC:\n%s", m.View())
	}
	m.key(key("esc"))
	if n := len(m.top().visible()); n != 3 {
		t.Fatalf("esc should clear the filter: %d rows", n)
	}
}

func TestExplorerJobsTab(t *testing.T) {
	m := explorerModel(t, "jobs", &fakeStreamsAPI{})
	view := runModelUntil(t, m, "k3x9", "Teardrop", key("enter"))
	testutil.Golden(t, "explorer_job_items", []byte(view))
	if !strings.Contains(view, "2 matches") || !strings.Contains(view, "Error: the connection was reset") {
		t.Fatalf("items:\n%s", view)
	}

	m = explorerModel(t, "jobs/k3x9", &fakeStreamsAPI{})
	view = runModelUntil(t, m, "Teardrop", "0:36", key("down"), key("enter"))
	if !strings.Contains(view, "0:00–0:36") || !strings.Contains(view, "0:36–1:12") {
		t.Fatalf("tracklist detail:\n%s", view)
	}
}

func TestExplorerResumeJob(t *testing.T) {
	var got app.BatchOptions
	old := app.RunBatch
	app.RunBatch = func(ctx context.Context, a *app.App, opts app.BatchOptions) (app.BatchSummary, error) {
		got = opts
		return app.BatchSummary{}, nil
	}
	t.Cleanup(func() { app.RunBatch = old })
	m := explorerModel(t, "jobs", &fakeStreamsAPI{})
	m.Update(m.load(tabJobs, m.top())())
	_, cmd := m.key(key("R"))
	if cmd == nil || m.after == nil {
		t.Fatal("R should quit and resume")
	}
	if err := m.after(); err != nil {
		t.Fatal(err)
	}
	if got.ResumeID != "k3x9" || !got.RetryFailed {
		t.Fatalf("%+v", got)
	}
}

func TestExplorerStreamsTab(t *testing.T) {
	api := &fakeStreamsAPI{}
	m := explorerModel(t, "streams", api)
	view := runModel(t, m, "Feeling Good")
	testutil.Golden(t, "explorer_streams", []byte(view))
	if !strings.Contains(view, "Can't connect (650)") {
		t.Fatalf("health column:\n%s", view)
	}

	m = explorerModel(t, "streams", api)
	view = runModelUntil(t, m, "Feeling Good", "Teardrop", key("enter"))
	if !strings.Contains(view, "Stream 1 · recent plays") || !strings.Contains(view, "Teardrop") {
		t.Fatalf("plays:\n%s", view)
	}
}

func TestExplorerStreamsTabForSomeStreams(t *testing.T) {
	m := explorerModel(t, "streams/2", &fakeStreamsAPI{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(m.load(tabStreams, m.top())())
	l := m.top()
	if l.title != "Stream 2" || len(l.rows) != 1 || l.rows[0].key != "2" {
		var keys []string
		for _, r := range l.rows {
			keys = append(keys, r.key)
		}
		t.Fatalf("title %q rows %v", l.title, keys)
	}
}

func TestExplorerStreamManagement(t *testing.T) {
	api := &fakeStreamsAPI{}
	m := explorerModel(t, "streams", api)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(m.load(tabStreams, m.top())())
	typeText := func(s string) {
		for _, r := range s {
			m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	run := func(cmd tea.Cmd) {
		if cmd != nil {
			if msg := cmd(); msg != nil {
				m.Update(msg)
			}
		}
	}
	m.key(key("a"))
	typeText("https://radio.example/new.mp3")
	m.key(key("enter"))
	typeText("7")
	_, cmd := m.key(key("enter"))
	run(cmd)

	m.key(key("down")) // stream 2
	m.key(key("d"))
	if !strings.Contains(m.View(), "Remove stream 2?") {
		t.Fatalf("confirmation:\n%s", m.View())
	}
	_, cmd = m.key(key("y"))
	run(cmd)

	m.key(key("u"))
	typeText("https://radio.example/b2.mp3")
	_, cmd = m.key(key("enter"))
	run(cmd)

	api.mu.Lock()
	defer api.mu.Unlock()
	if strings.Join(api.added, ",") != "https://radio.example/new.mp3#7" || len(api.removed) != 1 || api.removed[0] != 2 ||
		strings.Join(api.setURL, ",") != "2=https://radio.example/b2.mp3" {
		t.Fatalf("added %v removed %v set %v", api.added, api.removed, api.setURL)
	}
}

func TestExplorerUsageTab(t *testing.T) {
	m := explorerModel(t, "usage", &fakeStreamsAPI{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	cmd := m.load(tabUsage, nil)
	m.Update(cmd())
	testutil.Golden(t, "explorer_usage", []byte(m.View()))
}

func TestExplorerExport(t *testing.T) {
	dir := t.TempDir()
	old := explorerExportDir
	explorerExportDir = dir
	t.Cleanup(func() { explorerExportDir = old })
	m := explorerModel(t, "recent", &fakeStreamsAPI{})
	m.Update(m.load(tabRecent, m.top())())
	m.key(key("e"))
	m.key(key("c"))
	m.key(key("e"))
	m.key(key("j"))
	csvB, err := os.ReadFile(filepath.Join(dir, "audd-recent-20261008-120000.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(csvB), "When,Song,Album,Source\nOct 08 11:00,Imagine Dragons — Warriors,Warriors,warriors.mp3\n") {
		t.Fatalf("csv:\n%s", csvB)
	}
	jsonB, err := os.ReadFile(filepath.Join(dir, "audd-recent-20261008-120000.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion int          `json:"schema_version"`
		Items         []RecentItem `json:"items"`
	}
	if err := json.Unmarshal(jsonB, &doc); err != nil || doc.SchemaVersion != 1 || len(doc.Items) != 3 {
		t.Fatalf("json: %v %s", err, jsonB)
	}
}

func TestExplorerWithoutTerminalPrintsJSON(t *testing.T) {
	d := fakeExplorerData(&fakeStreamsAPI{})
	old := NewExplorerData
	NewExplorerData = func(a *app.App) (ExplorerData, error) { return d, nil }
	t.Cleanup(func() { NewExplorerData = old })
	for _, tab := range []string{"recent", "jobs", "jobs/k3x9", "streams", "usage"} {
		var out, errb bytes.Buffer
		a := npApp(&out, &errb, output.PrinterOptions{})
		if err := RunExplorer(context.Background(), a, tab); err != nil {
			t.Fatalf("%s: %v", tab, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["schema_version"] != float64(1) {
			t.Fatalf("%s: %v %s", tab, err, out.String())
		}
	}
	if app.RunExplorer == nil {
		t.Fatal("hook not assigned")
	}
}

func TestExplorerWithoutTerminalCSVIsFlat(t *testing.T) {
	d := fakeExplorerData(&fakeStreamsAPI{})
	old := NewExplorerData
	NewExplorerData = func(a *app.App) (ExplorerData, error) { return d, nil }
	t.Cleanup(func() { NewExplorerData = old })
	heads := map[string]string{
		"recent":    "source,at,status,position,artist,title,",
		"jobs":      "id,status,total,done,failed,created,command",
		"jobs/k3x9": "job_id,index,input,state,position,artist,",
		"streams":   "radio_id,url,stream_running,health,state,playing,timestamp,",
		"usage":     "date,requests",
	}
	for tab, head := range heads {
		var out, errb bytes.Buffer
		a := npApp(&out, &errb, output.PrinterOptions{Format: output.FormatCSV})
		if err := RunExplorer(context.Background(), a, tab); err != nil {
			t.Fatalf("%s: %v", tab, err)
		}
		got := out.String()
		if !strings.HasPrefix(got, head) || strings.Contains(got, "{") || strings.Contains(got, "[") {
			t.Fatalf("%s: not flat CSV:\n%s", tab, got)
		}
		if strings.Count(got, "\n") < 2 {
			t.Fatalf("%s: no rows:\n%s", tab, got)
		}
	}
}

// A stream action reply that arrives after the user left the Streams tab
// reloads the stream list, not the open tab.
func TestExplorerStreamActionAfterTabSwitch(t *testing.T) {
	for _, other := range []tabID{tabUsage, tabRecent, tabJobs} {
		m := explorerModel(t, "streams", &fakeStreamsAPI{})
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m.Update(m.load(tabStreams, m.top())())
		reply := m.streamAction(func(ctx context.Context) error { return nil }, "Stream added")().(actionMsg)
		reply.flash = "" // leave only the reload command
		m.tab = other
		_, cmd := m.Update(reply)
		if cmd == nil {
			t.Fatalf("tab %d: no reload", other)
		}
		got, ok := cmd().(rowsMsg)
		if !ok || got.tab != tabStreams || got.kind != "streams" {
			t.Fatalf("tab %d: want a streams reload, got %#v", other, got)
		}
	}
}

func TestExplorerUsageCSVExport(t *testing.T) {
	dir := t.TempDir()
	old := explorerExportDir
	explorerExportDir = dir
	t.Cleanup(func() { explorerExportDir = old })
	m := explorerModel(t, "usage", &fakeStreamsAPI{})
	m.Update(m.load(tabUsage, nil)())
	m.key(key("e"))
	m.key(key("c"))
	b, err := os.ReadFile(filepath.Join(dir, "audd-usage-20261008-120000.csv"))
	if err != nil {
		t.Fatal(err)
	}
	want := "date,requests\n2026-10-06,400\n2026-10-07,800\n2026-10-08,34\n"
	if string(b) != want {
		t.Fatalf("csv:\n%s", b)
	}
}

func TestUsageViewNegativeCounts(t *testing.T) {
	st := output.NewPrinter(new(bytes.Buffer), new(bytes.Buffer), output.PrinterOptions{NoColor: true}).Styles()
	got := dayChart([]account.DayUsage{{Date: "2026-10-07", Requests: -5}, {Date: "2026-10-08", Requests: 10}}, 80, 10, st)
	if !strings.Contains(got, "2026-10-07") || !strings.Contains(got, "2026-10-08") {
		t.Fatalf("chart:\n%s", got)
	}
	m := explorerModel(t, "usage", &fakeStreamsAPI{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.usage = &account.Usage{UsedThisCycle: -3, Allowance: 100, Remaining: 103}
	_ = m.usageView()
}

// A job resumed from the explorer runs under the command's context, so
// Ctrl-C (which cancels it) stops the job.
func TestExplorerResumeUsesTheCommandContext(t *testing.T) {
	m := explorerModel(t, "jobs/j1", &fakeStreamsAPI{})
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	var got context.Context
	old := app.RunBatch
	app.RunBatch = func(ctx context.Context, a *app.App, opts app.BatchOptions) (app.BatchSummary, error) {
		got = ctx
		return app.BatchSummary{}, nil
	}
	t.Cleanup(func() { app.RunBatch = old })
	m.resumeJob(&level{kind: "items", arg: "j1"}, false)
	if m.after == nil {
		t.Fatal("no resume queued")
	}
	if err := m.after(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if got == nil || got.Err() == nil {
		t.Fatal("the resumed job does not see Ctrl-C")
	}
}
