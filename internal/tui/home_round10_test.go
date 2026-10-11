package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
)

// After a dialog starts a run, no button has the keys (never Stop), and
// an enter right after does nothing but say so.
func TestNoStopRightAfterConfirming(t *testing.T) {
	f := &fakeRun{}
	f.reply("jobs resume k3x9 --retry-failed --dry-run", "Plan: 1 file, at most 1 request.\n", "", 0)
	release := make(chan struct{})
	f.on("jobs resume k3x9 --retry-failed --format", func(args []string, io RunIO) int {
		<-release
		return 0
	})
	h := newTestHome(t, f, homeOpts{start: "recognitions"})
	// Let the run end before the test's fakes are put back.
	t.Cleanup(func() { close(release); h.waitRuns(5 * time.Second) })
	drive(t, h, 100, 30, "Warriors", key("tab"), "k3x9", key("down"), key("enter"), "Retry 1 file")
	h.focusButton("Retry 1 file")
	press(h, key("enter"))
	pump(h, func() bool { return h.confirm != nil })
	press(h, key("right"), key("enter"))
	if h.btnActive {
		t.Fatalf("a button has the keys after confirming:\n%s", h.View())
	}
	press(h, key("enter"))
	if !strings.Contains(statusText(h.View()), "Started · to stop it") {
		t.Fatalf("enter right after:\n%s", h.View())
	}
	if hs := h.subs["recognitions"].(*historySection); !hs.panel.running {
		t.Fatal("the run was stopped")
	}
}

// A batch run's retryable failures get a Retry button right on the
// results, and their details say a retry can work.
func TestBatchResultsOfferRetry(t *testing.T) {
	bs := &batchState{jobID: "j1", selectable: true, results: []map[string]any{
		{"status": "failed", "input": "a.mp3", "error": map[string]any{"code": "server", "message": "AudD returned HTTP 502"}},
	}}
	h := navHome(t, homeOpts{})
	acts := h.batchButtons(bs)
	if len(acts) == 0 || acts[0].label != "Retry 1 file" {
		t.Fatalf("buttons: %+v", acts)
	}
	bs.detail = true
	if v := h.batchPage(bs, 80, 20); !strings.Contains(strings.Join(strings.Fields(v), " "), "A retry can work") {
		t.Fatalf("details:\n%s", v)
	}
}

// Without color the caret is a "|" where it is, in every text field.
func TestCaretWithoutColor(t *testing.T) {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Focus()
	ti.SetValue("rocks")
	ti.SetCursor(4)
	if got := inputView(ti, false); got != "rock|s" {
		t.Fatalf("caret: %q", got)
	}
	h := navHome(t, homeOpts{start: "recognize"})
	press(h, typeText("song.mp3")...)
	press(h, key("left"), key("left"), key("left"), key("left"))
	if !strings.Contains(h.View(), "song|.mp3") {
		t.Fatalf("the form's caret:\n%s", h.View())
	}
}

// Signed out, saving a callback or adding a stream asks how to get a
// token (your own: streams), never silently does nothing.
func TestSignedOutStreamActionsAsk(t *testing.T) {
	f := &fakeRun{}
	f.reply("streams callback get", "", `{"schema_version":1,"error":{"code":"no_token","message":"no API token is set"}}`+"\n", 3)
	h := sized(newTestHome(t, f, homeOpts{start: "streams", signedOut: true}), 100, 30)
	drain(h, h.activate(), 0)
	s := h.subs["streams"].(*streamsSection)
	press(h, key("]"), key("down"))
	press(h, typeText("https://hooks.example/audd")...)
	s.cbForm.focusName("set")
	press(h, key("enter"))
	if v := h.View(); h.confirm == nil || !strings.Contains(v, "Saving the callback URL needs an API token") || strings.Contains(v, "test token ]") {
		t.Fatalf("callback save signed out:\n%s", v)
	}
	press(h, key("esc"))
	press(h, key("["), key("down"))
	s.openAdd()
	h.focus = focusContent
	press(h, typeText("https://radio.example/x.mp3")...)
	s.add.focusName("add")
	press(h, key("enter"))
	if v := h.View(); h.confirm == nil || !strings.Contains(v, "Adding a stream needs an API token") {
		t.Fatalf("add signed out:\n%s", v)
	}
	if len(changed(f)) != 0 {
		t.Fatalf("sent: %v", changed(f))
	}
}

// ":" typed on a form never opens the palette (typed text never lands
// there).
func TestColonOnAFormStaysPut(t *testing.T) {
	h := navHome(t, homeOpts{start: "recognize"})
	press(h, key("esc")) // stop typing, stay on the form
	press(h, key(":"))
	if h.paletteO {
		t.Fatal("the palette opened from a form")
	}
}

// A recognition that ran is not "not saved" when quitting.
func TestQuitAfterARun(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 1 request.\n", "", 0)
	f.reply("recognize song.mp3", warriorsDoc, "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	press(h, typeText("song.mp3")...)
	press(h, key("enter"))
	pump(h, func() bool { return s.phase == "result" })
	press(h, key("esc"), key("esc"))
	for i := 0; i < 4 && h.focus != focusSidebar; i++ {
		press(h, key("esc"))
	}
	if _, cmd := h.Update(key("q")); cmd == nil || h.confirm != nil {
		t.Fatalf("q after a run asks:\n%s", h.View())
	}
}

// Help: ↓ from the tab bar lands on the first step; "retry" finds the
// command whose help mentions it.
func TestHelpFirstStepAndPaletteSearch(t *testing.T) {
	h := navHome(t, homeOpts{start: "help"})
	hs := h.subs["help"].(*helpSection)
	hs.stepCur = 3
	h.focus = focusTabs
	press(h, key("down"))
	if hs.stepCur != 0 {
		t.Fatalf("on step %d", hs.stepCur)
	}
	p := newPicker([]pickItem{{title: "jobs resume", desc: "Continue a job", value: "jobs resume", more: "--retry-failed also retry"}, {title: "recognize", desc: "Identify music", value: "recognize"}})
	p.filter.SetValue("retry")
	if m := p.matches(); len(m) != 1 || m[0].value != "jobs resume" {
		t.Fatalf("retry: %+v", m)
	}
}

var _ = time.Second
