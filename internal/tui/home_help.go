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
	{"Recognize", []keyHelp{{"↑/↓", "move between fields"}, {"enter", "in Input: recognize (the plan comes first); on a button: press it"}, {"→ space enter", "Source: a file or the microphone"}, {"space, x", "tick a box"}, {"ctrl+o", "pick a file or folder (← goes to the parent folder, s picks the folder)"}, {"s", "stop a batch or a recording"}, {"r", "run again"}, {"o, c, d", "result: open, copy the link, details"}}},
	{"Now playing", []keyHelp{{"←/→ ↑/↓", "move between streams; ← from the left-most one goes back"}, {"enter", "zoom in or out"}, {"h", "history of the stream"}, {"o, c", "open or copy the song link"}, {"r", "refresh"}}},
	{"Streams", []keyHelp{{"a", "add a stream"}, {"d", "remove the stream (asks first)"}, {"u", "change its URL"}, {"→ or enter", "recent plays of the stream"}, {"/", "filter"}, {"o, c, i", "open the song link, copy it, copy the ISRC"}, {"e", "export to CSV or JSON"}, {"r", "refresh"}, {"s, x", "Recorder: start, stop"}, {"pgdn/pgup", "History & reports: scroll the results"}}},
	{"History", []keyHelp{{"tab", "Recent or Jobs"}, {"/", "filter"}, {"→ or enter", "details, or a job's files"}, {"r, R", "resume a job, retry its failed files"}, {"s", "stop a resumed job"}, {"e", "export to CSV or JSON"}, {"o, c, i, u", "open the link, copy the link, ISRC, UPC"}}},
	{"Account", []keyHelp{{"r", "refresh"}, {"s, n, b", "Billing: subscribe, renew, buy requests (payment links only)"}, {"h", "Billing: payments"}, {"x", "close a payment link"}, {"v, c, h", "API token: reveal, copy, hide"}, {"R", "rotate the API token (type rotate to confirm)"}, {"f", "fetch the API token again"}, {"enter, L", "Profiles: switch, sign out (asks first)"}, {"o, p", "sign-in: open the page, paste the address"}}},
	{"Settings", []keyHelp{{"→ or enter, e", "change a setting"}, {"u", "unset it (asks first)"}, {"r", "reload"}}},
	{"Help", []keyHelp{{"/", "search the guide"}, {"n, N", "next or previous match"}, {"{ }", "previous or next heading"}, {"→ or enter", "open a step or a command's details"}, {"o, c", "Links: open, copy"}}},
	{"Sign in", []keyHelp{{"↑/↓", "choose"}, {"enter", "go"}, {"o, c, p", "open the sign-in page, copy it, paste the address"}}},
	{"Commands (ctrl+k)", []keyHelp{{"type", "filter the commands"}, {"enter", "open the command's form, then run it"}, {"r, e", "result: run again, edit"}, {"s", "stop"}}},
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
		}
		var cmd tea.Cmd
		s.search, cmd = s.search.Update(k)
		return cmd
	}
	if s.page == 2 && !s.details {
		switch ks {
		case "[", "]":
			// The filter is empty: [ and ] switch tabs as everywhere.
			if s.picker.filter.Value() == "" {
				return s.h.stepTab(map[string]int{"[": -1, "]": 1}[ks])
			}
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
		case "N":
			s.findMatch(s.scroll.off-1, -1)
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

// layoutGuide renders the guide for width w when it changed.
func (s *helpSection) layoutGuide(w int) {
	if s.guideW == w && s.guide != nil {
		return
	}
	s.guideW = w
	s.guide, s.heads = renderMarkdown(agent.CLIDoc, w, s.h.st)
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
			item := s.h.listRow(fmt.Sprintf("%d. %s", i+1, step.title), i == s.stepCur) + "\n" +
				styleLines(st.Dim, indent(output.Wrap(step.text, max(10, w-5)), "     ")) + "\n" +
				"     " + st.Accent.Render(truncate("$ "+step.cmd, w-5))
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
			status = fmt.Sprintf("%q · n next, N previous · %s", s.query, status)
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
		if i == cur {
			lines = append(lines, st.Bold.Render(truncate("› "+label, w)))
		} else {
			lines = append(lines, st.Dim.Render(truncate("  "+label, w)))
		}
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
	b.WriteString(indent(output.Wrap("The menu is the sidebar; Help, Streams, History, and Account have a tab bar above their page. A key that does nothing where you press it says so at the bottom.", max(10, w-2)), "  ") + "\n\n")
	for _, k := range moveKeys {
		b.WriteString(truncate(fmt.Sprintf("  %-14s %s", k.key, k.help), w) + "\n")
	}
	b.WriteString("\n" + st.Bold.Render("Everywhere") + "\n")
	for _, k := range globalKeys {
		b.WriteString(truncate(fmt.Sprintf("  %-14s %s", k.key, k.help), w) + "\n")
	}
	b.WriteString("\n" + st.Bold.Render("Mouse") + "\n")
	b.WriteString(indent(output.Wrap(mouseHelp, max(10, w-2)), "  ") + "\n")
	for _, sec := range sectionKeyDocs {
		b.WriteString("\n" + st.Bold.Render(sec.section) + "\n")
		for _, k := range sec.keys {
			b.WriteString(truncate(fmt.Sprintf("  %-14s %s", k.key, k.help), w) + "\n")
		}
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

func (s *helpSection) keys() []keyHelp {
	switch s.page {
	case 0:
		return []keyHelp{{"↑/↓", "move"}, {"enter", "go to the step"}}
	case 1:
		if s.searching {
			return []keyHelp{{"type", "search"}, {"enter", "find"}, {"esc", "cancel"}}
		}
		return []keyHelp{{"↑/↓", "scroll"}, {"/", "search"}, {"n N", "next, previous match"}, {"{ }", "headings"}}
	case 2:
		if s.details {
			return []keyHelp{{"↑/↓", "scroll"}, {"enter", "fill in and run"}}
		}
		return []keyHelp{{"type", "filter"}, {"↑/↓", "move"}, {"→", "details"}, {"enter", "fill in and run"}}
	case 5:
		return []keyHelp{{"↑/↓", "move"}, {"enter", "open in the browser"}, {"c", "copy"}}
	}
	return []keyHelp{{"↑/↓", "scroll"}}
}

func (s *helpSection) command() string {
	switch s.page {
	case 0:
		return gettingStarted[s.stepCur].cmd
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
