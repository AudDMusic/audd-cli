package tui

import (
	"fmt"
	"strings"

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
		rule := h.st.Dim.Render("│")
		var b strings.Builder
		for i := 0; i < ch; i++ {
			b.WriteString(padRight(side[i], sidebarW) + rule + " " + lines[i])
			if i < ch-1 {
				b.WriteString("\n")
			}
		}
		page = h.headerLine() + "\n" + b.String()
	}
	page += "\n" + h.hintsLine() + "\n" + h.commandLine()
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

func (h *home) sidebar(rows int) string {
	lines := make([]string, 0, rows)
	lines = append(lines, "")
	for i, t := range homeTitles {
		label := fmt.Sprintf(" %d %s", i+1, t)
		sel := i == h.cur && !h.showSignin && !h.paletteO
		switch {
		case sel && h.focus == focusSidebar:
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
		lines = append(lines, label)
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

// tabStrip is the sidebar on narrow terminals.
func (h *home) tabStrip() string {
	var parts []string
	for i, t := range homeTitles {
		label := fmt.Sprintf("%d %s", i+1, t)
		if i == h.cur && !h.showSignin && !h.paletteO {
			if h.color {
				label = h.st.Bold.Reverse(true).Render(" " + label + " ")
			} else {
				label = "[" + label + "]"
			}
			parts = append(parts, label)
			continue
		}
		parts = append(parts, h.st.Dim.Render(fmt.Sprintf("%d", i+1)))
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
		keys = []keyHelp{{"↑/↓", "section"}, {"enter", "open"}, {"1-8", "jump"}, {"ctrl+k", "commands"}, {"?", "keys"}, {"q", "quit"}}
	default:
		keys = h.active().keys()
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
	{"1-8", "jump to a section"},
	{"↑/↓ j/k", "move in the sidebar"},
	{"enter", "open the section"},
	{"esc", "back (to the sidebar)"},
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
