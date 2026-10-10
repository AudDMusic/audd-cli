package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/media"
)

// sidebarLit matches a sidebar item highlighted with the keys on the
// sidebar (no color: ">3 Streams").
var sidebarLit = regexp.MustCompile(`(?m)^>\d [A-Z]`)

// crumbText is the breadcrumb row of a wide frame, as text.
func crumbText(v string) string { return paneRow(v, 1) }

// paneRow is row y of a wide frame, the content pane part, as text.
func paneRow(v string, y int) string {
	lines := strings.Split(ansi.Strip(v), "\n")
	if y >= len(lines) {
		return ""
	}
	r := []rune(lines[y])
	if len(r) <= sidebarW+2 {
		return ""
	}
	return strings.TrimRight(string(r[sidebarW+2:]), " ")
}

// footerText is the key hints row of a frame.
func footerText(v string) string {
	lines := strings.Split(ansi.Strip(v), "\n")
	return lines[len(lines)-2]
}

// statusText is the status row of a frame (what just happened).
func statusText(v string) string {
	lines := strings.Split(ansi.Strip(v), "\n")
	return lines[len(lines)-3]
}

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

// focusShown checks that the frame shows where the keys are (the
// sidebar item, the tab, or the page: the divider marker and the
// breadcrumb), and that the footer is about arrows, not [ ].
func focusShown(t *testing.T, h *home, what string) {
	t.Helper()
	v := h.View()
	items, at := h.crumbs()
	crumb := crumbText(v)
	foot := footerText(v)
	if strings.Contains(foot, "[ ]") || strings.Contains(foot, "pages") {
		t.Fatalf("%s: the footer advertises [ ]: %q", what, foot)
	}
	switch h.focus {
	case focusSidebar:
		if !sidebarLit.MatchString(v) || at != 0 || !strings.HasPrefix(crumb, items[0].text) {
			t.Fatalf("%s: sidebar focus not shown (crumb %q):\n%s", what, crumb, v)
		}
		if !strings.Contains(foot, "↑/↓ move") || !strings.Contains(foot, "→ open") {
			t.Fatalf("%s: sidebar footer %q", what, foot)
		}
	case focusTabs:
		tab := items[1].text
		if at != 1 || !strings.HasSuffix(crumb, tab) || !strings.Contains(paneRow(v, 2), ">"+tab+"<") {
			t.Fatalf("%s: tab bar focus not shown (crumb %q):\n%s", what, crumb, v)
		}
		if !strings.HasPrefix(foot, "←/→ tabs  ↓ open  esc back") {
			t.Fatalf("%s: tab bar footer %q", what, foot)
		}
	default:
		var want []string
		for _, c := range items {
			want = append(want, c.text)
		}
		if at >= 0 && at != len(items)-1 || crumb != strings.Join(want, " › ") {
			t.Fatalf("%s: crumb %q (at %d)", what, crumb, at)
		}
		if !h.showSignin {
			// The frame's header, then the sidebar's blank first row.
			if r := []rune(strings.Split(ansi.Strip(v), "\n")[h.cur+2]); r[sidebarW] != '>' {
				t.Fatalf("%s: the divider does not mark the content:\n%s", what, v)
			}
		}
		if !strings.Contains(foot, "esc") {
			t.Fatalf("%s: the footer does not say how to go back: %q", what, foot)
		}
	}
}

type navState struct {
	focus  focusArea
	n      int
	id     string
	typing bool
	btn    bool
}

func stateOf(h *home) navState {
	items, _ := h.crumbs()
	return navState{h.focus, len(items), h.activeID(), h.focus == focusContent && h.active().capturing(), h.btnActive}
}

// escClimbs presses esc and checks that it went up exactly one level,
// and that the screen changed.
func escClimbs(t *testing.T, h *home, what string) {
	t.Helper()
	before, v0 := stateOf(h), h.View()
	tabbed := h.tabs() != nil
	press(h, key("esc"))
	after, v1 := stateOf(h), h.View()
	if v0 == v1 {
		t.Fatalf("%s: esc left the screen as it was:\n%s", what, v1)
	}
	var ok bool
	switch before.focus {
	case focusContent:
		ok = after.id == before.id && after.focus == focusContent && after.n == before.n-1 ||
			// Leaving the field being typed in, or the buttons.
			(before.typing && !after.typing || before.btn && !after.btn) && after.focus == focusContent && after.n == before.n ||
			after.id == before.id && tabbed && after.focus == focusTabs && after.n == before.n ||
			!tabbed && after.focus == focusSidebar
	case focusTabs:
		ok = after.focus == focusSidebar
	}
	if !ok {
		t.Fatalf("%s: esc went from %+v to %+v:\n%s", what, before, after, v1)
	}
	focusShown(t, h, what)
}

// climbOut presses esc until the sidebar, each press one level up.
func climbOut(t *testing.T, h *home, what string) {
	t.Helper()
	for i := 0; h.focus != focusSidebar; i++ {
		if i > 10 {
			t.Fatalf("%s: esc never reached the sidebar", what)
		}
		escClimbs(t, h, what)
	}
	if !sidebarLit.MatchString(h.View()) {
		t.Fatalf("%s: no sidebar highlight", what)
	}
}

func navHome(t *testing.T, o homeOpts) *home {
	t.Helper()
	useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"})
	h := sized(newTestHome(t, &fakeRun{}, o), 100, 30)
	drain(h, h.activate(), 0)
	return h
}

// Every section and tab: the sidebar opens it, the tab bar switches tabs
// and goes in and out of the page, ↑ at the top of a page goes back to
// the tab bar, and esc climbs one level at a time, visibly.
func TestHierarchy(t *testing.T) {
	for i, id := range HomeSections {
		t.Run(id, func(t *testing.T) {
			h := navHome(t, homeOpts{})
			h.cur = i
			drain(h, h.activate(), 0)
			title := homeTitles[i]
			focusShown(t, h, id+" sidebar")
			press(h, key("right"))
			tb := h.tabs()
			if tb == nil {
				if h.focus != focusContent {
					t.Fatalf("→ opens the page of a section without tabs: %v", h.focus)
				}
				focusShown(t, h, id+" page")
				if c := crumbText(h.View()); !strings.HasPrefix(c, title) {
					t.Fatalf("crumb %q", c)
				}
				climbOut(t, h, id)
				return
			}
			names := tb.tabNames()
			for ti, name := range names {
				what := id + "/" + name
				if h.focus != focusTabs || tb.tabIndex() != ti {
					t.Fatalf("%s: on the tab bar at %d, want %d (focus %v)", what, tb.tabIndex(), ti, h.focus)
				}
				focusShown(t, h, what+" tab bar")
				if c := crumbText(h.View()); c != title+" › "+name {
					t.Fatalf("%s: crumb %q", what, c)
				}
				press(h, key("down"))
				if h.focus != focusContent {
					t.Fatalf("%s: ↓ goes into the page", what)
				}
				focusShown(t, h, what+" page")
				if c := crumbText(h.View()); !strings.HasPrefix(c, title+" › "+name) {
					t.Fatalf("%s: page crumb %q", what, c)
				}
				press(h, key("up"))
				if h.focus != focusTabs {
					t.Fatalf("%s: ↑ at the top of the page goes to the tab bar", what)
				}
				press(h, key("enter"))
				if h.focus != focusContent {
					t.Fatalf("%s: enter goes into the page", what)
				}
				for i := 0; i < 3 && h.focus == focusContent; i++ {
					escClimbs(t, h, what) // a field first, then the page
				}
				if h.focus != focusTabs {
					t.Fatalf("%s: esc from the page goes to the tab bar", what)
				}
				v0 := h.View()
				press(h, key("right"))
				if ti < len(names)-1 {
					if tb.tabIndex() != ti+1 || h.focus != focusTabs {
						t.Fatalf("%s: → opens the next tab", what)
					}
				} else if tb.tabIndex() != ti || h.View() == v0 || !strings.Contains(statusText(h.View()), "last tab") {
					t.Fatalf("%s: → on the last tab stays and says so:\n%s", what, h.View())
				}
			}
			for ti := len(names) - 1; ti > 0; ti-- {
				press(h, key("left"))
				if tb.tabIndex() != ti-1 || h.focus != focusTabs {
					t.Fatalf("← goes to the previous tab: %d", tb.tabIndex())
				}
			}
			press(h, key("left"))
			if h.focus != focusSidebar {
				t.Fatal("← on the first tab goes back to the sidebar")
			}
			focusShown(t, h, id+" back on the sidebar")
		})
	}
}

// Deeper levels: each esc closes one thing, visibly, until the sidebar.
func TestEscClimbsFromDeepLevels(t *testing.T) {
	cases := []struct {
		name  string
		opts  homeOpts
		keys  []string
		crumb string // the breadcrumb at the deepest level
	}{
		{"help command details", homeOpts{start: "help"}, []string{"]", "]", "down", "down", "right"}, "Help › Commands › audd docs"},
		{"help guide search", homeOpts{start: "help"}, []string{"]", "down", "/"}, "Help › Guide › Search"},
		{"streams add", homeOpts{start: "streams"}, []string{"down", "a"}, "Streams › List › Add a stream"},
		{"stream page", homeOpts{start: "streams"}, []string{"down", "right"}, "Streams › List › Stream 1"},
		{"stream plays", homeOpts{start: "streams"}, []string{"down", "right", "tab", "enter"}, "Streams › List › Stream 1 · recent plays"},
		{"recognitions details", homeOpts{start: "recognitions"}, []string{"down", "right"}, "Recognitions › Recent › Details"},
		{"recognitions job", homeOpts{start: "recognitions"}, []string{"]", "down", "right"}, "Recognitions › Jobs › Job k3x9"},
		{"recognitions job files", homeOpts{start: "recognitions"}, []string{"]", "down", "right", "tab", "enter"}, "Recognitions › Jobs › Job k3x9"},
		{"settings edit", homeOpts{start: "settings"}, []string{"right"}, "Settings › format"},
		{"recognize file picker", homeOpts{start: "recognize"}, []string{"ctrl+o"}, "Recognize › File › Pick a file"},
		{"recognize microphone", homeOpts{start: "listen"}, nil, "Recognize › Microphone"},
		{"now playing history", homeOpts{start: "now-playing"}, []string{"enter", "tab", "enter"}, "Now playing › Stream 1 › History"},
		{"sign-in paste", homeOpts{signedOut: true}, []string{"down", "enter"}, "Sign in › Paste an API token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := navHome(t, c.opts)
			for _, k := range c.keys {
				press(h, key(k))
			}
			if got := crumbText(h.View()); got != c.crumb {
				t.Fatalf("crumb %q, want %q:\n%s", got, c.crumb, h.View())
			}
			focusShown(t, h, c.name)
			climbOut(t, h, c.name)
		})
	}
}

// The mouse: a click on a tab opens it with the keys on the tab bar, a
// click on the page puts the keys there, and a click on the section's
// name in the breadcrumb goes back to the sidebar.
func TestMouseLevels(t *testing.T) {
	for _, id := range []string{"streams", "recognitions", "account", "help"} {
		h := navHome(t, homeOpts{})
		for i, s := range HomeSections {
			if s == id {
				h.cur = i
			}
		}
		drain(h, h.activate(), 0)
		tb := h.tabs()
		second := tb.tabNames()[1]
		x, y := at(t, h.View(), " "+second+" ")
		_, cmd := h.Update(tea.MouseMsg{X: x + 2, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		drain(h, cmd, 0)
		if h.focus != focusTabs || tb.tabIndex() != 1 {
			t.Fatalf("%s: click on a tab: focus %v tab %d", id, h.focus, tb.tabIndex())
		}
		focusShown(t, h, id+" clicked tab")
		_, cmd = h.Update(tea.MouseMsg{X: sidebarW + 30, Y: 4 + h.headRows, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		drain(h, cmd, 0)
		if h.focus != focusContent {
			t.Fatalf("%s: click on the page: focus %v", id, h.focus)
		}
		click(t, h, homeTitles[h.cur]+" › ")
		if h.focus != focusSidebar {
			t.Fatalf("%s: click on the section in the breadcrumb: focus %v", id, h.focus)
		}
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

// Every jump comes back to where it started (at the level it was left
// from), then to the sidebar.
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
			h.focus = focusContent
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
			if h.activeID() != j.from || h.focus == focusSidebar {
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
	// Help opens on its tab bar; enter goes into the steps, enter again
	// opens the first.
	press(h, key("enter"), key("enter"))
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
	if h.focus != focusTabs || !strings.Contains(h.View(), ">Getting started<") {
		t.Fatalf("then esc goes to the tab bar:\n%s", h.View())
	}
	press(h, key("esc"))
	if h.focus != focusSidebar || !strings.Contains(h.View(), ">7 Help") {
		t.Fatalf("then esc goes to the sidebar:\n%s", h.View())
	}
}

// [ and ] switch tabs from any level, without wrapping around.
func TestBracketsSwitchTabs(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	s := h.subs["streams"].(*streamsSection)
	press(h, key("a"))
	if s.adding {
		t.Fatal("a on the tab bar does nothing: actions work on the page")
	}
	press(h, key("down"), key("a"))
	if !s.adding {
		t.Fatal("a opens the add form")
	}
	press(h, key("esc"), key("esc"), key("]"))
	if s.page != 1 || h.focus != focusTabs {
		t.Fatalf("] from the page: page %d focus %v", s.page, h.focus)
	}
	press(h, key("["), key("["))
	if s.page != 0 || !strings.Contains(statusText(h.View()), "first tab") {
		t.Fatalf("[ stops at the first tab:\n%s", h.View())
	}
	// In the Commands filter, [ and ] are typed once it has text.
	h = navHome(t, homeOpts{start: "help"})
	hs := h.subs["help"].(*helpSection)
	// Typing on the Commands tab bar goes nowhere: letters only go into
	// a field that has the keys.
	press(h, key("]"), key("]"))
	press(h, typeText("st")...)
	if h.focus != focusTabs || hs.picker.filter.Value() != "" || !strings.Contains(statusText(h.View()), "t does nothing here") {
		t.Fatalf("typing on the tab bar: focus %v filter %q", h.focus, hs.picker.filter.Value())
	}
	press(h, key("down"))
	press(h, typeText("st")...)
	press(h, key("]"))
	if hs.page != 2 || hs.picker.filter.Value() != "st]" {
		t.Fatalf("] typed in the filter: page %d filter %q", hs.page, hs.picker.filter.Value())
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
	// A tab: the keys go to the tab bar.
	click(t, h, "Recorder")
	if st := h.subs["streams"].(*streamsSection); st.page != 2 || h.focus != focusTabs {
		t.Fatalf("click on a tab: %d %v", st.page, h.focus)
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
	if s.mic() || s.form.current().name != "source" {
		t.Fatal("click on a choice selects it without changing it")
	}
	x, y := at(t, h.View(), "folder ›")
	_, cmd := h.Update(tea.MouseMsg{X: x + len("folder "), Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	drain(h, cmd, 0)
	if !s.mic() {
		t.Fatal("click on › changes the choice")
	}
	x, y = at(t, h.View(), "‹ Microphone")
	_, cmd = h.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	drain(h, cmd, 0)
	if s.mic() {
		t.Fatal("click on ‹ changes the choice back")
	}
	click(t, h, "Show the plan")
	f.waitFor(t, "recognize song.mp3 --return spotify --dry-run")

	// List rows: the first click selects, the second opens.
	click(t, h, "4 Recognitions")
	drain(h, h.activate(), 0)
	click(t, h, "No match")
	ex := h.subs["recognitions"].(*historySection).pane.ex
	if l := ex.top(); l.cursor != 1 || l.detail || h.focus != focusContent {
		t.Fatalf("click selects the row: cursor %d", l.cursor)
	}
	click(t, h, "No match")
	if !ex.top().detail {
		t.Fatal("a second click opens the row")
	}
	press(h, key("esc"))
	click(t, h, "Jobs")
	if ex.tab != tabJobs || h.focus != focusTabs {
		t.Fatal("click on the Jobs tab")
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
	_, cmd = h.Update(tea.MouseMsg{X: 40, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	drain(h, cmd, 0)
	if hs.page != 1 || hs.scroll.off != 3 {
		t.Fatalf("the wheel scrolls the guide: page %d off %d", hs.page, hs.scroll.off)
	}

	// Settings rows.
	click(t, h, "6 Settings")
	click(t, h, "max_requests")
	set := h.subs["settings"].(*settingsSection)
	if set.selected().key != "max_requests" || set.edit != nil {
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
	if s.choice != 1 || s.step != "paste" {
		t.Fatal("a click opens the choice")
	}
	press(h, typeText("abc")...)
	if s.paste.value("token") != "abc" {
		t.Fatalf("typing goes into the token field: %q", s.paste.value("token"))
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
	h.flash = ""
	v = h.View()
	if !strings.Contains(v, ">6 Settings") || strings.Contains(v, "     >") || strings.Contains(v, "esc back") {
		t.Fatalf("sidebar focus:\n%s", v)
	}
}

// The first-run tip states the model; esc or a click hides it for good,
// remembered in the profile.
func TestFirstRunTip(t *testing.T) {
	h := navHome(t, homeOpts{tip: true})
	if !strings.Contains(h.View(), tipText) {
		t.Fatalf("no tip:\n%s", h.View())
	}
	if n := strings.Count(h.View(), "\n") + 1; n != 30 {
		t.Fatalf("%d lines with the tip", n)
	}
	press(h, key("right"), key("esc"))
	if strings.Contains(h.View(), tipText) {
		t.Fatal("esc hides the tip")
	}
	cfg, err := config.LoadFrom(h.a.Cfg.Path)
	if err != nil || !cfg.Profile("default").UITipSeen {
		t.Fatalf("the tip is remembered in the profile: %v", err)
	}
	h = navHome(t, homeOpts{tip: true})
	click(t, h, "Menu on the left")
	if h.tip {
		t.Fatal("a click hides the tip")
	}
	if h = navHome(t, homeOpts{}); strings.Contains(h.View(), tipText) {
		t.Fatal("no tip once it was seen")
	}
}

// Typed text goes into the field: q there is a letter, not quit.
func TestQInTextFieldTypes(t *testing.T) {
	h := navHome(t, homeOpts{start: "recognize"})
	_, cmd := h.Update(key("q"))
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("q in a text field quits")
		}
	}
	if v := recognizeOf(h).form.value("input"); v != "q" {
		t.Fatalf("q is typed: %q", v)
	}
}

// A key that does nothing says so instead of nothing happening.
func TestUnknownKeyFlash(t *testing.T) {
	for _, c := range []struct {
		start string
		focus focusArea
	}{{"settings", focusContent}, {"streams", focusTabs}, {"", focusSidebar}} {
		h := navHome(t, homeOpts{start: c.start})
		if h.focus != c.focus {
			t.Fatalf("%s: focus %v", c.start, h.focus)
		}
		v0 := h.View()
		press(h, key("z"))
		v := h.View()
		if v == v0 || !strings.Contains(statusText(v), "z does nothing here · ? shows the keys") || strings.Contains(footerText(v), "does nothing") {
			t.Fatalf("%s: no feedback for z:\n%s", c.start, v)
		}
	}
}

// Leaving a form and coming back keeps what was typed.
func TestFormKeptAfterLeaving(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	s := h.subs["streams"].(*streamsSection)
	press(h, key("down"), key("a"))
	press(h, typeText("https://radio.example/1")...)
	for i := 0; i < 5 && h.focus != focusSidebar; i++ {
		press(h, key("esc"))
	}
	if h.focus != focusSidebar {
		t.Fatalf("esc reaches the sidebar: %v", h.focus)
	}
	press(h, key("right"), key("down"), key("a"))
	if got := s.add.value("url"); got != "https://radio.example/1" || !strings.Contains(h.View(), "https://radio.example/1") {
		t.Fatalf("the add form lost its URL: %q", got)
	}

	h = navHome(t, homeOpts{start: "recognize"})
	press(h, typeText("song.mp3")...)
	press(h, key("esc"), key("esc"), key("2"), key("1"))
	if got := recognizeOf(h).form.value("input"); got != "song.mp3" {
		t.Fatalf("Recognize lost its input: %q", got)
	}

	h = navHome(t, homeOpts{})
	drain(h, h.openPalette("streams add"), 0)
	press(h, typeText("https://radio.example/2")...)
	for i := 0; i < 4 && h.paletteO; i++ {
		press(h, key("esc"))
	}
	if h.paletteO {
		t.Fatal("esc closes the palette")
	}
	drain(h, h.openPalette("streams add"), 0)
	if got := h.palette.cf.form.value("arg:0"); got != "https://radio.example/2" {
		t.Fatalf("the palette form lost its value: %q", got)
	}
}

// Enter in a filled Input recognizes it: the plan first, then the run.
func TestEnterInInputRuns(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 1 file, 1 request.\n", "", 0)
	f.reply("recognize song.mp3", warriorsDoc, "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, typeText("song.mp3")...)
	press(h, key("enter"))
	f.waitFor(t, "recognize song.mp3 --dry-run")
	f.waitFor(t, "recognize song.mp3")
	waitView(t, h, "Warriors")
}

// A disabled button shows why, and pressing it says so.
func TestDisabledButtonSaysWhy(t *testing.T) {
	f := &fakeRun{}
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	s.form.focusName("recognize")
	if v := h.View(); !strings.Contains(v, "Enter a file, URL, or folder first") {
		t.Fatalf("the disabled button gives no reason:\n%s", v)
	}
	press(h, key("enter"))
	// Pressing it puts the cursor on the field to fill in, with why.
	if len(f.commands()) != 0 || s.form.current().name != "input" || !strings.Contains(h.View(), "Enter a file, URL, or folder (ctrl+o picks a file)") {
		t.Fatalf("pressing it ran %q:\n%s", f.commands(), h.View())
	}
}

// q (and ctrl+c) while a recognition runs asks first.
func TestQuitAsksWhileRunning(t *testing.T) {
	release := make(chan struct{})
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 1 file, 1 request.\n", "", 0)
	f.on("recognize song.mp3 --format", func(args []string, io RunIO) int {
		<-release
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	defer func() {
		// Let the run finish before the test's fakes are put back.
		close(release)
		drain(h, h.runs.wait(h.last), 0)
		h.waitRuns(5 * time.Second)
	}()
	press(h, typeText("song.mp3")...)
	press(h, key("enter"))
	deadline := time.Now().Add(3 * time.Second)
	for h.runningCommand() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.runningCommand() == "" {
		t.Fatalf("the recognition did not start: %q", f.commands())
	}
	press(h, key("esc"))
	_, cmd := h.Update(key("q"))
	if cmd != nil || !h.quitAsk || !strings.Contains(h.View(), "Quit audd?") || !strings.Contains(h.View(), "audd recognize song.mp3") {
		t.Fatalf("q asks while a run is in progress:\n%s", h.View())
	}
	press(h, key("n"))
	if h.quitAsk || !strings.Contains(h.View(), "Still running") {
		t.Fatalf("any other key keeps it running:\n%s", h.View())
	}
	_, cmd = h.Update(key("ctrl+c"))
	if cmd != nil || !h.quitAsk {
		t.Fatal("ctrl+c asks too")
	}
	_, cmd = h.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c again quits")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c again quits")
	}
}
