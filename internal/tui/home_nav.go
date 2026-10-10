package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// Moving around interactive mode is one hierarchy, laid out the way the
// screen is:
//
//	1 the sidebar: the sections
//	2 a section's tab bar, for sections with tabs
//	3 the page under it: a list, a form, or text
//	4 what is opened from the page: details, an edit, a drill-down
//
// ↑/↓ move within a level. → and enter go one level in; ← and esc come
// one level back (← moves the cursor inside typed text first). On a tab
// bar ←/→ switch tabs, and ↑ from the top of a page goes to its tab bar.
// Every key changes something on the screen; a key that does nothing
// where it is pressed says so in the footer.

// tipText is the first-run line: the whole model in one sentence.
const tipText = "Menu on the left, page on the right. Arrows move, Enter goes in, Esc comes back."

// tabbedSection is a section with tabs: a tab bar above its page.
type tabbedSection interface {
	tabNames() []string
	tabIndex() int
	// selectTab opens tab i.
	selectTab(i int) tea.Cmd
}

// upExiter is a section that says when its page is at the top, so ↑
// goes to the tab bar.
type upExiter interface{ upExits() bool }

// crumber is a section that names what is open on its page, for the
// breadcrumb ("Microphone", "Result", "audd recognize").
type crumber interface{ crumbs() []string }

// tabs is the active section's tab bar, nil when it has none.
func (h *home) tabs() tabbedSection {
	if h.showSignin {
		return nil
	}
	if t, ok := h.active().(tabbedSection); ok && len(t.tabNames()) > 1 {
		return t
	}
	return nil
}

func upExits(s section) bool {
	if u, ok := s.(upExiter); ok {
		return u.upExits()
	}
	return false
}

// selectTab opens tab i of the active section.
func (h *home) selectTab(i int) tea.Cmd {
	t := h.tabs()
	if t == nil {
		return nil
	}
	i = max(0, min(len(t.tabNames())-1, i))
	return h.wrap(h.activeID(), t.selectTab(i))
}

// stepTab is [ and ]: the previous or next tab, from any level of the
// section; the keys go to the tab bar.
func (h *home) stepTab(d int) tea.Cmd {
	t := h.tabs()
	if t == nil {
		return h.noop(map[int]string{-1: "[", 1: "]"}[d])
	}
	i, n := t.tabIndex(), len(t.tabNames())
	h.focus = focusTabs
	switch {
	case i+d < 0:
		return h.setFlash(t.tabNames()[i] + " is the first tab")
	case i+d >= n:
		return h.setFlash(t.tabNames()[i] + " is the last tab")
	}
	return h.selectTab(i + d)
}

// pageHasKeys reports whether the section's page has the keys (not the
// sidebar, the tab bar, or something shown over the page).
func (h *home) pageHasKeys() bool {
	return h.focus == focusContent && !h.overlay()
}

// overlay reports whether something is shown over the section: the
// palette, the keys, a confirmation.
func (h *home) overlay() bool {
	return h.paletteO || h.keysO || h.ask != nil || h.quitAsk
}

// hasKeys reports whether a list or form has the keys: one in the
// palette while the palette is open, one on a section's page while the
// page has them.
func (h *home) hasKeys(inPalette bool) bool {
	if h == nil {
		return true
	}
	if inPalette {
		return h.paletteO && h.ask == nil && !h.quitAsk
	}
	return h.pageHasKeys()
}

func (h *home) key(k tea.KeyMsg) tea.Cmd {
	s := k.String()
	if h.ask != nil {
		return h.askKey(k)
	}
	if h.quitAsk {
		return h.quitKey(s)
	}
	if s == "ctrl+c" {
		return h.quit()
	}
	if h.keysO {
		h.keysO = false
		if s == "f1" || s == "enter" {
			return h.navigate("help")
		}
		return nil
	}
	if h.paletteO {
		if s == "esc" || s == "left" && h.palette.leftExits() {
			h.seenTip()
			if !h.palette.back() {
				h.paletteO = false
			}
			return nil
		}
		if s == "ctrl+k" {
			h.paletteO = false
			return nil
		}
		if !h.palette.capturing() && s == "?" {
			h.keysO = true
			return nil
		}
		return h.send("palette", k)
	}
	switch s {
	case "ctrl+k":
		return h.openPalette("")
	case "f1":
		return h.navigate("help")
	}
	switch h.focus {
	case focusSidebar:
		return h.sidebarKey(k)
	case focusTabs:
		return h.tabsKey(k)
	}
	return h.pageKey(k)
}

// globalKey handles the keys that work wherever nothing is being typed.
func (h *home) globalKey(s string) (tea.Cmd, bool) {
	switch s {
	case ":":
		return h.openPalette(""), true
	case "?":
		h.keysO = true
		return nil, true
	case "q":
		return h.quit(), true
	case "y":
		if c := h.command(); c != "" {
			return h.copy(c, "the command: "+c), true
		}
		return h.setFlash("No command to copy here"), true
	case "1", "2", "3", "4", "5", "6", "7":
		return h.navigate(h.order[s[0]-'1']), true
	}
	return nil, false
}

// sidebarKey: ↑/↓ move between sections, → or enter opens one.
func (h *home) sidebarKey(k tea.KeyMsg) tea.Cmd {
	s := k.String()
	if cmd, ok := h.globalKey(s); ok {
		return cmd
	}
	h.backTo = nil
	switch s {
	case "down", "j":
		if h.showSignin {
			h.showSignin = false
		} else {
			h.cur = (h.cur + 1) % len(h.order)
		}
		return h.activate()
	case "up", "k":
		if h.showSignin {
			h.showSignin = false
		} else {
			h.cur = (h.cur + len(h.order) - 1) % len(h.order)
		}
		return h.activate()
	case "enter", "right", "l", "tab":
		if h.showSignin {
			h.focus = focusContent
		} else {
			h.focus = h.entryFocus()
		}
		return h.activate()
	case "esc", "left", "h":
		h.seenTip()
		return h.setFlash("This is the menu, the top level · → opens a section, q quits")
	}
	return h.noop(s)
}

// tabsKey: ←/→ switch tabs, ↓ or enter goes into the page, ← on the
// first tab or esc goes back.
func (h *home) tabsKey(k tea.KeyMsg) tea.Cmd {
	t := h.tabs()
	if t == nil {
		h.focus = focusContent
		return h.pageKey(k)
	}
	s := k.String()
	if cmd, ok := h.globalKey(s); ok {
		return cmd
	}
	i, n := t.tabIndex(), len(t.tabNames())
	switch s {
	case "left", "h", "shift+tab":
		if i > 0 {
			return h.selectTab(i - 1)
		}
		if s == "shift+tab" {
			return h.setFlash(t.tabNames()[i] + " is the first tab")
		}
		return h.backOne()
	case "right", "l", "tab":
		if i < n-1 {
			return h.selectTab(i + 1)
		}
		return h.setFlash(t.tabNames()[i] + " is the last tab · ↓ opens it")
	case "[":
		return h.stepTab(-1)
	case "]":
		return h.stepTab(1)
	case "down", "j", "enter":
		h.focus = focusContent
		return nil
	case "esc":
		return h.backOne()
	case "up", "k":
		return h.setFlash("This is the tab bar · ↓ opens the page, esc goes back")
	}
	if h.sectionKnows(s) || k.Type == tea.KeyRunes && h.active().capturing() {
		// A section's own key (a to add a stream) works from its tab bar
		// too, on the page; so does typing into a page that takes text
		// (the filter of Commands).
		h.focus = focusContent
		return h.send(h.activeID(), k)
	}
	return h.noop(s)
}

// pageKey handles a key on the section's page (level 3 and deeper).
func (h *home) pageKey(k tea.KeyMsg) tea.Cmd {
	s := k.String()
	cur := h.active()
	typing := cur.capturing()
	if s == "esc" || s == "left" && leftExits(cur) {
		return h.backOne()
	}
	if (s == "up" || s == "k" && !typing) && h.tabs() != nil && upExits(cur) {
		h.focus = focusTabs
		return nil
	}
	if typing {
		return h.send(h.activeID(), k)
	}
	if cmd, ok := h.globalKey(s); ok {
		return cmd
	}
	switch s {
	case "[":
		return h.stepTab(-1)
	case "]":
		return h.stepTab(1)
	}
	if !h.sectionKnows(s) {
		return h.noop(s)
	}
	return h.send(h.activeID(), k)
}

// backOne is esc: exactly one level up. Something opened on the page
// closes; then the keys go to the tab bar; then back to where a jump
// came from, or to the sidebar.
func (h *home) backOne() tea.Cmd {
	h.seenTip()
	if h.focus == focusContent {
		if h.active().back() {
			return nil
		}
		if h.tabs() != nil {
			h.focus = focusTabs
			return nil
		}
	}
	return h.goBack()
}

// noop says that a key does nothing where it was pressed.
func (h *home) noop(s string) tea.Cmd {
	return h.setFlash(keyName(s) + " does nothing here · ? shows the keys")
}

func keyName(s string) string {
	switch s {
	case " ":
		return "space"
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	}
	return s
}

// navKeys are the keys every page takes for moving around; a section
// gets them even when its hints do not name them.
var navKeys = map[string]bool{
	"up": true, "down": true, "left": true, "right": true, "enter": true, "esc": true,
	"tab": true, "shift+tab": true, "pgup": true, "pgdown": true, "home": true, "end": true,
	"backspace": true, "delete": true, "ctrl+d": true, "ctrl+u": true, " ": true,
	"j": true, "k": true, "g": true, "G": true,
}

// sectionKnows reports whether the active section does something with
// s: a key for moving around, or one its hints or the Keys page name.
func (h *home) sectionKnows(s string) bool {
	if navKeys[s] {
		return true
	}
	cur := h.active()
	for _, k := range cur.keys() {
		if keyMatches(k.key, s) {
			return true
		}
	}
	for _, d := range sectionKeyDocs {
		if d.section != cur.title() {
			continue
		}
		for _, k := range d.keys {
			if keyMatches(k.key, s) {
				return true
			}
		}
	}
	return false
}

// keyMatches reports whether the key s is one of those a hint names:
// "o, c, d", "n N", "s or esc", "↑/↓", "/".
func keyMatches(spec, s string) bool {
	for _, tok := range keyTokens(spec) {
		if tok == s {
			return true
		}
	}
	return false
}

func keyTokens(spec string) []string {
	if spec == "/" {
		return []string{"/"}
	}
	var out []string
	for _, f := range strings.Fields(strings.ReplaceAll(spec, ",", " ")) {
		parts := []string{f}
		if len(f) > 1 && strings.Contains(f, "/") {
			parts = strings.Split(f, "/")
		}
		for _, p := range parts {
			switch p {
			case "↑":
				p = "up"
			case "↓":
				p = "down"
			case "←":
				p = "left"
			case "→":
				p = "right"
			case "space":
				p = " "
			case "pgdn":
				p = "pgdown"
			case "or", "":
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

// seenTip hides the first-run tip for good: esc was used, so the model
// is known.
func (h *home) seenTip() {
	if !h.tip {
		return
	}
	h.tip = false
	if h.a.Profile != nil && h.a.Cfg != nil {
		h.a.Profile.UITipSeen = true
		_ = h.a.Cfg.Save()
	}
}

// quit is q and ctrl+c: right away, or after asking while a command
// that may spend requests is running.
func (h *home) quit() tea.Cmd {
	if h.runningCommand() != "" {
		h.quitAsk = true
		return nil
	}
	return tea.Quit
}

func (h *home) quitKey(s string) tea.Cmd {
	h.quitAsk = false
	switch s {
	case "y", "Y", "ctrl+c":
		return tea.Quit
	}
	return h.setFlash("Still running · it goes on in its section")
}

// runningCommand is a command in flight that may spend requests ("" for
// none).
func (h *home) runningCommand() string {
	h.runs.mu.Lock()
	defer h.runs.mu.Unlock()
	best := 0
	for id := range h.runs.spending {
		best = max(best, id)
	}
	return h.runs.spending[best]
}

func (h *home) quitView(w int) string {
	var b strings.Builder
	b.WriteString(h.st.Bold.Render("Quit audd?") + "\n\n")
	b.WriteString("This is still running:\n\n  " + h.st.Accent.Render("$ "+h.runningCommand()) + "\n\n")
	b.WriteString(styleLines(h.st.Warn, output.Wrap("Quitting stops it. Files a batch already finished are kept, and audd jobs resume continues the rest.", w)) + "\n\n")
	b.WriteString("Press y to quit, any other key to keep it running.")
	return b.String()
}

// crumb is one part of the breadcrumb; click runs when it is clicked.
type crumb struct {
	text  string
	click func() tea.Cmd
}

// crumbs is the path to what is shown, and the index of the part that
// has the keys (-1: the page itself, which has no name of its own).
func (h *home) crumbs() ([]crumb, int) {
	switch {
	case h.ask != nil:
		return []crumb{{text: "Confirm"}}, 0
	case h.quitAsk:
		return []crumb{{text: "Quit?"}}, 0
	case h.keysO:
		return []crumb{{text: "Keys"}}, 0
	case h.paletteO:
		out := []crumb{{text: "Commands"}}
		for _, c := range h.palette.crumbs() {
			out = append(out, crumb{text: c})
		}
		return out, len(out) - 1
	}
	cur := h.active()
	var out []crumb
	if h.showSignin {
		out = append(out, crumb{text: cur.title()})
	} else {
		out = append(out, crumb{text: cur.title(), click: func() tea.Cmd {
			h.toSidebar()
			return nil
		}})
	}
	base := 1
	if t := h.tabs(); t != nil {
		out = append(out, crumb{text: t.tabNames()[t.tabIndex()], click: func() tea.Cmd {
			h.focus = focusTabs
			return nil
		}})
		base++
	}
	if c, ok := cur.(crumber); ok {
		for _, s := range c.crumbs() {
			if s != "" {
				out = append(out, crumb{text: s})
			}
		}
	}
	if h.showSignin {
		return out, len(out) - 1
	}
	switch h.focus {
	case focusSidebar:
		return out, 0
	case focusTabs:
		return out, 1
	}
	if len(out) > base {
		return out, len(out) - 1
	}
	return out, -1
}

// crumbLine is the breadcrumb, with the part that has the keys in the
// accent color (between > and < without color).
func (h *home) crumbLine(w int) string {
	parts, at := h.crumbs()
	sep := h.st.Dim.Render(" › ")
	var out []string
	for i, c := range parts {
		label := c.text
		switch {
		case i == at && h.color:
			label = h.st.Bold.Inherit(h.st.Accent).Render(label)
		case i == at:
			label = ">" + label + "<"
		case h.color:
			label = h.st.Dim.Render(label)
		}
		out = append(out, h.mark(label, c.click))
	}
	return truncate(strings.Join(out, sep), w)
}

// tabBar is the active section's tabs. The current tab is highlighted;
// in reverse video (between > and < without color) while the bar has
// the keys. A click opens a tab and puts the keys on the bar.
func (h *home) tabBar(t tabbedSection, w int) string {
	names, cur := t.tabNames(), t.tabIndex()
	focused := h.focus == focusTabs && !h.overlay()
	var parts []string
	for i, n := range names {
		var label string
		switch {
		case i == cur && focused && h.color:
			label = h.st.Bold.Reverse(true).Render(" " + n + " ")
		case i == cur && focused:
			label = ">" + n + "<"
		case i == cur && h.color:
			label = h.st.Bold.Inherit(h.st.Accent).Render(" " + n + " ")
		case i == cur:
			label = "[" + n + "]"
		case h.color:
			label = h.st.Dim.Render(" " + n + " ")
		default:
			label = " " + n + " "
		}
		parts = append(parts, h.mark(label, func() tea.Cmd {
			h.paletteO = false
			h.focus = focusTabs
			if i == t.tabIndex() {
				return nil
			}
			return t.selectTab(i)
		}))
	}
	return truncate(strings.Join(parts, ""), w)
}

// headRowsNow is how many rows the breadcrumb and the tab bar take.
func (h *home) headRowsNow() int {
	if !h.overlay() && h.tabs() != nil {
		return 2
	}
	return 1
}

// footer is what can be done right now, in the same order everywhere:
// move, open, back, the section's own keys, then ? for the rest.
func (h *home) footer() []keyHelp {
	switch {
	case h.ask != nil, h.quitAsk:
		return nil
	case h.keysO:
		return []keyHelp{{"any key", "close"}, {"F1", "full help"}}
	case h.paletteO:
		return composeKeys(h.palette.keys(), h.palette.leftExits(), h.palette.capturing())
	case h.focus == focusSidebar:
		return []keyHelp{{"↑/↓", "move"}, {"→", "open"}, {"1-7", "jump"}, {"ctrl+k", "commands"}, {"q", "quit"}, {"?", "keys"}}
	case h.focus == focusTabs && h.tabs() != nil:
		k := []keyHelp{{"←/→", "tabs"}, {"↓", "open"}, {"esc", "back"}}
		_, _, _, acts := sortKeys(h.active().keys())
		k = append(k, acts[:min(2, len(acts))]...)
		return append(k, keyHelp{"?", "keys"})
	}
	cur := h.active()
	return composeKeys(cur.keys(), leftExits(cur), cur.capturing())
}

// sortKeys splits a section's hints into moving, opening, going back,
// and its own actions.
func sortKeys(keys []keyHelp) (move, open, back, acts []keyHelp) {
	for _, k := range keys {
		switch {
		case strings.Contains(k.key, "esc"):
			back = append(back, k)
		case k.key == "type" || strings.HasPrefix(k.key, "↑") || strings.HasPrefix(k.key, "↓") || strings.HasPrefix(k.key, "←/→"):
			move = append(move, k)
		case strings.HasPrefix(k.key, "→") || strings.HasPrefix(k.key, "enter"):
			open = append(open, k)
		default:
			acts = append(acts, k)
		}
	}
	return
}

// composeKeys orders a page's hints and adds how to go back: ← and esc
// when left goes back too.
func composeKeys(keys []keyHelp, left, typing bool) []keyHelp {
	move, open, back, acts := sortKeys(keys)
	b := keyHelp{"esc", "back"}
	if len(back) > 0 {
		b = back[0]
	}
	if left && b.key == "esc" {
		b.key = "←/esc"
	}
	out := append(append(append([]keyHelp{}, move...), open...), b)
	out = append(out, acts...)
	if len(out) > 5 {
		out = out[:5]
	}
	if !typing {
		out = append(out, keyHelp{"?", "keys"})
	}
	return out
}

// listRow is a row of a simple list. The selected row has a › and is
// bold while the page has the keys; otherwise it keeps a dim · so the
// place is not lost.
func (h *home) listRow(label string, selected bool) string {
	switch {
	case !selected:
		return "  " + label
	case h.pageHasKeys():
		return h.st.Accent.Render("›") + " " + h.st.Bold.Render(label)
	}
	return h.st.Dim.Render("·") + " " + label
}
