package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// A page's actions are buttons in a row under it: everything a page can
// do is on screen, reached with tab (or ↓ from the end of the page) and
// pressed with enter or a click. A few have a letter, shown on the
// button, that presses them from anywhere on the page.

// button is an action on a page.
type button struct {
	label string // "Resume"
	key   string // the letter shown on it, "" for none
	do    func() tea.Cmd
	// token: the button needs an API token. Without one it is shown
	// dimmed, and pressing it says so and offers the ways to get one.
	token bool
	off   string // why it is off ("" when it works)
}

// actioner is a section with buttons: the ones that apply right now.
type actioner interface{ actions() []button }

// downExiter is a section that says when ↓ is at the end of its page,
// so ↓ goes on to the buttons.
type downExiter interface{ downExits() bool }

// pageActions are the active page's buttons (none under an overlay).
func (h *home) pageActions() []button {
	if h.overlay() {
		return nil
	}
	if a, ok := h.active().(actioner); ok {
		acts := a.actions()
		if h.signedOut {
			for i := range acts {
				if acts[i].token {
					b := &acts[i]
					b.off = b.label + " needs an API token"
					next := b.do
					b.do = func() tea.Cmd { return h.askToken(h.activeID(), b.label+" needs an API token.", next) }
				}
			}
		}
		return acts
	}
	return nil
}

// actionRowsNow is how many rows the buttons take under the page: a
// blank row, then the buttons (on two rows when they do not fit one).
func (h *home) actionRowsNow() int {
	acts := h.pageActions()
	if len(acts) == 0 {
		return 0
	}
	w, _ := h.paneSize()
	return 1 + len(buttonRows(acts, w))
}

// buttonRows splits the buttons into rows that fit w cells.
func buttonRows(acts []button, w int) [][]int {
	var rows [][]int
	var cur []int
	used := 0
	for i, b := range acts {
		n := len([]rune(b.text())) + 4
		if len(cur) > 0 && used+2+n > w {
			rows = append(rows, cur)
			cur, used = nil, 0
		}
		if len(cur) > 0 {
			used += 2
		}
		cur = append(cur, i)
		used += n
	}
	return append(rows, cur)
}

func (b button) text() string {
	if b.key != "" {
		return b.label + " (" + b.key + ")"
	}
	return b.label
}

// buttonsLine draws the buttons; the focused one is highlighted while
// the buttons have the keys.
func (h *home) buttonsLine(acts []button, w int) string {
	h.btnCur = max(0, min(len(acts)-1, h.btnCur))
	focused := h.btnActive && h.pageHasKeys()
	var lines []string
	for _, row := range buttonRows(acts, w) {
		var parts []string
		for _, i := range row {
			b := acts[i]
			label := "[ " + b.text() + " ]"
			if b.off != "" {
				label = h.st.Dim.Render(label)
			}
			if focused && i == h.btnCur {
				label = "[>" + b.text() + "<]"
				if h.color {
					label = h.st.Bold.Reverse(true).Render(" " + b.text() + " ")
				}
			}
			parts = append(parts, h.mark(label, func() tea.Cmd {
				h.focus = focusContent
				h.btnActive, h.btnCur = true, i
				return b.do()
			}))
		}
		lines = append(lines, truncate(strings.Join(parts, "  "), w))
	}
	return strings.Join(lines, "\n")
}

// buttonKey handles a key while the buttons have the keys; ok is false
// when it is not theirs.
func (h *home) buttonKey(s string, acts []button) (tea.Cmd, bool) {
	h.btnCur = max(0, min(len(acts)-1, h.btnCur))
	switch s {
	case "left", "shift+tab":
		if h.btnCur > 0 {
			h.btnCur--
		} else {
			h.btnActive = false
		}
		return nil, true
	case "right", "tab":
		if h.btnCur < len(acts)-1 {
			h.btnCur++
		} else if s == "tab" {
			h.btnActive = false
		}
		return nil, true
	case "up", "esc":
		h.btnActive = false
		return nil, true
	case "down":
		return h.setFlash("These are the page's buttons · ↑ goes back to the page"), true
	case "enter":
		return h.pressButton(acts[h.btnCur]), true
	}
	return nil, false
}

// pressButton runs a button. When it opens another page (the buttons
// change), the keys go to that page.
func (h *home) pressButton(b button) tea.Cmd {
	before := h.pageActions()
	cmd := h.wrap(h.activeID(), b.do())
	if after := h.pageActions(); !sameButtons(before, after, b.label) {
		h.btnActive = false
	}
	return cmd
}

// sameButtons reports whether a page kept its buttons, but for the one
// pressed (Show becomes Hide).
func sameButtons(a, b []button, pressed string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].label != b[i].label && a[i].label != pressed {
			return false
		}
	}
	return true
}

// accelerator presses the button whose letter is s.
func (h *home) accelerator(s string, acts []button) (tea.Cmd, bool) {
	for i, b := range acts {
		if b.key != "" && b.key == s || s == "ctrl+z" && b.label == "Undo" {
			h.btnCur = i
			return h.pressButton(b), true
		}
	}
	return nil, false
}

// buttonHints are the footer while the buttons have the keys.
func (h *home) buttonHints(acts []button) []keyHelp {
	b := acts[max(0, min(len(acts)-1, h.btnCur))]
	k := []keyHelp{{"←/→", "buttons"}, {"enter", strings.ToLower(b.label)}, {"↑/esc", "back to the page"}}
	if b.off != "" {
		k[1] = keyHelp{"enter", "get an API token first"}
	}
	if b.key != "" {
		k = append(k, keyHelp{b.key, strings.ToLower(b.label)})
	}
	return k
}

// actionKeys are the buttons' letters, for the keys a page takes.
func actionKeys(acts []button) []keyHelp {
	var k []keyHelp
	for _, b := range acts {
		if b.key != "" {
			k = append(k, keyHelp{b.key, strings.ToLower(b.label)})
		}
	}
	return k
}

// focusButton puts the keys on the page's button with this label.
func (h *home) focusButton(label string) {
	for i, b := range h.pageActions() {
		if b.label == label {
			h.focus = focusContent
			h.btnActive, h.btnCur = true, i
		}
	}
}
