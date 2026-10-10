package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/AudDMusic/audd-cli/internal/output"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Layout of interactive mode:
//
//	header (1 row, 2 when it does not fit)
//	sidebar │ content        or, under 80 columns:  tab strip / content
//	status (1 row): what just happened, or the first-run tip
//	key hints (1 row)
//	$ command (1 row)
//
// The content pane starts with a breadcrumb and, for sections with
// tabs, the tab bar; the section's page is under them.
const (
	sidebarW    = 17 // sidebar width, without the rule
	narrowWidth = 80
)

func (h *home) narrow() bool { return h.w < narrowWidth }

// paneSize is the size of the content pane.
func (h *home) paneSize() (int, int) {
	rows := h.h - 3 - len(h.headerLines())
	if h.narrow() {
		rows--
	}
	if h.narrow() {
		return max(1, h.w), max(1, rows)
	}
	return max(1, h.w-sidebarW-2), max(1, rows)
}

// contentSize is the size of the page: the content pane under the
// breadcrumb and the tab bar.
func (h *home) contentSize() (int, int) {
	w, ht := h.paneSize()
	return w, max(1, ht-h.headRowsNow())
}

func (h *home) View() string {
	if h.w <= 0 || h.h <= 0 {
		return ""
	}
	h.arts.beginView()
	h.zoneFns = nil
	cw, ch := h.paneSize()
	head := h.crumbLine(cw)
	if t := h.tabs(); t != nil && !h.overlay() {
		head += "\n" + h.tabBar(t, cw)
	}
	h.headRows = h.headRowsNow()
	bh := max(1, ch-h.headRows)
	var body string
	switch {
	case h.ask != nil:
		body = h.askView(cw, bh)
	case h.confirm != nil:
		body = h.confirmView(cw)
	case h.quitAsk:
		body = h.quitView(cw)
	case h.keysO:
		h.keysScroll.lines = strings.Split(h.keysView(cw), "\n")
		body = h.keysScroll.view(cw, bh)
	case h.paletteO:
		body = h.palette.view(cw, bh)
	default:
		body = h.active().view(cw, bh)
	}
	body = fit(head+"\n"+fit(body, cw, bh), cw, ch)
	page := strings.Join(h.headerLines(), "\n")
	if h.narrow() {
		page += "\n" + h.tabStrip() + "\n" + body
	} else {
		side := strings.Split(h.sidebar(ch), "\n")
		lines := strings.Split(body, "\n")
		var b strings.Builder
		for i := 0; i < ch; i++ {
			b.WriteString(padRight(side[i], sidebarW) + h.rule(i) + " " + lines[i])
			if i < ch-1 {
				b.WriteString("\n")
			}
		}
		page += "\n" + b.String()
	}
	page += "\n" + h.statusLine() + "\n" + h.hintsLine() + "\n" + h.commandLine()
	page = h.scanZones(page)
	page = h.arts.finishView(page, h.w)
	if h.osc != "" {
		page = h.osc + page
	}
	return page
}

// headerLines is the header: on one row, or on two when it does not
// fit (the account line goes to the second).
func (h *home) headerLines() []string {
	parts := h.headerParts()
	sep := h.st.Dim.Render("  ·  ")
	if one := strings.Join(parts, sep); ansiWidth(one) <= h.w || len(parts) < 3 {
		return []string{truncate(one, h.w)}
	}
	return []string{truncate(strings.Join(parts[:2], sep), h.w), truncate("  "+strings.Join(parts[2:], sep), h.w)}
}

func (h *home) headerParts() []string {
	mark := h.st.Bold.Render("AudD")
	if h.color {
		mark = h.st.Bold.Inherit(h.st.Accent).Render("AudD")
	}
	parts := []string{mark}
	profile := "default"
	if h.a.Profile != nil {
		profile = h.a.Profile.Name
	}
	parts = append(parts, "profile "+profile)
	switch {
	case h.testToken:
		parts = append(parts, h.st.Warn.Render("test token: 10 requests a day, standard endpoint only"))
	case h.signedOut:
		parts = append(parts, h.st.Dim.Render("signed out"))
	case h.hdrLoaded && h.hdr.err == nil:
		acct := h.hdr.email
		if h.hdr.plan != "" {
			acct += " · " + h.hdr.plan
		}
		if acct != "" {
			parts = append(parts, acct)
		}
		if h.hdr.known {
			parts = append(parts, fmtInt(h.hdr.remaining)+" requests left")
		}
	}
	return parts
}

// statusLine says what just happened (until the next key), or shows
// the first-run tip; a click on the tip hides it.
func (h *home) statusLine() string {
	if h.flash != "" {
		return truncate(h.flash, h.w)
	}
	if !h.tip {
		return ""
	}
	t := truncate(tipText, h.w)
	if h.color {
		t = h.st.Accent.Render(t)
	}
	return h.mark(t, func() tea.Cmd {
		h.seenTip()
		return nil
	})
}

// contentFocused reports whether keys go to the content pane (or an
// overlay on it).
func (h *home) contentFocused() bool {
	return h.focus != focusSidebar || h.overlay()
}

// rule is row i of the line between the sidebar and the content. It
// shows which pane has the keys: in the accent color while the content
// has them, and without color with a > next to the open section.
func (h *home) rule(i int) string {
	if !h.contentFocused() {
		return h.st.Dim.Render("│")
	}
	if h.color {
		return h.st.Accent.Render("┃")
	}
	row := h.cur + 1 // the sidebar starts with a blank row
	if h.showSignin {
		row = 0
	}
	if i == row {
		return ">"
	}
	return "│"
}

func (h *home) sidebar(rows int) string {
	lines := make([]string, 0, rows)
	lines = append(lines, "")
	for i, t := range homeTitles {
		label := fmt.Sprintf(" %d %s", i+1, t)
		sel := i == h.cur && !h.showSignin && !h.paletteO
		switch {
		case sel && !h.contentFocused():
			if h.color {
				label = h.st.Bold.Reverse(true).Render(padRight(label, sidebarW-1))
			} else {
				label = ">" + label[1:]
			}
		case sel:
			if h.color {
				label = h.st.Bold.Inherit(h.st.Accent).Render(label)
			} else {
				label = "*" + label[1:]
			}
		default:
			label = h.st.Dim.Render(label)
		}
		lines = append(lines, h.mark(label, h.clickSection(i)))
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	if rows >= 13 {
		lines[rows-2] = h.st.Dim.Render(" ctrl+k commands")
		lines[rows-1] = h.st.Dim.Render(" ? keys  q quit")
	}
	return strings.Join(lines[:rows], "\n")
}

// clickSection selects sidebar item i, with the keys on the sidebar.
func (h *home) clickSection(i int) func() tea.Cmd {
	return func() tea.Cmd {
		h.paletteO = false
		h.backTo = nil
		h.cur = i
		h.toSidebar()
		return h.activate()
	}
}

// tabStrip is the sidebar on narrow terminals.
func (h *home) tabStrip() string {
	var parts []string
	for i, t := range homeTitles {
		label := fmt.Sprintf("%d %s", i+1, t)
		if i == h.cur && !h.showSignin && !h.paletteO {
			switch {
			case h.color && h.contentFocused():
				label = h.st.Bold.Inherit(h.st.Accent).Render("[" + label + "]")
			case h.color:
				label = h.st.Bold.Reverse(true).Render(" " + label + " ")
			case h.contentFocused():
				label = "[" + label + "]"
			default:
				label = ">" + label + "<"
			}
			parts = append(parts, h.mark(label, h.clickSection(i)))
			continue
		}
		parts = append(parts, h.mark(h.st.Dim.Render(fmt.Sprintf("%d", i+1)), h.clickSection(i)))
	}
	return truncate(strings.Join(parts, " "), h.w)
}

func (h *home) hintsLine() string {
	return truncate(h.st.Dim.Render(fitKeys(h.footer(), h.w)), h.w)
}

func (h *home) commandLine() string {
	c := h.command()
	if h.prompt() {
		// While a question is open, y answers it: nothing to copy.
		if c == "" {
			return ""
		}
		return truncate(h.st.Dim.Render("$ "+c), h.w)
	}
	if c == "" {
		return truncate(h.st.Dim.Render("ctrl+k runs any audd command"), h.w)
	}
	right := h.st.Dim.Render("y copy")
	left := "$ " + c
	if h.color {
		left = h.st.Dim.Render("$ ") + h.st.Accent.Render(c)
	}
	return spread(truncate(left, h.w-8), right, h.w)
}

// moveKeys are how to move around, for the keys overlay and the Keys
// page.
var moveKeys = []keyHelp{
	{"↑/↓", "move in the menu, a list, or a form"},
	{"→ or enter", "go in: open the section, the page, the selected item"},
	{"← or esc", "come back one level (in typed text ← moves the cursor)"},
	{"←/→", "on a tab bar: switch tabs; on a choice: change it"},
	{"↑", "from the top of a page: back to its tab bar"},
	{"[ ]", "previous or next tab, from anywhere in the section"},
}

// globalKeys are the keys that work everywhere.
var globalKeys = []keyHelp{
	{"1-7", "jump to a section (not while typing in a field)"},
	{"ctrl+k or :", "run any command; ctrl+k again closes it"},
	{"y", "copy the command shown at the bottom"},
	{"?", "keys for this screen"},
	{"F1", "help"},
	{"q", "quit; not while typing, and it asks while a command runs"},
	{"ctrl+c", "quit, also while typing (it asks while a command runs)"},
	{"ctrl+u", "clear the field being typed in"},
	{"click, wheel", "select or press; scroll"},
}

func ansiWidth(s string) int { return ansi.StringWidth(s) }

func (h *home) keysView(w int) string {
	var b strings.Builder
	title := homeTitles[h.cur]
	if h.showSignin {
		title = "Sign in"
	}
	if h.paletteO {
		title = "Commands"
	}
	b.WriteString(h.st.Bold.Render("Keys: "+title) + "\n\n")
	keys := h.active().keys()
	if h.paletteO {
		keys = h.palette.keys()
	}
	b.WriteString(keyLines(keys, w) + "\n")
	b.WriteString("\n" + h.st.Bold.Render("Moving around") + "\n")
	b.WriteString(indent(output.Wrap(tipText, max(10, w-2)), "  ") + "\n\n")
	b.WriteString(keyLines(moveKeys, w) + "\n")
	b.WriteString("\n" + h.st.Bold.Render("Everywhere") + "\n\n")
	b.WriteString(keyLines(globalKeys, w) + "\n")
	b.WriteString("\n" + styleLines(h.st.Dim, output.Wrap("Esc closes this; F1 opens the Help section, with the keys of every section.", w)))
	return b.String()
}

// keyLines lists keys and what they do, the descriptions wrapped under
// themselves.
func keyLines(keys []keyHelp, w int) string {
	var lines []string
	for _, k := range keys {
		desc := output.Wrap(k.help, max(10, w-19))
		for i, l := range strings.Split(desc, "\n") {
			if i == 0 {
				lines = append(lines, truncate(fmt.Sprintf("  %-14s %s", k.key, l), w))
			} else {
				lines = append(lines, truncate(strings.Repeat(" ", 17)+l, w))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// box frames s with a rounded border (plain when color is off).
func (h *home) box(s string, w int) string {
	st := h.st.Renderer.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(w - 2)
	if !h.color {
		st = st.Border(lipgloss.NormalBorder())
	}
	return st.Render(s)
}

// styleLines styles each line of s on its own. Rendering a block at once
// pads every line to the longest one, which can push lines past the pane.
func styleLines(st lipgloss.Style, s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = st.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}
