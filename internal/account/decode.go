package account

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Tool results are read leniently: a missing field, or one with an
// unexpected type, reads as the zero value and never fails the command.
// Values that can be converted are (the number 85 as a string reads as 85,
// a number read as text is rendered); anything else is skipped. The full
// result stays available as Raw, so JSON output shows everything the
// service sent.

// str reads a string. Numbers and booleans are rendered as text; objects,
// lists, and null read as "".
func str(m map[string]any, key string) string {
	switch x := m[key].(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// num reads a number, from a JSON number or a numeric string.
func num(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case json.Number:
		var err error
		if f, err = strconv.ParseFloat(string(x), 64); err != nil {
			return 0, false
		}
	case float64:
		f = x
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(x), 64); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 1<<53 {
		return 0, false
	}
	return f, true
}

// integer reads a whole number (fractions are truncated). ok is false when
// the field is absent or not a number.
func integer(m map[string]any, key string) (int, bool) {
	f, ok := num(m[key])
	if !ok {
		return 0, false
	}
	return int(math.Trunc(f)), true
}

func intOr0(m map[string]any, key string) int {
	n, _ := integer(m, key)
	return n
}

// boolean reads true or false. Numbers are true when not 0; strings are
// read only when they clearly say yes or no.
func boolean(m map[string]any, key string) (bool, bool) {
	switch x := m[key].(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off", "":
			return false, true
		}
		return false, false
	}
	if f, ok := num(m[key]); ok {
		return f != 0, true
	}
	return false, false
}

// object reads a nested object; nil when absent or not an object.
func object(m map[string]any, key string) map[string]any {
	o, _ := m[key].(map[string]any)
	return o
}

// objects reads a list of objects, skipping entries that are not objects.
func objects(m map[string]any, key string) []map[string]any {
	l, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(l))
	for _, e := range l {
		if o, ok := e.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

// requiredOf lists a JSON Schema's required property names.
func requiredOf(s map[string]any) []string {
	var out []string
	switch r := s["required"].(type) {
	case []any:
		for _, x := range r {
			if n, ok := x.(string); ok {
				out = append(out, n)
			}
		}
	case []string:
		out = append(out, r...)
	}
	return out
}
