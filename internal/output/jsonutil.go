package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// member is one key of a JSON object, kept in document order.
type member struct {
	Key string
	Val json.RawMessage
}

// object is a JSON object that marshals with its keys in order.
type object []member

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.Key)
		b.Write(k)
		b.WriteByte(':')
		if len(m.Val) == 0 {
			b.WriteString("null")
		} else {
			b.Write(m.Val)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Val, true
		}
	}
	return nil, false
}

func (o object) without(keys ...string) object {
	out := make(object, 0, len(o))
outer:
	for _, m := range o {
		for _, k := range keys {
			if m.Key == k {
				continue outer
			}
		}
		out = append(out, m)
	}
	return out
}

// marshalRaw encodes v without HTML escaping.
func marshalRaw(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(b.Bytes(), "\n")), nil
}

func kind(raw json.RawMessage) byte {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	if len(raw) == 0 {
		return 'n'
	}
	return raw[0]
}

// parseObject splits a JSON object into its members, in order.
func parseObject(raw json.RawMessage) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	var o object
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o = append(o, member{Key: kt.(string), Val: v})
	}
	return o, nil
}

func parseArray(raw json.RawMessage) ([]json.RawMessage, error) {
	var items []json.RawMessage
	err := json.Unmarshal(raw, &items)
	return items, err
}

// lookup follows a dotted path ("result.artist") through nested objects.
func lookup(raw json.RawMessage, path string) (json.RawMessage, bool) {
	cur := raw
	for _, part := range strings.Split(path, ".") {
		if kind(cur) != '{' {
			return nil, false
		}
		o, err := parseObject(cur)
		if err != nil {
			return nil, false
		}
		v, ok := o.get(part)
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// setPath stores val at a dotted path inside o, creating nested objects.
func setPath(o object, path []string, val json.RawMessage) object {
	if len(path) == 1 {
		for i := range o {
			if o[i].Key == path[0] {
				o[i].Val = val
				return o
			}
		}
		return append(o, member{Key: path[0], Val: val})
	}
	for i := range o {
		if o[i].Key == path[0] {
			child, err := parseObject(o[i].Val)
			if err != nil {
				child = nil
			}
			child = setPath(child, path[1:], val)
			o[i].Val, _ = child.MarshalJSON()
			return o
		}
	}
	child := setPath(nil, path[1:], val)
	raw, _ := child.MarshalJSON()
	return append(o, member{Key: path[0], Val: raw})
}

// selectFields keeps only the given dotted paths of an object, in field order.
// Missing paths are emitted as null so the shape is predictable.
func selectFields(raw json.RawMessage, fields []string) object {
	var out object
	for _, f := range fields {
		v, ok := lookup(raw, f)
		if !ok {
			v = json.RawMessage("null")
		}
		out = setPath(out, strings.Split(f, "."), v)
	}
	return out
}

// flatten turns a JSON value into ordered dotted-key cells for CSV and tables.
// Nested objects become "a.b" keys; arrays are kept as compact JSON text.
func flatten(raw json.RawMessage) []cell {
	var cells []cell
	var walk func(prefix string, v json.RawMessage)
	walk = func(prefix string, v json.RawMessage) {
		if kind(v) == '{' {
			o, err := parseObject(v)
			if err == nil {
				if len(o) == 0 && prefix != "" {
					cells = append(cells, cell{Key: prefix})
				}
				for _, m := range o {
					k := m.Key
					if prefix != "" {
						k = prefix + "." + m.Key
					}
					walk(k, m.Val)
				}
				return
			}
		}
		key := prefix
		if key == "" {
			key = "value"
		}
		cells = append(cells, cell{Key: key, Val: scalarText(v)})
	}
	walk("", raw)
	return cells
}

type cell struct {
	Key, Val string
}

func scalarText(v json.RawMessage) string {
	switch kind(v) {
	case 'n':
		return ""
	case '"':
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s
		}
	case '[', '{':
		var b bytes.Buffer
		if err := json.Compact(&b, v); err == nil {
			return b.String()
		}
	}
	return string(bytes.TrimSpace(v))
}
