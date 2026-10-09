package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cache"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newCacheCmd(a))
	})
}

func newCacheCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cache",
		Short:   "Manage cached recognition results",
		GroupID: GroupLocal,
		Long: `audd keeps recognition results (never audio) so the same file or URL is not
paid for twice. Files are matched by their contents, URLs for 24 hours.`,
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "clear",
		Short:   "Delete every cached result",
		Args:    cobra.NoArgs,
		Example: "  audd cache clear",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cache.Open()
			if err != nil {
				return output.AsError(err)
			}
			defer c.Close()
			n, err := c.Count()
			if err != nil {
				return output.AsError(err)
			}
			if err := c.Clear(); err != nil {
				return output.AsError(err)
			}
			return a.Out.Result(struct {
				Cleared int `json:"cleared"`
			}{n}, func(w io.Writer) {
				switch n {
				case 0:
					fmt.Fprintln(w, "The cache was already empty.")
				case 1:
					fmt.Fprintln(w, "Cleared 1 cached result.")
				default:
					fmt.Fprintf(w, "Cleared %d cached results.\n", n)
				}
			})
		},
	})
	return cmd
}
