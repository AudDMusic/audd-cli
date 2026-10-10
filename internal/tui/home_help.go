package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// cmdInfo is one command of the tree, by its path without "audd".
type cmdInfo struct {
	path string
	cmd  *cobra.Command
}

// commandTree lists every command that is not hidden, in tree order
// (subcommands after their parent, by name).
func commandTree(root *cobra.Command) []cmdInfo {
	var out []cmdInfo
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		subs := append([]*cobra.Command(nil), c.Commands()...)
		sort.SliceStable(subs, func(i, j int) bool {
			if c == root {
				return false // the root keeps the help order
			}
			return subs[i].Name() < subs[j].Name()
		})
		for _, s := range subs {
			if s.Hidden || !s.IsAvailableCommand() || s.Name() == "help" {
				continue
			}
			out = append(out, cmdInfo{path: strings.TrimPrefix(s.CommandPath(), root.Name()+" "), cmd: s})
			walk(s)
		}
	}
	walk(root)
	return out
}

// visibleFlags are a command's own flags that are not hidden.
func visibleFlags(c *cobra.Command) []*pflag.Flag {
	var out []*pflag.Flag
	c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		out = append(out, f)
	})
	return out
}

// commandDoc is a command's help for the Commands page.
func commandDoc(c *cobra.Command, w int, st output.Styles) string {
	var b strings.Builder
	b.WriteString(st.Bold.Render(strings.TrimSuffix(c.UseLine(), " [flags]")) + "\n")
	if c.Short != "" {
		b.WriteString(c.Short + "\n")
	}
	if c.Long != "" {
		b.WriteString("\n" + output.Wrap(c.Long, w) + "\n")
	}
	if fl := visibleFlags(c); len(fl) > 0 {
		b.WriteString("\n" + st.Bold.Render("Flags") + "\n")
		for _, f := range fl {
			name := "--" + f.Name
			if f.Shorthand != "" {
				name = "-" + f.Shorthand + ", " + name
			}
			if t := f.Value.Type(); t != "bool" {
				name += " " + t
			}
			usage := f.Usage
			if f.DefValue != "" && f.DefValue != "[]" && f.DefValue != "false" && f.DefValue != "0" {
				usage += " (default " + f.DefValue + ")"
			}
			b.WriteString("  " + st.Accent.Render(name) + "\n")
			b.WriteString(indent(output.Wrap(usage, max(10, w-6)), "      ") + "\n")
		}
	}
	if c.Example != "" {
		b.WriteString("\n" + st.Bold.Render("Examples") + "\n" + strings.TrimRight(c.Example, "\n") + "\n")
	}
	if len(c.Commands()) > 0 {
		b.WriteString("\n" + st.Bold.Render("Commands") + "\n")
		for _, s := range c.Commands() {
			if !s.Hidden && s.IsAvailableCommand() {
				b.WriteString(fmt.Sprintf("  %-14s %s\n", s.Name(), s.Short))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

var helpPages = []string{"Getting started", "Guide", "Commands", "Keys", "Exit codes", "Links"}

// helpLinks are the Links page.
var helpLinks = []struct{ label, url string }{
	{"This guide online", "https://docs.audd.io/cli"},
	{"The AudD API docs", "https://docs.audd.io"},
	{"Your dashboard (API token, plans)", "https://dashboard.audd.io"},
	{"Report a problem", "https://github.com/AudDMusic/audd-cli/issues"},
	{"Email AudD", "mailto:api@audd.io"},
}

// gettingStarted are the steps of the first Help page.
var gettingStarted = []struct{ title, text, cmd string }{
	{"Sign in", "Sign in to your AudD account (or paste an API token) so audd can recognize music and show your account.", "audd login"},
	{"Recognize a file", "Pick a file, URL, or folder and recognize it. This fills in AudD's example song.", "audd recognize https://audd.tech/example.mp3"},
	{"Use the microphone", "In Recognize, set Source to Microphone to record a few seconds and identify the song.", "audd listen"},
	{"Add a stream", "Have AudD monitor a radio or other live stream, then watch it in Now playing.", "audd streams add <url> --id 1"},
	{"Run any command", "Press ctrl+k to search every command and run it from a form.", "audd commands"},
}

// mouseHelp is the mouse paragraph of the Keys page.
const mouseHelp = "Click a section, a tab, a row, a field, or a button; click a selected row again to open it. A click on a part of the path at the top goes back to it. The wheel scrolls. To select text with the mouse, hold Shift (Option in iTerm2). AUDD_NO_MOUSE=1 turns the mouse off."

// sectionKeyDocs are the keys of each section, for the Keys page (and
// the keys a section takes beyond moving around).
var sectionKeyDocs = []struct {
	section string
	keys    []keyHelp
}{
	{"Recognize", []keyHelp{{"↑/↓", "move between fields"}, {"enter", "in a text field: recognize (the hint says what); on a box: tick it; on a choice: next field; on a button: press it"}, {"←/→", "change a choice such as Source"}, {"space", "tick a box"}, {"esc", "leave the field you type in; again: back"}, {"ctrl+o", "in Input: pick a file or folder"}, {"→ or enter", "result: details"}}},
	{"Now playing", []keyHelp{{"←/→/↑/↓", "move between streams; ← from the left-most one goes back"}, {"enter", "zoom in, or back to all streams"}, {"tab", "the buttons: History, Open link, Copy link, Refresh"}}},
	{"Streams", []keyHelp{{"→ or enter", "a stream's page: History, Edit URL, Remove"}, {"/", "filter"}, {"tab", "the page's buttons: Add a stream, Refresh, Export"}, {"enter", "Recorder: Start or Stop"}}},
	{"Recognitions", []keyHelp{{"→ or enter", "details; a job's page: Files, Resume, Retry failed files, Copy the batch command"}, {"/", "filter"}, {"tab", "the page's buttons"}}},
	{"Account", []keyHelp{{"enter", "Billing: Subscribe, or Renew your own plan; Profiles: switch"}, {"tab", "the page's buttons: Billing, Buy requests, Payments, Show, Copy, New token…, Paste a different token…, Sign out"}}},
	{"Settings", []keyHelp{{"→ or enter", "edit a setting (enter saves after asking, esc cancels)"}, {"tab", "the buttons: Edit, Unset, Reload"}}},
	{"Help", []keyHelp{{"/", "search the guide"}, {"n", "next match"}, {"{ }", "previous or next heading"}, {"→ or enter", "open a step or a command's details"}}},
	{"Sign in", []keyHelp{{"↑/↓", "choose"}, {"enter", "go (a click works too)"}}},
	{"Commands (ctrl+k)", []keyHelp{{"type", "filter the commands"}, {"enter", "open the command's form; on Run, run it"}, {"ctrl+k", "in a form: back to the list; on the list: close"}}},
}

// letterKeys are the letters shown on buttons; each means one thing
// everywhere.
var letterKeys = []keyHelp{
	{"a", "add"}, {"c", "copy what the button names (a link, a path, a command, the token)"}, {"d", "remove or unset, after asking"},
	{"e", "edit"}, {"i, u", "copy the ISRC, the UPC"}, {"o", "open in the browser"}, {"r", "refresh"}, {"s", "stop"},
	{"x", "export"}, {"y", "copy the command at the bottom"}, {"/", "filter or search"},
}

type helpSection struct {
	h    *home
	page int

	stepCur int

	// Guide
	guideW    int
	guide     []string
	heads     []mdHeading
	scroll    scrollView
	search    textinput.Model
	searching bool
	query     string
	matchN    int

	// Commands
	cmds    []cmdInfo
	picker  *picker
	details bool
	dscroll scrollView

	// Exit codes and output, Keys
	text scrollView

	linkCur int
}

func newHelpSection(h *home) section {
	s := &helpSection{h: h, search: newInput()}
	s.search.Prompt = "/"
	return s
}

func (s *helpSection) title() string { return "Help" }

func (s *helpSection) init() tea.Cmd {
	s.cmds = commandTree(HomeCommands())
	items := make([]pickItem, len(s.cmds))
	for i, c := range s.cmds {
		items[i] = pickItem{title: c.path, desc: c.cmd.Short, value: c.path}
	}
	s.picker = newPicker(items)
	s.picker.h = s.h
	s.picker.click = func(i int) tea.Cmd {
		return s.picker.clickRow(i, func() tea.Cmd {
			s.details, s.dscroll.off = true, 0
			return nil
		})
	}
	return nil
}

func (s *helpSection) setPage(p int) {
	s.page = max(0, min(len(helpPages)-1, p))
	s.searching = false
	s.details = false
	s.text.off = 0
}

func (s *helpSection) tabNames() []string { return helpPages }
func (s *helpSection) tabIndex() int      { return s.page }
func (s *helpSection) selectTab(i int) tea.Cmd {
	s.setPage(i)
	return nil
}

// upExits: ↑ at the top of the page goes to the tab bar.
func (s *helpSection) upExits() bool {
	switch s.page {
	case 0:
		return s.stepCur == 0
	case 1:
		return !s.searching && s.scroll.off == 0
	case 2:
		return !s.details && (s.picker == nil || s.picker.cursor == 0)
	case 5:
		return s.linkCur == 0
	}
	return s.text.off == 0
}

func (s *helpSection) crumbs() []string {
	switch {
	case s.searching:
		return []string{"Search"}
	case s.page == 2 && s.details && s.picker != nil:
		if it := s.picker.selected(); it != nil {
			return []string{"audd " + it.value}
		}
	}
	return nil
}

func (s *helpSection) capturing() bool {
	return s.searching || s.page == 2 && !s.details
}

func (s *helpSection) back() bool {
	switch {
	case s.searching:
		s.searching = false
		return true
	case s.details:
		s.details = false
		return true
	case s.page == 2 && s.picker.filter.Value() != "":
		s.picker.filter.SetValue("")
		return true
	}
	return false
}

func (s *helpSection) update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	ks := k.String()
	_, ht := s.h.contentSize()
	if s.searching {
		switch ks {
		case "enter":
			s.searching = false
			s.query = strings.TrimSpace(s.search.Value())
			s.matchN = 0
			s.findMatch(0, 1)
			return nil
		case "esc":
			s.searching = false
			return nil
		case "ctrl+u":
			s.search.SetValue("")
			return nil
		}
		var cmd tea.Cmd
		s.search, cmd = s.search.Update(k)
		return cmd
	}
	if s.page == 2 && !s.details {
		switch ks {
		case "right", "tab":
			if s.picker.selected() != nil {
				s.details = true
				s.dscroll.off = 0
			}
			return nil
		}
		picked, cmd := s.picker.update(k)
		if picked {
			return s.h.openPalette(s.picker.selected().value)
		}
		return cmd
	}
	switch s.page {
	case 0:
		switch ks {
		case "down", "j":
			s.stepCur = min(len(gettingStarted)-1, s.stepCur+1)
		case "up", "k":
			s.stepCur = max(0, s.stepCur-1)
		case "enter", "right":
			return s.runStep(s.stepCur)
		}
	case 1:
		switch ks {
		case "/":
			s.searching = true
			s.search.SetValue("")
			return s.search.Focus()
		case "n":
			s.findMatch(s.scroll.off+1, 1)
		case "}":
			s.jumpHeading(1)
		case "{":
			s.jumpHeading(-1)
		default:
			s.scroll.key(ks, ht-1)
		}
	case 2:
		switch ks {
		case "backspace":
			s.details = false
		case "enter":
			if it := s.picker.selected(); it != nil {
				return s.h.openPalette(it.value)
			}
		default:
			s.dscroll.key(ks, ht)
		}
	case 3, 4:
		s.text.key(ks, ht)
	case 5:
		switch ks {
		case "down", "j":
			s.linkCur = min(len(helpLinks)-1, s.linkCur+1)
		case "up", "k":
			s.linkCur = max(0, s.linkCur-1)
		case "enter", "o":
			return s.openLink()
		case "c":
			u := strings.TrimPrefix(helpLinks[s.linkCur].url, "mailto:")
			return s.h.copy(u, u)
		}
	}
	return nil
}

// openLink opens the selected link.
func (s *helpSection) openLink() tea.Cmd {
	u := helpLinks[s.linkCur].url
	if err := openURL(u); err != nil {
		return s.h.setFlash("Could not open it: " + err.Error())
	}
	return s.h.setFlash("Opened in your browser: " + u)
}

// stepDone reports whether a getting-started step is done: a token is
// set, something was recognized, a stream exists.
func (s *helpSection) stepDone(i int) bool {
	switch i {
	case 0:
		// The test token is a try, not a sign-in.
		return !s.h.signedOut && !s.h.testToken
	case 1:
		return s.h.recognized
	case 2:
		return s.h.listened
	case 3:
		if s.h.streamAdded {
			return true
		}
		if st, ok := s.h.subs["streams"].(*streamsSection); ok && st.pane.ex != nil {
			if l := st.pane.ex.tabs[tabStreams]; len(l) > 0 && len(l[0].rows) > 0 {
				return true
			}
		}
	}
	return false
}

// runStep jumps to the section a getting-started step is about.
func (s *helpSection) runStep(i int) tea.Cmd {
	switch i {
	case 0:
		return s.h.jump("signin")
	case 1:
		if r, ok := s.h.subs["recognize"].(*recognizeSection); ok {
			r.prefill("https://audd.tech/example.mp3")
		}
		return s.h.jump("recognize")
	case 2:
		return s.h.jump("listen")
	case 3:
		cmd := s.h.jump("streams")
		if st, ok := s.h.subs["streams"].(*streamsSection); ok {
			st.openAdd()
			st.add.get("id").setValue("1")
			st.add.focusName("url")
		}
		return cmd
	}
	return s.h.openPalette("")
}

// guideIntro starts the guide in interactive mode: how to move around.
const guideIntro = `## Using interactive mode

Menu on the left, page on the right. Arrows move, Enter goes in, Esc comes back.

- On the sidebar, up and down pick a section; right or Enter opens it.
- Help, Streams, Recognitions, and Account have a tab bar: left and right switch tabs, down or Enter goes into the page.
- On a page, up and down move; right or Enter opens what is selected; left or Esc comes back one level.
- The path at the top shows where you are. The bottom line shows the command each action runs; y copies it.
- ctrl+k runs any command from a form. ? lists the keys of the screen; the Keys tab lists them all.

`

// layoutGuide renders the guide for width w when it changed.
func (s *helpSection) layoutGuide(w int) {
	if s.guideW == w && s.guide != nil {
		return
	}
	s.guideW = w
	s.guide, s.heads = renderMarkdown(guideIntro+agent.CLIDoc, w, s.h.st)
	s.scroll.lines = s.guide
	if s.query != "" {
		s.findMatch(0, 1)
	}
}

// findMatch scrolls to the first line from `from` (going dir) that
// contains the query.
func (s *helpSection) findMatch(from, dir int) {
	if s.query == "" || len(s.guide) == 0 {
		return
	}
	q := strings.ToLower(s.query)
	n := len(s.guide)
	for i := 0; i < n; i++ {
		j := ((from+dir*i)%n + n) % n
		if strings.Contains(strings.ToLower(ansi.Strip(s.guide[j])), q) {
			s.scroll.off = j
			s.matchN = j
			return
		}
	}
	s.matchN = -1
}

func (s *helpSection) jumpHeading(dir int) {
	cur := s.scroll.off
	if dir > 0 {
		for _, hd := range s.heads {
			if hd.line > cur {
				s.scroll.off = hd.line
				return
			}
		}
		return
	}
	for i := len(s.heads) - 1; i >= 0; i-- {
		if s.heads[i].line < cur {
			s.scroll.off = s.heads[i].line
			return
		}
	}
}

// wheel scrolls the page, or moves through its list.
func (s *helpSection) wheel(dir int) tea.Cmd {
	_, ht := s.h.contentSize()
	k := "down"
	if dir < 0 {
		k = "up"
	}
	switch {
	case s.page == 0:
		s.stepCur = max(0, min(len(gettingStarted)-1, s.stepCur+dir))
	case s.page == 1:
		for range 3 {
			s.scroll.key(k, ht-2)
		}
	case s.page == 2 && s.details:
		for range 3 {
			s.dscroll.key(k, ht-2)
		}
	case s.page == 2:
		s.picker.wheel(dir)
	case s.page == 5:
		s.linkCur = max(0, min(len(helpLinks)-1, s.linkCur+dir))
	default:
		for range 3 {
			s.text.key(k, ht-2)
		}
	}
	return nil
}

func (s *helpSection) view(w, h int) string {
	st := s.h.st
	bar := ""
	var b strings.Builder
	switch s.page {
	case 0:
		b.WriteString(st.Bold.Render("Getting started") + "\n\n")
		for i, step := range gettingStarted {
			title := fmt.Sprintf("%d. %s", i+1, step.title)
			if s.stepDone(i) {
				title += st.Dim.Render("  ✓ done")
			}
			item := s.h.listRow(title, i == s.stepCur) + "\n" +
				styleLines(st.Dim, indent(output.Wrap(step.text, max(10, w-5)), "     ")) + "\n" +
				"     " + st.Accent.Render(truncate("$ "+s.stepCommand(i), w-5))
			b.WriteString(s.h.mark(item, func() tea.Cmd {
				if s.stepCur == i {
					return s.runStep(i)
				}
				s.stepCur = i
				return nil
			}) + "\n\n")
		}
		b.WriteString(styleLines(st.Dim, output.Wrap("Enter goes to the step. Every screen shows the command it runs at the bottom; y copies it.", w)))
		return bar + b.String()
	case 1:
		outlineW := 0
		if w >= 100 {
			outlineW = 26
		}
		tw := w - outlineW
		if outlineW > 0 {
			tw -= 2
		}
		s.layoutGuide(tw)
		textH := h - 1
		text := s.scroll.view(tw, textH)
		if outlineW > 0 {
			text = joinColumns(s.outline(outlineW, textH), text, outlineW, st)
		}
		status := fmt.Sprintf("%d%%", 100*min(len(s.guide), s.scroll.off+textH)/max(1, len(s.guide)))
		switch {
		case s.searching:
			status = s.search.View()
		case s.query != "" && s.matchN < 0:
			status = fmt.Sprintf("No match for %q · %s", s.query, status)
		case s.query != "":
			status = fmt.Sprintf("%q · n next match · %s", s.query, status)
		}
		return bar + fit(text, w, textH) + "\n" + truncate(st.Dim.Render(status), w)
	case 2:
		if s.picker == nil {
			return bar
		}
		if s.details {
			if it := s.picker.selected(); it != nil {
				for _, c := range s.cmds {
					if c.path == it.value {
						s.dscroll.lines = strings.Split(commandDoc(c.cmd, w, st), "\n")
					}
				}
			}
			return bar + s.dscroll.view(w, h)
		}
		return bar + s.picker.view(w, h, st, s.h.color, s.h.hasKeys(false))
	case 3:
		s.text.lines = strings.Split(s.keysText(w), "\n")
		return bar + s.text.view(w, h)
	case 4:
		s.text.lines = strings.Split(s.exitText(w), "\n")
		return bar + s.text.view(w, h)
	}
	b.WriteString(st.Bold.Render("Links") + "\n\n")
	for i, l := range helpLinks {
		line := truncate(s.h.listRow(padRight(l.label, min(36, w/2)), i == s.linkCur)+st.Accent.Render(l.url), w)
		b.WriteString(s.h.mark(line, func() tea.Cmd {
			if s.linkCur == i {
				return s.openLink()
			}
			s.linkCur = i
			return nil
		}) + "\n")
	}
	b.WriteString("\n" + st.Dim.Render("Enter opens the link, c copies it."))
	return bar + b.String()
}

// outline is the guide's headings, the one on screen marked.
func (s *helpSection) outline(w, h int) string {
	st := s.h.st
	cur := 0
	for i, hd := range s.heads {
		if hd.line <= s.scroll.off {
			cur = i
		}
	}
	var lines []string
	for i, hd := range s.heads {
		if hd.level < 2 {
			continue
		}
		label := strings.Repeat("  ", hd.level-2) + hd.text
		line := st.Dim.Render(truncate("  "+label, w))
		if i == cur {
			line = st.Bold.Render(truncate("› "+label, w))
		}
		// A click on a heading scrolls the guide to it.
		at := hd.line
		lines = append(lines, s.h.mark(line, func() tea.Cmd {
			s.scroll.off = at
			return nil
		}))
	}
	return strings.Join(lines, "\n")
}

func joinColumns(left, right string, lw int, st output.Styles) string {
	l := strings.Split(left, "\n")
	r := strings.Split(right, "\n")
	n := max(len(l), len(r))
	var b strings.Builder
	for i := 0; i < n; i++ {
		a, c := "", ""
		if i < len(l) {
			a = l[i]
		}
		if i < len(r) {
			c = r[i]
		}
		b.WriteString(padRight(truncate(a, lw), lw) + st.Dim.Render("│ ") + c)
		if i < n-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (s *helpSection) keysText(w int) string {
	st := s.h.st
	var b strings.Builder
	b.WriteString(st.Bold.Render("Moving around") + "\n")
	b.WriteString(indent(output.Wrap(tipText, max(10, w-2)), "  ") + "\n")
	b.WriteString(indent(output.Wrap("The menu is the sidebar; Help, Streams, Recognitions, and Account have a tab bar above their page. On a choice ‹ like this ›, ←/→ change it and ← on its first option goes back. While you type in a field, letters and digits go into it: esc leaves the field, ctrl+u clears it. What a page can do is on its buttons: tab (or ↓ from the end of the page) reaches them, ←/→ move between them, enter presses one. A letter on a button, such as (o), presses it from anywhere on the page. A key works only where it is listed; any other key says it does nothing. Opening a page never starts typing: its field is selected, a letter starts typing in it, enter edits it, and 1-7 jump to a section except while you type. Settings of audd save right away (u undoes the last change). Spending more than one request, removing, signing out, or replacing the token asks in a dialog: enter presses the focused button (the action after a plan, Cancel otherwise), y confirms, n or esc cancels; in a dialog y confirms instead of copying the command.", max(10, w-2)), "  ") + "\n\n")
	b.WriteString(keyLines(moveKeys, w) + "\n")
	b.WriteString("\n" + st.Bold.Render("Everywhere") + "\n")
	b.WriteString(keyLines(globalKeys, w) + "\n")
	b.WriteString("\n" + st.Bold.Render("Letters on buttons") + "\n")
	b.WriteString(keyLines(letterKeys, w) + "\n")
	b.WriteString("\n" + st.Bold.Render("Mouse") + "\n")
	b.WriteString(indent(output.Wrap(mouseHelp, max(10, w-2)), "  ") + "\n")
	for _, sec := range sectionKeyDocs {
		b.WriteString("\n" + st.Bold.Render(sec.section) + "\n")
		b.WriteString(keyLines(sec.keys, w) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *helpSection) exitText(w int) string {
	st := s.h.st
	var b strings.Builder
	b.WriteString(st.Bold.Render("Exit codes") + "\n")
	for _, e := range agent.ExitCodes {
		b.WriteString(fmt.Sprintf("  %-4d %-12s ", e.Code, e.Name))
		b.WriteString(strings.TrimLeft(indent(output.Wrap(e.Meaning, max(10, w-20)), strings.Repeat(" ", 20)), " ") + "\n")
	}
	b.WriteString("\n" + st.Bold.Render("Output for scripts") + "\n")
	b.WriteString(output.Wrap("Piped output is JSON (one document, or JSON lines for streaming commands); --format picks table, json, jsonl, or csv. Errors go to stderr as {\"error\":{\"code\",\"message\",\"hint\",\"retryable\"}}. audd commands --json describes every command.", w) + "\n")
	keys := make([]string, 0, len(agent.Outputs))
	for k := range agent.Outputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\n" + st.Accent.Render(k) + "\n")
		b.WriteString(indent(output.Wrap(agent.Outputs[k].Description, max(10, w-2)), "  ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// actions are the buttons of the Links page, for the selected link.
func (s *helpSection) actions() []button {
	if s.page != 5 {
		return nil
	}
	return []button{
		{"Open in browser", "o", s.openLink},
		{"Copy", "c", func() tea.Cmd {
			u := strings.TrimPrefix(helpLinks[s.linkCur].url, "mailto:")
			return s.h.copy(u, u)
		}},
	}
}

func (s *helpSection) downExits() bool { return s.page == 5 && s.linkCur == len(helpLinks)-1 }

func (s *helpSection) keys() []keyHelp {
	switch s.page {
	case 0:
		return []keyHelp{{"↑/↓", "move"}, {"enter", "go to the step"}}
	case 1:
		if s.searching {
			return []keyHelp{{"type", "search"}, {"enter", "find"}, {"esc", "cancel"}}
		}
		return []keyHelp{{"↑/↓", "scroll"}, {"/", "search"}, {"n", "next match"}, {"{ }", "headings"}}
	case 2:
		if s.details {
			return []keyHelp{{"↑/↓", "scroll"}, {"enter", "fill in and run"}}
		}
		return []keyHelp{{"type", "filter"}, {"↑/↓", "move"}, {"→", "details"}, {"enter", "fill in and run"}}
	case 5:
		return []keyHelp{{"↑/↓", "move"}, {"enter", "open in browser"}}
	}
	return []keyHelp{{"↑/↓", "scroll"}}
}

// stepCommand is the command a getting-started step runs, as it runs
// here (the sign-in picks the browser or a code).
func (s *helpSection) stepCommand(i int) string {
	if si, ok := s.h.subs["signin"].(*signinSection); ok && i == 0 {
		return displayCommand(si.loginArgs())
	}
	return gettingStarted[i].cmd
}

func (s *helpSection) command() string {
	switch s.page {
	case 0:
		return s.stepCommand(s.stepCur)
	case 1:
		return "audd docs cli"
	case 2:
		if s.picker != nil {
			if it := s.picker.selected(); it != nil {
				return "audd " + it.value + " --help"
			}
		}
		return "audd commands"
	case 4:
		return "audd commands --json"
	}
	return ""
}

// leftExits: left goes back, except inside typed text (the search, the
// filter of Commands).
func (s *helpSection) leftExits() bool {
	switch {
	case s.searching:
		return s.search.Position() == 0
	case s.page == 2 && !s.details && s.picker != nil:
		return s.picker.filter.Position() == 0
	}
	return true
}
