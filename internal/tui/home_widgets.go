package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// fieldKind is the kind of a form field.
type fieldKind int

const (
	fText     fieldKind = iota // any text
	fInt                       // a whole number
	fDuration                  // a duration such as 30s or 5m
	fBool                      // a checkbox
	fList                      // comma-separated values
	fEnum                      // one of options
	fButton                    // an action
)

// field is one row of a form.
type field struct {
	kind     fieldKind
	name     string // flag or argument name, or the button's action
	label    string
	help     string
	required bool
	masked   bool
	hidden   bool // left out of the form for now (see form.visible)

	input   textinput.Model
	on      bool
	options []string
	choice  int
	def     string // the default value, for flags
}

func newInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Cursor.SetMode(cursor.CursorStatic)
	ti.CharLimit = 4096
	return ti
}

func textField(name, label, help string) *field {
	return &field{kind: fText, name: name, label: label, help: help, input: newInput()}
}

func boolField(name, label, help string) *field {
	return &field{kind: fBool, name: name, label: label, help: help}
}

func enumField(name, label, help string, options []string) *field {
	return &field{kind: fEnum, name: name, label: label, help: help, options: options}
}

func buttonField(action, label string) *field {
	return &field{kind: fButton, name: action, label: label}
}

func (f *field) isText() bool {
	switch f.kind {
	case fText, fInt, fDuration, fList:
		return true
	}
	return false
}

// value is the field's value as a flag value.
func (f *field) value() string {
	switch f.kind {
	case fBool:
		return strconv.FormatBool(f.on)
	case fEnum:
		if len(f.options) == 0 {
			return ""
		}
		return f.options[f.choice]
	case fButton:
		return ""
	}
	return strings.TrimSpace(f.input.Value())
}

func (f *field) setValue(v string) {
	switch f.kind {
	case fBool:
		f.on = v == "true"
	case fEnum:
		for i, o := range f.options {
			if o == v {
				f.choice = i
			}
		}
	default:
		f.input.SetValue(v)
		f.input.CursorEnd()
	}
}

// check validates a text field's value; "" when fine.
func (f *field) check() string {
	v := f.value()
	if v == "" {
		if f.required {
			return f.label + " is required"
		}
		return ""
	}
	switch f.kind {
	case fInt:
		if _, err := strconv.Atoi(v); err != nil {
			return f.label + " must be a whole number"
		}
	case fDuration:
		if _, err := parseDurationLoose(v); err != nil {
			return f.label + " must be a duration such as 30s or 5m"
		}
	}
	return ""
}

// form is a list of fields with a cursor.
type form struct {
	fields []*field
	cursor int
	err    string
}

func newForm(fields ...*field) *form {
	f := &form{fields: fields}
	f.focus()
	return f
}

func (f *form) visible() []*field {
	var out []*field
	for _, x := range f.fields {
		if !x.hidden {
			out = append(out, x)
		}
	}
	return out
}

func (f *form) current() *field {
	v := f.visible()
	if len(v) == 0 || f.cursor < 0 {
		return nil
	}
	if f.cursor >= len(v) {
		f.cursor = len(v) - 1
	}
	return v[f.cursor]
}

func (f *form) get(name string) *field {
	for _, x := range f.fields {
		if x.name == name {
			return x
		}
	}
	return nil
}

func (f *form) value(name string) string {
	if x := f.get(name); x != nil {
		return x.value()
	}
	return ""
}

func (f *form) focus() {
	for _, x := range f.fields {
		x.input.Blur()
	}
	if c := f.current(); c != nil && c.isText() {
		c.input.Focus()
	}
}

// focusName moves the cursor to a field.
func (f *form) focusName(name string) {
	for i, x := range f.visible() {
		if x.name == name {
			f.cursor = i
		}
	}
	f.focus()
}

func (f *form) move(d int) {
	n := len(f.visible())
	if n == 0 {
		return
	}
	f.cursor = (f.cursor + d + n) % n
	f.focus()
}

// capturing reports whether the cursor is on a text field.
func (f *form) capturing() bool {
	c := f.current()
	return c != nil && c.isText()
}

// update handles a key. It returns the button's action when one is
// pressed (or "submit" for enter on the last text field).
func (f *form) update(k tea.KeyMsg) (string, tea.Cmd) {
	c := f.current()
	if c == nil {
		return "", nil
	}
	s := k.String()
	switch s {
	case "down", "tab":
		f.move(1)
		return "", nil
	case "up", "shift+tab":
		f.move(-1)
		return "", nil
	}
	switch c.kind {
	case fBool:
		switch s {
		case " ", "enter", "x":
			c.on = !c.on
		case "j":
			f.move(1)
		case "k":
			f.move(-1)
		}
		return "", nil
	case fEnum:
		switch s {
		case "right", "l", " ", "enter":
			if len(c.options) > 0 {
				c.choice = (c.choice + 1) % len(c.options)
			}
		case "left", "h":
			if len(c.options) > 0 {
				c.choice = (c.choice + len(c.options) - 1) % len(c.options)
			}
		case "j":
			f.move(1)
		case "k":
			f.move(-1)
		}
		return "", nil
	case fButton:
		switch s {
		case "enter", " ":
			return c.name, nil
		case "j":
			f.move(1)
		case "k":
			f.move(-1)
		}
		return "", nil
	}
	if s == "enter" {
		// Enter on a text field moves on; on the last one before the
		// buttons it presses the first button.
		v := f.visible()
		for i := f.cursor + 1; i < len(v); i++ {
			if v[i].kind == fButton {
				if i == f.cursor+1 {
					return v[i].name, nil
				}
				break
			}
		}
		f.move(1)
		return "", nil
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(k)
	f.err = ""
	return "", cmd
}

// check validates every visible field.
func (f *form) check() string {
	for _, x := range f.visible() {
		if e := x.check(); e != "" {
			return e
		}
	}
	return ""
}

func (f *form) labelWidth() int {
	w := 8
	for _, x := range f.visible() {
		if x.kind != fBool && x.kind != fButton {
			w = max(w, len([]rune(x.label))+2)
		}
	}
	return min(w, 24)
}

// view draws the form; st styles it, color says whether reverse video
// is available for the cursor.
func (f *form) view(w int, st output.Styles, color bool) string {
	var b strings.Builder
	lw := f.labelWidth()
	vis := f.visible()
	var buttons []string
	for i, x := range vis {
		cur := i == f.cursor
		mark := "  "
		if cur {
			mark = "› "
		}
		req := " "
		if x.required {
			req = "*"
		}
		switch x.kind {
		case fButton:
			label := "[ " + x.label + " ]"
			if cur {
				if color {
					label = st.Bold.Reverse(true).Render(" " + x.label + " ")
				} else {
					label = "[>" + x.label + "<]"
				}
			}
			buttons = append(buttons, label)
			continue
		case fBool:
			box := "[ ]"
			if x.on {
				box = "[x]"
			}
			b.WriteString(truncate(mark+box+" "+x.label, w))
		case fEnum:
			val := ""
			if len(x.options) > 0 {
				val = "‹ " + x.options[x.choice] + " ›"
			}
			if cur {
				val = st.Bold.Render(val)
			}
			b.WriteString(truncate(mark+padRight(x.label+req, lw)+val, w))
		default:
			x.input.Width = max(4, w-lw-4)
			if x.masked {
				x.input.EchoMode = textinput.EchoPassword
				x.input.EchoCharacter = '•'
			}
			val := x.input.View()
			if !cur {
				val = x.input.Value()
				if x.masked && val != "" {
					val = strings.Repeat("•", min(len(val), 12))
				}
				if val == "" {
					val = st.Dim.Render("·")
				}
			}
			b.WriteString(truncate(mark+padRight(x.label+req, lw)+val, w))
		}
		b.WriteString("\n")
		if cur && x.help != "" {
			b.WriteString(truncate("  "+strings.Repeat(" ", lw)+st.Dim.Render(x.help), w) + "\n")
		}
	}
	if len(buttons) > 0 {
		b.WriteString("\n  " + strings.Join(buttons, "  ") + "\n")
	}
	if f.err != "" {
		b.WriteString("\n" + st.Warn.Render(output.Wrap(f.err, w)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// scrollView is text that scrolls.
type scrollView struct {
	lines []string
	off   int
}

func (s *scrollView) set(text string) {
	s.lines = strings.Split(strings.TrimRight(text, "\n"), "\n")
	s.off = 0
}

func (s *scrollView) key(k string, h int) bool {
	switch k {
	case "down", "j":
		s.off++
	case "up", "k":
		s.off--
	case "pgdown", " ", "ctrl+d":
		s.off += max(1, h-1)
	case "pgup", "ctrl+u":
		s.off -= max(1, h-1)
	case "home", "g":
		s.off = 0
	case "end", "G":
		s.off = len(s.lines)
	default:
		return false
	}
	s.clamp(h)
	return true
}

func (s *scrollView) clamp(h int) {
	if s.off > len(s.lines)-h {
		s.off = len(s.lines) - h
	}
	if s.off < 0 {
		s.off = 0
	}
}

func (s *scrollView) view(w, h int) string {
	s.clamp(h)
	end := min(len(s.lines), s.off+h)
	out := make([]string, 0, h)
	for _, l := range s.lines[s.off:end] {
		out = append(out, truncate(l, w))
	}
	return strings.Join(out, "\n")
}

// jsonNode is one value in a JSON tree.
type jsonNode struct {
	key      string
	val      any
	kids     []*jsonNode
	open     bool
	depth    int
	isArray  bool
	isObject bool
}

// jsonTree shows any JSON value as a tree that folds.
type jsonTree struct {
	root   *jsonNode
	cursor int
	off    int
}

func newJSONTree(raw string) *jsonTree {
	var v any
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		v = raw
	}
	t := &jsonTree{root: buildNode("", v, 0)}
	t.root.open = true
	for _, k := range t.root.kids {
		if len(k.kids) <= 6 {
			k.open = true
		}
	}
	return t
}

func buildNode(key string, v any, depth int) *jsonNode {
	n := &jsonNode{key: key, val: v, depth: depth}
	switch x := v.(type) {
	case map[string]any:
		n.isObject = true
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// schema_version is noise here.
		for _, k := range keys {
			if k == "schema_version" && depth == 0 {
				continue
			}
			n.kids = append(n.kids, buildNode(k, x[k], depth+1))
		}
	case []any:
		n.isArray = true
		for i, e := range x {
			n.kids = append(n.kids, buildNode(strconv.Itoa(i), e, depth+1))
		}
	}
	return n
}

func (t *jsonTree) rows() []*jsonNode {
	var out []*jsonNode
	var walk func(n *jsonNode)
	walk = func(n *jsonNode) {
		for _, k := range n.kids {
			out = append(out, k)
			if k.open {
				walk(k)
			}
		}
	}
	if t.root.isObject || t.root.isArray {
		walk(t.root)
	} else {
		out = append(out, t.root)
	}
	return out
}

func (t *jsonTree) key(k string, h int) bool {
	rows := t.rows()
	if len(rows) == 0 {
		return false
	}
	t.cursor = max(0, min(t.cursor, len(rows)-1))
	n := rows[t.cursor]
	switch k {
	case "down", "j":
		t.cursor = min(len(rows)-1, t.cursor+1)
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "enter", " ":
		if len(n.kids) > 0 {
			n.open = !n.open
		}
	case "right", "l":
		if len(n.kids) > 0 {
			n.open = true
		}
	case "left", "h":
		n.open = false
	case "pgdown":
		t.cursor = min(len(rows)-1, t.cursor+h)
	case "pgup":
		t.cursor = max(0, t.cursor-h)
	default:
		return false
	}
	return true
}

func scalarText(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return strconv.Quote(x)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (t *jsonTree) view(w, h int, st output.Styles) string {
	rows := t.rows()
	if len(rows) == 0 {
		return st.Dim.Render("(empty)")
	}
	t.cursor = max(0, min(t.cursor, len(rows)-1))
	if t.cursor < t.off {
		t.off = t.cursor
	}
	if t.cursor >= t.off+h {
		t.off = t.cursor - h + 1
	}
	var b strings.Builder
	for i := t.off; i < len(rows) && i < t.off+h; i++ {
		n := rows[i]
		ind := strings.Repeat("  ", max(0, n.depth-1))
		mark := "  "
		if i == t.cursor {
			mark = "› "
		}
		var line string
		switch {
		case n.isObject || n.isArray:
			arrow := "▸"
			if n.open {
				arrow = "▾"
			}
			count := fmt.Sprintf("{%d}", len(n.kids))
			if n.isArray {
				count = fmt.Sprintf("[%d]", len(n.kids))
			}
			line = fmt.Sprintf("%s%s %s %s", ind, arrow, st.Key.Render(n.key), st.Dim.Render(count))
		default:
			line = fmt.Sprintf("%s  %s: %s", ind, st.Key.Render(n.key), scalarText(n.val))
		}
		b.WriteString(truncate(mark+line, w) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// pickItem is one entry of a picker.
type pickItem struct {
	title, desc, value string
}

// picker is a list filtered by fuzzy matching.
type picker struct {
	items  []pickItem
	filter textinput.Model
	cursor int
	off    int
}

func newPicker(items []pickItem) *picker {
	p := &picker{items: items, filter: newInput()}
	p.filter.Prompt = "> "
	p.filter.Focus()
	return p
}

// matches are the items matching the filter, best first.
func (p *picker) matches() []pickItem {
	q := strings.TrimSpace(p.filter.Value())
	if q == "" {
		return p.items
	}
	type scored struct {
		it    pickItem
		score int
		i     int
	}
	var out []scored
	for i, it := range p.items {
		if s, ok := fuzzyScore(q, it.title); ok {
			out = append(out, scored{it, s, i})
		} else if s, ok := fuzzyScore(q, it.desc); ok && len(q) > 2 {
			out = append(out, scored{it, s - 50, i})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].score != out[b].score {
			return out[a].score > out[b].score
		}
		return out[a].i < out[b].i
	})
	items := make([]pickItem, len(out))
	for i, s := range out {
		items[i] = s.it
	}
	return items
}

func (p *picker) selected() *pickItem {
	m := p.matches()
	if p.cursor < 0 || p.cursor >= len(m) {
		return nil
	}
	return &m[p.cursor]
}

// update handles a key; it reports true when enter picked an item.
func (p *picker) update(k tea.KeyMsg) (bool, tea.Cmd) {
	n := len(p.matches())
	switch k.String() {
	case "down", "ctrl+n", "ctrl+j":
		p.cursor = min(n-1, p.cursor+1)
		return false, nil
	case "up", "ctrl+p":
		p.cursor = max(0, p.cursor-1)
		return false, nil
	case "pgdown":
		p.cursor = min(n-1, p.cursor+10)
		return false, nil
	case "pgup":
		p.cursor = max(0, p.cursor-10)
		return false, nil
	case "enter":
		return p.selected() != nil, nil
	}
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(k)
	p.cursor = 0
	return false, cmd
}

func (p *picker) view(w, h int, st output.Styles, color bool) string {
	p.filter.Width = max(4, w-4)
	var b strings.Builder
	b.WriteString(p.filter.View() + "\n\n")
	m := p.matches()
	listH := max(1, h-2)
	if p.cursor < p.off {
		p.off = p.cursor
	}
	if p.cursor >= p.off+listH {
		p.off = p.cursor - listH + 1
	}
	titleW := 0
	for _, it := range m {
		titleW = max(titleW, len([]rune(it.title)))
	}
	titleW = min(titleW+2, w/2)
	for i := p.off; i < len(m) && i < p.off+listH; i++ {
		line := padRight(truncate(m[i].title, titleW-1), titleW) + st.Dim.Render(m[i].desc)
		if i == p.cursor {
			if color {
				line = st.Bold.Reverse(true).Render(padRight(truncate(m[i].title, titleW-1), titleW)) + m[i].desc
			} else {
				line = "> " + line
			}
		} else if !color {
			line = "  " + line
		}
		b.WriteString(truncate(line, w) + "\n")
	}
	if len(m) == 0 {
		b.WriteString(st.Dim.Render("Nothing matches."))
	}
	return strings.TrimRight(b.String(), "\n")
}

// fuzzyScore matches q against s as a subsequence, ignoring case. Higher
// is better: matches at the start, at word starts, and in a row count
// more, and a shorter s wins ties.
func fuzzyScore(q, s string) (int, bool) {
	q, ls := strings.ToLower(q), strings.ToLower(s)
	if q == "" {
		return 0, true
	}
	if i := strings.Index(ls, q); i >= 0 {
		score := 1000 - len(ls)
		if i == 0 {
			score += 500
		} else if !unicode.IsLetter(rune(ls[i-1])) {
			score += 300
		}
		return score, true
	}
	qr, sr := []rune(q), []rune(ls)
	score, qi, prev := 0, 0, -2
	for i, r := range sr {
		if qi < len(qr) && r == qr[qi] {
			score += 10
			if i == prev+1 {
				score += 15
			}
			if i == 0 || !unicode.IsLetter(sr[i-1]) {
				score += 20
			}
			prev = i
			qi++
		}
	}
	if qi < len(qr) {
		return 0, false
	}
	return score - len(sr), true
}
