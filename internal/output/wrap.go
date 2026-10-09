package output

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// StdoutWidth is the terminal width of stdout, or 0 when stdout is not a
// terminal (machine output and pipes are never fitted to a width).
func (p *Printer) StdoutWidth() int {
	if !p.opts.StdoutTTY {
		return 0
	}
	if p.opts.StdoutWidth > 0 {
		return p.opts.StdoutWidth
	}
	if f, ok := p.stdout.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return defaultWidth
}

// noteWidth is the width notes on stderr are wrapped at, or 0 when stderr
// is not a terminal.
func (p *Printer) noteWidth() int {
	if !p.opts.StderrTTY {
		return 0
	}
	return p.stderrWidth()
}

// Wrap breaks each line of s at spaces so it fits in width columns.
// Continuation lines keep the line's leading indent. A word wider than the
// line stays whole. Escape sequences take no width. width <= 0 returns s.
func Wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = wrapLine(l, width)
	}
	return strings.Join(lines, "\n")
}

func wrapLine(l string, width int) string {
	if ansi.StringWidth(l) <= width {
		return l
	}
	body := strings.TrimLeft(l, " ")
	indent := l[:len(l)-len(body)]
	if len(indent) >= width/2 {
		indent = ""
	}
	var b strings.Builder
	cur := indent
	curW := len(indent)
	start := true
	for _, word := range strings.Split(body, " ") {
		ww := ansi.StringWidth(word)
		if !start && curW+1+ww > width {
			b.WriteString(cur + "\n")
			cur, curW, start = indent, len(indent), true
		}
		if !start {
			cur += " "
			curW++
		}
		cur += word
		curW += ww
		start = false
	}
	b.WriteString(cur)
	return b.String()
}

// Truncate cuts s to at most w columns, ending it with "…" when cut.
func Truncate(s string, w int) string {
	if w <= 0 || ansi.StringWidth(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return ansi.Truncate(s, w, "…")
}

// FitTable cuts the cells of a table (columns separated by gap spaces) so
// each row fits in width columns. The columns in shrink are cut, in that
// order, down to at most min columns each; cut cells end with "…". width
// <= 0 leaves the table as is.
func FitTable(rows [][]string, width, gap, min int, shrink ...int) {
	if width <= 0 || len(rows) == 0 {
		return
	}
	colW := func(c int) int {
		w := 0
		for _, r := range rows {
			if c < len(r) {
				w = max(w, ansi.StringWidth(r[c]))
			}
		}
		return w
	}
	total := func() int {
		n := len(rows[0])
		t := gap * (n - 1)
		for c := 0; c < n; c++ {
			t += colW(c)
		}
		return t
	}
	for _, c := range shrink {
		over := total() - width
		if over <= 0 {
			return
		}
		w := colW(c)
		target := max(min, w-over)
		if target >= w {
			continue
		}
		for _, r := range rows {
			if c < len(r) {
				r[c] = Truncate(r[c], target)
			}
		}
	}
}

// WriteTable writes rows as columns separated by two spaces, the first row
// being the header, fitted to width (see FitTable). The last column is not
// padded.
func WriteTable(w io.Writer, rows [][]string, width int, shrink ...int) {
	FitTable(rows, width, 2, 12, shrink...)
	if len(rows) == 0 {
		return
	}
	n := len(rows[0])
	widths := make([]int, n)
	for _, r := range rows {
		for c := 0; c < n && c < len(r); c++ {
			widths[c] = max(widths[c], ansi.StringWidth(r[c]))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for c := 0; c < n && c < len(r); c++ {
			b.WriteString(r[c])
			if c < n-1 {
				b.WriteString(strings.Repeat(" ", widths[c]-ansi.StringWidth(r[c])+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// WriteKeyValues writes "key  value" rows with the values aligned, wrapping
// long values at spaces to fit width (continuation lines are aligned with
// the value). width <= 0 does not wrap.
func WriteKeyValues(w io.Writer, rows [][2]string, width int) {
	keyW := 0
	for _, r := range rows {
		keyW = max(keyW, ansi.StringWidth(r[0]))
	}
	pad := strings.Repeat(" ", keyW+2)
	for _, r := range rows {
		val := r[1]
		if width > 0 && width-keyW-2 >= 20 {
			val = Wrap(val, width-keyW-2)
		}
		lines := strings.Split(val, "\n")
		fmt.Fprintf(w, "%s%s%s\n", r[0], strings.Repeat(" ", keyW+2-ansi.StringWidth(r[0])), lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(w, "%s%s\n", pad, l)
		}
	}
}
