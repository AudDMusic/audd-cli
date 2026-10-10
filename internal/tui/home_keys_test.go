package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/media"
)

// changing are the commands that change something: on the account, the
// streams, the files, or the settings. Stray keys must never run them.
var changing = []string{"recognize", "listen", "streams add", "streams remove", "streams set-url",
	"streams callback set", "streams recorder start", "streams recorder stop", "config", "token rotate",
	"billing subscribe", "billing renew", "billing buy", "auth", "login", "cache", "jobs resume"}

func changed(f *fakeRun) []string {
	var out []string
	for _, c := range f.commands() {
		for _, p := range changing {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
			}
		}
	}
	return out
}

// formValues are the values of every form field, by section, form, and
// field, except the field the cursor is typing in.
func formValues(h *home) map[string]string {
	out := map[string]string{}
	add := func(where string, f *form, typing bool) {
		for _, x := range f.fields {
			if typing && x == f.current() && f.capturing() {
				continue
			}
			out[where+"/"+x.name] = x.value()
		}
	}
	r := recognizeOf(h)
	add("recognize", r.form, h.activeID() == "recognize" && h.focus == focusContent)
	st := h.subs["streams"].(*streamsSection)
	on := h.activeID() == "streams" && h.focus == focusContent
	add("streams-add", st.add, on)
	add("streams-callback", st.cbForm, on)
	add("streams-data", st.dataForm, on)
	ac := h.subs["account"].(*accountSection)
	add("account-buy", ac.buyIn, h.activeID() == "account" && h.focus == focusContent)
	return out
}

// Typing words anywhere does nothing it should not: no command that
// changes something runs, and no field outside the one being typed in
// changes.
func TestStrayKeys(t *testing.T) {
	const stray = "hello cache clear 12345 ezR"
	for i, id := range HomeSections {
		for _, level := range []string{"sidebar", "tab bar", "page"} {
			t.Run(id+"/"+level, func(t *testing.T) {
				useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"})
				f := &fakeRun{}
				h := sized(newTestHome(t, f, homeOpts{loggedIn: true}), 100, 30)
				drain(h, h.activate(), 0)
				h.cur = i
				drain(h, h.activate(), 0)
				switch level {
				case "tab bar":
					press(h, key("right"))
					if h.focus != focusTabs {
						t.Skip("no tab bar")
					}
				case "page":
					press(h, key("right"))
					if h.focus == focusTabs {
						press(h, key("down"))
					}
				}
				before := formValues(h)
				press(h, typeText(stray)...)
				if c := changed(f); len(c) > 0 {
					t.Fatalf("typing %q ran %q", stray, c)
				}
				after := formValues(h)
				for k, v := range before {
					if a, ok := after[k]; ok && a != v {
						t.Fatalf("%s changed from %q to %q", k, v, a)
					}
				}
			})
		}
	}
}

// Every list and picker with nothing in it takes every key and the
// wheel without trouble.
func TestEmptyLists(t *testing.T) {
	keysToTry := []string{"up", "down", "pgup", "pgdown", "home", "end", "enter", "left", "right", "tab"}
	try := func(h *home, what string) {
		t.Helper()
		for _, k := range keysToTry {
			var msg tea.KeyMsg
			switch k {
			case "pgup":
				msg = tea.KeyMsg{Type: tea.KeyPgUp}
			case "pgdown":
				msg = tea.KeyMsg{Type: tea.KeyPgDown}
			case "home":
				msg = tea.KeyMsg{Type: tea.KeyHome}
			case "end":
				msg = tea.KeyMsg{Type: tea.KeyEnd}
			default:
				msg = key(k)
			}
			press(h, msg)
			h.View()
		}
		for _, b := range []tea.MouseButton{tea.MouseButtonWheelDown, tea.MouseButtonWheelUp} {
			_, cmd := h.Update(tea.MouseMsg{X: sidebarW + 10, Y: 6, Button: b, Action: tea.MouseActionPress})
			drain(h, cmd, 0)
			h.View()
		}
	}
	// The palette and Help's Commands, filtered to nothing.
	h := navHome(t, homeOpts{})
	press(h, ctrlK())
	press(h, typeText("zzzz")...)
	try(h, "palette")
	h = navHome(t, homeOpts{start: "help"})
	press(h, key("]"), key("]"), key("down"))
	press(h, typeText("zzzz")...)
	try(h, "help commands")
	// Lists with no rows.
	for _, id := range []string{"recognitions", "streams", "now-playing"} {
		h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: id}), 100, 30)
		NewExplorerData = func(a *app.App) (ExplorerData, error) { return ExplorerData{}, nil }
		NewFeed = func(a *app.App) (Feed, error) { return &fakeFeed{}, nil }
		drain(h, h.activate(), 0)
		if h.focus == focusTabs {
			press(h, key("down"))
		}
		try(h, id)
	}
	// An empty folder in the file picker, a batch with no results.
	h = navHome(t, homeOpts{start: "recognize"})
	r := recognizeOf(h)
	r.form.get("input").setValue(t.TempDir() + "/")
	press(h, key("ctrl+o"))
	try(h, "file picker")
	r.browser = nil
	r.phase, r.batch, r.bs = "result", true, &batchState{selectable: true}
	r.panel.res = &runResult{}
	try(h, "batch")
}

// Enter never confirms a question; y does.
func TestEnterDoesNotConfirm(t *testing.T) {
	h := navHome(t, homeOpts{start: "settings"})
	press(h, key("d"))
	if h.confirm == nil {
		t.Fatal("d asks first")
	}
	press(h, key("enter"), key("enter"))
	if h.confirm == nil || !strings.Contains(statusText(h.View()), "Press y to confirm, or n to cancel") {
		t.Fatalf("enter confirmed:\n%s", h.View())
	}
	if !strings.Contains(h.View(), "[ Confirm ]") || !strings.Contains(crumbText(h.View()), "Settings › Confirm") {
		t.Fatalf("the question:\n%s", h.View())
	}
	press(h, key("n"))
	if h.confirm != nil || !strings.Contains(statusText(h.View()), "Kept token") {
		t.Fatalf("n cancels:\n%s", h.View())
	}
}

// Signed in with a token only, Account still offers every way in; the
// test token does not count as signing in; Settings says where the
// token comes from in words; the commands shown carry --token test.
func TestTestTokenAndSignIn(t *testing.T) {
	h := navHome(t, homeOpts{start: "account"})
	for _, want := range []string{"Paste an API token", "Try with the test token"} {
		if !strings.Contains(h.View(), want) {
			t.Fatalf("no %q:\n%s", want, h.View())
		}
	}
	drain(h, h.signedIn(true), 0)
	hs := h.subs["help"].(*helpSection)
	if hs.stepDone(0) {
		t.Fatal("the test token is not a sign-in")
	}
	if c := h.command(); !strings.HasSuffix(c, "--token test") {
		t.Fatalf("command with the test token: %q", c)
	}
	drain(h, h.navigate("settings"), 0)
	if !strings.Contains(h.View(), "test token for this session") || strings.Contains(h.View(), "--token flag") {
		t.Fatalf("token source:\n%s", h.View())
	}
}

// Recognize: enter on Source goes on; esc leaves the field, then the
// page; digits on a box say how jumps work.
func TestRecognizeFormKeys(t *testing.T) {
	h := navHome(t, homeOpts{start: "recognize"})
	s := recognizeOf(h)
	press(h, key("up"), key("enter"))
	if s.form.current().name != "input" {
		t.Fatalf("enter on Source goes to the next field: %s", s.form.current().name)
	}
	press(h, typeText("a.mp3")...)
	press(h, key("esc"))
	if h.focus != focusContent || s.form.capturing() || !strings.Contains(footerText(h.View()), "enter type in it") {
		t.Fatalf("esc leaves the field:\n%s", h.View())
	}
	press(h, typeText("z")...)
	if s.form.value("input") != "a.mp3" {
		t.Fatal("letters go nowhere once the field is left")
	}
	press(h, key("esc"))
	if h.focus != focusSidebar {
		t.Fatal("the next esc goes back")
	}
	press(h, key("right"), key("down"))
	press(h, key("3"))
	if h.activeID() != "recognize" || !strings.Contains(statusText(h.View()), "3 jumps to a section from the menu") {
		t.Fatalf("a digit on a box: %s\n%s", h.activeID(), h.View())
	}
}

// The palette's form starts on its first required field, or on Run;
// enter on a box ticks it; y copies the command.
func TestPaletteFormKeys(t *testing.T) {
	h := navHome(t, homeOpts{})
	drain(h, h.openPalette("streams add"), 0)
	if c := h.palette.cf.form.current(); c.name != "arg:0" {
		t.Fatalf("starts on %s", c.name)
	}
	drain(h, h.openPalette("streams report"), 0)
	if c := h.palette.cf.form.current(); c.kind != fButton {
		t.Fatalf("no required field: starts on %s", c.name)
	}
	press(h, key("y"))
	if !strings.Contains(statusText(h.View()), "Copied") {
		t.Fatalf("y copies:\n%s", h.View())
	}
}

// Settings: unset only what is not the default; arrows inside an edit
// say what to do.
func TestSettingsKeys(t *testing.T) {
	h := navHome(t, homeOpts{start: "settings"})
	s := h.subs["settings"].(*settingsSection)
	press(h, key("down"))
	if strings.Contains(footerText(h.View()), "unset") {
		t.Fatal("unset is offered for a default value")
	}
	press(h, key("d"))
	if h.confirm != nil {
		t.Fatal("d on a default value asks")
	}
	press(h, key("enter"), key("down"))
	if s.edit == nil || !strings.Contains(statusText(h.View()), "Enter saves, esc cancels") {
		t.Fatalf("↓ in an edit:\n%s", h.View())
	}
}

// The export's file name takes typing, and an existing file is not
// written over without asking.
func TestExportFileName(t *testing.T) {
	dir := t.TempDir()
	old := explorerExportDir
	explorerExportDir = dir
	t.Cleanup(func() { explorerExportDir = old })
	f := &fakeRun{}
	f.reply("streams export", "id\n1\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("]"), key("]"), key("]"), key("down"))
	s := h.subs["streams"].(*streamsSection)
	s.dataForm.get("kind").setValue("export")
	s.syncData()
	s.dataForm.focusName("file")
	press(h, key("ctrl+u"))
	press(h, typeText("plays.csv")...)
	if s.dataForm.value("file") != "plays.csv" || !strings.HasSuffix(h.command(), filepath.Join(dir, "plays.csv")) {
		t.Fatalf("file name: %q, command %s", s.dataForm.value("file"), h.command())
	}
	os.WriteFile(filepath.Join(dir, "plays.csv"), []byte("old"), 0o644)
	press(h, key("enter"))
	if h.confirm == nil || !strings.Contains(h.View(), "Replace plays.csv?") {
		t.Fatalf("no question before writing over:\n%s", h.View())
	}
	press(h, key("n"))
	if b, _ := os.ReadFile(filepath.Join(dir, "plays.csv")); string(b) != "old" || len(changed(f)) != 0 || strings.Contains(strings.Join(f.commands(), " "), "export") {
		t.Fatal("the file was written over")
	}
}

// e changes a stream's URL, starting from the URL it has, and asks with
// both URLs.
func TestEditStreamURL(t *testing.T) {
	api := &fakeStreamsAPI{}
	d := fakeExplorerData(api)
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "streams"}), 100, 30)
	NewExplorerData = func(a *app.App) (ExplorerData, error) { return d, nil }
	drain(h, h.activate(), 0)
	press(h, key("e"))
	if h.subs["streams"].(*streamsSection).pane.ex.purpose != inputNone {
		t.Fatal("e on the tab bar does nothing")
	}
	press(h, key("down"), key("e"))
	ex := h.subs["streams"].(*streamsSection).pane.ex
	if ex.purpose != inputSetURL || ex.input.Value() != "https://radio.example/a.mp3" {
		t.Fatalf("e starts from the URL: %q", ex.input.Value())
	}
	press(h, key("ctrl+u"))
	press(h, typeText("not a url")...)
	press(h, key("enter"))
	if h.confirm != nil || !strings.Contains(statusText(h.View()), "not a stream URL") {
		t.Fatalf("a bad URL:\n%s", h.View())
	}
	press(h, key("down"), key("up"), key("e"), key("ctrl+u"))
	press(h, typeText("https://radio.example/new.mp3")...)
	press(h, key("enter"))
	v := h.View()
	if h.confirm == nil || !strings.Contains(v, "Now: https://radio.example/a.mp3") || !strings.Contains(v, "New: https://radio.example/new.mp3") {
		t.Fatalf("the question:\n%s", v)
	}
}
