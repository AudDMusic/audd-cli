package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/AudDMusic/audd-cli/internal/art"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func (m *explorer) listHeight() int {
	h := m.h - 6
	if h < 1 {
		h = 1
	}
	return h
}

func (m *explorer) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	if !m.embedded {
		m.arts.beginView()
	}
	bodyH := m.h - 3
	if m.embedded {
		bodyH = m.h - 1 // the status line
		if len(m.allowed) > 1 {
			bodyH--
		}
	}
	var body string
	switch {
	case m.help:
		body = m.helpView()
	case m.tab == tabUsage:
		body = m.usageView()
	default:
		body = m.levelView(m.top(), bodyH)
	}
	if m.embedded {
		page := fit(body, m.w, bodyH) + "\n" + m.statusLine()
		if len(m.allowed) > 1 {
			page = m.tabBar() + "\n" + page
		}
		return page
	}
	page := m.tabBar() + "\n" + fit(body, m.w, bodyH) + "\n" + m.statusLine() + "\n" + m.keysLine()
	page = m.arts.finishView(page, m.w)
	if m.osc != "" {
		page = m.osc + page
	}
	return page
}

func (m *explorer) tabBar() string {
	var parts []string
	for i, t := range tabTitles {
		label := fmt.Sprintf(" %d %s ", i+1, t)
		if m.embedded {
			if !slices.Contains(m.allowed, tabID(i)) {
				continue
			}
			label = " " + t + " "
		}
		if tabID(i) == m.tab {
			s := m.st.Bold.Reverse(true)
			if !m.color {
				label = "[" + strings.TrimSpace(label) + "]"
				s = m.st.Bold
			}
			parts = append(parts, s.Render(label))
		} else {
			parts = append(parts, m.st.Dim.Render(label))
		}
	}
	if m.embedded {
		return truncate(strings.Join(parts, " "), m.w)
	}
	return spread(strings.Join(parts, " "), m.st.Dim.Render("audd browse"), m.w)
}

func (m *explorer) statusLine() string {
	switch {
	case m.purpose != inputNone:
		return truncate(m.input.Prompt+m.input.Value()+"█", m.w)
	case m.confirm != "":
		return truncate(m.st.Warn.Render(m.confirm), m.w)
	case m.exportAs:
		return truncate("Export this view as (c)sv or (j)son?", m.w)
	case m.flash != "":
		return truncate(m.st.Dim.Render(m.flash), m.w)
	}
	if l := m.top(); l != nil && m.tab != tabUsage && !m.help {
		n := len(l.visible())
		s := fmt.Sprintf("%s · %d", l.title, n)
		if l.filter != "" {
			s += fmt.Sprintf(" matching %q", l.filter)
		}
		return truncate(m.st.Dim.Render(s), m.w)
	}
	return ""
}

func (m *explorer) keysLine() string {
	return truncate(m.st.Dim.Render(strings.Join(m.keyList(), "  ")), m.w)
}

// keyList is the key hints for the current view. Embedded, it leaves out
// what the surrounding screen handles (help, quit, a single tab).
func (m *explorer) keyList() []string {
	var keys []string
	l := m.top()
	tabs := !m.embedded || len(m.allowed) > 1
	tail := func(k ...string) []string {
		if tabs {
			k = append(k, "tab next")
		}
		if !m.embedded {
			k = append(k, "? help", "q quit")
		}
		return k
	}
	switch {
	case m.help:
		keys = []string{"any key: close help"}
	case m.tab == tabUsage:
		if tabs {
			keys = append(keys, "tab next")
		}
		keys = append(keys, "r refresh", "e export")
		if !m.embedded {
			keys = append(keys, "? help", "q quit")
		}
	case l != nil && l.detail:
		keys = []string{"esc back", "↑/↓ scroll", "o open", "c copy link", "i ISRC", "u UPC"}
		if !m.embedded {
			keys = append(keys, "q quit")
		}
	default:
		keys = []string{"↑/↓ move", "enter open", "/ filter", "o open", "c copy"}
		switch l.kind {
		case "streams":
			keys = append(keys, "a add", "d remove", "u set URL")
		case "jobs", "items":
			keys = append(keys, "r resume", "R retry failed")
		}
		keys = append(keys, tail("e export")...)
	}
	return keys
}

func (m *explorer) helpView() string {
	rows := [][2]string{
		{"tab / 1-4", "switch tabs"},
		{"↑/↓ j/k", "move"},
		{"enter", "open the item (job items, stream plays, details)"},
		{"esc", "back"},
		{"/", "filter the list"},
		{"o", "open the song link in the browser"},
		{"c, i, u", "copy the song link, ISRC, or UPC"},
		{"e", "export the view to CSV or JSON in this folder"},
		{"r", "refresh (Jobs: resume the job)"},
		{"R", "Jobs: retry failed items"},
		{"a, d, u", "Streams: add, remove, set URL"},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(m.st.Bold.Render("Keys") + "\n\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-10s %s\n", r[0], r[1])
	}
	return b.String()
}

func (m *explorer) detailStyles() detailStyles {
	return detailStyles{title: m.st.Title.Inherit(m.st.Accent), dim: m.st.Dim, key: m.st.Key}
}

func (m *explorer) levelView(l *level, h int) string {
	if l == nil {
		return ""
	}
	if l.detail {
		if r := l.selected(); r != nil && r.detail != nil {
			lines := strings.Split(r.detail(m.detailStyles()), "\n")
			if l.dscroll > len(lines)-1 {
				l.dscroll = max(0, len(lines)-1)
			}
			text := strings.Join(lines[l.dscroll:], "\n")
			return m.withCover(r, text, h)
		}
	}
	if l.err != nil && len(l.rows) == 0 {
		msg := "Could not load: " + output.AsError(l.err).Message
		if hint := output.AsError(l.err).Hint; hint != "" {
			msg += "\nTry: " + hint
		}
		return m.st.Warn.Render(msg)
	}
	if l.loading && len(l.rows) == 0 {
		return m.st.Dim.Render("Loading…")
	}
	rows := l.visible()
	if len(rows) == 0 {
		switch {
		case l.filter != "":
			return m.st.Dim.Render("Nothing matches the filter.")
		case l.kind == "recent":
			return m.st.Dim.Render("Nothing recognized yet. Try: audd recognize song.mp3")
		case l.kind == "jobs":
			return m.st.Dim.Render("No jobs yet. Batch runs (folders, globs, enterprise) appear here.")
		case l.kind == "streams":
			return m.st.Dim.Render("No streams yet. Press a to add one, or run: audd streams add <url> --id 1")
		}
		return m.st.Dim.Render("Nothing here yet.")
	}
	widths := colWidths(l.cols, m.w)
	var b strings.Builder
	head := cellsLine(colTitles(l.cols), widths)
	if !m.color {
		head = "  " + head
	}
	b.WriteString(truncate(m.st.Dim.Render(head), m.w) + "\n")
	listH := h - 1
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+listH {
		l.offset = l.cursor - listH + 1
	}
	for i := l.offset; i < len(rows) && i < l.offset+listH; i++ {
		line := cellsLine(rows[i].cells, widths)
		if i == l.cursor {
			if m.color {
				line = m.st.Bold.Reverse(true).Render(padRight(line, m.w))
			} else {
				line = "> " + truncate(line, m.w-2)
			}
		} else if !m.color {
			line = "  " + truncate(line, m.w-2)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// detailArtRows is the height of the cover next to a detail view.
const detailArtRows = 10

// withCover puts the song's cover to the left of a detail view when there
// is room. The cover stays put while the text scrolls.
func (m *explorer) withCover(r *row, text string, h int) string {
	if r.view == nil || m.arts.proto == art.ProtoNone {
		return text
	}
	rows := min(detailArtRows, h-1)
	cols := m.arts.colsFor(rows)
	if rows < 4 || m.w-cols-3 < 40 {
		return text
	}
	cover := m.arts.render(*r.view, cols, rows, m.st.Dim)
	return lipgloss.JoinHorizontal(lipgloss.Top, cover, "   ", fit(text, m.w-cols-3, h))
}

func colTitles(cols []column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.title
	}
	return out
}

// colWidths gives flex columns the space the fixed ones leave, and drops
// fixed widths proportionally on narrow terminals.
func colWidths(cols []column, w int) []int {
	out := make([]int, len(cols))
	fixed, flex := 0, 0
	for _, c := range cols {
		if c.width == 0 {
			flex++
		}
		fixed += c.width + 2
	}
	avail := w - 2 - fixed
	for i, c := range cols {
		out[i] = c.width
		if c.width == 0 {
			out[i] = max(8, avail/max(1, flex))
		}
	}
	if avail < 8*flex { // shrink fixed columns on narrow screens
		for i, c := range cols {
			if c.width > 8 {
				out[i] = max(6, c.width*w/120)
			}
		}
	}
	return out
}

func cellsLine(cells []string, widths []int) string {
	var parts []string
	for i, c := range cells {
		if i >= len(widths) {
			break
		}
		parts = append(parts, padRight(truncate(c, widths[i]), widths[i]))
	}
	return strings.TrimRight(strings.Join(parts, "  "), " ")
}

func padRight(s string, w int) string {
	if n := w - ansi.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
