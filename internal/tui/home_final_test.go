package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
)

// A batch with a 502 failure: the list and the failed file's details
// both have [ Retry 1 file ], it takes the keys, and it asks with the
// plan (rendered at 80x24 and 120x40).
func TestBatchRetryButtonOnDetails(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		f := &fakeRun{}
		dir := t.TempDir()
		for _, n := range []string{"a.mp3", "b.mp3"} {
			os.WriteFile(filepath.Join(dir, n), nil, 0o644)
		}
		f.reply("recognize "+dir+" --max-files 2 --dry-run", "Plan: 2 files, at most 2 requests.\n", "", 0)
		f.reply("recognize "+dir+" --max-files 2", `{"schema_version":1,"type":"event","event":"start","job_id":"j7q2","to_run":2}`+"\n"+
			`{"schema_version":1,"type":"result","job_id":"j7q2","index":0,"input":"a.mp3","status":"matched","result":{"artist":"A","title":"One"}}`+"\n"+
			`{"schema_version":1,"type":"result","job_id":"j7q2","index":1,"input":"b.mp3","status":"failed","error":{"code":"server","message":"AudD returned HTTP 502","retryable":true}}`+"\n"+
			`{"schema_version":1,"type":"summary","job_id":"j7q2","recognized":1,"no_match":0,"failed":1}`+"\n",
			`{"schema_version":1,"error":{"code":"partial_failure","message":"1 of 2 files failed","hint":"see the failures with audd jobs show j7q2"}}`+"\n", 5)
		f.reply("jobs resume j7q2 --retry-failed --dry-run", "Plan: 1 file, at most 1 request.\n", "", 0)
		h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), size[0], size[1])
		drain(h, h.activate(), 0)
		s := recognizeOf(h)
		press(h, typeText(dir)...)
		pump(h, func() bool { return s.counted() })
		s.form.get("max-files").setValue("2")
		s.form.focusName("recognize")
		press(h, key("enter"))
		pump(h, func() bool { return h.ask != nil || s.phase == "result" })
		if h.ask != nil {
			press(h, key("right"), key("enter"))
		}
		pump(h, func() bool { return s.phase == "result" && !s.panel.running })
		press(h, key("enter"), key("enter")) // one or two enters after confirming
		if v := h.View(); !strings.Contains(v, "Retry 1 file") {
			t.Fatalf("%v: the list has no Retry:\n%s", size, v)
		}
		// The failed file's details (back on the list first).
		if s.bs.detail {
			press(h, key("esc"))
		}
		h.btnActive = false
		for i := 0; i < 3 && s.bs.cursor != 1; i++ {
			press(h, key("down"))
		}
		if s.bs.cursor != 1 {
			t.Fatalf("%v: cursor %d, btn %v:\n%s", size, s.bs.cursor, h.btnActive, h.View())
		}
		press(h, key("enter"))
		v := h.View()
		if !s.bs.detail || !strings.Contains(v, "[ Retry 1 file ]") || !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(contentText(v), "│", " ")), " "), "A retry can work") {
			t.Fatalf("%v: details:\n%s", size, v)
		}
		press(h, key("tab"))
		if selectedButton(h) != "Retry 1 file" {
			t.Fatalf("%v: focus on %q", size, selectedButton(h))
		}
		press(h, key("enter"))
		pump(h, func() bool { return h.confirm != nil })
		if v := h.View(); h.confirm == nil || !strings.Contains(v, "Retry the failed files of job j7q2?") || !strings.Contains(v, "at most 1 request") {
			t.Fatalf("%v: the retry question:\n%s", size, v)
		}
	}
}

// A saved value: typing replaces it (as if all selected), enter keeps it
// to edit, esc puts it back; letters are never shortcuts there.
func TestTypingReplacesASavedValue(t *testing.T) {
	h, f := callbackHome(t)
	s := h.subs["streams"].(*streamsSection)
	press(h, key("down"))
	press(h, typeText("https://hooks.example/u")...) // "u" is undo elsewhere
	if v := s.cbForm.value("url"); v != "https://hooks.example/u" {
		t.Fatalf("typed over: %q", v)
	}
	if !strings.Contains(h.View(), "https://hooks.example/u|") {
		t.Fatalf("the field while typing:\n%s", h.View())
	}
	press(h, key("esc"))
	if v := s.cbForm.value("url"); v != "https://audd.tech/empty/" {
		t.Fatalf("esc restores: %q", v)
	}
	press(h, key("enter"))
	press(h, typeText("x")...)
	if v := s.cbForm.value("url"); v != "https://audd.tech/empty/x" {
		t.Fatalf("enter keeps the value: %q", v)
	}
	if len(changed(f)) != 0 {
		t.Fatalf("sent: %v", changed(f))
	}
	// A stream's URL editor too: the first letter replaces the URL.
	h2 := navHome(t, homeOpts{start: "streams"})
	press(h2, key("down"), key("enter"), key("e"))
	press(h2, typeText("twitch:me")...)
	if ex := h2.subs["streams"].(*streamsSection).pane.ex; ex.input.Value() != "twitch:me" {
		t.Fatalf("stream URL: %q", ex.input.Value())
	}
}

// Stream history reads older plays at the end of the list, and says
// how many are shown.
func TestStreamHistoryLoadsMore(t *testing.T) {
	feed := &fakeFeed{stations: []Station{{RadioID: 1, URL: "https://radio.example/a.mp3", Running: true}}, plays: map[int][]Play{}}
	for i := 0; i < 120; i++ {
		feed.plays[1] = append(feed.plays[1], Play{RadioID: 1, At: testNow.Add(-time.Duration(i) * time.Minute), ResultView: app.ResultView{Artist: "A", Title: fmt.Sprintf("Song %d", i)}})
	}
	d := fakeExplorerData(&fakeStreamsAPI{})
	d.Feed = feed
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "streams"}), 100, 30)
	NewExplorerData = func(a *app.App) (ExplorerData, error) { return d, nil }
	drain(h, h.activate(), 0)
	press(h, key("down"), key("enter"), key("tab"), key("enter")) // the stream's History
	ex := h.subs["streams"].(*streamsSection).pane.ex
	l := ex.top()
	if l.kind != "plays" || len(l.rows) != 50 || !l.more {
		t.Fatalf("first page: kind %s rows %d more %v\n%s", l.kind, len(l.rows), l.more, h.View())
	}
	if v := h.View(); !strings.Contains(v, "the latest 50 plays · ↓ at the end loads older ones") {
		t.Fatalf("the count:\n%s", v)
	}
	h.btnActive = false
	for i := 0; i < 60 && len(l.rows) == 50; i++ {
		press(h, key("down"))
	}
	if len(l.rows) != 100 {
		t.Fatalf("after the end: %d rows", len(l.rows))
	}
	for i := 0; i < 150; i++ {
		press(h, key("down"))
	}
	if len(l.rows) != 120 || l.more || !strings.Contains(h.View(), "all 120 plays") {
		t.Fatalf("all of them: %d more %v\n%s", len(l.rows), l.more, h.View())
	}
}

// Recorder stopped: Now playing and Streams › List say so, with a
// button to start it.
func TestRecorderStoppedNotice(t *testing.T) {
	f := &fakeRun{}
	old := RecorderRunning
	RecorderRunning = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { RecorderRunning = old })
	for _, start := range []string{"now-playing", "streams"} {
		h := sized(newTestHome(t, f, homeOpts{start: start}), 100, 30)
		drain(h, h.activate(), 0)
		pump(h, func() bool { return h.recorderStopped() })
		if start == "streams" {
			press(h, key("down"))
		}
		v := h.View()
		if !strings.Contains(v, "The recorder is stopped: new songs aren't being saved") || !strings.Contains(v, "Start the recorder") {
			t.Fatalf("%s:\n%s", start, v)
		}
	}
}

// Pasting a token says it was saved: on the welcome page and in
// Account.
func TestPasteTokenSaysSaved(t *testing.T) {
	f := &fakeRun{}
	f.reply("config set token", `{"schema_version":1}`, "", 0)
	h := sized(newTestHome(t, f, homeOpts{signedOut: true}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("down"), key("enter"))
	press(h, typeText("abcdef0123456789abcdef0123456789")...)
	press(h, key("enter"))
	pump(h, func() bool { return strings.Contains(statusText(h.View()), "Token saved for profile default") })
	if !strings.Contains(statusText(h.View()), "Token saved for profile default") {
		t.Fatalf("welcome paste:\n%s", h.View())
	}
	f2 := accountFake()
	f2.reply("config set token", `{"schema_version":1}`, "", 0)
	h2 := sized(newTestHome(t, f2, homeOpts{start: "account", loggedIn: true}), 100, 30)
	drain(h2, h2.activate(), 0)
	a := h2.subs["account"].(*accountSection)
	a.page = 3
	a.tokPasting = true
	a.tokForm.focusName("token")
	a.tokForm.resume()
	h2.focus = focusContent
	press(h2, typeText("abcdef0123456789abcdef0123456789")...)
	press(h2, key("enter"))
	if h2.confirm != nil {
		press(h2, key("right"), key("enter"))
	}
	pump(h2, func() bool { return strings.Contains(statusText(h2.View()), "Token saved for profile") })
	if !strings.Contains(statusText(h2.View()), "Token saved for profile") {
		t.Fatalf("account paste:\n%s", h2.View())
	}
}
