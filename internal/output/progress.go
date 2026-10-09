package output

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
)

// Progress reports progress on stderr. It is a no-op when stderr is not a TTY.
type Progress interface {
	Start(total int, label string)
	Inc(n int)
	SetLabel(s string)
	Done()
}

// Progress returns a progress reporter bound to stderr.
func (p *Printer) Progress() Progress {
	if !p.opts.StderrTTY || p.opts.Quiet {
		return nopProgress{}
	}
	l := &lineProgress{w: p.stderr, width: p.stderrWidth()}
	if p.opts.StdoutTTY {
		// stdout and stderr are probably the same terminal: output lines
		// must not land on the progress line (see out).
		l.owner = p
	}
	return l
}

// out is the writer for results. When a progress line is drawn and stdout
// is a terminal too, each write first erases the progress line and then
// draws it again below what was written, so rows are never mixed into it.
func (p *Printer) out() io.Writer {
	if !p.opts.StdoutTTY || !p.opts.StderrTTY {
		return p.stdout
	}
	return progressSafe{p}
}

type progressSafe struct{ p *Printer }

func (w progressSafe) Write(b []byte) (int, error) {
	w.p.progMu.Lock()
	l := w.p.prog
	w.p.progMu.Unlock()
	if l == nil {
		return w.p.stdout.Write(b)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		return w.p.stdout.Write(b)
	}
	fmt.Fprint(l.w, "\r\033[K")
	n, err := w.p.stdout.Write(b)
	if len(b) > 0 && b[len(b)-1] == '\n' {
		l.draw()
	}
	return n, err
}

func (p *Printer) setProgress(l *lineProgress) {
	if p == nil {
		return
	}
	p.progMu.Lock()
	p.prog = l
	p.progMu.Unlock()
}

type nopProgress struct{}

func (nopProgress) Start(int, string) {}
func (nopProgress) Inc(int)           {}
func (nopProgress) SetLabel(string)   {}
func (nopProgress) Done()             {}

// lineProgress redraws one stderr line: "label  3/10".
type lineProgress struct {
	mu          sync.Mutex
	w           io.Writer
	width       int // terminal columns; the line is cut to fit so it never wraps
	total, done int
	label       string
	active      bool
	owner       *Printer // set when stdout is a terminal too
}

func (l *lineProgress) Start(total int, label string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total, l.done, l.label, l.active = total, 0, label, true
	l.owner.setProgress(l)
	l.draw()
}

func (l *lineProgress) Inc(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done += n
	l.draw()
}

func (l *lineProgress) SetLabel(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.label = s
	l.draw()
}

func (l *lineProgress) Done() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active {
		fmt.Fprint(l.w, "\r\033[K")
		l.active = false
		l.owner.setProgress(nil)
	}
}

func (l *lineProgress) draw() {
	if !l.active {
		return
	}
	count := fmt.Sprintf("  %d", l.done)
	if l.total > 0 {
		count = fmt.Sprintf("  %d/%d", l.done, l.total)
	}
	label := fitLine(l.label, l.width-1-runewidth.StringWidth(count))
	fmt.Fprintf(l.w, "\r\033[K%s%s", label, count)
}

// Spinner shows label with a spinner on stderr until stop is called. It
// draws nothing when stderr is not a TTY or with --quiet.
func (p *Printer) Spinner(label string) (stop func()) {
	if !p.opts.StderrTTY || p.opts.Quiet {
		return func() {}
	}
	label = fitLine(label, p.stderrWidth()-3) // the frame and a space
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			p.errMu.Lock()
			p.writeErrLocked(fmt.Sprintf("\r\033[K%s %s", frames[i%len(frames)], label))
			p.errMu.Unlock()
			select {
			case <-done:
				p.errMu.Lock()
				p.writeErrLocked("\r\033[K")
				p.errMu.Unlock()
				return
			case <-t.C:
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
		})
	}
}

// fitLine cuts s to at most w columns, ending it with "…" when cut, so a
// line redrawn with a carriage return never wraps onto a second row.
func fitLine(s string, w int) string {
	if w < 1 {
		w = 1
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	return runewidth.Truncate(s, w, "…")
}
