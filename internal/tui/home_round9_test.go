package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// Every editable setting refuses what audd config set refuses: no
// panic, the reason next to it, the text kept, nothing sent.
func TestSettingsRejectBadValues(t *testing.T) {
	f := &fakeRun{}
	f.reply("config set", `{"schema_version":1}`, "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "settings"}), 100, 30)
	drain(h, h.activate(), 0)
	s := h.subs["settings"].(*settingsSection)
	for _, k := range []string{"max_requests", "concurrency"} {
		for _, bad := range []string{"0", "-1", "-5", "abc", "1.5", "9999999999999999999999", "٣", "  ", "1 2", "0x10"} {
			for i, r := range s.rows {
				if r.key == k {
					s.cur = i
				}
			}
			s.edit = nil
			press(h, key("enter"))
			if s.edit == nil {
				t.Fatalf("%s: no editor", k)
			}
			press(h, key("ctrl+u"))
			press(h, typeText(bad)...)
			press(h, key("enter"))
			if s.edit == nil || s.edit.problem == "" || s.edit.input.Value() != bad {
				t.Fatalf("%s=%q: editor %v problem %q value %q\n%s", k, bad, s.edit != nil, func() string {
					if s.edit == nil {
						return ""
					}
					return s.edit.problem
				}(), func() string {
					if s.edit == nil {
						return ""
					}
					return s.edit.value()
				}(), h.View())
			}
			if v := h.View(); !strings.Contains(v, s.edit.problem) && !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(contentText(v), "│", " ")), " "), s.edit.problem) {
				t.Fatalf("%s=%q: the problem is not shown:\n%s", k, bad, v)
			}
			press(h, key("esc"))
		}
	}
	if len(changed(f)) != 0 {
		t.Fatalf("sent: %q", changed(f))
	}
}

// panicSection's update panics, to show a bug never closes the window.
type panicSection struct{ *testSection }

func (p panicSection) update(msg tea.Msg) tea.Cmd { panic("boom") }

func TestUpdateRecovers(t *testing.T) {
	testutil.Isolate(t)
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{}), 100, 30)
	ts := withTestSection(h)
	h.subs["recognize"] = panicSection{ts}
	drain(h, wrap("recognize", func() tea.Msg { return struct{}{} }), 0)
	if !strings.Contains(h.flash, "Something went wrong: boom (your data is safe") {
		t.Fatalf("flash: %q", h.flash)
	}
}

// q from the menu asks when an Add a stream form has text, naming it.
func TestQuitFromMenuNamesUnsavedAdd(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	press(h, key("down"), key("a"))
	press(h, typeText("https://radio.example/x.mp3")...)
	for i := 0; i < 5 && h.focus != focusSidebar; i++ {
		press(h, key("esc"))
	}
	if h.focus != focusSidebar {
		t.Fatalf("focus %v", h.focus)
	}
	_, cmd := h.Update(key("q"))
	if cmd != nil || h.confirm == nil || !h.confirm.quit || !strings.Contains(h.View(), "The stream you were adding is not saved.") {
		t.Fatalf("q from the menu:\n%s", h.View())
	}
	if foot := footerText(h.View()); strings.Contains(foot, "q quit") && !strings.Contains(foot, "from the menu") {
		t.Fatalf("footer: %q", foot)
	}
}

// The stream URL editor draws its caret where it is, not at the end.
func TestStreamURLCaretMidText(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	press(h, key("down"), key("enter"), key("e"))
	ex := h.subs["streams"].(*streamsSection).pane.ex
	press(h, key("ctrl+u"))
	press(h, typeText("https://radio.example/ab.mp3")...)
	press(h, key("left"), key("left"), key("left"), key("left"), key("left"))
	if ex.input.Position() != len("https://radio.example/a") {
		t.Fatalf("caret at %d", ex.input.Position())
	}
	if line := ex.statusLine(); strings.HasSuffix(strings.TrimSpace(line), "█") {
		t.Fatalf("the caret is drawn at the end: %q", line)
	}
	press(h, typeText("X")...)
	if ex.input.Value() != "https://radio.example/aXb.mp3" {
		t.Fatalf("typed mid-text: %q", ex.input.Value())
	}
}

// Signed out, the Callback page offers your own token only; a usage form
// in Commands never points at a button that is not there.
func TestSignedOutCallbackAndPaletteHint(t *testing.T) {
	f := &fakeRun{}
	f.reply("streams callback get", "", `{"schema_version":1,"error":{"code":"no_token","message":"no API token is set","hint":"audd login"}}`+"\n", 3)
	h := sized(newTestHome(t, f, homeOpts{start: "streams", signedOut: true}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("]"))
	pump(h, func() bool { return strings.Contains(h.View(), "Sign in or add a token") })
	v := h.View()
	if !strings.Contains(v, "Sign in or add a token to monitor streams") || strings.Contains(v, "test token ]") || strings.Contains(v, "test token, ") {
		t.Fatalf("signed-out Callback:\n%s", v)
	}
	if hh := humanHint("audd login"); strings.Contains(hh, "below") {
		t.Fatalf("hint points at a button: %q", hh)
	}
}

// Getting started scrolls to keep the selected step in view at 80x24.
func TestGettingStartedScrolls(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "help"}), 80, 24)
	drain(h, h.activate(), 0)
	press(h, key("down"))
	for i := 0; i < len(gettingStarted); i++ {
		press(h, key("down"))
	}
	hs := h.subs["help"].(*helpSection)
	last := gettingStarted[hs.stepCur].title
	v := h.View()
	if !strings.Contains(v, last) || !strings.Contains(v, "↑ ") || !strings.Contains(v, "more line") {
		t.Fatalf("step %d (%s) not in view:\n%s", hs.stepCur, last, v)
	}
}
