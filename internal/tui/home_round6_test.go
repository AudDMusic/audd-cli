package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/media"
)

// Typing a path never reads the disk while a key is handled: counting
// runs in the background, a moment after the last key, and stops at a
// cap; a whole disk is never walked.
func TestTypingAPathStaysFast(t *testing.T) {
	big := t.TempDir()
	for d := range 30 {
		dir := filepath.Join(big, fmt.Sprintf("d%02d", d))
		os.Mkdir(dir, 0o755)
		for f := range 50 {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("t%02d.mp3", f)), nil, 0o644)
		}
	}
	h := navHome(t, homeOpts{start: "recognize"})
	s := recognizeOf(h)
	for _, k := range typeText("/" + strings.TrimPrefix(big, "/")) {
		start := time.Now()
		h.Update(k)
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Fatalf("a key took %v", d)
		}
	}
	if s.counted() {
		t.Fatal("counted while typing")
	}
	// The count, when it runs, stops at the cap.
	c := media.CountInput(context.Background(), big, 1000, 0)
	if !c.Batch || c.Files != 1000 || !c.More {
		t.Fatalf("capped count: %+v", c)
	}
	// "/" is not walked.
	start := time.Now()
	if c := media.CountInput(context.Background(), "/", 1000, 0); !c.Root || c.Files != 0 {
		t.Fatalf("root: %+v", c)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("the root was walked")
	}
	// The section counts in the background and shows it.
	press(h, key("ctrl+u"))
	press(h, typeText(big)...)
	pump(h, func() bool { return s.counted() })
	if v := h.View(); !strings.Contains(v, "Found 1000+ files") {
		t.Fatalf("the count:\n%s", v)
	}
}

// The Recorder page reads the status JSON: Running with Stop, Stopped
// with Start, and it reads the status again after each action.
func TestRecorderPageState(t *testing.T) {
	f := &fakeRun{}
	var running atomic.Bool
	running.Store(true)
	f.on("streams recorder status", func(args []string, io RunIO) int {
		fmt.Fprintf(io.Stdout, `{"schema_version":1,"running":%v,"pid":4242,"store":"/data/streams-default.db","plays":3}`+"\n", running.Load())
		return 0
	})
	f.on("streams recorder stop", func(args []string, io RunIO) int { running.Store(false); return 0 })
	f.on("streams recorder start", func(args []string, io RunIO) int { running.Store(true); return 0 })
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("]"), key("]"), key("down"))
	pump(h, func() bool { return strings.Contains(h.View(), "[ Stop ]") })
	if v := h.View(); !strings.Contains(v, "Running (process 4242)") || strings.Contains(v, "[ Start ]") {
		t.Fatalf("running:\n%s", v)
	}
	press(h, key("enter"))
	pump(h, func() bool { return h.confirm != nil })
	press(h, key("y"))
	pump(h, func() bool { return strings.Contains(h.View(), "[ Start ]") })
	if v := h.View(); !strings.Contains(v, "Stopped. Press enter to start it.") || strings.Contains(v, "[ Stop ]") {
		t.Fatalf("stopped:\n%s", v)
	}
	press(h, key("enter"))
	pump(h, func() bool { return strings.Contains(h.View(), "[ Stop ]") })
}

// After an error the cursor is back in the field it is about, typing,
// at the end of the text: a missing path, Max files, the limit.
func TestErrorFocusesItsField(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --enterprise --limit 5 --dry-run", "", `{"schema_version":1,"error":{"code":"limit_too_high","message":"--limit 5 is more than the file needs","hint":"lower --limit"}}`+"\n", 2)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize", signedOut: true}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	// A path that is not there: said before any token question.
	press(h, typeText("nope.mp3")...)
	press(h, key("enter"))
	pump(h, func() bool { return s.form.current().problem != "" })
	if h.confirm != nil || s.form.current().name != "input" || !s.form.capturing() || s.form.get("input").input.Position() != len("nope.mp3") ||
		!strings.Contains(s.form.current().problem, "No such file or folder") {
		t.Fatalf("missing path: confirm %v field %s\n%s", h.confirm != nil, s.form.current().name, h.View())
	}
	// A run error about --limit: back in Limit.
	drain(h, h.signedIn(true), 0)
	press(h, key("ctrl+u"))
	press(h, typeText("song.mp3")...)
	s.form.get("enterprise").on = true
	s.sync()
	s.form.get("limit").setValue("5")
	s.form.focusName("recognize")
	s.form.focus()
	press(h, key("enter"))
	pump(h, func() bool { return s.phase == "form" && s.form.current().name == "limit" })
	if !s.form.capturing() || !strings.Contains(s.form.current().problem, "--limit 5 is more than the file needs") || h.btnActive {
		t.Fatalf("limit error:\n%s", h.View())
	}
}

// A folder with more files than Max files: a dialog (on Cancel) offers
// to set Max files; Cancel puts the cursor in Max files.
func TestTooManyFilesOffersTheFix(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		os.WriteFile(filepath.Join(dir, n+".mp3"), nil, 0o644)
	}
	f := &fakeRun{}
	f.reply("recognize "+dir+" --max-files 3 --dry-run", "Plan: 3 files, at most 3 requests.\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	press(h, typeText(dir)...)
	pump(h, func() bool { return s.counted() })
	s.form.get("max-files").setValue("2")
	s.form.focusName("recognize")
	press(h, key("enter"))
	v := h.View()
	if h.confirm == nil || !strings.Contains(v, "The input has 3 files, more than Max files 2") || !strings.Contains(v, "[>Cancel<]") || !strings.Contains(v, "[ Set Max files to 3 ]") {
		t.Fatalf("the offer:\n%s", v)
	}
	press(h, key("enter"))
	if s.form.current().name != "max-files" || !s.form.capturing() {
		t.Fatalf("cancel: in %s", s.form.current().name)
	}
	press(h, key("enter"))
	press(h, key("y"))
	if s.form.value("max-files") != "3" {
		t.Fatalf("set to %q", s.form.value("max-files"))
	}
}

// Enterprise matches are a list everywhere: one match too, and in
// Recognitions › Recent, where each match has its own link.
func TestEnterpriseMatchesAreLists(t *testing.T) {
	f := &fakeRun{}
	f.on("recognize song.mp3 --enterprise --limit 1", func(args []string, io RunIO) int {
		if strings.Contains(strings.Join(args, " "), "--dry-run") {
			io.Stdout.Write([]byte("Plan: 1 request.\n"))
			return 0
		}
		io.Stdout.Write([]byte(`{"schema_version":1,"enterprise":true,"result":[{"offset":"00:00","songs":[{"artist":"A","title":"One","start_seconds":2,"end_seconds":10,"song_link":"https://lis.tn/one"}]}]}` + "\n"))
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	press(h, typeText("song.mp3")...)
	s.form.get("enterprise").on = true
	s.sync()
	s.form.get("limit").setValue("1")
	press(h, key("enter"))
	pump(h, func() bool { return s.phase == "result" })
	if v := h.View(); !strings.Contains(v, "1 match in song.mp3") || !strings.Contains(v, "[ Copy the song link (c) ]") {
		t.Fatalf("one match:\n%s", v)
	}
	// Recent: the mix with 2 matches opens a list; each has its link.
	h2 := navHome(t, homeOpts{start: "recognitions"})
	press(h2, key("down"), key("down"), key("down"), key("enter"))
	v := h2.View()
	if !strings.Contains(crumbText(v), "2 matches in mix.mp3") || !strings.Contains(v, "One More Time") || !strings.Contains(v, "Aerodynamic") {
		t.Fatalf("matches list:\n%s", v)
	}
	press(h2, key("down"), key("enter"))
	if v := h2.View(); !strings.Contains(v, "Aerodynamic") || strings.Contains(v, "One More Time") {
		t.Fatalf("the second match's details:\n%s", v)
	}
}

// Undo lasts until the next change or until the section is left; its
// button is reachable; ctrl+z works; with nothing to undo, u says so.
func TestUndoSurvivesNavigation(t *testing.T) {
	f := &fakeRun{}
	f.reply("config set", `{"schema_version":1}`, "", 0)
	f.reply("config unset", `{"schema_version":1}`, "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "settings"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("enter"), key("right"), key("enter"))
	f.waitFor(t, "config set format table")
	pump(h, func() bool { return strings.Contains(h.View(), "[ Undo (u) ]") })
	press(h, key("down"), key("down"), key("up"), key("up"))
	if !strings.Contains(h.View(), "[ Undo (u) ]") {
		t.Fatalf("undo after moving:\n%s", h.View())
	}
	press(h, key("tab"))
	for selectedButton(h) != "Undo" && h.btnCur < 5 {
		press(h, key("right"))
	}
	if selectedButton(h) != "Undo" {
		t.Fatal("Undo is reachable with tab and arrows")
	}
	press(h, key("esc"))
	press(h, key("ctrl+z"))
	f.waitFor(t, "config unset format")
	// Another change, then leaving Settings: no undo after coming back.
	press(h, key("enter"), key("right"), key("right"), key("enter"))
	f.waitFor(t, "config set format json")
	press(h, key("esc"), key("2"), key("6"), key("right"))
	press(h, key("u"))
	if strings.Contains(h.View(), "[ Undo (u) ]") || !strings.Contains(statusText(h.View()), "Nothing to undo") {
		t.Fatalf("after leaving:\n%s", h.View())
	}
}

// Signed out, buttons that need a token are dimmed and offer the ways
// to get one; empty and signed-out states are not errors.
func TestSignedOutButtons(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams", signedOut: true})
	press(h, key("down"), key("tab"))
	acts := h.pageActions()
	if len(acts) == 0 || acts[0].label != "Add a stream" || acts[0].off == "" {
		t.Fatalf("Add a stream: %+v", acts)
	}
	if foot := footerText(h.View()); !strings.Contains(foot, "get an API token first") {
		t.Fatalf("footer: %q", foot)
	}
	press(h, key("enter"))
	if v := h.View(); h.confirm == nil || !strings.Contains(v, "Add a stream needs an API token") || !strings.Contains(v, "[ Use the test token ]") {
		t.Fatalf("the token question:\n%s", v)
	}
	if strings.Contains(h.View(), "Error") {
		t.Fatalf("not an error:\n%s", h.View())
	}
}

// Esc in a new form's field stops typing and keeps the value (the form
// keeps the keys); in a saved value it cancels and puts it back.
func TestEscInFields(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	s := h.subs["streams"].(*streamsSection)
	press(h, key("down"), key("a"))
	press(h, typeText("https://radio.example/x.mp3")...)
	if foot := footerText(h.View()); !strings.Contains(foot, "esc done") {
		t.Fatalf("new form footer: %q", foot)
	}
	press(h, key("esc"))
	if !s.adding || s.add.capturing() || s.add.value("url") != "https://radio.example/x.mp3" || h.focus != focusContent {
		t.Fatalf("esc in a new field:\n%s", h.View())
	}
	press(h, key("x"))
	if s.add.value("url") != "https://radio.example/x.mp3" || !strings.Contains(footerText(h.View()), "enter type in URL") {
		t.Fatalf("a letter after esc:\n%s", h.View())
	}
	// A saved value: esc cancels.
	h2, _ := callbackHome(t)
	s2 := h2.subs["streams"].(*streamsSection)
	press(h2, key("down"), key("enter"))
	press(h2, typeText("zz")...)
	if foot := footerText(h2.View()); !strings.Contains(foot, "esc cancel") {
		t.Fatalf("saved value footer: %q", foot)
	}
	press(h2, key("esc"))
	if s2.cbForm.value("url") != "https://audd.tech/empty/" {
		t.Fatalf("esc puts it back: %q", s2.cbForm.value("url"))
	}
}
