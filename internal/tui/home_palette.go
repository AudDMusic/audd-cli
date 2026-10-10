package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// paletteOpenMsg opens the palette: on a command's form (path, such as
// "streams add"), or on a command line from a hint (line).
type paletteOpenMsg struct{ path, line string }

// openPaletteLine opens the palette on a command line ("audd login").
func (h *home) openPaletteLine(line string) tea.Cmd {
	h.paletteO = true
	return h.send("palette", paletteOpenMsg{line: line})
}

// widgetFor is the form field for a flag type; ok is false when the
// palette has no widget for it (the coverage test fails then).
func widgetFor(flagType string) (fieldKind, bool) {
	switch flagType {
	case "string":
		return fText, true
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "count":
		return fInt, true
	case "float32", "float64":
		return fText, true
	case "bool":
		return fBool, true
	case "duration":
		return fDuration, true
	case "stringSlice", "stringArray", "intSlice", "int32Slice", "int64Slice", "uintSlice", "float64Slice", "boolSlice", "durationSlice":
		return fList, true
	}
	return 0, false
}

// argSpec is a positional argument parsed from a command's Use line.
type argSpec struct {
	name     string
	required bool
	variadic bool
}

var useArg = regexp.MustCompile(`^[\[<]+([^\]>]+)[\]>]+(\.\.\.|…)?\]?$`)

// argSpecs reads the positional arguments from cmd's Use line: <x> is
// required, [x] optional, ... or … repeats. A bare a|b|c word is one
// required choice (its values come from ValidArgs).
func argSpecs(cmd *cobra.Command) ([]argSpec, error) {
	parts := strings.Fields(cmd.Use)
	var out []argSpec
	for i := 1; i < len(parts); i++ {
		p := parts[i]
		switch {
		case strings.HasPrefix(p, "-"):
			// A flag in the usage (--id N): skip it and its value.
			if i+1 < len(parts) && !strings.ContainsAny(parts[i+1], "<[") {
				i++
			}
		case strings.HasPrefix(p, "<") || strings.HasPrefix(p, "["):
			m := useArg.FindStringSubmatch(p)
			if m == nil {
				return nil, fmt.Errorf("cannot read the argument %q of %q", p, cmd.CommandPath())
			}
			name := strings.TrimSuffix(strings.TrimSuffix(strings.Trim(m[1], "<>[]"), "..."), "…")
			out = append(out, argSpec{name: name, required: strings.HasPrefix(p, "<"),
				variadic: m[2] != "" || strings.HasSuffix(m[1], "...") || strings.HasSuffix(m[1], "…")})
		case strings.Contains(p, "|") && len(cmd.ValidArgs) > 0:
			out = append(out, argSpec{name: "choice", required: true})
		default:
			return nil, fmt.Errorf("cannot read the argument %q of %q", p, cmd.CommandPath())
		}
	}
	return out, nil
}

// flagChoices are the fixed values a flag's completion offers, if any.
func flagChoices(cmd *cobra.Command, name string) []string {
	fn, ok := cmd.GetFlagCompletionFunc(name)
	if !ok || fn == nil {
		return nil
	}
	vals, dir := fn(cmd, nil, "")
	if dir&cobra.ShellCompDirectiveNoFileComp == 0 || len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		s, _, _ := strings.Cut(string(v), "\t")
		out = append(out, s)
	}
	return out
}

// cmdForm is the form for one command.
type cmdForm struct {
	info  cmdInfo
	args  []argSpec
	flags []*pflag.Flag
	form  *form
}

// newCmdForm builds the form for a command: one field per positional
// argument and per flag, defaults filled in, required ones marked.
func newCmdForm(info cmdInfo) (*cmdForm, error) {
	c := info.cmd
	specs, err := argSpecs(c)
	if err != nil {
		return nil, err
	}
	cf := &cmdForm{info: info, args: specs}
	var fields []*field
	for i, a := range specs {
		f := textField(fmt.Sprintf("arg:%d", i), a.name, "")
		if a.variadic {
			f.kind = fList
			f.help = "One or more, separated by spaces"
		}
		f.required = a.required
		if len(c.ValidArgs) > 0 && !a.variadic {
			opts := append([]string(nil), c.ValidArgs...)
			if !a.required {
				opts = append([]string{""}, opts...)
			}
			f = enumField(f.name, a.name, "←/→ to choose", opts)
			f.required = a.required
		}
		fields = append(fields, f)
	}
	for _, fl := range visibleFlags(c) {
		kind, ok := widgetFor(fl.Value.Type())
		if !ok {
			return nil, fmt.Errorf("%s --%s: no form field for flag type %s", c.CommandPath(), fl.Name, fl.Value.Type())
		}
		help := fl.Usage
		var f *field
		if choices := flagChoices(c, fl.Name); kind == fText && len(choices) > 0 {
			f = enumField("flag:"+fl.Name, "--"+fl.Name, help, choices)
		} else {
			f = &field{kind: kind, name: "flag:" + fl.Name, label: "--" + fl.Name, help: help, input: newInput()}
		}
		if r, ok := fl.Annotations[cobra.BashCompOneRequiredFlag]; ok && len(r) > 0 && r[0] == "true" {
			f.required = true
		}
		if w := fl.Annotations[agent.AnnotationRequiredWhen]; len(w) > 0 {
			f.help += " · required " + w[0]
		}
		if fl.Name == "limit" || fl.Name == "max-files" {
			f.help += " · a number, or type none"
		}
		f.def = fl.DefValue
		switch {
		case f.required && kind != fBool:
			// A required flag has no usable default: the user types it.
			f.def = ""
		case kind == fList && (f.def == "[]" || f.def == ""):
			f.def = ""
		default:
			f.setValue(fl.DefValue)
		}
		cf.flags = append(cf.flags, fl)
		fields = append(fields, f)
	}
	fields = append(fields, buttonField("run", "Run"))
	cf.form = newForm(fields...)
	return cf, nil
}

// argv is the command line for the form as it is.
func (cf *cmdForm) argv() []string {
	args := strings.Fields(cf.info.path)
	for i, a := range cf.args {
		v := cf.form.value(fmt.Sprintf("arg:%d", i))
		if v == "" {
			if a.required {
				args = append(args, "<"+a.name+">")
			}
			continue
		}
		if a.variadic {
			args = append(args, strings.Fields(v)...)
		} else {
			args = append(args, v)
		}
	}
	for _, fl := range cf.flags {
		f := cf.form.get("flag:" + fl.Name)
		v := f.value()
		if v == f.def || v == "" && f.kind != fBool {
			continue
		}
		if f.kind == fBool {
			if v == "true" {
				args = append(args, "--"+fl.Name)
			} else {
				args = append(args, "--"+fl.Name+"=false")
			}
			continue
		}
		args = append(args, "--"+fl.Name, v)
	}
	return args
}

// FormProblems builds the palette form of every command in the tree and
// returns what it could not build. A test keeps it empty, so a new
// command or flag type cannot silently miss interactive mode.
func FormProblems(root *cobra.Command) []string {
	var out []string
	for _, c := range commandTree(root) {
		if _, err := newCmdForm(c); err != nil {
			out = append(out, err.Error())
		}
	}
	return out
}

// interactiveTarget is the section a command with its own screen opens
// instead of running in the palette ("" for none).
func interactiveTarget(path string) string {
	switch path {
	case "now-playing", "streams watch":
		return "now-playing"
	case "browse", "jobs browse":
		return "history"
	case "login", "auth login":
		return "signin"
	case "listen":
		return "listen"
	case "ui", "mcp":
		return "none"
	}
	return ""
}

type palette struct {
	h      *home
	cmds   []cmdInfo
	picker *picker
	phase  string // "list", "form", "result"
	cf     *cmdForm
	panel  *cmdPanel
	tree   *jsonTree
	argv   []string
}

func newPalette(h *home) *palette { return &palette{h: h, phase: "list", panel: newPanel(h)} }

func (p *palette) title() string { return "Commands" }

func (p *palette) init() tea.Cmd { return nil }

func (p *palette) load() {
	if p.cmds != nil {
		return
	}
	p.cmds = commandTree(HomeCommands())
	items := make([]pickItem, 0, len(p.cmds))
	for _, c := range p.cmds {
		if c.cmd.Runnable() {
			items = append(items, pickItem{title: c.path, desc: c.cmd.Short, value: c.path})
		}
	}
	p.picker = newPicker(items)
	p.picker.h = p.h
	p.picker.click = func(i int) tea.Cmd {
		return p.picker.clickRow(i, func() tea.Cmd { return p.openForm(p.picker.selected().value) })
	}
}

// leftExits reports whether left goes back a step, as esc does: at the
// start of the filter, at the left of the form, or on a result.
func (p *palette) leftExits() bool {
	switch p.phase {
	case "list":
		return p.picker == nil || p.picker.filter.Position() == 0
	case "form":
		return p.cf == nil || p.cf.form.leftExits()
	}
	return !p.panel.running && p.tree == nil
}

// wheel moves through the list, the form, or the result.
func (p *palette) wheel(dir int) tea.Cmd {
	switch p.phase {
	case "list":
		if p.picker != nil {
			p.picker.wheel(dir)
		}
		return nil
	case "form":
		if p.cf != nil {
			p.cf.form.move(dir)
		}
		return nil
	}
	return keyWheel(dir, 3, func(k tea.KeyMsg) tea.Cmd { return p.update(k) })
}

func (p *palette) find(path string) (cmdInfo, bool) {
	for _, c := range p.cmds {
		if c.path == path {
			return c, true
		}
	}
	return cmdInfo{}, false
}

// openForm opens a command's form, or its section for commands with a
// screen of their own.
func (p *palette) openForm(path string) tea.Cmd {
	switch t := interactiveTarget(path); t {
	case "":
	case "none":
		p.h.paletteO = false
		if path == "mcp" {
			return p.h.setFlash("audd mcp is a server for AI agents; start it from your agent's settings")
		}
		return p.h.setFlash("This is audd ui")
	default:
		p.h.paletteO = false
		p.phase = "list"
		return p.h.jump(t)
	}
	info, ok := p.find(path)
	if !ok {
		p.phase = "list"
		p.picker.filter.SetValue(path)
		return nil
	}
	cf, err := newCmdForm(info)
	if err != nil {
		p.phase = "list"
		return p.h.setFlash(err.Error())
	}
	p.cf = cf
	cf.form.h = p.h
	p.phase = "form"
	cf.form.cursor = 0
	cf.form.focus()
	return nil
}

// fill sets form fields from the rest of a command line.
func (p *palette) fill(rest []string) {
	if p.cf == nil {
		return
	}
	argi := 0
	for i := 0; i < len(rest); i++ {
		t := rest[i]
		if strings.HasPrefix(t, "--") {
			name, val, hasVal := strings.Cut(t[2:], "=")
			f := p.cf.form.get("flag:" + name)
			if f == nil {
				continue
			}
			if f.kind == fBool {
				f.setValue(map[bool]string{true: val, false: "true"}[hasVal])
				continue
			}
			if !hasVal && i+1 < len(rest) {
				i++
				val = rest[i]
			}
			if !isPlaceholder(val) {
				f.setValue(val)
			}
			continue
		}
		if argi < len(p.cf.args) && !isPlaceholder(t) {
			p.cf.form.get(fmt.Sprintf("arg:%d", argi)).setValue(t)
		}
		argi++
	}
}

func isPlaceholder(s string) bool {
	return strings.HasPrefix(s, "<") || s == "N" || s == "VALUE" || strings.Contains(s, "…")
}

func (p *palette) update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(paletteOpenMsg); ok {
		p.load()
		p.phase = "list"
		p.picker.filter.SetValue("")
		p.picker.cursor = 0
		switch {
		case m.path != "":
			return p.openForm(m.path)
		case m.line != "":
			words := splitCommandLine(strings.TrimPrefix(strings.TrimSpace(m.line), "audd "))
			for n := min(2, len(words)); n > 0; n-- {
				if path := strings.Join(words[:n], " "); interactiveTarget(path) != "" {
					return p.openForm(path)
				}
			}
			// The longest command path the line starts with.
			best := ""
			for _, c := range p.cmds {
				n := len(strings.Fields(c.path))
				if n <= len(words) && strings.Join(words[:n], " ") == c.path && len(c.path) > len(best) {
					best = c.path
				}
			}
			if best == "" {
				p.picker.filter.SetValue(m.line)
				return nil
			}
			cmd := p.openForm(best)
			p.fill(words[len(strings.Fields(best)):])
			return cmd
		}
		return p.picker.filter.Focus()
	}
	if mine, done := p.panel.handle(msg); mine {
		if done {
			p.phase = "result"
			p.tree = nil
			if p.panel.res.err == nil {
				if _, ok := p.rendered(p.panel.res.stdout, 80); !ok && strings.HasPrefix(strings.TrimSpace(p.panel.res.stdout), "{") {
					p.tree = newJSONTree(p.panel.res.stdout)
				}
				if rec := p.recognition(); rec != nil && rec.view != nil {
					return p.h.arts.want(p.h.ctx, *rec.view)
				}
			}
		}
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	_, ht := p.h.contentSize()
	switch p.phase {
	case "list":
		picked, cmd := p.picker.update(k)
		if picked {
			return p.openForm(p.picker.selected().value)
		}
		return cmd
	case "form":
		act, cmd := p.cf.form.update(k)
		if act == "run" {
			return p.run()
		}
		return cmd
	}
	// Result (or running).
	if p.panel.running {
		if k.String() == "s" && p.panel.pending == nil {
			p.panel.stop()
			return nil
		}
		return p.panel.key(k, ht)
	}
	if p.tree != nil && p.tree.key(k.String(), ht) {
		return nil
	}
	switch k.String() {
	case "r":
		return p.run()
	case "e":
		p.phase = "form"
		return nil
	}
	return p.panel.key(k, ht)
}

func (p *palette) run() tea.Cmd {
	if e := p.cf.form.check(); e != "" {
		p.cf.form.err = e
		return nil
	}
	p.cf.form.err = ""
	p.argv = p.cf.argv()
	p.phase = "result"
	p.panel.render = func(res runResult, w int) string {
		s, _ := p.rendered(res.stdout, w)
		return s
	}
	p.panel.live = p.live
	return p.panel.start(runReq{args: p.argv, stream: true, spends: true})
}

// live shows a run's output as it arrives.
func (p *palette) live(lines []string, w int) string {
	st := p.h.st
	var bs batchState
	for _, l := range lines {
		bs.add(l)
	}
	if len(bs.results) > 0 || bs.total > 0 {
		return bs.view(st, w, 20, true) + "\n\n" + st.Dim.Render("s stops")
	}
	out := st.Dim.Render("Running " + displayCommand(p.argv) + " …  (s stops)")
	if n := len(lines); n > 0 {
		from := max(0, n-15)
		out += "\n\n" + strings.Join(lines[from:], "\n")
	}
	return out
}

// recognition is the run's recognition result, when it printed one.
func (p *palette) recognition() *recognition {
	if p.panel.res == nil {
		return nil
	}
	doc := parseDoc(p.panel.res.stdout)
	if doc == nil {
		return nil
	}
	if _, ok := doc["input"]; !ok {
		return nil
	}
	if _, ok := doc["result"]; !ok && doc["enterprise"] != true {
		return nil
	}
	r := parseRecognition(doc)
	return &r
}

// rendered shows known output shapes with the screens' own renderers;
// ok is false when the JSON tree should show it instead.
func (p *palette) rendered(stdout string, w int) (string, bool) {
	st := p.h.st
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return st.Dim.Render("Done. The command printed nothing."), true
	}
	if rec := p.recognition(); rec != nil {
		if rec.view != nil {
			_, ht := p.h.contentSize()
			return p.h.cardText(*rec.view, true, w, ht-2), true
		}
		return rec.text, true
	}
	if doc := parseDoc(trimmed); doc != nil {
		if t, ok := tableFromDoc(doc, st, w); ok {
			return t, true
		}
		if u := str(doc["url"]); u != "" && doc["charged"] == false {
			return "Open this link to review and pay (nothing is charged until you confirm there):\n\n  " + st.Accent.Render(u), true
		}
		return "", false
	}
	if !strings.HasPrefix(trimmed, "{") {
		return stdout, true // text output: docs, completion scripts
	}
	lines := parseLines(trimmed)
	var bs batchState
	var rows []any
	for _, l := range lines {
		bs.addDoc(l)
		if l["type"] == "result" {
			m := map[string]any{}
			for k, v := range l {
				if k != "type" && k != "schema_version" {
					m[k] = v
				}
			}
			rows = append(rows, m)
		}
	}
	if bs.summary != nil {
		return bs.view(st, w, 30, false), true
	}
	if len(rows) > 0 {
		return tableOf(rows, st, w, ""), true
	}
	return stdout, true
}

func (p *palette) view(w, h int) string {
	st := p.h.st
	p.load()
	switch p.phase {
	case "list":
		return st.Bold.Render("Run a command") + "\n" + p.picker.view(w, h-1, st, p.h.color)
	case "form":
		var b strings.Builder
		c := p.cf.info.cmd
		b.WriteString(st.Bold.Render("audd "+p.cf.info.path) + "  " + st.Dim.Render(c.Short) + "\n\n")
		b.WriteString(p.cf.form.view(w, st, p.h.color))
		b.WriteString("\n\n" + styleLines(st.Dim, output.Wrap("* required. The command runs as shown at the bottom; it asks here before anything that needs a confirmation.", w)))
		return b.String()
	}
	head := st.Dim.Render(truncate("$ "+displayCommand(p.argv), w)) + "\n\n"
	if p.tree != nil && !p.panel.running {
		return head + p.tree.view(w, h-2, st)
	}
	return head + p.panel.view(w, h-2)
}

func (p *palette) keys() []keyHelp {
	switch p.phase {
	case "list":
		return []keyHelp{{"type", "filter"}, {"↑/↓", "choose"}, {"enter", "open"}, {"esc", "close"}}
	case "form":
		return []keyHelp{{"↑/↓", "field"}, {"space", "toggle"}, {"enter", "next / run"}, {"esc", "back"}}
	}
	if p.panel.running {
		return []keyHelp{{"s", "stop"}}
	}
	k := []keyHelp{{"r", "run again"}, {"e", "edit"}, {"esc", "back"}}
	if p.tree != nil {
		k = append([]keyHelp{{"enter", "fold"}}, k...)
	}
	return k
}

func (p *palette) command() string {
	switch p.phase {
	case "form":
		return displayCommand(p.cf.argv())
	case "result":
		return displayCommand(p.argv)
	}
	if p.picker != nil {
		if it := p.picker.selected(); it != nil {
			return "audd " + it.value
		}
	}
	return ""
}

func (p *palette) capturing() bool {
	switch p.phase {
	case "list":
		return true
	case "form":
		return p.cf.form.capturing()
	}
	return p.panel.capturing()
}

// back goes from a result to the form, and from the form to the list.
func (p *palette) back() bool {
	switch p.phase {
	case "result":
		if p.panel.back() {
			return true
		}
		if p.panel.running {
			p.panel.stop()
		}
		p.phase = "form"
		return true
	case "form":
		p.phase = "list"
		return true
	}
	if p.picker.filter.Value() != "" {
		p.picker.filter.SetValue("")
		return true
	}
	return false
}

// splitCommandLine splits a command line into words, honoring quotes.
func splitCommandLine(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	in := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
