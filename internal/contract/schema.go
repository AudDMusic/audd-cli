package contract

import (
	"fmt"
	"sort"
)

// SchemaDiff compares what the CLI expects (want) with the live schema (got),
// recursively through objects and arrays. diffs are the changes that break
// the CLI: a field it requires that is missing or no longer required, and
// type changes. notes are fields the CLI reads leniently that the live
// schema lacks; the CLI still works without them.
func SchemaDiff(path string, want, got map[string]any) (diffs, notes []string) {
	if wt, gt := types(want["type"]), types(got["type"]); len(wt) > 0 && len(gt) > 0 && !subset(gt, wt) {
		diffs = append(diffs, fmt.Sprintf("%s: type %v, the CLI expects %v", path, gt, wt))
	}
	if items, ok := want["items"].(map[string]any); ok {
		if gi, ok := got["items"].(map[string]any); ok {
			d, n := SchemaDiff(path+"[]", items, gi)
			diffs, notes = append(diffs, d...), append(notes, n...)
		} else {
			diffs = append(diffs, fmt.Sprintf("%s: no item schema", path))
		}
	}
	wp, _ := want["properties"].(map[string]any)
	gp, _ := got["properties"].(map[string]any)
	req := map[string]bool{}
	for _, r := range asList(got["required"]) {
		req[r] = true
	}
	for _, r := range asList(want["required"]) {
		if gp[r] == nil {
			diffs = append(diffs, fmt.Sprintf("%s.%s: missing (the CLI requires it)", path, r))
		} else if !req[r] {
			diffs = append(diffs, fmt.Sprintf("%s.%s: optional in the live schema (the CLI requires it)", path, r))
		}
	}
	keys := make([]string, 0, len(wp))
	for k := range wp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w, _ := wp[k].(map[string]any)
		g, ok := gp[k].(map[string]any)
		if !ok {
			if !contains(asList(want["required"]), k) {
				notes = append(notes, fmt.Sprintf("%s.%s: missing (optional for the CLI)", path, k))
			}
			continue
		}
		d, n := SchemaDiff(path+"."+k, w, g)
		diffs, notes = append(diffs, d...), append(notes, n...)
	}
	return diffs, notes
}

func types(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}

// subset reports whether every live type is one the CLI accepts ("number"
// covers "integer" values that arrive as whole numbers).
func subset(got, want []string) bool {
	for _, g := range got {
		if !contains(want, g) && !(g == "integer" && contains(want, "number")) {
			return false
		}
	}
	return true
}

func asList(v any) []string { return types(v) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
