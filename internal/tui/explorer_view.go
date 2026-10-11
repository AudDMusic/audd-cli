package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
		bodyH = m.h - 1 // the status line; the screen shows the tabs
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
		return fit(body, m.w, bodyH) + "\n" + m.statusLine()
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
			parts = append(parts, m.zone(s.Render(label), m.clickTab(tabID(i))))
		} else {
			parts = append(parts, m.zone(m.st.Dim.Render(label), m.clickTab(tabID(i))))
		}
	}
	if m.embedded {
		return truncate(strings.Join(parts, " "), m.w)
	}
	return spread(strings.Join(parts, " "), m.st.Dim.Render("audd browse"), m.w)
}

// zone makes s clickable when the explorer is in interactive mode.
func (m *explorer) zone(s string, fn func() tea.Cmd) string {
	if m.mark == nil {
		return s
	}
	return m.mark(s, fn)
}

func (m *explorer) clickTab(t tabID) func() tea.Cmd {
	return func() tea.Cmd {
		if t == m.tab {
			return nil
		}
		return m.switchTab(t)
	}
}

// clickRow selects row i; a click on the selected row opens it.
func (m *explorer) clickRow(l *level, i int) func() tea.Cmd {
	return func() tea.Cmd {
		if l.cursor == i {
			return m.enter(l)
		}
		l.cursor = i
		return nil
	}
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
	if l := m.top(); l != nil && m.tab != tabUsage && !m.help && !l.detail {
		if r := l.selected(); r != nil && r.hint != "" && !l.detail {
			// What to do about the selected row (a stream that is down).
			return truncate(m.st.Warn.Render(r.hint), m.w)
		}
		n := len(l.visible())
		s := l.title + " · " + output.Plural(n, rowNoun(l.kind))
		if r := l.selected(); r != nil && l.kind == "streams" && m.w < 100 && !l.detail && len(r.cells) > 4 {
			// The URL column is left out on narrow screens: the selected
			// stream's URL is here.
			s += " · " + truncMiddle(r.cells[4], max(8, m.w-len([]rune(s))-3))
		}
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
	if m.embedded {
		return m.embeddedKeys()
	}
	var keys []string
	l := m.top()
	tail := func(k ...string) []string {
		return append(k, "tab next", "? help", "q quit")
	}
	switch {
	case m.help:
		keys = []string{"any key: close help"}
	case m.tab == tabUsage:
		keys = append(keys, "tab next", "r refresh", "e export", "? help", "q quit")
	case l != nil && l.detail:
		keys = []string{"esc back", "↑/↓ scroll", "o open", "c copy link", "i ISRC", "u UPC", "q quit"}
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

// embeddedKeys are the hints in interactive mode: moving around. What
// can be done is on the page's buttons.
func (m *explorer) embeddedKeys() []string {
	l := m.top()
	switch {
	case m.purpose == inputSetURL:
		return []string{"enter save", "esc cancel"}
	case m.purpose == inputFilter:
		return []string{"enter keep the filter", "esc clear it"}
	case m.purpose != inputNone:
		return []string{"enter done", "esc cancel"}
	case m.tab == tabUsage || l == nil:
		return nil
	case l.detail:
		return []string{"↑/↓ scroll"}
	}
	keys := []string{"↑/↓ move"}
	if r := l.selected(); r != nil && r.detail != nil {
		keys = append(keys, "→ open")
	}
	return append(keys, "/ filter")
}

// jobOf is the job a level is about: the selected one on the list, the
// opened one in its files.
func (m *explorer) jobOf(l *level) *JobRow {
	if l.kind == "jobs" {
		if r := l.selected(); r != nil {
			if j, ok := r.data.(JobRow); ok {
				return &j
			}
		}
		return nil
	}
	for _, r := range m.tabs[tabJobs][0].rows {
		if r.key == l.arg {
			if j, ok := r.data.(JobRow); ok {
				return &j
			}
		}
	}
	return nil
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
			// Long paths and URLs wrap instead of being cut.
			lines := hardWrap(strings.Split(output.Wrap(r.detail(m.detailStyles()), m.w), "\n"), m.w)
			if l.dscroll > len(lines)-1 {
				l.dscroll = max(0, len(lines)-1)
			}
			text := strings.Join(lines[l.dscroll:], "\n")
			return m.withCover(r, text, h)
		}
	}
	if l.err != nil && len(l.rows) == 0 {
		e := output.AsError(l.err)
		if m.embedded {
			// In interactive mode: what to do, with the page's buttons.
			if l.kind == "streams" && (e.Code == "no_token" || e.Code == "token_rejected" || hintKind(e.Hint) != "") {
				return "Sign in or add a token to monitor streams.\n" + m.st.Dim.Render(output.Wrap(noTokenChoicesReal, max(20, m.w)))
			}
			msg := "Could not load: " + e.Message
			if hint := humanHint(e.Hint); hint != "" {
				msg += "\n" + hint
			}
			return m.st.Warn.Render(output.Wrap(msg, max(20, m.w)))
		}
		msg := "Could not load: " + e.Message
		if hint := e.Hint; hint != "" {
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
			if m.embedded && m.signedOut != nil && m.signedOut() {
				return "No API token yet.\n" + m.st.Dim.Render(output.Wrap(noTokenChoicesReal, max(20, m.w)))
			}
			if m.embedded {
				return "No streams yet.\n" + m.st.Dim.Render("Add one with the Add a stream button below (or a).")
			}
			return m.st.Dim.Render("No streams yet. Press a to add one, or run: audd streams add <url> --id 1")
		}
		return m.st.Dim.Render("Nothing here yet.")
	}
	cols := l.cols
	var pick []int // the cells shown, when not all
	switch l.kind {
	case "streams":
		cols = streamColsFor(m.w)
	case "recent":
		if m.w < 100 {
			// When, song, and source; the album is in the details.
			cols = []column{recentCols[0], recentCols[1], {title: "Source", width: 14}}
			pick = []int{0, 1, 3}
		}
	case "jobs":
		if m.w < 100 {
			// Job, status, done with the failures, and what it ran, which
			// tells jobs apart.
			cols = []column{jobCols[0], jobCols[1], {title: "Done", width: 13}, jobCols[5]}
			pick = []int{0, 1, 6, 5}
		}
	}
	var note string
	if l.kind == "items" {
		if j := m.jobOf(l); j != nil {
			note = m.st.Dim.Render(truncate("$ "+jobCommand(j.Command), m.w))
			h--
		}
	}
	widths := colWidths(cols, m.w)
	var b strings.Builder
	if note != "" {
		b.WriteString(note + "\n")
	}
	head := cellsLine(colTitles(cols), widths)
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
		cells := rows[i].cells
		if pick != nil {
			var sel []string
			for _, c := range pick {
				if c < len(cells) {
					sel = append(sel, cells[c])
				}
			}
			cells = sel
		}
		for c, col := range cols {
			if col.mid && c < len(cells) && c < len(widths) {
				cells = append([]string(nil), cells...)
				cells[c] = truncMiddle(cells[c], widths[c])
			}
		}
		line := cellsLine(cells, widths)
		if i == l.cursor {
			switch {
			case m.color && m.blur:
				line = m.st.Bold.Render(line)
			case m.color:
				line = m.st.Bold.Reverse(true).Render(padRight(line, m.w))
			case m.blur:
				line = "· " + truncate(line, m.w-2)
			default:
				line = "› " + truncate(line, m.w-2)
			}
		} else if !m.color {
			line = "  " + truncate(line, m.w-2)
		}
		b.WriteString(m.zone(line, m.clickRow(l, i)) + "\n")
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

// truncMiddle shortens s to w cells keeping its start and its end
// (https://radio.example…/stream.mp3).
func truncMiddle(s string, w int) string {
	r := []rune(s)
	if len(r) <= w || w < 8 {
		return truncate(s, w)
	}
	// Keep the scheme and host when there is room, then the end.
	head := (w - 1) * 3 / 5
	if i := strings.Index(s, "://"); i >= 0 {
		host := i + 3 + strings.IndexByte(s[i+3:]+"/", '/')
		head = max(head, min(len([]rune(s[:host])), w-1-8))
	}
	tail := w - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// rowNoun is what a list's rows are, for its count ("3 files").
func rowNoun(kind string) string {
	switch kind {
	case "recent":
		return "recognition"
	case "jobs":
		return "job"
	case "items":
		return "file"
	case "streams":
		return "stream"
	case "plays":
		return "play"
	}
	return "row"
}

// jobCommand is a job's command as typed.
func jobCommand(args []string) string {
	if len(args) > 0 && args[0] == "audd" {
		args = args[1:]
	}
	return displayCommand(args)
}
