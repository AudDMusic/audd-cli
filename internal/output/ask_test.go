package output

import (
	"bytes"
	"testing"
)

func TestConfirmUsesAsk(t *testing.T) {
	var asked []string
	answer := true
	p := NewPrinter(&bytes.Buffer{}, &bytes.Buffer{}, PrinterOptions{Ask: func(q string) bool {
		asked = append(asked, q)
		return answer
	}})
	if !p.CanAsk() {
		t.Fatal("a printer with Ask can ask")
	}
	if err := p.Confirm("Go?", false); err != nil {
		t.Fatalf("yes: %v", err)
	}
	answer = false
	err := p.Confirm("Go again?", false)
	if e := AsError(err); err == nil || e.Code != "declined" || ExitCode(e) != ExitSafety {
		t.Fatalf("no: %v", err)
	}
	if err := p.Confirm("Skipped?", true); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != "Go?" || asked[1] != "Go again?" {
		t.Fatalf("asked %q", asked)
	}
	if NewPrinter(nil, nil, PrinterOptions{}).CanAsk() {
		t.Fatal("no terminal and no Ask: cannot ask")
	}
	if !NewPrinter(nil, nil, PrinterOptions{StdinTTY: true}).CanAsk() {
		t.Fatal("a terminal on stdin can ask")
	}
}
