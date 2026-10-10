package tui

import (
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/output"
)

func TestFormFields(t *testing.T) {
	f := newForm(
		&field{kind: fText, name: "input", label: "Input", required: true, input: newInput()},
		&field{kind: fInt, name: "limit", label: "Limit", input: newInput()},
		boolField("tracklist", "Tracklist", ""),
		enumField("by", "Group by", "", []string{"song", "artist", "label"}),
		buttonField("run", "Run"),
	)
	if !f.capturing() {
		t.Fatal("a text field takes the keys")
	}
	if e := f.check(); e != "Input is required" {
		t.Fatalf("check: %q", e)
	}
	for _, k := range typeText("mix.mp3") {
		f.update(k)
	}
	f.update(key("down"))
	for _, k := range typeText("2x") {
		f.update(k)
	}
	if e := f.check(); e != "Limit must be a whole number" {
		t.Fatalf("check: %q", e)
	}
	f.update(key("backspace"))
	f.update(key("down"))
	if f.capturing() {
		t.Fatal("a checkbox does not take letters")
	}
	f.update(key(" "))
	f.update(key("down"))
	f.update(key("right"))
	f.update(key("right"))
	f.update(key("down"))
	act, _ := f.update(key("enter"))
	if act != "run" {
		t.Fatalf("button: %q", act)
	}
	if f.value("input") != "mix.mp3" || f.value("limit") != "2" || f.value("tracklist") != "true" || f.value("by") != "label" {
		t.Fatalf("values: %q %q %q %q", f.value("input"), f.value("limit"), f.value("tracklist"), f.value("by"))
	}
	v := f.view(60, output.Styles{}, false)
	for _, s := range []string{"Input*", "[x] Tracklist", "‹ label ›", "[>Run<]"} {
		if !strings.Contains(v, s) {
			t.Fatalf("view lacks %q:\n%s", s, v)
		}
	}
}

func TestFormEnterPressesButton(t *testing.T) {
	f := newForm(&field{kind: fText, name: "url", label: "URL", input: newInput()}, buttonField("add", "Add"))
	act, _ := f.update(key("enter"))
	if act != "add" {
		t.Fatalf("enter on the last field presses the button: %q", act)
	}
}

func TestFuzzyOrder(t *testing.T) {
	p := newPicker([]pickItem{
		{title: "streams record"}, {title: "recognize"}, {title: "streams recorder start"}, {title: "listen"},
	})
	for _, k := range typeText("rec") {
		p.update(k)
	}
	m := p.matches()
	if len(m) != 3 || m[0].title != "recognize" {
		t.Fatalf("matches: %+v", m)
	}
	if _, ok := fuzzyScore("sad", "streams add"); !ok {
		t.Fatal("subsequence across words")
	}
	if _, ok := fuzzyScore("zz", "recognize"); ok {
		t.Fatal("no match")
	}
}

func TestJSONTree(t *testing.T) {
	tr := newJSONTree(`{"schema_version":1,"plans":[{"plan":"a"},{"plan":"b"}],"note":"x"}`)
	v := tr.view(60, 20, output.Styles{})
	if strings.Contains(v, "schema_version") || !strings.Contains(v, "▾ plans [2]") || !strings.Contains(v, `note: "x"`) {
		t.Fatalf("tree:\n%s", v)
	}
	tr.key("down", 20) // keys are sorted: note, then plans
	tr.key("enter", 20)
	if v := tr.view(60, 20, output.Styles{}); !strings.Contains(v, "▸ plans [2]") {
		t.Fatalf("collapse:\n%s", v)
	}
}

func TestScrollView(t *testing.T) {
	var s scrollView
	s.set("a\nb\nc\nd\ne")
	s.key("down", 2)
	s.key("end", 2)
	if v := s.view(10, 2); v != "d\ne" {
		t.Fatalf("%q", v)
	}
}
