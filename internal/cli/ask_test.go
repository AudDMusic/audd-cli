package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func runAsk(args []string, ask func(string) bool) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, IO{In: strings.NewReader(""), Out: &out, Err: &errb, Ask: ask, NoUpdateNotice: true})
	return code, out.String(), errb.String()
}

// A run with an Ask hook asks through it instead of failing for want of a
// terminal, and an answer of no cancels like on a terminal.
func TestRunAskHook(t *testing.T) {
	srv := setupStreams(t)
	if r := runStreams(t, "streams", "add", "https://radio.example/a", "--id", "5"); r.Code != 0 {
		t.Fatalf("add: %d %s", r.Code, r.Stderr)
	}
	var asked []string
	code, _, stderr := runAsk([]string{"streams", "remove", "5"}, func(q string) bool { asked = append(asked, q); return false })
	if code != 6 || !strings.Contains(stderr, `"code":"declined"`) || len(srv.Streams()) != 1 {
		t.Fatalf("declined: %d %s", code, stderr)
	}
	code, stdout, stderr := runAsk([]string{"streams", "remove", "5"}, func(q string) bool { asked = append(asked, q); return true })
	if code != 0 || !strings.Contains(stdout, `"removed":true`) || len(srv.Streams()) != 0 {
		t.Fatalf("confirmed: %d %s %s", code, stdout, stderr)
	}
	if len(asked) != 2 || asked[0] != "Stop monitoring stream 5?" {
		t.Fatalf("asked %q", asked)
	}
}
