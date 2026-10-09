package contract

import (
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func obj(props map[string]any, required ...any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

var (
	str = map[string]any{"type": "string"}
	num = map[string]any{"type": "integer"}
)

func TestSchemaDiffSame(t *testing.T) {
	for name, s := range testutil.FakeOutputSchemas {
		if d, n := SchemaDiff(name, s, s); len(d) != 0 || len(n) != 0 {
			t.Errorf("%s against itself: %v %v", name, d, n)
		}
	}
}

func TestSchemaDiffFindsChanges(t *testing.T) {
	want := obj(map[string]any{
		"email": str,
		"count": num,
		"note":  str,
		"days":  map[string]any{"type": "array", "items": obj(map[string]any{"date": str}, "date")},
	}, "email", "count", "days")
	got := obj(map[string]any{
		"email_address": str,
		"count":         map[string]any{"type": "string"},
		"days":          map[string]any{"type": "array", "items": obj(map[string]any{"day": str})},
	}, "count", "days")
	d, n := SchemaDiff("tool", want, got)
	diffs := strings.Join(d, "\n")
	for _, w := range []string{
		"tool.email: missing (the CLI requires it)",
		"tool.count: type [string], the CLI expects [integer]",
		"tool.days[].date: missing (the CLI requires it)",
	} {
		if !strings.Contains(diffs, w) {
			t.Errorf("diff lacks %q:\n%s", w, diffs)
		}
	}
	if strings.Contains(diffs, "tool.note") {
		t.Errorf("a missing optional field is reported as breaking:\n%s", diffs)
	}
	if len(n) != 1 || n[0] != "tool.note: missing (optional for the CLI)" {
		t.Errorf("notes: %v", n)
	}
}

func TestSchemaDiffOptionalMissingOnly(t *testing.T) {
	want := obj(map[string]any{"email": str, "plan": str}, "email")
	got := obj(map[string]any{"email": str}, "email")
	d, n := SchemaDiff("get_profile", want, got)
	if len(d) != 0 {
		t.Fatalf("breaking diffs for a missing optional field: %v", d)
	}
	if len(n) != 1 {
		t.Fatalf("notes: %v", n)
	}
}

func TestSchemaDiffRequiredBecameOptional(t *testing.T) {
	want := obj(map[string]any{"email": str}, "email")
	got := obj(map[string]any{"email": str})
	d, _ := SchemaDiff("get_profile", want, got)
	if len(d) != 1 || !strings.Contains(d[0], "optional in the live schema") {
		t.Fatalf("%v", d)
	}
}

func TestSchemaDiffNullable(t *testing.T) {
	want := obj(map[string]any{"paid_until": map[string]any{"type": []any{"string", "null"}}})
	got := obj(map[string]any{"paid_until": map[string]any{"type": "string"}})
	if d, _ := SchemaDiff("s", want, got); len(d) != 0 {
		t.Fatalf("a non-null live type fits a nullable expectation: %v", d)
	}
	if d, _ := SchemaDiff("s", got, want); len(d) == 0 {
		t.Fatal("a nullable live type does not fit a non-null expectation")
	}
}
