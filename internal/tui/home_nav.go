package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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

// tipKeysMax is how many keys the tip stays for (esc hides it sooner).
const tipKeysMax = 12

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
	return h.paletteO || h.keysO || h.prompt()
}

// prompt reports whether a question waits for an answer: a command's
// confirmation, one of the screen's own, or quitting.
func (h *home) prompt() bool {
	return h.ask != nil || h.confirm != nil
}

// hasKeys reports whether a list or form has the keys: one in the
// palette while the palette is open, one on a section's page while the
// page has them.
func (h *home) hasKeys(inPalette bool) bool {
	if h == nil {
		return true
	}
	if inPalette {
		return h.paletteO && !h.prompt()
	}
	return h.pageHasKeys()
}

func (h *home) key(k tea.KeyMsg) tea.Cmd {
	s := k.String()
	// Messages in the status line last until the next key (lastFlash
	// keeps it for this key: u undoes what it says).
	h.lastFlash, h.flash = h.flash, ""
	if h.tip {
		// The tip goes once the keys are in use.
		if h.tipKeys++; h.tipKeys >= tipKeysMax {
			h.seenTip()
		}
	}
	if h.ask != nil {
		return h.askKey(k)
	}
	if h.confirm != nil {
		return h.confirmKey(s)
	}
	if s == "ctrl+c" {
		return h.quit(true)
	}
	if h.keysO {
		return h.keysKey(s)
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
			// ctrl+k in a command's form goes back to the list; on the
			// list it closes the palette, and the keys are back where
			// they were.
			if h.palette.phase != "list" {
				h.palette.phase = "list"
				h.palette.picker.filter.SetValue("")
				h.palette.picker.cursor = 0
				return h.palette.picker.filter.Focus()
			}
			h.paletteO = false
			return nil
		}
		if !h.palette.capturing() {
			switch {
			case s == "?":
				h.keysO = true
				return nil
			case s == "y":
				if c := h.command(); c != "" {
					return h.copy(c, "the command: "+c)
				}
				return h.setFlash("No command to copy here")
			case !navKeys[s] && !listed(h.palette.keys(), s):
				return h.noop(s)
			}
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
		h.keysScroll.off = 0
		return nil, true
	case "q":
		return h.quit(false), true
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
	switch s {
	case "up", "down", "enter", "right":
	default:
		h.sideBtn = 0
	}
	if cmd, ok := h.globalKey(s); ok {
		return cmd
	}
	h.backTo = nil
	if h.sideBtn > 0 {
		// On the Commands or Keys button under the menu.
		switch s {
		case "down":
			h.sideBtn = min(2, h.sideBtn+1)
			return nil
		case "up":
			h.sideBtn--
			return nil
		case "enter", "right":
			b := h.sideBtn
			h.sideBtn = 0
			if b == 1 {
				return h.openPalette("")
			}
			h.keysO, h.keysScroll.off = true, 0
			return nil
		}
	}
	switch s {
	case "down":
		switch {
		case h.showSignin:
			h.showSignin = false
		case h.cur == len(h.order)-1:
			// Past the last section: the buttons under the menu (no
			// wrapping to the top).
			h.sideBtn = 1
			return nil
		default:
			h.cur++
		}
		return h.activate()
	case "up":
		if h.showSignin {
			h.showSignin = false
		} else {
			h.cur = max(0, h.cur-1)
		}
		return h.activate()
	case "enter", "right":
		if h.showSignin {
			h.focus = focusContent
		} else {
			h.focus = h.entryFocus()
		}
		cmd := h.activate()
		h.pageEntered()
		return cmd
	case "esc", "left":
		h.seenTip()
		return h.setFlash("This is the menu, the top level · → opens a section, q quits")
	}
	return h.noop(s)
}

// tabsKey: ←/→ switch tabs, ↓ or enter goes into the page, ← on the
// first tab or esc goes back. A section's own keys work on its page
// only, never from here.
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
	case "left", "shift+tab":
		if i > 0 {
			return h.selectTab(i - 1)
		}
		if s == "shift+tab" {
			return h.setFlash(t.tabNames()[i] + " is the first tab")
		}
		return h.backOne()
	case "right", "tab":
		if i < n-1 {
			return h.selectTab(i + 1)
		}
		return h.setFlash(t.tabNames()[i] + " is the last tab · ↓ opens it")
	case "[":
		return h.stepTab(-1)
	case "]":
		return h.stepTab(1)
	case "down", "enter":
		h.focus = focusContent
		h.pageEntered()
		return nil
	case "esc":
		return h.backOne()
	case "up":
		return h.setFlash("This is the tab bar · ↓ opens the page, esc goes back")
	}
	if cmd, ok := h.accelerator(s, h.pageActions()); ok {
		// A letter shown on a visible button works from here too.
		h.focus = focusContent
		return cmd
	}
	return h.noop(s)
}

// pageKey handles a key on the section's page (level 3 and deeper). A
// key does something only when it moves around or the page lists it.
func (h *home) pageKey(k tea.KeyMsg) tea.Cmd {
	s := k.String()
	cur := h.active()
	typing := cur.capturing()
	acts := h.pageActions()
	if len(acts) == 0 {
		h.btnActive = false
	}
	if h.btnActive && !typing {
		if cmd, ok := h.buttonKey(s, acts); ok {
			return cmd
		}
	}
	if !typing && len(acts) > 0 {
		if cmd, ok := h.accelerator(s, acts); ok {
			return cmd
		}
		if p, ok := cur.(enterPresser); ok && s == "enter" && p.enterPresses() {
			// A page whose one action is its button: enter presses it,
			// and the keys stay on it.
			h.btnActive, h.btnCur = true, 0
			return h.pressButton(acts[0])
		}
		d, isD := cur.(downExiter)
		if s == "tab" || s == "down" && isD && d.downExits() {
			h.btnActive, h.btnCur = true, 0
			return nil
		}
	}
	if s == "esc" || s == "left" && leftExits(cur) {
		return h.backOne()
	}
	if s == "up" && upExits(cur) {
		// ↑ at the top of a page: its tab bar, or back to the menu.
		if h.tabs() != nil {
			h.focus = focusTabs
		} else {
			h.focus = focusSidebar
		}
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

// enterPresser is a page whose enter presses its first button.
type enterPresser interface{ enterPresses() bool }

// pageEnterer is a section with forms: when its page gets the keys, the
// cursor's text field takes typing again (esc may have left it).
type pageEnterer interface{ pageEntered() }

// pageEntered tells the active section its page has the keys.
func (h *home) pageEntered() {
	if h.focus != focusContent {
		return
	}
	if p, ok := h.active().(pageEnterer); ok {
		p.pageEntered()
	}
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
	if s == "u" || s == "ctrl+z" {
		return h.setFlash("Nothing to undo")
	}
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
// gets them even when its hints do not name them. No other key acts
// unless the page lists it.
var navKeys = map[string]bool{
	"up": true, "down": true, "left": true, "right": true, "enter": true, "esc": true,
	"tab": true, "shift+tab": true, "pgup": true, "pgdown": true, "home": true, "end": true,
}

// sectionKnows reports whether the page does something with s: a key
// for moving around, or one of the keys it lists (the footer shows the
// first of them; ? shows them all).
func (h *home) sectionKnows(s string) bool {
	if navKeys[s] {
		return true
	}
	if listed(actionKeys(h.pageActions()), s) {
		return true
	}
	return listed(h.active().keys(), s)
}

// listed reports whether hints name the key s.
func listed(keys []keyHelp, s string) bool {
	for _, k := range keys {
		if keyMatches(k.key, s) {
			return true
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

// quit is q and ctrl+c: right away, or after asking when something
// would be lost (a command running, what was typed and not saved).
// From the menu or a tab bar only a run in progress asks.
func (h *home) quit(ctrlC bool) tea.Cmd {
	lost := h.lostOnQuit()
	if len(lost) == 0 {
		return tea.Quit
	}
	h.confirm = &confirmState{
		owner:    h.activeID(),
		question: "Quit?",
		notes:    append(lost, "ctrl+c again quits."),
		action:   "Quit",
		topic:    "Quit",
		cancel:   "Not quitting.",
		quit:     true,
		yes:      func() tea.Cmd { return tea.Quit },
	}
	if !ctrlC {
		h.confirm.notes = lost
	}
	return nil
}

// unsaveder is a section that can say what is typed and not saved
// ("" when nothing).
type unsaveder interface{ unsaved() string }

// lostOnQuit is what quitting now loses, in words.
func (h *home) lostOnQuit() []string {
	var lost []string
	if c := h.runningCommand(); c != "" {
		lost = append(lost, "This is still running: $ "+h.withSession(c)+". Quitting stops it; a batch keeps the files it finished, and audd jobs resume continues the rest.")
	}
	if h.focus == focusContent || h.paletteO {
		if u, ok := h.active().(unsaveder); ok {
			if s := u.unsaved(); s != "" {
				lost = append(lost, s+" is not saved.")
			}
		}
	}
	return lost
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

// crumb is one part of the breadcrumb; click runs when it is clicked.
type crumb struct {
	text  string
	click func() tea.Cmd
}

// crumbs is the path to what is shown, and the index of the part that
// has the keys (-1: the page itself, which has no name of its own).
func (h *home) crumbs() ([]crumb, int) {
	switch {
	case h.ask != nil, h.confirm != nil:
		// The path stays, and ends with what is asked: "Streams ›
		// Stream 2 › Remove?".
		ask, c := h.ask, h.confirm
		h.ask, h.confirm = nil, nil
		out, _ := h.crumbs()
		h.ask, h.confirm = ask, c
		for i := range out {
			out[i].click = nil
		}
		topic := "Confirm"
		switch {
		case c != nil && c.topic != "":
			topic = c.topic
		case c != nil && c.action != "":
			topic = c.action
		case ask != nil:
			topic = ask.action()
		}
		// A path, not a question: no "?".
		return append(out, crumb{text: strings.TrimSuffix(topic, "?")}), len(out)
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
		case !h.color:
			// Without color the focused item has its own marker (the
			// sidebar item, the tab, the row); the path stays plain.
		case h.color:
			label = h.st.Dim.Render(label)
		}
		out = append(out, h.mark(label, c.click))
	}
	line := strings.Join(out, sep)
	if ansi.StringWidth(line) > w && len(parts) > 2 {
		// Too long: the middle levels give way, the first and the last
		// stay readable.
		plain := make([]string, len(parts))
		for i, c := range parts {
			plain[i] = c.text
		}
		for len(plain) > 2 && ansi.StringWidth(strings.Join(plain, " › ")) > w {
			if plain[1] == "…" && len(plain) > 3 {
				plain = append(plain[:1], plain[2:]...)
				plain[1] = "…"
				continue
			}
			if plain[1] == "…" {
				break
			}
			plain[1] = "…"
		}
		line = h.st.Dim.Render(strings.Join(plain[:len(plain)-1], " › ") + " › ")
		last := plain[len(plain)-1]
		if room := w - ansi.StringWidth(line); room < len([]rune(last)) {
			last = truncMiddle(last, max(5, room))
		}
		line += last
	}
	return truncate(line, w)
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
	case h.ask != nil && h.ask.typed != "":
		return []keyHelp{{"type", h.ask.typed}, {"enter", "confirm"}, {"esc", "cancel"}}
	case h.confirm != nil && h.confirm.choices != nil:
		return []keyHelp{{"←/→/tab", "choose"}, {"enter", "press"}, {"esc", "cancel"}}
	case h.prompt():
		return []keyHelp{{"←/→/tab", "choose"}, {"enter", "press the selected button"}, {"n/esc", "cancel"}, {"y", "copy the command"}}
	case h.keysO:
		return []keyHelp{{"↑/↓", "scroll"}, {"esc", "close"}, {"F1", "the Help section"}}
	case h.paletteO:
		return composeKeys(h.palette.keys(), h.palette.leftExits(), h.palette.capturing())
	case h.focus == focusSidebar:
		return []keyHelp{{"↑/↓", "move"}, {"→", "open"}, {"1-7", "jump"}, {"ctrl+k", "commands"}, {"q", "quit"}, {"?", "keys"}}
	case h.focus == focusTabs && h.tabs() != nil:
		// The page's own keys work on the page, so they are not listed
		// here.
		return []keyHelp{{"←/→", "tabs"}, {"↓", "open"}, {"esc", "back"}, {"?", "keys"}}
	}
	cur := h.active()
	acts := h.pageActions()
	if h.btnActive && len(acts) > 0 {
		return append(h.buttonHints(acts), keyHelp{"?", "keys"})
	}
	keys := cur.keys()
	if len(acts) > 0 && !cur.capturing() {
		keys = append(keys, keyHelp{"tab", "buttons"})
	}
	return composeKeys(keys, leftExits(cur), cur.capturing())
}

// fitKeys lays out hints in w columns: the ones that do not fit are
// left out from the end, but ? keys always stays.
func fitKeys(keys []keyHelp, w int) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k.key+" "+k.help)
	}
	for len(parts) > 1 && ansi.StringWidth(strings.Join(parts, "  ")) > w {
		last := len(parts) - 1
		if keys[last].key == "?" && len(parts) > 2 {
			parts = append(parts[:last-1], parts[last])
			keys = append(keys[:last-1], keys[last])
			continue
		}
		parts, keys = parts[:last], keys[:last]
	}
	return strings.Join(parts, "  ")
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
	if typing {
		b.help = "done"
	}
	if len(back) > 0 {
		b = back[0]
	}
	leftShown := false
	for _, k := range keys {
		if strings.Contains(k.key, "←") {
			leftShown = true
		}
	}
	if left && b.key == "esc" && !typing && !leftShown {
		b.key = "←/esc"
	}
	out := append(append(append([]keyHelp{}, move...), open...), b)
	if typing {
		out = append(out, keyHelp{"ctrl+u", "clear"})
	}
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

// confirmState is a question the screen asks before something that
// spends requests, cannot be undone, or changes a saved value. It is a
// dialog: the question, what happens, and buttons named for what they
// do, [ Cancel ] first and focused. It takes every key until answered.
type confirmState struct {
	owner    string   // the section the answer goes to
	question string   // "Remove stream 3?"
	notes    []string // what happens, in plain words
	command  string   // the command it runs
	action   string   // the button that does it: "Remove stream"
	yes      func() tea.Cmd
	cancel   string // said when the answer is no
	// choices, when set, are buttons of their own after Cancel (export
	// as CSV or JSON); pick gets the index of the one pressed.
	choices []string
	pick    func(i int) tea.Cmd
	cur     int // the focused button; 0 is Cancel
	// quit: the dialog asks before quitting.
	quit bool
	// topic ends the path while the dialog shows ("Remove?").
	topic string
	// onCancel runs when the answer is no (the field keeps what was
	// typed, without typing in it).
	onCancel func()
}

// buttons are the dialog's buttons, Cancel first.
func (c *confirmState) buttons() []string {
	if c.choices != nil {
		return append([]string{"Cancel"}, c.choices...)
	}
	action := c.action
	if action == "" {
		action = "Go ahead"
	}
	return []string{"Cancel", action}
}

// askFirst opens a dialog. Every dialog starts on Cancel: a double enter
// never changes or spends anything.
func (h *home) askFirst(c *confirmState) tea.Cmd {
	c.cur = 0
	h.confirm = c
	return nil
}

// press is the dialog's button i.
func (h *home) pressConfirm(i int) tea.Cmd {
	c := h.confirm
	h.confirm = nil
	switch {
	case i == 0:
		if c.onCancel != nil {
			c.onCancel()
		}
		msg := c.cancel
		if msg == "" {
			msg = "Cancelled. Nothing was changed."
		}
		return h.setFlash(msg)
	case c.choices != nil:
		return h.wrap(c.owner, c.pick(i-1))
	case c.quit:
		return tea.Quit
	}
	return h.wrap(c.owner, c.yes())
}

func (h *home) confirmKey(s string) tea.Cmd {
	c := h.confirm
	n := len(c.buttons())
	switch s {
	case "left", "shift+tab":
		c.cur = max(0, c.cur-1)
		return nil
	case "right", "tab":
		c.cur = min(n-1, c.cur+1)
		return nil
	case "enter":
		return h.pressConfirm(c.cur)
	case "y":
		return h.copyCommand()
	case "n", "N", "esc":
		return h.pressConfirm(0)
	case "ctrl+c":
		if c.quit {
			return tea.Quit // ctrl+c twice quits
		}
		h.confirm = nil
		return h.quit(true)
	case "?":
		return h.setFlash("←/→ or tab choose · enter presses the selected button · n or esc cancels · y copies the command")
	}
	return h.setFlash(keyName(s) + " does nothing here · ←/→ choose, enter presses, esc cancels")
}

// dialogButtons draws a dialog's buttons, the focused one highlighted;
// a click presses one.
func (h *home) dialogButtons(labels []string, cur int, press func(i int) tea.Cmd, w int) string {
	var lines []string
	line, lineW := "  ", 2
	for i, l := range labels {
		label := "[ " + l + " ]"
		if i == cur {
			label = "[>" + l + "<]"
			if h.color {
				label = h.st.Bold.Reverse(true).Render(" " + l + " ")
			}
		}
		// Buttons that do not fit go on the next row, never cut.
		lw := len([]rune(l)) + 4
		if lineW > 2 && lineW+2+lw > w {
			lines = append(lines, line)
			line, lineW = "  ", 2
		}
		if lineW > 2 {
			line += "  "
			lineW += 2
		}
		line += h.mark(label, func() tea.Cmd { return press(i) })
		lineW += lw
	}
	return strings.Join(append(lines, line), "\n")
}

func (h *home) confirmView(w int) string {
	c := h.confirm
	var b strings.Builder
	// The question, what happens, the buttons; the command is on the
	// bottom line, once.
	b.WriteString(styleLines(h.st.Bold, output.Wrap(c.question, w)) + "\n\n")
	for _, n := range c.notes {
		b.WriteString(output.Wrap(n, w) + "\n")
	}
	if len(c.notes) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(h.dialogButtons(c.buttons(), c.cur, h.pressConfirm, w))
	return b.String()
}

// keysKey: the keys overlay is a page: it scrolls, and esc closes it.
func (h *home) keysKey(s string) tea.Cmd {
	_, ht := h.contentSize()
	switch s {
	case "esc", "?", "q", "left":
		h.keysO = false
		return nil
	case "f1":
		h.keysO = false
		return h.navigate("help")
	}
	if !h.keysScroll.key(s, ht) {
		return h.noop(s)
	}
	return nil
}

// copyCommand copies the command on the bottom line (y), in a dialog
// too.
func (h *home) copyCommand() tea.Cmd {
	if c := h.command(); c != "" {
		return h.copy(c, "the command: "+c)
	}
	return h.setFlash("No command to copy here")
}
