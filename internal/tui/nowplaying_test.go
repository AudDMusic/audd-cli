package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// fakeFeed serves fixed stations and plays; plays can change between calls.
type fakeFeed struct {
	mu        sync.Mutex
	stations  []Station
	plays     map[int][]Play
	calls     int
	onPlays   func(calls int, f *fakeFeed) // runs before each Plays call
	stationsE error
}

func (f *fakeFeed) Stations(ctx context.Context) ([]Station, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Station(nil), f.stations...), f.stationsE
}

func (f *fakeFeed) Plays(ctx context.Context, id, limit int) ([]Play, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.onPlays != nil {
		f.onPlays(f.calls, f)
	}
	p := f.plays[id]
	if len(p) > limit {
		p = p[:limit]
	}
	return append([]Play(nil), p...), nil
}

// songRaw is a song object as stream results carry it by default: no
// provider metadata (apple_music, spotify, deezer).
func songRaw(artist, title string) map[string]any {
	return map[string]any{"artist": artist, "title": title, "album": title + " (album)", "label": "Label", "release_date": "2019-05-01",
		"timecode": "00:56", "song_link": "https://lis.tn/" + strings.ReplaceAll(title, " ", "")}
}

func playFrom(id int, started time.Duration, playLength int, raw map[string]any) Play {
	b, _ := json.Marshal(raw)
	return PlayFromRaw(id, testNow.Add(-started), playLength, b)
}

// play is a result as AudD sends it by default: when the song ends, with
// timestamp (the song start) and play_length (seconds played), and no
// provider metadata. started is how long ago the song started.
func play(id int, started time.Duration, artist, title string, played int) Play {
	return playFrom(id, started, played, songRaw(artist, title))
}

// startPlay is a result of a stream added with --start: sent when the song
// starts, without play_length.
func startPlay(id int, started time.Duration, artist, title string) Play {
	return playFrom(id, started, 0, songRaw(artist, title))
}

// withMetadata adds the apple_music block AudD includes when provider
// metadata is turned on for stream results, which gives the track length.
func withMetadata(raw map[string]any, length time.Duration) map[string]any {
	raw["apple_music"] = map[string]any{"durationInMillis": length.Milliseconds(), "url": "https://music.apple.com/us/album/x"}
	return raw
}

func stations3() []Station {
	return []Station{
		{RadioID: 2, URL: "https://radio.example/b.mp3", Running: true},
		{RadioID: 1, URL: "https://radio.example/a.mp3", Running: true},
		{RadioID: 3, URL: "https://radio.example/c.mp3", Running: false, Health: &Health{Code: 650, Message: "can't connect", At: testNow.Add(-time.Minute)}},
	}
}

// sampleFeed is the default: results sent when each song ends.
func sampleFeed() *fakeFeed {
	return &fakeFeed{
		stations: stations3(),
		plays: map[int][]Play{
			1: {
				play(1, 4*time.Minute, "Imagine Dragons", "Warriors", 170), // ended 1:10 ago
				play(1, 9*time.Minute, "Daft Punk", "One More Time", 300),
				play(1, 15*time.Minute, "Massive Attack", "Teardrop", 330),
			},
			2: {play(2, 25*time.Minute, "Nina Simone", "Feeling Good", 230)}, // ended 21 min ago
			3: {play(3, 3*time.Hour+5*time.Minute, "Portishead", "Roads", 300)},
		},
	}
}

// startFeed is a stream added with --start: results arrive when songs
// start, without play_length.
func startFeed() *fakeFeed {
	return &fakeFeed{
		stations: stations3(),
		plays: map[int][]Play{
			1: {
				startPlay(1, 83*time.Second, "Imagine Dragons", "Warriors"),
				startPlay(1, 5*time.Minute, "Daft Punk", "One More Time"),
				startPlay(1, 11*time.Minute, "Massive Attack", "Teardrop"),
			},
			2: {startPlay(2, 25*time.Minute, "Nina Simone", "Feeling Good")},
			3: {startPlay(3, 3*time.Hour, "Portishead", "Roads")},
		},
	}
}

// metadataFeed has provider metadata turned on: stream 1 was added with
// --start, so its song plays with a known length; stream 2 sends results
// when songs end.
func metadataFeed() *fakeFeed {
	return &fakeFeed{
		stations: stations3(),
		plays: map[int][]Play{
			1: {
				playFrom(1, 83*time.Second, 0, withMetadata(songRaw("Imagine Dragons", "Warriors"), 170*time.Second)),
				playFrom(1, 5*time.Minute, 0, withMetadata(songRaw("Daft Punk", "One More Time"), 320*time.Second)),
			},
			2: {playFrom(2, 6*time.Minute, 200, withMetadata(songRaw("Nina Simone", "Feeling Good"), 230*time.Second))},
			3: {playFrom(3, 3*time.Hour, 300, withMetadata(songRaw("Portishead", "Roads"), 300*time.Second))},
		},
	}
}

func useFeed(t *testing.T, f Feed) {
	t.Helper()
	old := NewFeed
	NewFeed = func(a *app.App) (Feed, error) { return f, nil }
	t.Cleanup(func() { NewFeed = old })
	oldZone := localZone
	localZone = time.UTC
	t.Cleanup(func() { localZone = oldZone })
}

func npApp(out, errb *bytes.Buffer, opts output.PrinterOptions) *app.App {
	a := app.New()
	a.Out = output.NewPrinter(out, errb, opts)
	a.Now = func() time.Time { return testNow }
	a.In = strings.NewReader("")
	return a
}

// feedModes are the three kinds of stream results: sent when the song ends
// (the default), sent when it starts (--start), and with provider metadata.
var feedModes = []struct {
	name string
	feed func() *fakeFeed
}{
	{"", sampleFeed},
	{"_start", startFeed},
	{"_metadata", metadataFeed},
}

func TestNowPlayingOnceJSON(t *testing.T) {
	for _, mode := range feedModes {
		useFeed(t, mode.feed())
		var out, errb bytes.Buffer
		a := npApp(&out, &errb, output.PrinterOptions{})
		if err := RunNowPlaying(a, nil, NowPlayingOptions{Once: true}); err != nil {
			t.Fatal(err)
		}
		testutil.Golden(t, "nowplaying_once_json"+mode.name, out.Bytes())
		var doc map[string]any
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["schema_version"] != float64(1) {
			t.Fatalf("not a schema_version 1 document: %v", err)
		}
	}
}

// The timing fields of a result match its state.
func TestNowPlayingTimingFields(t *testing.T) {
	cases := []struct {
		p            Play
		state        PlayState
		want, absent []string
	}{
		{sampleFeed().plays[1][0], StateJustPlayed, []string{"played_seconds", "ended_at", "ago_seconds"}, []string{"elapsed_seconds", "length_seconds"}},
		{sampleFeed().plays[2][0], StateLastRecognized, []string{"played_seconds", "ended_at", "ago_seconds"}, []string{"elapsed_seconds"}},
		{startFeed().plays[1][0], StatePlaying, []string{"elapsed_seconds"}, []string{"played_seconds", "ended_at", "ago_seconds", "length_seconds"}},
		{startFeed().plays[2][0], StateLastRecognized, []string{"ago_seconds"}, []string{"elapsed_seconds", "played_seconds", "ended_at"}},
		{metadataFeed().plays[1][0], StatePlaying, []string{"elapsed_seconds", "length_seconds"}, []string{"played_seconds", "ago_seconds"}},
		{metadataFeed().plays[2][0], StateJustPlayed, []string{"played_seconds", "length_seconds", "ago_seconds"}, []string{"elapsed_seconds"}},
	}
	for i, c := range cases {
		m := c.p.Timing(testNow)
		if m["state"] != string(c.state) || m["playing"] != (c.state == StatePlaying) {
			t.Errorf("%d: state %v playing %v, want %s", i, m["state"], m["playing"], c.state)
		}
		for _, k := range c.want {
			if _, ok := m[k]; !ok {
				t.Errorf("%d (%s): missing %s in %v", i, c.state, k, m)
			}
		}
		for _, k := range c.absent {
			if _, ok := m[k]; ok {
				t.Errorf("%d (%s): unexpected %s in %v", i, c.state, k, m)
			}
		}
	}
	if m := sampleFeed().plays[1][0].Timing(testNow); m["ago_seconds"] != 70 || m["ended_at"] != "2026-10-08T11:58:50Z" || m["played_seconds"] != 170 {
		t.Fatalf("end-of-song timing: %v", m)
	}
}

func TestNowPlayingOnceCSVIsFlat(t *testing.T) {
	useFeed(t, sampleFeed())
	var out, errb bytes.Buffer
	a := npApp(&out, &errb, output.PrinterOptions{Format: output.FormatCSV})
	if err := RunNowPlaying(a, []int{1, 2}, NowPlayingOptions{Once: true}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "radio_id,url,stream_running,health,state,playing,timestamp,elapsed_seconds,played_seconds,ended_at,ago_seconds,length_seconds,artist,title,") ||
		!strings.Contains(lines[1], "Imagine Dragons,Warriors") || strings.Contains(out.String(), "{") {
		t.Fatalf("csv:\n%s", out.String())
	}
}

func TestNowPlayingOnceHuman(t *testing.T) {
	for _, mode := range feedModes {
		useFeed(t, mode.feed())
		withEnv(t, map[string]string{"AUDD_ART": "none"})
		var out, errb bytes.Buffer
		a := npApp(&out, &errb, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, NoColor: true})
		if err := RunNowPlaying(a, []int{1, 2, 3}, NowPlayingOptions{Once: true}); err != nil {
			t.Fatal(err)
		}
		testutil.Golden(t, "nowplaying_once_human"+mode.name, out.Bytes())
	}
}

func TestNowPlayingOnceFormat(t *testing.T) {
	const format = "{{.RadioID}}: {{.Artist}} – {{.Title}} [{{.State}}{{if .Playing}} {{.Elapsed}}{{else}} {{.Ago}}{{end}}{{if .Played}} played {{.Played}}{{end}}{{if .Length}} of {{.Length}}{{end}}]"
	cases := []struct {
		feed *fakeFeed
		want string
	}{
		{sampleFeed(), "1: Imagine Dragons – Warriors [just_played 1m10s played 2m50s]\n2: Nina Simone – Feeling Good [last_recognized 21m10s played 3m50s]\n"},
		{startFeed(), "1: Imagine Dragons – Warriors [playing 1m23s]\n2: Nina Simone – Feeling Good [last_recognized 25m0s]\n"},
		{metadataFeed(), "1: Imagine Dragons – Warriors [playing 1m23s of 2m50s]\n2: Nina Simone – Feeling Good [just_played 2m40s played 3m20s of 3m50s]\n"},
	}
	for _, c := range cases {
		useFeed(t, c.feed)
		var out, errb bytes.Buffer
		a := npApp(&out, &errb, output.PrinterOptions{})
		if err := RunNowPlaying(a, []int{1, 2}, NowPlayingOptions{Once: true, Format: format}); err != nil {
			t.Fatal(err)
		}
		if out.String() != c.want {
			t.Errorf("got %q want %q", out.String(), c.want)
		}
	}
	var out, errb bytes.Buffer
	err := RunNowPlaying(npApp(&out, &errb, output.PrinterOptions{}), nil, NowPlayingOptions{Once: true, Format: "{{.Artist"})
	if output.ExitCode(err) != output.ExitUsage {
		t.Fatalf("bad template should be a usage error: %v", err)
	}
}

func TestNowPlayingNoStreams(t *testing.T) {
	useFeed(t, &fakeFeed{})
	var out, errb bytes.Buffer
	err := RunNowPlaying(npApp(&out, &errb, output.PrinterOptions{}), nil, NowPlayingOptions{Once: true})
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "no_streams" || oe.Exit != output.ExitUsage {
		t.Fatalf("got %v", err)
	}
}

func TestNowPlayingStreamsChangesAsJSONL(t *testing.T) {
	f := sampleFeed()
	f.onPlays = func(calls int, f *fakeFeed) {
		if calls == 3 { // second poll of station 1
			f.plays[1] = append([]Play{play(1, 70*time.Second, "Björk", "Hyperballad", 68)}, f.plays[1]...)
		}
	}
	useFeed(t, f)
	var notes []string
	var mu sync.Mutex
	oldNotify := notify
	notify = func(title, body string) error {
		mu.Lock()
		notes = append(notes, title+"|"+body)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { notify = oldNotify })

	recorderCalls := 0
	oldRec := app.EnsureRecorder
	app.EnsureRecorder = func(a *app.App) (bool, error) { recorderCalls++; return true, nil }
	t.Cleanup(func() { app.EnsureRecorder = oldRec })

	var out, errb bytes.Buffer
	a := npApp(&out, &errb, output.PrinterOptions{})
	err := RunNowPlaying(a, []int{1, 3}, NowPlayingOptions{Notify: true, Interval: 5 * time.Millisecond, Timeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var kinds, titles []string
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q", l)
		}
		if m["schema_version"] != float64(1) {
			t.Fatalf("missing schema_version: %q", l)
		}
		kinds = append(kinds, m["type"].(string))
		if r, ok := m["result"].(map[string]any); ok {
			titles = append(titles, r["title"].(string))
		}
	}
	if strings.Join(kinds, ",") != "result,event,result,result" {
		t.Fatalf("kinds %v\n%s", kinds, out.String())
	}
	if strings.Join(titles, ",") != "Warriors,Roads,Hyperballad" {
		t.Fatalf("titles %v", titles)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 || notes[0] != "Björk — Hyperballad|Stream 1" {
		t.Fatalf("notifications %v", notes)
	}
	if recorderCalls != 1 || !strings.Contains(errb.String(), "Recording stream results in the background") {
		t.Fatalf("recorder not started (%d): %q", recorderCalls, errb.String())
	}
}

// --- full-screen snapshots ---

func npTestModel(t *testing.T, f *fakeFeed, ids []int, noArt bool) *npModel {
	t.Helper()
	useFeed(t, f)
	if noArt {
		withEnv(t, map[string]string{"AUDD_ART": "none"})
	} else {
		withEnv(t, map[string]string{"COLORTERM": "truecolor"})
		withArt(t, testImage())
	}
	var out, errb bytes.Buffer
	a := npApp(&out, &errb, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, StdinTTY: true})
	stations, err := selectStations(context.Background(), f, ids)
	if err != nil {
		t.Fatal(err)
	}
	return newNowPlayingModel(context.Background(), a, f, stations, ids, NowPlayingOptions{Interval: time.Hour})
}

func runModel(t *testing.T, m tea.Model, wait string, keys ...tea.KeyMsg) string {
	t.Helper()
	return runModelUntil(t, m, wait, "", keys...)
}

// runModelUntil waits for wait, sends keys, then waits for after (when set)
// before quitting, and returns the final view.
func runModelUntil(t *testing.T, m tea.Model, wait, after string, keys ...tea.KeyMsg) string {
	t.Helper()
	return runModelSized(t, m, 100, 30, wait, after, keys...)
}

// runModelSized is runModelUntil on a w×h terminal.
func runModelSized(t *testing.T, m tea.Model, w, h int, wait, after string, keys ...tea.KeyMsg) string {
	t.Helper()
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(w, h))
	out := tm.Output()
	waitFor := func(s string) {
		teatest.WaitFor(t, out, func(b []byte) bool { return bytes.Contains(b, []byte(s)) },
			teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	}
	waitFor(wait)
	for _, k := range keys {
		tm.Send(k)
	}
	if after != "" {
		waitFor(after)
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	return fm.View()
}

func key(s string) tea.KeyMsg {
	switch s {
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestNowPlayingScreenSingle(t *testing.T) {
	m := npTestModel(t, sampleFeed(), []int{1}, true)
	view := runModel(t, m, "Warriors")
	testutil.Golden(t, "nowplaying_single", []byte(view))
	if !strings.Contains(view, "Earlier on this station") || !strings.Contains(view, "JUST PLAYED · ended 1 min ago") ||
		!strings.Contains(view, "played 2:50") || strings.Contains(view, "━") || strings.Contains(view, "NOW PLAYING") {
		t.Fatalf("missing parts:\n%s", view)
	}
}

// Each kind of stream result has its own view, at 100×34: sent when the
// song ends (just played, how long it played), sent when it starts (live
// elapsed time, no bar), and with provider metadata (progress bar).
func TestNowPlayingScreenModes(t *testing.T) {
	cases := []struct {
		name      string
		feed      func() *fakeFeed
		want, not []string
	}{
		{"end", sampleFeed, []string{"JUST PLAYED · ended 1 min ago", "played 2:50", "Earlier on this station"}, []string{"━", "NOW PLAYING", " / "}},
		{"start", startFeed, []string{"● NOW PLAYING · 1:23", "Earlier on this station"}, []string{"━", "played", " / "}},
		{"metadata", metadataFeed, []string{"● NOW PLAYING", "━", "1:23 / 2:50"}, []string{"played"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := npTestModel(t, c.feed(), []int{1}, true)
			view := runModelSized(t, m, 100, 34, "Warriors", "")
			testutil.Golden(t, "nowplaying_mode_"+c.name, []byte(view))
			for _, w := range c.want {
				if !strings.Contains(view, w) {
					t.Errorf("missing %q:\n%s", w, view)
				}
			}
			for _, w := range c.not {
				if strings.Contains(view, w) {
					t.Errorf("unexpected %q:\n%s", w, view)
				}
			}
		})
	}
	// A --start song with no new result for 10 minutes is shown as the
	// last recognized one; an end-of-song result older than a few minutes
	// as the last played one.
	m := npTestModel(t, startFeed(), []int{2}, true)
	if view := runModel(t, m, "Feeling Good"); !strings.Contains(view, "LAST RECOGNIZED · 25 min ago") || strings.Contains(view, "played") {
		t.Fatalf("old --start result:\n%s", view)
	}
	m = npTestModel(t, metadataFeed(), []int{2}, true)
	if view := runModel(t, m, "Feeling Good"); !strings.Contains(view, "JUST PLAYED · ended 2 min ago") || !strings.Contains(view, "played 3:20 of 3:50") || strings.Contains(view, "━") {
		t.Fatalf("end-of-song result with a track length:\n%s", view)
	}
}

func TestNowPlayingScreenSingleWithArt(t *testing.T) {
	m := npTestModel(t, sampleFeed(), []int{1}, false)
	view := runModel(t, m, "Warriors")
	if !strings.Contains(view, "▀") {
		t.Fatalf("expected half-block cover art:\n%s", view)
	}
	testutil.Golden(t, "nowplaying_single_art", []byte(view))
}

func TestNowPlayingScreenGridAndZoom(t *testing.T) {
	m := npTestModel(t, sampleFeed(), nil, true)
	view := runModel(t, m, "Feeling Good")
	testutil.Golden(t, "nowplaying_grid", []byte(view))
	if !strings.Contains(view, "Can't connect to the stream (650)") {
		t.Fatalf("health should replace the song:\n%s", view)
	}

	m = npTestModel(t, sampleFeed(), nil, true)
	view = runModel(t, m, "Feeling Good", key("right"), key("enter"))
	if !strings.Contains(view, "stream 2 of 3") || !strings.Contains(view, "Stream 2  https://radio.example/b.mp3") || !strings.Contains(view, "LAST PLAYED · ended 21 min ago") || !strings.Contains(view, "played 3:50") {
		t.Fatalf("zoom on the second stream:\n%s", view)
	}
}

func TestNowPlayingScreenHealthDown(t *testing.T) {
	m := npTestModel(t, sampleFeed(), []int{3}, true)
	view := runModel(t, m, "Roads")
	testutil.Golden(t, "nowplaying_down", []byte(view))
	if !strings.Contains(view, "Last heard: Portishead — Roads · 3 h ago") {
		t.Fatalf("down view:\n%s", view)
	}
}

func TestNowPlayingScreenHistoryAndKeys(t *testing.T) {
	var opened []string
	oldOpen := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = oldOpen })

	m := npTestModel(t, sampleFeed(), []int{1}, true)
	view := runModel(t, m, "Warriors", key("h"), key("o"))
	if !strings.Contains(view, "recent plays") || !strings.Contains(view, "Teardrop") || !strings.Contains(view, "Opened https://lis.tn/Warriors") {
		t.Fatalf("history view:\n%s", view)
	}
	if len(opened) != 1 || opened[0] != "https://lis.tn/Warriors" {
		t.Fatalf("opened %v", opened)
	}

	m = npTestModel(t, sampleFeed(), []int{1}, true)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.applyPoll(npPollMsg{data: loadAll(context.Background(), m.feed, []Station{m.stations[0].Station}, historyLimit)})
	m.key(key("c"))
	if v := m.View(); !strings.HasPrefix(v, "\x1b]52;c;aHR0cHM6Ly9saXMudG4vV2FycmlvcnM=\a") {
		t.Fatalf("copy should emit OSC 52: %q", v[:40])
	}
	m.key(key("?"))
	if !strings.Contains(m.View(), "Keys") {
		t.Fatal("help not shown")
	}
}

func TestNowPlayingOnceStartsRecorder(t *testing.T) {
	for _, format := range []string{"", "{{.Title}}"} {
		useFeed(t, sampleFeed())
		calls := 0
		oldRec := app.EnsureRecorder
		app.EnsureRecorder = func(a *app.App) (bool, error) { calls++; return true, nil }
		var out, errb bytes.Buffer
		a := npApp(&out, &errb, output.PrinterOptions{})
		if err := RunNowPlaying(a, nil, NowPlayingOptions{Once: true, Format: format}); err != nil {
			t.Fatal(err)
		}
		app.EnsureRecorder = oldRec
		if calls != 1 {
			t.Fatalf("format %q: recorder started %d times, want 1", format, calls)
		}
		if !strings.Contains(errb.String(), streams.RecorderNote) || strings.Contains(out.String(), streams.RecorderNote) {
			t.Fatalf("format %q: the recorder note belongs on stderr only:\nstdout %q\nstderr %q", format, out.String(), errb.String())
		}
	}
}

func TestStationDocReportsReadError(t *testing.T) {
	d := stationData{Station: Station{RadioID: 4, Running: true}, Err: errors.New("disk is full")}
	m := stationDoc(d, testNow)
	if e, _ := m["error"].(map[string]any); e == nil || e["message"] != "disk is full" || e["code"] != "unexpected" {
		t.Fatalf("want the read error in the document: %v", m)
	}
	ok := stationDoc(stationData{Station: Station{RadioID: 4}}, testNow)
	if _, has := ok["error"]; has {
		t.Fatalf("no error field when the read worked: %v", ok)
	}
}

// Pressing r replaces the scheduled poll instead of adding a second poll
// loop, and a reply from the replaced poll is dropped.
func TestNowPlayingRefreshKeepsOnePollLoop(t *testing.T) {
	f := sampleFeed()
	m := npTestModel(t, f, []int{1}, true)
	clock := testNow
	m.now = func() time.Time { return clock }

	scheduled := m.poll(false)() // a scheduled poll, still in flight when r is pressed
	clock = clock.Add(2 * time.Minute)
	_, refresh := m.key(key("r"))
	if refresh == nil {
		t.Fatal("r should poll")
	}
	if !m.lastList.Equal(clock) {
		t.Fatalf("r refreshes the station list, so lastList should be now: %v", m.lastList)
	}
	// The older reply arrives after r: no data applied, no new tick.
	if _, cmd := m.Update(scheduled); cmd != nil {
		t.Fatal("a replaced poll's reply must not schedule another poll")
	}
	if m.loaded {
		t.Fatal("a replaced poll's reply must not be applied")
	}
	// The r reply is applied and schedules exactly the next poll.
	reply := refresh()
	if _, cmd := m.Update(reply); cmd == nil || !m.loaded {
		t.Fatal("the current poll's reply should be applied and schedule the next poll")
	}
	// A tick armed by a replaced poll does nothing.
	if _, cmd := m.Update(npPollNowMsg{gen: reply.(npPollMsg).gen - 1}); cmd != nil {
		t.Fatal("a stale tick must not start a poll")
	}
	if _, cmd := m.Update(npPollNowMsg{gen: reply.(npPollMsg).gen}); cmd == nil {
		t.Fatal("the current tick should poll")
	}
}

// With no IDs given, the station refresh picks up streams added or removed
// while the screen is open.
func TestNowPlayingRefreshFollowsAllStreams(t *testing.T) {
	f := sampleFeed()
	m := npTestModel(t, f, nil, true)
	m.sel = 2
	f.mu.Lock()
	f.stations = []Station{{RadioID: 1, URL: "https://radio.example/a.mp3", Running: true}, {RadioID: 4, URL: "https://radio.example/d.mp3", Running: true}}
	f.mu.Unlock()
	m.Update(m.poll(true)())
	got := ids(m.stationList())
	if fmt.Sprint(got) != "[1 4]" {
		t.Fatalf("stations after refresh: %v", got)
	}
	if m.sel != 1 {
		t.Fatalf("selection should be clamped: %d", m.sel)
	}
	if len(m.stations[0].Plays) == 0 {
		t.Fatal("plays for a kept station should be loaded")
	}
}

// With explicit IDs, the refresh keeps exactly those stations.
func TestNowPlayingRefreshKeepsExplicitIDs(t *testing.T) {
	f := sampleFeed()
	m := npTestModel(t, f, []int{2, 1}, true)
	f.mu.Lock()
	f.stations = append(f.stations, Station{RadioID: 4, URL: "https://radio.example/d.mp3"})
	f.mu.Unlock()
	m.Update(m.poll(true)())
	if got := ids(m.stationList()); fmt.Sprint(got) != "[2 1]" {
		t.Fatalf("stations after refresh: %v", got)
	}
}

func ids(stations []Station) []int {
	out := make([]int, len(stations))
	for i, s := range stations {
		out[i] = s.RadioID
	}
	return out
}

func TestNowPlayingUnknownStream(t *testing.T) {
	useFeed(t, sampleFeed())
	var out, errb bytes.Buffer
	err := RunNowPlaying(npApp(&out, &errb, output.PrinterOptions{}), []int{99}, NowPlayingOptions{Once: true})
	if e := output.AsError(err); e.Code != "unknown_stream" || e.Exit != output.ExitUsage || e.Hint != "audd streams list" {
		t.Fatalf("got %v", err)
	}
	// When the stream list cannot be read, the stored results are shown
	// and whether the stream runs is reported as unknown.
	f := sampleFeed()
	f.stationsE = errors.New("offline")
	useFeed(t, f)
	out.Reset()
	if err := RunNowPlaying(npApp(&out, &errb, output.PrinterOptions{}), []int{1}, NowPlayingOptions{Once: true}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Stations []map[string]any `json:"stations"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || len(doc.Stations) != 1 {
		t.Fatalf("%s %v", out.String(), err)
	}
	if v, ok := doc.Stations[0]["stream_running"]; !ok || v != nil || doc.Stations[0]["now_playing"] == nil {
		t.Fatalf("unknown running state: %s", out.String())
	}
}

// An empty row separates the header from the card, the zoomed card, and
// the grid, and the page still fits: the footer is on the last row and the
// cover keeps its full height.
func TestNowPlayingHeaderGap(t *testing.T) {
	cases := []struct {
		name        string
		ids         []int
		noArt       bool
		wait, after string
		keys        []tea.KeyMsg
		want        string
	}{
		{"single", []int{1}, false, "Warriors", "", nil, "Stream 1"},
		{"grid", nil, true, "Feeling Good", "", nil, "┏"},
		{"zoomed", nil, false, "Feeling Good", "enter grid", []tea.KeyMsg{key("enter")}, "Stream 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := npTestModel(t, sampleFeed(), c.ids, c.noArt)
			view := runModelSized(t, m, 100, 34, c.wait, c.after, c.keys...)
			lines := strings.Split(visible(view), "\n")
			if len(lines) != 34 || !strings.HasPrefix(lines[0], "Now playing") || strings.TrimSpace(lines[1]) != "" ||
				!strings.Contains(lines[2], c.want) || !strings.Contains(lines[33], "q quit") {
				t.Fatalf("%d lines:\n%s", len(lines), strings.Join(lines, "\n"))
			}
			if !c.noArt {
				if n := strings.Count(view, "▀"); n < 14*28 {
					t.Fatalf("the cover is cut: %d cells", n)
				}
			}
		})
	}
}
