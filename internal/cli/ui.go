package cli

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/tui"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newUICmd(a))
	})
	tui.HomeRun = func(ctx context.Context, args []string, io tui.RunIO) int {
		return Run(ctx, args, IO{In: io.Stdin, Out: io.Stdout, Err: io.Stderr, Ask: io.Ask,
			NoUpdateNotice: true, StdoutWidth: io.Width, StderrWidth: io.Width})
	}
	tui.HomeCommands = func() *cobra.Command {
		return newRoot(app.New(), IO{}, &runState{})
	}
}

func newUICmd(a *app.App) *cobra.Command {
	var section string
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open interactive mode (also: audd with no arguments)",
		Long: `Open interactive mode: a full-screen interface to everything audd does.

Running audd with no arguments on a terminal opens it too. Sections:
` + strings.Join(tui.HomeSections, ", ") + `. Press ctrl+k for a palette of
every command; the command each action runs is shown at the bottom (y copies
it). Esc or left goes back; the mouse clicks and scrolls (hold Shift to
select text; AUDD_NO_MOUSE=1 turns it off). Set AUDD_NO_TUI=1 to have audd
with no arguments print help instead.`,
		Example: "  audd ui\n  audd ui --section streams",
		GroupID: GroupOther,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tui.HomeApp = func(flags app.GlobalFlags) (*app.App, error) { return sessionFor(a, flags) }
			return app.RunHome(cmd.Context(), a, section)
		},
	}
	cmd.Flags().StringVar(&section, "section", "", "open this section: "+strings.Join(tui.HomeSections, ", "))
	_ = cmd.RegisterFlagCompletionFunc("section", cobra.FixedCompletions(tui.HomeSections, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// sessionFor builds a set-up App like a's (same terminal) for other global
// flags: interactive mode uses it after a sign-in or a profile switch.
func sessionFor(a *app.App, flags app.GlobalFlags) (*app.App, error) {
	opts := a.Out.Options()
	stdio := IO{In: a.In, Out: a.Out.Stdout(), Err: a.Out.Stderr(),
		StdinTTY: opts.StdinTTY, StdoutTTY: opts.StdoutTTY, StderrTTY: opts.StderrTTY,
		StdoutWidth: opts.StdoutWidth, StderrWidth: opts.StderrWidth}
	n := app.New()
	n.In = a.In
	n.Now = a.Now
	n.Secrets = a.Secrets
	root := newRoot(n, stdio, &runState{})
	n.Flags = flags
	n.Flags.MaxRequests, n.Flags.MaxRequestsFromConfig = 0, false
	if flags.MaxRequests > 0 && !flags.MaxRequestsFromConfig {
		n.Flags.MaxRequests = flags.MaxRequests
		_ = root.PersistentFlags().Set("max-requests", strconv.Itoa(flags.MaxRequests))
	}
	if err := setup(n, root, stdio); err != nil {
		return nil, err
	}
	return n, nil
}

// opensHome reports whether audd with these arguments opens interactive
// mode: no command, no help or version flag, a terminal on stdin and
// stdout, and neither CI nor AUDD_NO_TUI set.
func opensHome(root *cobra.Command, args []string, stdio IO) bool {
	if !stdio.StdinTTY || !stdio.StdoutTTY || os.Getenv("CI") != "" || os.Getenv("AUDD_NO_TUI") != "" {
		return false
	}
	if slices.ContainsFunc(args, func(s string) bool {
		return s == "-h" || s == "--help" || s == "--version" || s == "help" || s == "--"
	}) {
		return false
	}
	c, _, err := root.Find(args)
	return err == nil && c == root && firstPositional(root, args) == ""
}
