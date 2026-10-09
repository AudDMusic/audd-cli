package output

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestLineReaderKeepsLineAfterCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	l := NewLineReader(pr)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.ReadLine(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
	go func() { _, _ = io.WriteString(pw, "yes\nsecond\n") }()
	for _, want := range []string{"yes\n", "second\n"} {
		got, err := l.ReadLine(context.Background())
		if err != nil || got != want {
			t.Fatalf("got %q %v, want %q", got, err, want)
		}
	}
	pw.Close()
	if _, err := l.ReadLine(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}
