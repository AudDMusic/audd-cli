package output

import (
	"bufio"
	"context"
	"io"
	"sync"
)

// LineReader reads lines from an input such as stdin and lets a caller stop
// waiting (through its context) without losing input: a line that arrives
// after the caller gave up is handed to the next ReadLine call instead.
type LineReader struct {
	r        *bufio.Reader
	mu       sync.Mutex
	inflight chan lineResult // a read in progress, or nil
}

type lineResult struct {
	line string
	err  error
}

// NewLineReader reads lines from r.
func NewLineReader(r io.Reader) *LineReader {
	return &LineReader{r: bufio.NewReader(r)}
}

// ReadLine returns the next line, including its newline when present. It
// returns ctx.Err() when ctx ends first; the line still being read is then
// kept for the next call.
func (l *LineReader) ReadLine(ctx context.Context) (string, error) {
	l.mu.Lock()
	ch := l.inflight
	if ch == nil {
		ch = make(chan lineResult, 1)
		l.inflight = ch
		go func() {
			s, err := l.r.ReadString('\n')
			ch <- lineResult{s, err}
		}()
	}
	l.mu.Unlock()
	select {
	case res := <-ch:
		l.mu.Lock()
		if l.inflight == ch {
			l.inflight = nil
		}
		l.mu.Unlock()
		return res.line, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
