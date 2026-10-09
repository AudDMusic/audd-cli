package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/art"
)

func (m *npModel) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	m.arts.beginView()
	bodyH := m.bodyHeight()
	var body string
	switch {
	case m.help:
		body = m.helpView()
	case len(m.stations) == 0:
		body = m.st.Dim.Render("No streams.")
	case m.single() && m.history:
		body = m.historyView(m.stations[m.sel], bodyH)
	case m.single():
		body = m.cardView(m.stations[m.sel], m.w, bodyH)
	default:
		body = m.gridView(bodyH)
	}
	header := m.headerLine()
	if m.headerGap() {
		header += "\n"
	}
	footer := m.footerLine()
	page := header + "\n" + fit(body, m.w, bodyH) + "\n" + footer
	page = m.arts.finishView(page, m.w)
	if m.osc != "" {
		page = m.osc + page
	}
	return page
}

// headerGap reports whether an empty row separates the header from the
// body (left out on very short terminals).
func (m *npModel) headerGap() bool { return m.h >= 6 }

// bodyHeight is the number of rows between the header (and its gap) and
// the footer.
func (m *npModel) bodyHeight() int {
	h := m.h - 2
	if m.headerGap() {
		h--
	}
	return h
}

func (m *npModel) headerLine() string {
	left := m.st.Bold.Render("Now playing")
	right := ""
	if len(m.stations) > 1 {
		right = m.st.Dim.Render(fmt.Sprintf("stream %d of %d", m.sel+1, len(m.stations)))
	}
	return spread(left, right, m.w)
}

func (m *npModel) footerLine() string {
	if m.flash != "" {
		return truncate(m.st.Dim.Render(m.flash), m.w)
	}
	var keys []string
	switch {
	case m.help:
		keys = []string{"any key: close help"}
	case m.single():
		if len(m.stations) > 1 {
			keys = append(keys, "←/→ stream", "enter grid")
		}
		keys = append(keys, "o open", "c copy link", "h history", "r refresh", "? help", "q quit")
	default:
		keys = []string{"←/→/↑/↓ select", "enter zoom", "o open", "c copy link", "h history", "? help", "q quit"}
	}
	return truncate(m.st.Dim.Render(strings.Join(keys, "  ")), m.w)
}

func (m *npModel) helpView() string {
	rows := [][2]string{
		{"←/→", "previous or next stream"},
		{"↑/↓", "move in the grid"},
		{"enter", "zoom into a stream or back to the grid"},
		{"h", "full history of the stream"},
		{"o", "open the song link in the browser"},
		{"c", "copy the song link"},
		{"r", "refresh now"},
		{"esc", "back"},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(m.st.Bold.Render("Keys") + "\n\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-7s %s\n", r[0], r[1])
	}
	b.WriteString("\n" + m.st.Dim.Render("Results come from the stream store, which the background recorder keeps up to date."))
	return b.String()
}

// accentStyle is bold text in the cover's accent color.
func (m *npModel) accentStyle(v ...app.ResultView) lipgloss.Style {
	s := m.st.Title
	if !m.color {
		return s
	}
	if len(v) > 0 {
		if c := m.arts.accent(v[0]); c != "" {
			return m.r.NewStyle().Bold(true).Foreground(c)
		}
	}
	return s.Inherit(m.st.Accent)
}

func (m *npModel) cardView(d stationData, w, h int) string {
	artRows := min(h-9, 14)
	artCols := m.arts.colsFor(artRows)
	for artRows > 0 && artCols > w/2 {
		artRows--
		artCols = m.arts.colsFor(artRows)
	}
	if m.arts.proto == art.ProtoNone || artRows < 4 {
		artRows, artCols = 0, 0
	}
	infoW := w - artCols - 3
	if artRows == 0 {
		infoW = w - 1
	}
	info := m.infoBlock(d, infoW, true)
	top := info
	if artRows > 0 {
		var cover string
		if len(d.Plays) > 0 {
			cover = m.arts.render(d.Plays[0].ResultView, artCols, artRows, m.st.Dim)
		} else {
			cover = placeholder(artCols, artRows, m.st.Dim)
		}
		top = lipgloss.JoinHorizontal(lipgloss.Top, cover, "   ", info)
	}
	topH := lipgloss.Height(top)
	left := h - topH - 2
	if left < 2 || len(d.Plays) < 2 {
		return top
	}
	var b strings.Builder
	b.WriteString(top + "\n\n")
	b.WriteString(m.st.Bold.Render("Earlier on this station") + "\n")
	b.WriteString(m.playLines(d.Plays[1:], left-1, w, false))
	return b.String()
}

// infoBlock is the text part of a card: status, title, artist, meta,
// progress, link.
func (m *npModel) infoBlock(d stationData, w int, big bool) string {
	now := m.now()
	var lines []string
	label := fmt.Sprintf("Stream %d", d.RadioID)
	if d.URL != "" && big {
		label += "  " + m.st.Dim.Render(d.URL)
	}
	lines = append(lines, m.st.Dim.Render(label))
	if d.Down() || (!d.Running && d.Health == nil && len(d.Plays) == 0) {
		lines = append(lines, m.st.Err.Render(d.HealthText()))
	}
	if len(d.Plays) == 0 {
		if d.Err != nil {
			lines = append(lines, m.st.Warn.Render("Could not read results: "+d.Err.Error()))
		} else if !m.loaded {
			lines = append(lines, m.st.Dim.Render("Loading…"))
		} else {
			lines = append(lines, m.st.Dim.Render("No songs recognized yet."))
		}
		return clip(lines, w)
	}
	p := d.Plays[0]
	if d.Down() {
		lines = append(lines, m.st.Dim.Render("Last heard: "+songLine(p.ResultView)+" · "+ago(p.Ago(now))))
		return clip(lines, w)
	}
	head, detail, bar := playStatus(p, now)
	if p.State(now) == StatePlaying {
		lines = append(lines, m.accentStyle(p.ResultView).Render("● "+upperState(head)))
	} else {
		lines = append(lines, m.st.Dim.Render(upperState(head)))
	}
	if big {
		lines = append(lines, "")
	}
	lines = append(lines, m.accentStyle(p.ResultView).Render(p.Title))
	if p.Artist != "" {
		lines = append(lines, p.Artist)
	}
	if meta := metaLine(p.ResultView); meta != "" {
		lines = append(lines, m.st.Dim.Render(meta))
	}
	if bar || detail != "" {
		if big {
			lines = append(lines, "")
		}
		if bar {
			lines = append(lines, m.progress(p, now, w))
		} else {
			lines = append(lines, m.st.Dim.Render(detail))
		}
	}
	if big && p.SongLink != "" {
		lines = append(lines, "", m.st.Dim.Render(p.SongLink))
	}
	return clip(lines, w)
}

// upperState capitalizes the state part of a status for the card ("Just
// played · ended 2 min ago" → "JUST PLAYED · ended 2 min ago", "Last
// recognized 25 min ago" → "LAST RECOGNIZED · 25 min ago").
func upperState(head string) string {
	for _, s := range []string{"Now playing", "Just played", "Last played", "Last recognized"} {
		if rest, ok := strings.CutPrefix(head, s); ok {
			rest = strings.TrimPrefix(strings.TrimPrefix(rest, " · "), " ")
			if rest == "" {
				return strings.ToUpper(s)
			}
			return strings.ToUpper(s) + " · " + rest
		}
	}
	return head
}

// progress is the bar of a playing song whose length is known.
func (m *npModel) progress(p Play, now time.Time, w int) string {
	el := p.Elapsed(now)
	label := " " + clock(el) + " / " + clock(p.TrackLength)
	barW := w - ansi.StringWidth(label)
	if barW > 40 {
		barW = 40
	}
	if barW < 4 {
		return m.st.Dim.Render(strings.TrimSpace(label))
	}
	frac := float64(el) / float64(p.TrackLength)
	if frac > 1 {
		frac = 1
	}
	if frac < 0 {
		frac = 0
	}
	n := int(frac*float64(barW) + 0.5)
	return m.accentStyle(p.ResultView).UnsetBold().Render(strings.Repeat("━", n)) +
		m.st.Dim.Render(strings.Repeat("─", barW-n)) + m.st.Dim.Render(label)
}

// playLines lists plays as "15:04  Artist — Title" (with album when full).
func (m *npModel) playLines(plays []Play, max, w int, full bool) string {
	if max <= 0 {
		return ""
	}
	var lines []string
	for i, p := range plays {
		if i >= max {
			break
		}
		t := p.At.In(localZone).Format("15:04")
		line := m.st.Dim.Render(t) + "  " + songLine(p.ResultView)
		if full {
			if meta := metaLine(p.ResultView); meta != "" {
				line += m.st.Dim.Render("  " + meta)
			}
		}
		lines = append(lines, truncate(line, w))
	}
	return strings.Join(lines, "\n")
}

func (m *npModel) historyView(d stationData, h int) string {
	head := m.st.Bold.Render(fmt.Sprintf("Stream %d · recent plays", d.RadioID))
	if len(d.Plays) == 0 {
		return head + "\n\n" + m.st.Dim.Render("No songs recognized yet.")
	}
	return head + "\n\n" + m.playLines(d.Plays, h-2, m.w, true)
}

func (m *npModel) gridView(h int) string {
	cols := m.gridCols()
	cardW := m.w / cols
	const cardH = 8 // 6 content rows plus the border
	visibleRows := h / cardH
	if visibleRows < 1 {
		visibleRows = 1
	}
	selRow := m.sel / cols
	firstRow := 0
	if selRow >= visibleRows {
		firstRow = selRow - visibleRows + 1
	}
	var rows []string
	for r := firstRow; r < firstRow+visibleRows; r++ {
		var cards []string
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if i >= len(m.stations) {
				break
			}
			cards = append(cards, m.gridCard(i, cardW))
		}
		if len(cards) == 0 {
			break
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	return strings.Join(rows, "\n")
}

func (m *npModel) gridCard(i, w int) string {
	d := m.stations[i]
	inner := w - 4
	const artRows = 6
	artCols := 0
	if m.arts.proto != art.ProtoNone && inner >= 24+m.arts.colsFor(artRows) {
		artCols = m.arts.colsFor(artRows)
	}
	infoW := inner - artCols
	if artCols > 0 {
		infoW -= 2
	}
	info := m.infoBlock(d, infoW, false)
	content := info
	if artCols > 0 {
		var cover string
		if len(d.Plays) > 0 {
			cover = m.arts.render(d.Plays[0].ResultView, artCols, artRows, m.st.Dim)
		} else {
			cover = placeholder(artCols, artRows, m.st.Dim)
		}
		content = lipgloss.JoinHorizontal(lipgloss.Top, cover, "  ", info)
	}
	content = fit(content, inner, artRows)
	border := lipgloss.RoundedBorder()
	style := m.r.NewStyle().Border(border).Padding(0, 1).Width(w - 2)
	if i == m.sel {
		style = style.Border(lipgloss.ThickBorder())
		if m.color {
			if c := m.cardAccent(d); c != "" {
				style = style.BorderForeground(c)
			} else {
				style = style.BorderForeground(lipgloss.Color("39"))
			}
		}
	} else if m.color {
		style = style.BorderForeground(lipgloss.Color("240"))
	}
	return style.Render(content)
}

func (m *npModel) cardAccent(d stationData) lipgloss.Color {
	if len(d.Plays) == 0 {
		return ""
	}
	return m.arts.accent(d.Plays[0].ResultView)
}

// --- layout helpers ---

// clip truncates each line to w cells and joins them.
func clip(lines []string, w int) string {
	for i, l := range lines {
		lines[i] = truncate(l, w)
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// fit pads or cuts s to exactly h lines of at most w cells.
func fit(s string, w, h int) string {
	if h <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = truncate(lines[i], w)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// spread puts left and right on one line of width w.
func spread(left, right string, w int) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func oscCopy(s string) string {
	var b strings.Builder
	copyOSC52(&b, s)
	return b.String()
}
