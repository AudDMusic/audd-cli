package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/tui"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newBrowseCmd(a))
	})
}

func newBrowseCmd(a *app.App) *cobra.Command {
	var tab string
	cmd := &cobra.Command{
		Use:     "browse",
		Short:   "Explore recognized songs, jobs, streams, and usage",
		GroupID: GroupLocal,
		Long: `Open the interactive explorer. Tabs:

  recent   everything recognized with audd: search, details, links, ISRC and UPC
  jobs     batch jobs: progress, failures, results, enterprise tracklists; resume
  streams  your streams live: what is playing, health, recent plays; add or remove
  usage    requests this billing cycle and per day

Keys: tab or 1-4 switch tabs, arrows move, Enter opens, / filters, o opens the
song link, c copies it, e exports the view to CSV or JSON, ? shows all keys,
q quits.

Without a terminal, browse prints the tab's data as JSON, or with --format csv
as flat rows: a column per field and a row per song.`,
		Example: `  audd browse
  audd browse --tab streams
  audd browse --tab usage --format json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.RunExplorer(cmd.Context(), a, tab)
		},
	}
	cmd.Flags().StringVar(&tab, "tab", "recent", "tab to open: "+strings.Join(tui.Tabs(), ", "))
	_ = cmd.RegisterFlagCompletionFunc("tab", cobra.FixedCompletions(tui.Tabs(), cobra.ShellCompDirectiveNoFileComp))
	return cmd
}
