package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/AudDMusic/audd-cli/internal/media"
)

// sidebarLit matches a sidebar item highlighted with the keys on the
// sidebar (no color: ">3 Streams").
var sidebarLit = regexp.MustCompile(`(?m)^>\d [A-Z]`)

// backOut presses k until the keys are on the sidebar (at most n times)
// and checks that the sidebar shows it.
func backOut(t *testing.T, h *home, k string, n int, what string) {
	t.Helper()
	for i := 0; i < n && h.focus != focusSidebar; i++ {
		press(h, key(k))
	}
	v := h.View()
	if h.focus != focusSidebar || h.showSignin || h.paletteO {
		t.Fatalf("%s: %d × %s did not reach the sidebar (on %s):\n%s", what, n, k, h.activeID(), v)
	}
	if !sidebarLit.MatchString(v) {
		t.Fatalf("%s: no sidebar item is highlighted:\n%s", what, v)
	}
	if strings.Contains(v, "\x1b[733") {
		t.Fatalf("%s: click marks left in the frame", what)
	}
}

func TestEscAndLeftReachTheSidebar(t *testing.T) {
	useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"})
	for _, k := range []string{"esc", "left"} {
		for _, id := range append([]string{"listen"}, HomeSections...) {
			h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: id}), 100, 30)
			drain(h, h.activate(), 0)
			backOut(t, h, k, 10, id)
		}
		// Signed out, the first screen is the sign-in.
		h := sized(newTestHome(t, &fakeRun{}, homeOpts{signedOut: true}), 100, 30)
		if !h.showSignin {
			t.Fatal("signed out starts on the sign-in")
		}
		backOut(t, h, k, 10, "signin")
		if h.cur != 0 || !strings.Contains(h.View(), ">1 Recognize") {
			t.Fatalf("leaving the sign-in highlights Recognize:\n%s", h.View())
		}
	}
}

// Every jump comes back to where it started, then to the sidebar.
func TestJumpsGoBackToTheirOrigin(t *testing.T) {
	useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"})
	type jump struct {
		name, from string
		do         func(h *home) tea.Cmd
	}
	var jumps []jump
	for i := range gettingStarted[:4] {
		jumps = append(jumps, jump{gettingStarted[i].title, "help", func(h *home) tea.Cmd {
			s := h.subs["help"].(*helpSection)
			s.setPage(0)
			s.stepCur = i
			return h.Update2(key("enter"))
		}})
	}
	jumps = append(jumps,
		jump{"hint audd login", "recognize", func(h *home) tea.Cmd { return h.openPaletteLine("audd login") }},
		jump{"palette browse", "settings", func(h *home) tea.Cmd { return h.openPalette("browse") }},
		jump{"palette listen", "streams", func(h *home) tea.Cmd { return h.openPalette("listen") }},
		jump{"palette now-playing", "help", func(h *home) tea.Cmd { return h.openPalette("now-playing") }},
	)
	for _, k := range []string{"esc", "left"} {
		for _, j := range jumps {
			h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: j.from}), 100, 30)
			drain(h, h.activate(), 0)
			drain(h, j.do(h), 0)
			if h.activeID() == j.from || h.paletteO {
				t.Fatalf("%s: did not jump (on %s, palette %v)", j.name, h.activeID(), h.paletteO)
			}
			// Left first moves through text typed in a field.
			for i := 0; i < 40 && h.activeID() != j.from; i++ {
				press(h, key(k))
			}
			if h.activeID() != j.from || h.focus != focusContent {
				t.Fatalf("%s %s: back on %s, focus %v; want %s", j.name, k, h.activeID(), h.focus, j.from)
			}
			backOut(t, h, k, 10, j.name)
		}
	}
}

// Update2 is Update for a key, returning only the command.
func (h *home) Update2(k tea.KeyMsg) tea.Cmd {
	_, cmd := h.Update(k)
	return cmd
}

func TestHelpSignInEscReturnsToHelp(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "help"}), 80, 24)
	drain(h, h.activate(), 0)
	press(h, key("enter"))
	if !h.showSignin || !strings.Contains(h.View(), "Welcome to AudD") {
		t.Fatalf("the Sign in step opens the sign-in:\n%s", h.View())
	}
	press(h, key("esc"))
	v := h.View()
	if h.showSignin || h.activeID() != "help" || h.focus != focusContent {
		t.Fatalf("esc goes back to Help (on %s):\n%s", h.activeID(), v)
	}
	if !strings.Contains(v, "[Getting started]") || !strings.Contains(v, "› 1. Sign in") || !strings.Contains(v, "*7 Help") {
		t.Fatalf("Help, with the Sign in step selected:\n%s", v)
	}
	press(h, key("esc"))
	if h.focus != focusSidebar || !strings.Contains(h.View(), ">7 Help") {
		t.Fatalf("then esc goes to the sidebar:\n%s", h.View())
	}
}

func TestArrowsWalkPages(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "help"}), 100, 30)
	drain(h, h.activate(), 0)
	s := h.subs["help"].(*helpSection)
	press(h, key("right"), key("right"))
	if s.page != 2 || s.details {
		t.Fatalf("right walks to Commands: page %d", s.page)
	}
	press(h, key("right"))
	if !s.details {
		t.Fatal("right on Commands opens the details")
	}
	press(h, key("left"))
	if s.details || s.page != 2 {
		t.Fatal("left closes the details")
	}
	s.setPage(5)
	press(h, key("right"))
	if s.page != 5 {
		t.Fatal("right stops at the last page")
	}
	for want := 4; want >= 0; want-- {
		press(h, key("left"))
		if s.page != want || h.focus != focusContent {
			t.Fatalf("left: page %d, want %d", s.page, want)
		}
	}
	press(h, key("left"))
	if h.focus != focusSidebar {
		t.Fatal("left on the first page goes back")
	}

	h = sized(newTestHome(t, &fakeRun{}, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	st := h.subs["streams"].(*streamsSection)
	for want := 1; want <= 3; want++ {
		press(h, key("right"))
		if st.page != want {
			t.Fatalf("streams right: page %d, want %d", st.page, want)
		}
	}
	press(h, key("right"))
	if st.page != 3 {
		t.Fatal("streams: right stops at the last page")
	}
	// In a page's form, left at its start leaves the form first.
	press(h, key("enter"))
	if !st.inForm {
		t.Fatal("enter goes into the form")
	}
	press(h, key("left"))
	if st.inForm || st.page != 3 {
		t.Fatalf("left on the first choice leaves the form: inForm %v page %d", st.inForm, st.page)
	}
	for want := 2; want >= 0; want-- {
		press(h, key("left"))
		if st.page != want {
			t.Fatalf("streams left: page %d, want %d", st.page, want)
		}
	}
	press(h, key("left"))
	if h.focus != focusSidebar {
		t.Fatal("streams: left on the first page goes back")
	}

	for _, loggedIn := range []bool{false, true} {
		h = sized(newTestHome(t, &fakeRun{}, homeOpts{start: "account", loggedIn: loggedIn}), 100, 30)
		drain(h, h.activate(), 0)
		ac := h.subs["account"].(*accountSection)
		for want := 1; want < len(accountPages); want++ {
			press(h, key("right"))
			if ac.page != want {
				t.Fatalf("account right: page %d, want %d", ac.page, want)
			}
		}
		for want := len(accountPages) - 2; want >= 0; want-- {
			press(h, key("left"))
			if ac.page != want {
				t.Fatalf("account left: page %d, want %d", ac.page, want)
			}
		}
		press(h, key("left"))
		if h.focus != focusSidebar {
			t.Fatal("account: left on the first page goes back")
		}
	}
}

// at is the screen cell where text starts in v.
func at(t *testing.T, v, text string) (int, int) {
	t.Helper()
	for y, l := range strings.Split(v, "\n") {
		plain := ansi.Strip(l)
		if i := strings.Index(plain, text); i >= 0 {
			return ansi.StringWidth(plain[:i]), y
		}
	}
	t.Fatalf("no %q on screen:\n%s", text, v)
	return 0, 0
}

// click draws h and clicks where text is.
func click(t *testing.T, h *home, text string) {
	t.Helper()
	x, y := at(t, h.View(), text)
	_, cmd := h.Update(tea.MouseMsg{X: x + 1, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	drain(h, cmd, 0)
}

func TestMouse(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --return spotify --dry-run", "Plan: 1 file, 1 request.\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)

	// Sidebar items.
	click(t, h, "3 Streams")
	if h.activeID() != "streams" || h.focus != focusSidebar {
		t.Fatalf("click on a sidebar item: %s %v", h.activeID(), h.focus)
	}
	// A page of the strip.
	click(t, h, "Recorder")
	if st := h.subs["streams"].(*streamsSection); st.page != 2 || h.focus != focusContent {
		t.Fatalf("click on a page: %d %v", st.page, h.focus)
	}

	// Form fields: a checkbox, the Source choice, a button.
	click(t, h, "1 Recognize")
	s := recognizeOf(h)
	s.form.get("input").setValue("song.mp3")
	click(t, h, "Add Spotify data")
	if !s.form.get("return:spotify").on || h.focus != focusContent {
		t.Fatal("click ticks a box")
	}
	click(t, h, "File, URL, or folder")
	if !s.mic() {
		t.Fatal("click on Source switches it")
	}
	click(t, h, "Microphone")
	click(t, h, "Show the plan")
	f.waitFor(t, "recognize song.mp3 --return spotify --dry-run")

	// List rows: the first click selects, the second opens.
	click(t, h, "4 History")
	drain(h, h.activate(), 0)
	click(t, h, "No match")
	ex := h.subs["history"].(*historySection).pane.ex
	if l := ex.top(); l.cursor != 1 || l.detail {
		t.Fatalf("click selects the row: cursor %d", l.cursor)
	}
	click(t, h, "No match")
	if !ex.top().detail {
		t.Fatal("a second click opens the row")
	}
	press(h, key("esc"))
	click(t, h, "Jobs")
	if ex.tab != tabJobs {
		t.Fatal("click on an explorer tab")
	}

	// Help: a tab, a Commands row, and the wheel on the guide.
	click(t, h, "7 Help")
	click(t, h, "Commands")
	hs := h.subs["help"].(*helpSection)
	if hs.page != 2 {
		t.Fatalf("help page %d", hs.page)
	}
	second := hs.picker.matches()[1].title
	click(t, h, second)
	if hs.picker.cursor != 1 || hs.details {
		t.Fatal("click selects a command")
	}
	click(t, h, second)
	if !hs.details {
		t.Fatal("a second click shows the command's details")
	}
	click(t, h, "Guide")
	h.View()
	_, cmd := h.Update(tea.MouseMsg{X: 40, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	drain(h, cmd, 0)
	if hs.page != 1 || hs.scroll.off != 3 {
		t.Fatalf("the wheel scrolls the guide: page %d off %d", hs.page, hs.scroll.off)
	}

	// Settings rows.
	click(t, h, "6 Settings")
	click(t, h, "format")
	set := h.subs["settings"].(*settingsSection)
	if set.selected().key != "format" || set.edit != nil {
		t.Fatal("click selects a setting")
	}

	// The palette list.
	press(h, ctrlK())
	click(t, h, "streams add")
	click(t, h, "streams add")
	if h.palette.phase != "form" {
		t.Fatalf("two clicks open a command: %s", h.palette.phase)
	}
}

func TestMouseSignIn(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{signedOut: true}), 100, 30)
	click(t, h, "Paste an API token")
	s := h.subs["signin"].(*signinSection)
	if s.choice != 1 || s.step != "" {
		t.Fatal("click selects a choice")
	}
	click(t, h, "Paste an API token")
	if s.step != "paste" {
		t.Fatal("a second click goes")
	}
}

func TestNoMouseVariable(t *testing.T) {
	t.Setenv("AUDD_NO_MOUSE", "")
	if !mouseEnabled() {
		t.Fatal("the mouse is on by default")
	}
	t.Setenv("AUDD_NO_MOUSE", "1")
	if mouseEnabled() {
		t.Fatal("AUDD_NO_MOUSE=1 turns it off")
	}
}

// The focused pane shows: the rule next to the content, the highlighted
// sidebar item otherwise, and the footer says how to go back.
func TestFocusIsVisible(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "settings"}), 100, 30)
	drain(h, h.activate(), 0)
	v := h.View()
	if !strings.Contains(v, padRight("*6 Settings", sidebarW)+">") || !strings.Contains(v, "←/esc back") {
		t.Fatalf("content focus:\n%s", v)
	}
	press(h, key("esc"))
	v = h.View()
	if !strings.Contains(v, ">6 Settings") || strings.Contains(v, "     >") || strings.Contains(v, "esc back") {
		t.Fatalf("sidebar focus:\n%s", v)
	}
}
