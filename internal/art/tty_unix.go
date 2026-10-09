//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package art

import (
	"errors"
	"os"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// CellSizeOf returns the cell size of the terminal on f from the pixel
// fields of TIOCGWINSZ. Terminals that leave them at zero report false.
func CellSizeOf(f *os.File) (CellSize, bool) {
	if f == nil {
		return CellSize{}, false
	}
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return CellSize{}, false
	}
	c := CellSize{W: int(ws.Xpixel) / int(ws.Col), H: int(ws.Ypixel) / int(ws.Row)}
	return c, c.Known()
}

// ttyTerminal reads replies from in and writes requests to out.
type ttyTerminal struct{ in, out *os.File }

func (t ttyTerminal) Write(p []byte) (int, error) { return t.out.Write(p) }

func (t ttyTerminal) ReadTimeout(p []byte, d time.Duration) (int, error) {
	ms := int(d / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	fds := []unix.PollFd{{Fd: int32(t.in.Fd()), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, ms)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 || fds[0].Revents&unix.POLLIN == 0 {
			return 0, nil
		}
		return unix.Read(int(t.in.Fd()), p)
	}
}

// OpenTTY returns a Terminal on in and out with in in raw mode, so
// replies are not echoed or held back until Enter, and a function that
// restores in. It returns nil when either is not a terminal.
func OpenTTY(in, out *os.File) (Terminal, func()) {
	if in == nil || out == nil || !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return nil, func() {}
	}
	state, err := term.MakeRaw(in.Fd())
	if err != nil {
		return nil, func() {}
	}
	return ttyTerminal{in: in, out: out}, func() { _ = term.Restore(in.Fd(), state) }
}
