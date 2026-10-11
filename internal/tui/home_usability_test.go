package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/media"
)

// callbackHome is Streams on the Callback tab bar, with the callback URL
// in use read from the account.
func callbackHome(t *testing.T) (*home, *fakeRun) {
	t.Helper()
	f := &fakeRun{}
	f.reply("streams callback get", `{"schema_version":1,"url":"https://audd.tech/empty/"}`, "", 0)
	f.reply("streams callback set", `{"schema_version":1}`, "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("]"))
	f.waitFor(t, "streams callback get")
	waitView(t, h, "Callback URL: https://audd.tech/empty/")
	return h, f
}

// Letters typed on a tab bar never reach a text field, and a field that
// holds an existing value changes only after enter or e.
func TestLettersNeverReachAFieldWithoutTheKeys(t *testing.T) {
	h, _ := callbackHome(t)
	s := h.subs["streams"].(*streamsSection)
	press(h, typeText("ab")...)
	if got := s.cbForm.value("url"); got != "https://audd.tech/empty/" || h.focus != focusTabs {
		t.Fatalf("typing on the tab bar changed the URL to %q (focus %v)", got, h.focus)
	}
	if !strings.Contains(statusText(h.View()), "b does nothing here") {
		t.Fatalf("no feedback:\n%s", h.View())
	}
	press(h, key("down"))
	press(h, typeText("zz")...)
	if got := s.cbForm.value("url"); got != "https://audd.tech/empty/" {
		t.Fatalf("a read-only value changed: %q", got)
	}
	if !strings.Contains(h.View(), "(enter or e to edit)") || !strings.Contains(footerText(h.View()), "enter/e edit") {
		t.Fatalf("the read-only field does not say how to change it:\n%s", h.View())
	}
	press(h, key("e"))
	press(h, typeText("x")...)
	if got := s.cbForm.value("url"); got != "https://audd.tech/empty/x" {
		t.Fatalf("after e the field takes typing: %q", got)
	}
}

// Changing the account's callback URL asks first, names both URLs, and
// the question has the keys to itself; no is not an error.
func TestCallbackChangeAsks(t *testing.T) {
	h, f := callbackHome(t)
	s := h.subs["streams"].(*streamsSection)
	press(h, key("down"), key("enter"), key("ctrl+u"))
	press(h, typeText("https://hooks.example/audd")...)
	press(h, key("enter"))
	v := h.View()
	for _, want := range []string{"Change the callback URL?", "Now: https://audd.tech/empty/", "New: https://hooks.example/audd", "every result of every stream"} {
		if !strings.Contains(v, want) {
			t.Fatalf("no %q:\n%s", want, v)
		}
	}
	if foot := footerText(v); !strings.HasPrefix(foot, "←/→/tab choose  enter press the selected button") || strings.Contains(foot, "confirm") || !strings.Contains(crumbText(v), "Streams › Callback › Change the callback URL") || !strings.Contains(v, "[ Change the callback URL ]") {
		t.Fatalf("the question's hints: %q\n%s", foot, v)
	}
	press(h, key("n"))
	if strings.Contains(strings.Join(f.commands(), "\n"), "callback set") || !strings.Contains(statusText(h.View()), "Not saved · esc puts the old value back") || s.cbForm.capturing() {
		t.Fatalf("n: %q\n%s", f.commands(), h.View())
	}
	if s.cbForm.value("url") != "https://hooks.example/audd" {
		t.Fatal("the typed URL is kept after cancelling")
	}
	// enter edits the kept text, enter asks again, and a double enter
	// lands on Cancel.
	press(h, key("enter"), key("enter"), key("enter"))
	if strings.Contains(strings.Join(f.commands(), "\n"), "callback set") {
		t.Fatal("a double enter lands on Cancel")
	}
	press(h, key("enter"), key("enter"), key("right"), key("enter"))
	f.waitFor(t, "streams callback set https://hooks.example/audd")
}

// ctrl+k opens and closes the palette; the keys are back where they
// were.
func TestCtrlKToggles(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	press(h, ctrlK())
	if !h.paletteO {
		t.Fatal("ctrl+k opens the palette")
	}
	press(h, ctrlK())
	if h.paletteO || h.focus != focusTabs {
		t.Fatalf("ctrl+k closes it: palette %v focus %v", h.paletteO, h.focus)
	}
}

// While typing, digits are typed (not jumps), the footer says how to
// leave and clear the field, and ctrl+u clears it.
func TestTypingInAField(t *testing.T) {
	h := navHome(t, homeOpts{start: "recognize"})
	press(h, typeText("song3.mp3")...)
	s := recognizeOf(h)
	if h.activeID() != "recognize" || s.form.value("input") != "song3.mp3" {
		t.Fatalf("3 was a jump: %s %q", h.activeID(), s.form.value("input"))
	}
	foot := footerText(h.View())
	if !strings.Contains(foot, "esc done") || !strings.Contains(foot, "ctrl+u clear") || strings.Contains(foot, "? keys") {
		t.Fatalf("footer while typing: %q", foot)
	}
	press(h, key("ctrl+u"))
	if s.form.value("input") != "" {
		t.Fatal("ctrl+u clears the field")
	}
}

// A status message lasts until the next key and never hides the hints.
func TestStatusLineIsSeparate(t *testing.T) {
	h := navHome(t, homeOpts{start: "settings"})
	press(h, key("z"))
	v := h.View()
	if !strings.Contains(statusText(v), "does nothing here") || !strings.Contains(footerText(v), "↑/↓ move") {
		t.Fatalf("status and hints:\n%s", v)
	}
	press(h, key("down"))
	if statusText(h.View()) != "" {
		t.Fatalf("the message stays after the next key: %q", statusText(h.View()))
	}
}

// No to a spending question goes back to the form, as it was.
func TestCancelledRunIsNotAnError(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 1 file, 1 request.\nNothing was sent (--dry-run).\n", "", 0)
	f.on("recognize song.mp3 --format", func(args []string, io RunIO) int {
		if !io.Ask("Recognize 1 file, using 1 request?") {
			io.Stderr.Write([]byte(`{"error":{"code":"declined","message":"cancelled"}}` + "\n"))
			return 6
		}
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, typeText("song.mp3")...)
	press(h, key("enter"))
	waitView(t, h, "Recognize 1 file, using 1 request?")
	press(h, key("n"))
	s := recognizeOf(h)
	// The run ends after the answer.
	pump(h, func() bool { return s.phase == "form" })
	v := h.View()
	if !strings.Contains(statusText(v), "Cancelled. Nothing was sent.") || s.phase != "form" || s.form.value("input") != "song.mp3" || strings.Contains(v, "Error") {
		t.Fatalf("after no: phase %s\n%s", s.phase, v)
	}
}

// The plan disappears when the options change.
func TestPlanGoesWhenOptionsChange(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 1 file, 1 request.\nNothing was sent (--dry-run).\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	s.form.get("input").setValue("song.mp3")
	s.form.focusName("plan")
	press(h, key("enter"))
	v := waitView(t, h, "Plan: 1 file, 1 request.")
	if strings.Contains(v, "Nothing was sent") {
		t.Fatalf("the dry run's own note shows:\n%s", v)
	}
	s.form.get("return:spotify").on = true
	if strings.Contains(h.View(), "Plan: 1 file") {
		t.Fatal("a plan for other options stays")
	}
}

// Choices change with ←/→ without wrapping; ← on the first option goes
// back. Buttons in a row are walked with ←/→.
func TestChoicesAndButtons(t *testing.T) {
	h := navHome(t, homeOpts{start: "recognize"})
	s := recognizeOf(h)
	press(h, key("up"), key("right"), key("right"))
	if !s.mic() || h.focus != focusContent {
		t.Fatal("→ stops on the last option")
	}
	press(h, key("left"))
	if s.mic() || h.focus != focusContent {
		t.Fatal("← changes the choice back")
	}
	s.form.get("input").setValue("song.mp3")
	s.form.focusName("recognize")
	press(h, key("right"))
	if s.form.current().name != "plan" {
		t.Fatalf("→ moves to the next button: %s", s.form.current().name)
	}
	press(h, key("left"), key("left"))
	if h.focus != focusSidebar {
		t.Fatal("← from the first button goes back")
	}
}

// Max files is required (and marked) as soon as the input is a folder;
// the command shows what is missing; pressing Recognize goes to it.
func TestRequiredFollowsTheInput(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("x"), 0o644)
	f := &fakeRun{}
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, typeText(dir)...)
	s := recognizeOf(h)
	if !strings.Contains(h.View(), "Max files*") || !strings.Contains(h.command(), "--max-files '<max files>'") {
		t.Fatalf("max files:\n%s\n%s", h.View(), h.command())
	}
	press(h, key("enter"))
	if s.form.current().name != "max-files" || !strings.Contains(h.View(), "need a file limit") || len(f.commands()) != 0 {
		t.Fatalf("enter goes to the missing field: %s\n%s", s.form.current().name, h.View())
	}
}

// A batch's results are a list: details, open and copy the link.
func TestBatchResultsAreAList(t *testing.T) {
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	dir := t.TempDir()
	for _, n := range []string{"a.mp3", "b.mp3"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	f := &fakeRun{}
	f.reply("recognize "+dir+" --max-files 2 --dry-run", "Plan: 2 files, 2 requests.\n", "", 0)
	a, _ := json.Marshal(filepath.Join(dir, "a.mp3"))
	b, _ := json.Marshal(filepath.Join(dir, "b.mp3"))
	f.reply("recognize "+dir+" --max-files 2 --format",
		`{"schema_version":1,"type":"result","job_id":"j1","input":`+string(a)+`,"status":"no_match","result":null}`+"\n"+
			`{"schema_version":1,"type":"result","job_id":"j1","input":`+string(b)+`,"status":"matched","result":{"artist":"Imagine Dragons","title":"Warriors","album":"Night Visions","song_link":"https://lis.tn/Warriors"}}`+"\n"+
			`{"schema_version":1,"type":"summary","job_id":"j1","recognized":1,"no_match":1,"failed":0,"requests_spent_this_run":2}`+"\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	s.form.get("input").setValue(dir)
	s.form.get("max-files").setValue("2")
	s.sync()
	s.form.focusName("recognize")
	press(h, key("enter"))
	waitView(t, h, "Job j1: 1 recognized")
	press(h, key("down"), key("enter"))
	if v := h.View(); !strings.Contains(v, "Night Visions") || !strings.Contains(crumbText(v), "Result › Details") {
		t.Fatalf("details of the second file:\n%s", v)
	}
	press(h, key("o"))
	if len(opened) != 1 || opened[0] != "https://lis.tn/Warriors" {
		t.Fatalf("o opens the link: %v", opened)
	}
	press(h, key("esc"))
	if s.bs.detail || s.phase != "result" {
		t.Fatal("esc goes back to the list")
	}
}

// Without ffmpeg or sox, Listen says why and does nothing.
func TestListenWithoutTools(t *testing.T) {
	useListenTools(t, media.Tools{})
	f := &fakeRun{}
	h := sized(newTestHome(t, f, homeOpts{start: "listen"}), 100, 30)
	drain(h, h.activate(), 0)
	drain(h, recognizeOf(h).findDevices(), 0)
	press(h, key("enter"))
	v := h.View()
	if len(f.commands()) != 0 || !strings.Contains(v, "Listening needs ffmpeg or sox") || !strings.Contains(v, "To install it:") || strings.Contains(v, "try again") {
		t.Fatalf("listen without tools ran %q:\n%s", f.commands(), v)
	}
}

// Signed out, Account offers every way in, not only the browser sign-in.
func TestAccountSignedOutOffersEveryWayIn(t *testing.T) {
	h := navHome(t, homeOpts{start: "account", signedOut: true})
	v := h.View()
	for _, want := range []string{"Sign in", "Paste an API token", "Try with the test token"} {
		if !strings.Contains(v, want) {
			t.Fatalf("no %q:\n%s", want, v)
		}
	}
	press(h, key("]"), key("]"), key("]"), key("]"), key("down"))
	if strings.Contains(footerText(h.View()), "sign out") {
		t.Fatalf("sign out shows when signed out: %q", footerText(h.View()))
	}
}

// Enter on Usage goes to Billing.
func TestUsageLeadsToBilling(t *testing.T) {
	f := accountFake()
	h := sized(newTestHome(t, f, homeOpts{start: "account", loggedIn: true}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("]"), key("down"))
	if !strings.Contains(h.View(), "opens the Billing tab") {
		t.Fatalf("usage:\n%s", h.View())
	}
	press(h, key("enter"))
	if a := h.subs["account"].(*accountSection); a.page != 2 {
		t.Fatalf("enter on Usage: page %d", a.page)
	}
}

// Settings start on the value in effect, say when nothing changed, and
// check numbers as they are typed.
func TestSettingsEditor(t *testing.T) {
	h := navHome(t, homeOpts{start: "settings"})
	s := h.subs["settings"].(*settingsSection)
	press(h, key("enter"))
	if s.edit == nil || s.edit.value() != "auto" {
		t.Fatalf("format starts on auto: %v", s.edit)
	}
	press(h, key("enter"))
	if s.edit != nil || !strings.Contains(statusText(h.View()), "No change") {
		t.Fatalf("enter without a change:\n%s", h.View())
	}
	press(h, key("down"), key("down"), key("enter"))
	if s.edit == nil || s.edit.value() != "4" {
		t.Fatalf("concurrency starts on 4: %v", s.edit)
	}
	press(h, typeText("x")...)
	if !strings.Contains(h.View(), "x is not a whole number") || s.edit.value() != "x" {
		t.Fatalf("no check while typing:\n%s", h.View())
	}
}

// A new stream with an ID in use offers to change that stream's URL.
func TestAddStreamWithATakenID(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	s := h.subs["streams"].(*streamsSection)
	press(h, key("down"), key("a"))
	press(h, typeText("https://radio.example/new.mp3")...)
	press(h, key("down"))
	press(h, key("ctrl+u"))
	press(h, typeText("1")...)
	// Enter in the last text field adds (the box below is optional).
	press(h, key("enter"))
	if v := h.View(); !strings.Contains(v, "Stream 1 exists already. Change its URL instead?") || !s.adding {
		t.Fatalf("taken ID:\n%s", v)
	}
	press(h, key("right"), key("enter"))
	pump(h, func() bool {
		return strings.Contains(statusText(h.View()), "Stream 1 now uses https://radio.example/new.mp3")
	})
}

func TestTruncMiddle(t *testing.T) {
	got := truncMiddle("https://radio.example.com/streams/live/high.mp3", 30)
	if !strings.HasPrefix(got, "https://radio.example") || !strings.HasSuffix(got, "high.mp3") || !strings.Contains(got, "…") || len([]rune(got)) != 30 {
		t.Fatalf("%q", got)
	}
}

// Enter on "Nothing matches." says so.
func TestPaletteNothingMatches(t *testing.T) {
	h := navHome(t, homeOpts{})
	press(h, ctrlK())
	press(h, typeText("zzzz")...)
	press(h, key("enter"))
	if !strings.Contains(statusText(h.View()), "No command matches; keep typing or esc") {
		t.Fatalf("no feedback:\n%s", h.View())
	}
}
