package tui

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Mouse support. While a frame is drawn, clickable parts are wrapped in
// zero-width marks (h.mark); View then finds where each mark landed on
// the screen, removes the marks, and keeps the regions. A click runs the
// action of the region under it.

// mouseEnabled reports whether interactive mode turns the mouse on:
// AUDD_NO_MOUSE=1 turns it off (the terminal then selects text as usual).
func mouseEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUDD_NO_MOUSE"))) {
	case "", "0", "false", "no":
		return true
	}
	return false
}

// zoneHit is a clicked region: from (x0, y0) to (x1, y1), x1 exclusive.
type zoneHit struct {
	x0, y0, x1, y1 int
	fn             func() tea.Cmd
}

func (z zoneHit) contains(x, y int) bool {
	switch {
	case y < z.y0 || y > z.y1:
		return false
	case z.y0 == z.y1:
		return x >= z.x0 && x < z.x1
	case y == z.y0:
		return x >= z.x0
	case y == z.y1:
		return x < z.x1
	}
	return true
}

// The marks are CSI sequences no terminal acts on; width functions skip
// them like any other escape sequence.
const (
	markOpen  = "\x1b[7337;"
	markClose = "\x1b[7338;"
)

var markRE = regexp.MustCompile(`\x1b\[733([78]);(\d+)z`)

// mark makes s clickable: fn runs when it is clicked.
func (h *home) mark(s string, fn func() tea.Cmd) string {
	if h == nil || fn == nil || s == "" {
		return s
	}
	id := strconv.Itoa(len(h.zoneFns))
	h.zoneFns = append(h.zoneFns, fn)
	return markOpen + id + "z" + s + markClose + id + "z"
}

// scanZones finds the marks in a frame, records their regions, and
// returns the frame without them.
func (h *home) scanZones(page string) string {
	h.hits = h.hits[:0]
	if !strings.Contains(page, "\x1b[733") {
		h.zoneFns = nil
		return page
	}
	type pos struct{ x, y int }
	open := map[int]pos{}
	lines := strings.Split(page, "\n")
	for y, l := range lines {
		if !strings.Contains(l, "\x1b[733") {
			continue
		}
		var b strings.Builder
		col, last := 0, 0
		for _, m := range markRE.FindAllStringSubmatchIndex(l, -1) {
			seg := l[last:m[0]]
			b.WriteString(seg)
			col += ansi.StringWidth(seg)
			last = m[1]
			id, _ := strconv.Atoi(l[m[4]:m[5]])
			if l[m[2]:m[3]] == "7" {
				open[id] = pos{col, y}
				continue
			}
			if o, ok := open[id]; ok && id < len(h.zoneFns) {
				h.hits = append(h.hits, zoneHit{o.x, o.y, col, y, h.zoneFns[id]})
				delete(open, id)
			}
		}
		b.WriteString(l[last:])
		lines[y] = b.String()
	}
	h.zoneFns = nil
	return strings.Join(lines, "\n")
}

// hitAt is the action of the smallest region at (x, y).
func (h *home) hitAt(x, y int) func() tea.Cmd {
	var best *zoneHit
	for i := range h.hits {
		z := &h.hits[i]
		if !z.contains(x, y) {
			continue
		}
		if best == nil || z.y1-z.y0 < best.y1-best.y0 || z.y1-z.y0 == best.y1-best.y0 && z.x1-z.x0 < best.x1-best.x0 {
			best = z
		}
	}
	if best == nil {
		return nil
	}
	return best.fn
}

// inContent reports whether (x, y) is in the content pane.
func (h *home) inContent(x, y int) bool {
	cw, ch := h.contentSize()
	if h.narrow() {
		return y >= 2 && y < 2+ch && x < cw
	}
	return y >= 1 && y < 1+ch && x >= sidebarW+2
}

// receiver is who gets keys now: the palette when it is open, or the
// section in the content pane.
func (h *home) receiver() string {
	if h.paletteO {
		return "palette"
	}
	return h.activeID()
}

// sendKey sends a key to the receiver as if it was typed there.
func (h *home) sendKey(k tea.KeyMsg) tea.Cmd { return h.send(h.receiver(), k) }

// wheeler is a screen that scrolls its own way with the mouse wheel.
type wheeler interface {
	wheel(dir int) tea.Cmd
}

func (h *home) mouse(m tea.MouseMsg) tea.Cmd {
	if m.Action != tea.MouseActionPress {
		return nil
	}
	if h.ask != nil {
		return nil
	}
	if h.keysO {
		h.keysO = false
		return nil
	}
	switch m.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		dir := 1
		if m.Button == tea.MouseButtonWheelUp {
			dir = -1
		}
		if h.paletteO {
			return h.wrap("palette", h.palette.wheel(dir))
		}
		if !h.inContent(m.X, m.Y) {
			return nil
		}
		if w, ok := h.active().(wheeler); ok {
			return h.wrap(h.activeID(), w.wheel(dir))
		}
		return nil
	case tea.MouseButtonLeft:
	default:
		return nil
	}
	owner := h.receiver()
	if h.inContent(m.X, m.Y) && !h.paletteO {
		h.focus = focusContent
	}
	fn := h.hitAt(m.X, m.Y)
	if fn == nil {
		return nil
	}
	return h.wrap(owner, fn())
}

// keyWheel scrolls by sending up or down, n times.
func keyWheel(dir, n int, send func(tea.KeyMsg) tea.Cmd) tea.Cmd {
	k := tea.KeyMsg{Type: tea.KeyDown}
	if dir < 0 {
		k = tea.KeyMsg{Type: tea.KeyUp}
	}
	var cmds []tea.Cmd
	for range n {
		cmds = append(cmds, send(k))
	}
	return tea.Batch(cmds...)
}

// pageStrip is a row of page names with the current one marked; a click
// opens a page.
func (h *home) pageStrip(pages []string, cur, w int, open func(int) tea.Cmd) string {
	st := h.st
	var parts []string
	for i, p := range pages {
		var label string
		switch {
		case i == cur && h.color:
			label = st.Bold.Reverse(true).Render(" " + p + " ")
		case i == cur:
			label = "[" + p + "]"
		default:
			label = st.Dim.Render(" " + p + " ")
		}
		parts = append(parts, h.mark(label, func() tea.Cmd { return open(i) }))
	}
	return truncate(strings.Join(parts, " "), w)
}
