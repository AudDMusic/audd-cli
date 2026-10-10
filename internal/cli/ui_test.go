package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// audd with no arguments and no terminal prints the root help, exactly as
// audd --help does, so scripts, pipes, and agents see no change.
func TestNoArgsWithoutTerminalPrintsHelp(t *testing.T) {
	testutil.Isolate(t)
	help := testutil.Exec(t, Main, "", "--help")
	if help.Code != 0 || !strings.Contains(help.Stdout, "audd ui") {
		t.Fatalf("--help: %d %q", help.Code, help.Stdout)
	}
	bare := testutil.Exec(t, Main, "")
	if bare.Code != 0 || bare.Stdout != help.Stdout || bare.Stderr != help.Stderr {
		t.Fatalf("audd with no arguments differs from audd --help:\n%s", bare.Stdout)
	}
	// A terminal, but CI or AUDD_NO_TUI is set: still the help.
	for _, env := range []string{"CI", "AUDD_NO_TUI"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "1")
			var out, errb bytes.Buffer
			code := Run(context.Background(), nil, IO{In: strings.NewReader(""), Out: &out, Err: &errb,
				StdinTTY: true, StdoutTTY: true, StderrTTY: true, StdoutWidth: 200, StderrWidth: 200, NoUpdateNotice: true})
			if code != 0 || !strings.Contains(out.String(), "Usage:\n  audd [command]") {
				t.Fatalf("%s set: %d %q", env, code, out.String())
			}
		})
	}
}

func TestOpensHome(t *testing.T) {
	testutil.Isolate(t)
	t.Setenv("CI", "")
	root := newRoot(app.New(), IO{}, &runState{})
	tty := IO{StdinTTY: true, StdoutTTY: true}
	cases := []struct {
		args []string
		io   IO
		want bool
	}{
		{nil, tty, true},
		{[]string{"--profile", "work"}, tty, true},
		{[]string{"--token", "abc", "--debug"}, tty, true},
		{nil, IO{StdoutTTY: true}, false},
		{nil, IO{StdinTTY: true}, false},
		{[]string{"--help"}, tty, false},
		{[]string{"--version"}, tty, false},
		{[]string{"recognize"}, tty, false},
		{[]string{"nonsense"}, tty, false},
		{[]string{"help"}, tty, false},
	}
	for _, c := range cases {
		if got := opensHome(root, c.args, c.io); got != c.want {
			t.Errorf("opensHome(%q, tty in %v out %v) = %v", c.args, c.io.StdinTTY, c.io.StdoutTTY, got)
		}
	}
	t.Setenv("AUDD_NO_TUI", "1")
	if opensHome(root, nil, tty) {
		t.Error("AUDD_NO_TUI turns it off")
	}
}

func TestUIWithoutTerminal(t *testing.T) {
	testutil.Isolate(t)
	r := testutil.Exec(t, Main, "", "ui")
	if r.Code != 2 || !strings.Contains(r.Stderr, "needs a terminal") {
		t.Fatalf("ui without a terminal: %d %s", r.Code, r.Stderr)
	}
	r = testutil.Exec(t, Main, "", "ui", "--section", "nowhere")
	if r.Code != 2 || !strings.Contains(r.Stderr, "unknown section") {
		t.Fatalf("unknown section: %d %s", r.Code, r.Stderr)
	}
}
