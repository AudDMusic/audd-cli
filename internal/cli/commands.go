package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newCommandsCmd(a))
	})
}

func newCommandsCmd(a *app.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "commands",
		Short: "List every command; --json for a machine-readable reference",
		Long: `List every audd command. With --json (or when piped), print a reference for
scripts and agents: each command's usage, flags with types and defaults,
examples, output shape, and exit codes, plus the global flags and the
meaning of every exit code.`,
		Example: `  audd commands
  audd commands --json
  audd commands --json | jq '.commands[] | select(.path == "audd recognize")'`,
		GroupID:     GroupAgents,
		Annotations: map[string]string{annotationNoConfig: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON {
				opts := a.Out.Options()
				opts.Format = output.FormatJSON
				a.Out = output.NewPrinter(a.Out.Stdout(), a.Out.Stderr(), opts)
			}
			ref := agent.BuildReference(cmd.Root(), app.Version)
			return a.Out.Result(ref, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				for _, c := range ref.Commands {
					if !c.Runnable {
						continue
					}
					fmt.Fprintf(tw, "%s\t%s\n", strings.TrimPrefix(c.Path, "audd "), c.Summary)
				}
				tw.Flush()
				if !a.Out.Quiet() {
					fmt.Fprintln(w, "\nDetails: audd <command> --help, or audd commands --json")
				}
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the full reference as JSON")
	return cmd
}
