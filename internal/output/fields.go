package output

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

// optionalKeys are result blocks AudD includes only when asked for (--return)
// and found. A --fields path through one of them is valid even when no
// result in this output has it.
var optionalKeys = map[string]bool{"apple_music": true, "spotify": true, "deezer": true, "musicbrainz": true}

// checkFields returns a usage error (exit 2) when a --fields path names a
// field that this output does not have. A path is known when the value v
// declares it (struct fields, including ones left out when empty), when an
// item has it, or when an item has null (or a list) where the path goes
// deeper: result.artist is valid for a result that is null because nothing
// matched. items are the objects --fields applies to; with none, any path
// the type does not rule out is accepted.
func (p *Printer) checkFields(v any, items []json.RawMessage) error {
	if len(p.opts.Fields) == 0 {
		return nil
	}
	declared, open := declaredPaths(v)
	var unknown []string
	for _, f := range p.opts.Fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if fieldKnown(f, declared, open, items) {
			continue
		}
		unknown = append(unknown, f)
	}
	if len(unknown) == 0 {
		return nil
	}
	return unknownFieldsError(unknown, availablePaths(declared, items))
}

func unknownFieldsError(unknown, avail []string) error {
	what := "field"
	if len(unknown) > 1 {
		what = "fields"
	}
	msg := "unknown --fields " + what + " " + quoteList(unknown)
	if len(avail) > 0 {
		msg += "; available fields: " + strings.Join(avail, ", ")
	}
	hint := "pass fields from the list"
	if flat := flatSpelling(unknown, avail); flat != "" {
		return &Error{Code: "invalid_argument", Message: msg, Hint: "CSV columns are flat: use " + flat, Exit: ExitUsage}
	}
	for _, a := range avail {
		if strings.Contains(a, ".") {
			hint += ", dotted for nested ones (" + a + ")"
			break
		}
	}
	return &Error{Code: "invalid_argument", Message: msg, Hint: hint, Exit: ExitUsage}
}

// flatSpelling is the flat CSV column for a dotted unknown field
// (result.isrc → isrc) when the output has it, or "".
func flatSpelling(unknown, avail []string) string {
	for _, u := range unknown {
		_, last, ok := strings.Cut(u, ".")
		if !ok {
			continue
		}
		for _, a := range avail {
			if a == last && !strings.Contains(a, ".") {
				return last
			}
		}
	}
	return ""
}

func quoteList(s []string) string {
	q := make([]string, len(s))
	for i, x := range s {
		b, _ := json.Marshal(x)
		q[i] = string(b)
	}
	return strings.Join(q, ", ")
}

func fieldKnown(path string, declared map[string]bool, open bool, items []json.RawMessage) bool {
	if declared[path] {
		return true
	}
	for d := range declared {
		if strings.HasPrefix(d, path+".") {
			return true // an object with declared fields under it
		}
	}
	if len(items) == 0 {
		// Nothing to look at: only the declared paths can rule a path out.
		return open || len(declared) == 0
	}
	for _, it := range items {
		if pathInItem(it, path) {
			return true
		}
	}
	return false
}

// pathInItem reports whether path exists in item, or could: it stops at a
// null or a list, or at an optional result block that is absent.
func pathInItem(item json.RawMessage, path string) bool {
	cur := item
	for _, seg := range strings.Split(path, ".") {
		switch kind(cur) {
		case 'n', '[':
			return true
		case '{':
		default:
			return false
		}
		o, err := parseObject(cur)
		if err != nil {
			return false
		}
		v, ok := o.get(seg)
		if !ok {
			return optionalKeys[seg]
		}
		cur = v
	}
	return true
}

// availablePaths lists the field paths of the output: the leaves of the
// items in first-seen order, then declared paths the items left out.
func availablePaths(declared map[string]bool, items []json.RawMessage) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		for _, c := range flatten(it) {
			if c.Key != "schema_version" && c.Key != "type" && c.Key != "value" && !seen[c.Key] {
				seen[c.Key] = true
				out = append(out, c.Key)
			}
		}
	}
	var extra []string
	for d := range declared {
		if !seen[d] && !hasDeclaredChild(declared, d) {
			extra = append(extra, d)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

func hasDeclaredChild(declared map[string]bool, p string) bool {
	for d := range declared {
		if strings.HasPrefix(d, p+".") {
			return true
		}
	}
	return false
}

var (
	marshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	rawType       = reflect.TypeOf(json.RawMessage(nil))
	timeType      = reflect.TypeOf(time.Time{})
)

// declaredPaths returns the JSON field paths the Go type of v declares (for a
// slice, its element type). open is true when v's shape is not fully known
// from its type (maps, interfaces, raw JSON, custom marshalers).
func declaredPaths(v any) (paths map[string]bool, open bool) {
	paths = map[string]bool{}
	if v == nil {
		return paths, true
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice && t != rawType {
		t = t.Elem()
	}
	open = walkType(t, "", paths, nil, 0)
	return paths, open
}

// CheckFieldsFor checks --fields against the type of the result lines a
// command will print, before it does any work: a path is valid when v's
// type declares it, or when known names it. A known entry ending in ".*"
// accepts anything below it (an optional provider block, say). Without
// known, a path into a part of v whose shape is open (raw API JSON, maps)
// is accepted too and checked again against the output. It is a usage
// error (exit 2) that lists the available fields otherwise.
//
// Once the check passes on fields the type or known fully describe, the
// output is not checked again: a field a result happens to lack prints as
// null rather than failing after the work is done.
func (p *Printer) CheckFieldsFor(v any, known ...string) error {
	if len(p.opts.Fields) == 0 {
		return nil
	}
	declared := map[string]bool{}
	opens := map[string]bool{}
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	walkType(t, "", declared, opens, 0)
	below := map[string]bool{}
	for _, k := range known {
		if q, ok := strings.CutSuffix(k, ".*"); ok {
			below[q] = true
			declared[q] = true
			continue
		}
		declared[k] = true
	}
	strict := len(known) > 0
	var unknown []string
	usedOpen := false
	for _, f := range p.opts.Fields {
		f = strings.TrimSpace(f)
		if f == "" || declared[f] || hasDeclaredChild(declared, f) || underAny(f, below) {
			continue
		}
		if !strict && underAny(f, opens) {
			usedOpen = true
			continue
		}
		unknown = append(unknown, f)
	}
	if len(unknown) > 0 {
		return unknownFieldsError(unknown, availablePaths(declared, nil))
	}
	if !usedOpen {
		p.outMu.Lock()
		p.fieldsOK = true
		p.outMu.Unlock()
	}
	return nil
}

func underAny(f string, prefixes map[string]bool) bool {
	for q := range prefixes {
		if strings.HasPrefix(f, q+".") {
			return true
		}
	}
	return false
}

// walkType records the fields of t under prefix and reports whether any part
// of t is open (its fields are not known from the type).
// When opens is not nil, it also records the paths whose own shape is open.
func walkType(t reflect.Type, prefix string, paths, opens map[string]bool, depth int) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if depth > 6 {
		return true
	}
	if t == timeType {
		return false
	}
	if t == rawType || t.Implements(marshalerType) || reflect.PointerTo(t).Implements(marshalerType) {
		return true
	}
	switch t.Kind() {
	case reflect.Map, reflect.Interface:
		return true
	case reflect.Struct:
	default:
		return false
	}
	open := false
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		if n, _, _ := strings.Cut(tag, ","); n != "" {
			name = n
		} else if f.Anonymous {
			if walkType(f.Type, prefix, paths, opens, depth+1) {
				open = true
			}
			continue
		}
		p := name
		if prefix != "" {
			p = prefix + "." + name
		}
		paths[p] = true
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct || ft.Kind() == reflect.Map || ft.Kind() == reflect.Interface || ft == rawType {
			if opens != nil && (ft.Kind() == reflect.Map || ft.Kind() == reflect.Interface || ft == rawType ||
				ft.Implements(marshalerType) || reflect.PointerTo(ft).Implements(marshalerType)) {
				opens[p] = true
			}
			if walkType(ft, p, paths, opens, depth+1) {
				open = true
			}
		}
	}
	return open
}

// zeroSample is the JSON of the zero value of v's element type (v is a
// slice) or of v itself, for a CSV header when there are no rows.
func zeroSample(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	raw, err := marshalRaw(reflect.New(t).Elem().Interface())
	if err != nil || kind(raw) != '{' {
		return nil
	}
	return raw
}
