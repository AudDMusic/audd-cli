package output

import (
	"fmt"
	"os"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// defaultWidth is the terminal width assumed when it cannot be read.
const defaultWidth = 80

// TransientBlock is text on stderr that can be erased later, such as the
// instructions shown while a sign-in waits. While a block is open, every
// line the Printer writes to stderr (notes, warnings, the spinner) is part
// of it. Clear erases it by moving the cursor up over the screen rows it
// took (counted with the terminal width, so wrapped lines count) and
// clearing to the end of the screen; it does not rely on saving the cursor
// position, which breaks once the terminal scrolls.
//
// When stderr is not a terminal or the output is not for people, the block
// only writes through and Clear does nothing.
type TransientBlock struct {
	p     *Printer
	width int

	// Guarded by p.errMu.
	rows int // rows above the cursor's row
	col  int // cursor column
	esc  int // escape sequence state: escNone, escStart, escCSI, escOSC
}

const (
	escNone = iota
	escStart
	escCSI
	escOSC
)

// TransientBlock opens a block on stderr (see TransientBlock). When one is
// already open it is returned, so a nested waiting block is part of it.
func (p *Printer) TransientBlock() *TransientBlock {
	if !p.opts.StderrTTY || !p.IsHuman() {
		return &TransientBlock{p: p}
	}
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if p.transient == nil {
		p.transient = &TransientBlock{p: p, width: p.stderrWidth()}
	}
	return p.transient
}

// Transient returns the open block, or nil.
func (p *Printer) Transient() *TransientBlock {
	if p == nil {
		return nil
	}
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.transient
}

// stderrWidth is the terminal width of stderr: StderrWidth when set, else
// read from the terminal, else defaultWidth.
func (p *Printer) stderrWidth() int {
	if p.opts.StderrWidth > 0 {
		return p.opts.StderrWidth
	}
	if f, ok := p.stderr.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return defaultWidth
}

// writeErrLocked writes s to stderr and counts it in the open block. The
// caller holds p.errMu.
func (p *Printer) writeErrLocked(s string) {
	if p.transient != nil {
		p.transient.count(s)
	}
	_, _ = p.stderr.Write([]byte(s))
}

// Write writes to stderr as part of the block.
func (b *TransientBlock) Write(data []byte) (int, error) {
	b.p.errMu.Lock()
	defer b.p.errMu.Unlock()
	b.p.writeErrLocked(string(data))
	return len(data), nil
}

// CountInput counts a line the terminal echoed while the block was open
// (typed or pasted input; line without its newline), so Clear erases it too.
func (b *TransientBlock) CountInput(line string) {
	b.p.errMu.Lock()
	defer b.p.errMu.Unlock()
	if b.p.transient != b {
		return
	}
	b.count(line + "\n")
}

// Clear erases the block and closes it. It reports whether it erased
// anything: false for a pass-through block or one already closed.
func (b *TransientBlock) Clear() bool {
	b.p.errMu.Lock()
	defer b.p.errMu.Unlock()
	if b.p.transient != b {
		return false
	}
	b.p.transient = nil
	seq := "\r\033[J"
	if b.rows > 0 {
		seq = fmt.Sprintf("\033[%dA", b.rows) + seq
	}
	_, _ = b.p.stderr.Write([]byte(seq))
	return true
}

// Keep closes the block and leaves it on screen.
func (b *TransientBlock) Keep() {
	b.p.errMu.Lock()
	defer b.p.errMu.Unlock()
	if b.p.transient == b {
		b.p.transient = nil
	}
}

// Rows is the number of rows the block takes above the cursor's row.
func (b *TransientBlock) Rows() int {
	b.p.errMu.Lock()
	defer b.p.errMu.Unlock()
	return b.rows
}

// count advances the row and column the way a terminal does: a line feed
// starts a new row, a carriage return goes back to the start of the row,
// text that does not fit wraps onto the next row, and escape sequences
// take no space.
func (b *TransientBlock) count(s string) {
	width := b.width
	if width <= 0 {
		width = defaultWidth
	}
	for _, r := range s {
		switch b.esc {
		case escStart:
			switch r {
			case '[':
				b.esc = escCSI
			case ']':
				b.esc = escOSC
			default:
				b.esc = escNone
			}
			continue
		case escCSI:
			if r >= 0x40 && r <= 0x7e {
				b.esc = escNone
			}
			continue
		case escOSC:
			switch r {
			case 0x07:
				b.esc = escNone
			case 0x1b:
				b.esc = escStart // ESC \ ends it
			}
			continue
		}
		switch {
		case r == 0x1b:
			b.esc = escStart
		case r == '\n':
			b.rows++
			b.col = 0
		case r == '\r':
			b.col = 0
		case r == '\t':
			b.col = min((b.col/8+1)*8, width)
		case r == '\b':
			if b.col > 0 {
				b.col--
			}
		case r < 0x20 || r == 0x7f:
		default:
			w := runewidth.RuneWidth(r)
			if w == 0 {
				continue
			}
			if b.col+w > width {
				b.rows++
				b.col = 0
			}
			b.col += w
		}
	}
}
