package output

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func ttyPrinter(errb *bytes.Buffer, width int) *Printer {
	return NewPrinter(&bytes.Buffer{}, errb, PrinterOptions{Format: FormatTable, StderrTTY: true, StderrWidth: width, NoColor: true})
}

func TestTransientBlockCountsRows(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		writes []string
		input  []string
		want   int
	}{
		{"plain lines", 80, []string{"one\n", "two\n\n", "three\n"}, nil, 4},
		{"text without a newline stays on the row", 80, []string{"one\ntwo"}, nil, 1},
		{"exactly the width does not wrap by itself", 10, []string{"0123456789\n"}, nil, 1},
		{"one past the width wraps", 10, []string{"0123456789a\n"}, nil, 2},
		{"long line wraps several times", 10, []string{strings.Repeat("x", 35) + "\n"}, nil, 4},
		{"writes split mid-line", 10, []string{"01234", "56789", "ab\n"}, nil, 2},
		{"escape sequences take no space", 10, []string{"\033[1m0123456789\033[0m\n"}, nil, 1},
		{"wide runes", 10, []string{"漢字漢字漢字\n"}, nil, 2},
		{"echoed input", 80, []string{"paste it here\n"}, []string{"http://127.0.0.1/callback?code=x"}, 2},
		{"echoed input that wraps", 10, []string{"prompt\n"}, []string{strings.Repeat("y", 25)}, 4},
		{"empty input is a row", 80, []string{"prompt\n"}, []string{""}, 2},
		{"spinner updates stay on one row", 80, []string{"block\n", "\r\033[K⠋ Waiting", "\r\033[K⠙ Waiting", "\r\033[K"}, nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errb bytes.Buffer
			p := ttyPrinter(&errb, tt.width)
			b := p.TransientBlock()
			for _, w := range tt.writes {
				fmt.Fprint(b, w)
			}
			for _, in := range tt.input {
				b.CountInput(in)
			}
			if got := b.Rows(); got != tt.want {
				t.Fatalf("rows = %d, want %d", got, tt.want)
			}
			if errb.String() != strings.Join(tt.writes, "") {
				t.Fatalf("writes go through to stderr: %q", errb.String())
			}
		})
	}
}

func TestTransientBlockCountsPrinterNotes(t *testing.T) {
	var errb bytes.Buffer
	p := ttyPrinter(&errb, 80)
	b := p.TransientBlock()
	p.Warn("Sign in here:\n\n  http://example.com\n")
	p.Info("Could not open a browser.")
	stop := p.Spinner("Waiting")
	time.Sleep(10 * time.Millisecond)
	stop()
	if got := b.Rows(); got != 4 {
		t.Fatalf("rows = %d, want 4 (%q)", got, errb.String())
	}
	if p.TransientBlock() != b {
		t.Fatal("a nested block is the open one")
	}
}

func TestTransientBlockClear(t *testing.T) {
	var errb bytes.Buffer
	p := ttyPrinter(&errb, 20)
	b := p.TransientBlock()
	fmt.Fprint(b, "one\n", strings.Repeat("x", 30), "\n")
	b.CountInput("typed")
	errb.Reset()
	if !b.Clear() {
		t.Fatal("Clear should erase the block")
	}
	if got := errb.String(); got != "\033[4A\r\033[J" {
		t.Fatalf("Clear wrote %q", got)
	}
	if p.Transient() != nil || b.Clear() {
		t.Fatal("Clear closes the block")
	}
	errb.Reset()
	p.Warn("after")
	if errb.String() != "after\n" || b.Rows() != 4 {
		t.Fatal("writes after Clear are not part of the block")
	}

	// A block of text without a newline only goes back to the start of the row.
	errb.Reset()
	b = p.TransientBlock()
	fmt.Fprint(b, "\r\033[K⠋ Waiting")
	b.Clear()
	if got := errb.String(); !strings.HasSuffix(got, "Waiting\r\033[J") {
		t.Fatalf("Clear of one row wrote %q", got)
	}
}

func TestTransientBlockKeep(t *testing.T) {
	var errb bytes.Buffer
	p := ttyPrinter(&errb, 80)
	b := p.TransientBlock()
	fmt.Fprint(b, "instructions\n")
	b.Keep()
	errb.Reset()
	if b.Clear() || errb.Len() != 0 || p.Transient() != nil {
		t.Fatalf("a kept block stays on screen: %q", errb.String())
	}
}

func TestTransientBlockPassThrough(t *testing.T) {
	cases := map[string]PrinterOptions{
		"stderr not a terminal": {Format: FormatTable},
		"json on a terminal":    {Format: FormatJSON, StderrTTY: true},
		"jsonl on a terminal":   {Format: FormatJSONL, StderrTTY: true},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			var errb bytes.Buffer
			p := NewPrinter(&bytes.Buffer{}, &errb, opts)
			b := p.TransientBlock()
			fmt.Fprint(b, "instructions\n")
			b.CountInput("typed")
			if p.Transient() != nil {
				t.Fatal("no block opens")
			}
			if b.Clear() || errb.String() != "instructions\n" {
				t.Fatalf("pass-through only: %q", errb.String())
			}
		})
	}
}

func TestSeparateNotes(t *testing.T) {
	var errb bytes.Buffer
	p := NewPrinter(&bytes.Buffer{}, &errb, PrinterOptions{Format: FormatTable})
	p.SeparateNotes()
	if errb.Len() != 0 {
		t.Fatal("no notes, no empty line")
	}
	p.Info("note")
	p.SeparateNotes()
	p.SeparateNotes()
	if errb.String() != "note\n\n" {
		t.Fatalf("one empty line after notes: %q", errb.String())
	}
	for name, opts := range map[string]PrinterOptions{"json": {Format: FormatJSON}, "quiet": {Format: FormatTable, Quiet: true}} {
		errb.Reset()
		p := NewPrinter(&bytes.Buffer{}, &errb, opts)
		p.Warn("note")
		p.SeparateNotes()
		if errb.String() != "note\n" {
			t.Fatalf("%s: no empty line: %q", name, errb.String())
		}
	}
}

func TestProgressAndSpinnerFitOneRow(t *testing.T) {
	var errb bytes.Buffer
	p := ttyPrinter(&errb, 20)
	b := p.TransientBlock()
	pr := p.Progress()
	pr.Start(10, "Recognizing a-very-long-file-name.mp3")
	pr.Inc(1)
	pr.Done()
	stop := p.Spinner("Waiting for you to approve the sign-in")
	time.Sleep(10 * time.Millisecond)
	stop()
	if b.Rows() != 0 {
		t.Fatalf("progress wrapped: %q", errb.String())
	}
	if !strings.Contains(errb.String(), "Recognizing …  1/10") {
		t.Fatalf("progress line: %q", errb.String())
	}
}

// When stdout and stderr are the same terminal, a CSV or JSON line never
// starts on the progress line: it is erased before each row and drawn
// again after it.
func TestProgressIsErasedBeforeRows(t *testing.T) {
	for _, format := range []Format{FormatCSV, FormatJSONL} {
		var term bytes.Buffer // one terminal for both streams
		p := NewPrinter(&term, &term, PrinterOptions{Format: format, StdoutTTY: true, StderrTTY: true, StderrWidth: 80})
		p.SetStreaming()
		pr := p.Progress()
		pr.Start(3, "Recognizing")
		for i := 0; i < 3; i++ {
			if err := p.Event("result", map[string]any{"input": "a.mp3", "artist": "A"}); err != nil {
				t.Fatal(err)
			}
			pr.Inc(1)
		}
		pr.Done()
		// What the terminal shows: each \r returns to the line start and
		// \033[K clears it.
		var lines []string
		for _, l := range strings.Split(term.String(), "\n") {
			if i := strings.LastIndex(l, "\r\033[K"); i >= 0 {
				l = l[i+len("\r\033[K"):]
			}
			lines = append(lines, l)
		}
		for _, l := range lines {
			if strings.Contains(l, "Recognizing") && (strings.Contains(l, "a.mp3") || strings.Contains(l, "input")) {
				t.Fatalf("%s: a row was written onto the progress line: %q", format, term.String())
			}
		}
		if !strings.Contains(term.String(), "a.mp3") {
			t.Fatalf("%s: no rows: %q", format, term.String())
		}
	}
	// Piped stdout is left alone.
	var out, errb bytes.Buffer
	p := NewPrinter(&out, &errb, PrinterOptions{Format: FormatCSV, StderrTTY: true})
	pr := p.Progress()
	pr.Start(1, "Recognizing")
	_ = p.Event("result", map[string]any{"input": "a.mp3"})
	pr.Done()
	if strings.Contains(out.String(), "\033") {
		t.Fatalf("escape codes on piped stdout: %q", out.String())
	}
}
