package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Layout of interactive mode:
//
//	header (1 row)
//	sidebar │ content        or, under 80 columns:  tab strip / content
//	key hints (1 row)
//	$ command (1 row)
const (
	sidebarW    = 17 // sidebar width, without the rule
	narrowWidth = 80
)

func (h *home) narrow() bool { return h.w < narrowWidth }

// contentSize is the size of the content pane.
func (h *home) contentSize() (int, int) {
	if h.narrow() {
		return max(1, h.w), max(1, h.h-4)
	}
	return max(1, h.w-sidebarW-2), max(1, h.h-3)
}

func (h *home) View() string {
	if h.w <= 0 || h.h <= 0 {
		return ""
	}
	h.arts.beginView()
	h.zoneFns = nil
	cw, ch := h.contentSize()
	var body string
	switch {
	case h.ask != nil:
		body = h.askView(cw, ch)
	case h.keysO:
		body = h.keysView(cw)
	case h.paletteO:
		body = h.palette.view(cw, ch)
	default:
		body = h.active().view(cw, ch)
	}
	body = fit(body, cw, ch)
	var page string
	if h.narrow() {
		page = h.headerLine() + "\n" + h.tabStrip() + "\n" + body
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
		page = h.headerLine() + "\n" + b.String()
	}
	page += "\n" + h.hintsLine() + "\n" + h.commandLine()
	page = h.scanZones(page)
	page = h.arts.finishView(page, h.w)
	if h.osc != "" {
		page = h.osc + page
	}
	return page
}

func (h *home) headerLine() string {
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
	return truncate(strings.Join(parts, h.st.Dim.Render("  ·  ")), h.w)
}

// contentFocused reports whether keys go to the content pane (or an
// overlay on it).
func (h *home) contentFocused() bool {
	return h.focus == focusContent || h.paletteO || h.ask != nil || h.keysO
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
	if h.flash != "" {
		return truncate(h.flash, h.w)
	}
	var keys []keyHelp
	switch {
	case h.ask != nil:
		return ""
	case h.keysO:
		keys = []keyHelp{{"any key", "close"}, {"F1", "full help"}}
	case h.paletteO:
		keys = h.palette.keys()
	case h.focus == focusSidebar:
		keys = []keyHelp{{"↑/↓", "section"}, {"enter", "open"}, {"1-7", "jump"}, {"ctrl+k", "commands"}, {"?", "keys"}, {"q", "quit"}}
	default:
		keys = backHint(h.active().keys(), leftExits(h.active()))
		if h.narrow() {
			keys = append(keys, keyHelp{"ctrl+k", "commands"})
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k.key+" "+k.help)
	}
	return truncate(h.st.Dim.Render(strings.Join(parts, "  ")), h.w)
}

// backHint puts how to go back first in a section's keys: ← and esc
// when left leaves too, esc alone otherwise (unless the section says
// what esc does).
func backHint(keys []keyHelp, left bool) []keyHelp {
	out := make([]keyHelp, 0, len(keys)+1)
	hasEsc := false
	for _, k := range keys {
		if k.key == "esc" && k.help == "sidebar" {
			continue
		}
		if strings.Contains(k.key, "esc") {
			hasEsc = true
		}
		out = append(out, k)
	}
	switch {
	case left && hasEsc:
		return append([]keyHelp{{"←", "back"}}, out...)
	case left:
		return append([]keyHelp{{"←/esc", "back"}}, out...)
	case !hasEsc:
		return append([]keyHelp{{"esc", "back"}}, out...)
	}
	return out
}

func (h *home) commandLine() string {
	c := h.command()
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

// globalKeys are the keys that work everywhere.
var globalKeys = []keyHelp{
	{"ctrl+k or :", "command palette: run any command"},
	{"1-7", "jump to a section"},
	{"↑/↓ j/k", "move in the sidebar"},
	{"enter", "open the section"},
	{"esc, ←", "back (← from the left-most item)"},
	{"←/→", "tabs and pages"},
	{"click, wheel", "select or press; scroll"},
	{"y", "copy the command shown at the bottom"},
	{"?", "keys for this screen"},
	{"F1", "help"},
	{"q", "quit (ctrl+c always quits)"},
}

func (h *home) keysView(w int) string {
	var b strings.Builder
	title := homeTitles[h.cur]
	if h.showSignin {
		title = "Sign in"
	}
	b.WriteString(h.st.Bold.Render("Keys: "+title) + "\n\n")
	for _, k := range h.active().keys() {
		fmt.Fprintf(&b, "  %-14s %s\n", k.key, k.help)
	}
	b.WriteString("\n" + h.st.Bold.Render("Everywhere") + "\n\n")
	for _, k := range globalKeys {
		fmt.Fprintf(&b, "  %-14s %s\n", k.key, k.help)
	}
	b.WriteString("\n" + h.st.Dim.Render("Press F1 or Enter for the Help section, any other key to close."))
	return b.String()
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
