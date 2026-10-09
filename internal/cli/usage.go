package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newUsageCmd(a))
	})
}

func newUsageCmd(a *app.App) *cobra.Command {
	var days, minRemaining int
	var check bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show requests used and remaining this billing cycle",
		Long: `Show requests used this billing cycle, the allowance, what remains, and
requests per day.

With --check --min-remaining N, exit with code 8 when fewer than N requests
remain, for cron jobs and CI. Needs audd login.`,
		Example: `  audd usage
  audd usage --days 7
  audd usage --check --min-remaining 1000 --quiet`,
		GroupID: GroupAccount,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if days < 1 || days > 366 {
				return output.Errf(output.ExitUsage, "invalid_argument", "", "--days must be between 1 and 366")
			}
			threshold := check
			if check != cmd.Flags().Changed("min-remaining") {
				return output.Errf(output.ExitUsage, "invalid_argument", "audd usage --check --min-remaining 1000",
					"--check and --min-remaining N go together")
			}
			if threshold && minRemaining < 0 {
				return output.Errf(output.ExitUsage, "invalid_argument", "", "--min-remaining must be 0 or more")
			}
			b, err := a.Account()
			if err != nil {
				return err
			}
			u, err := b.Usage(cmd.Context(), days)
			if err != nil {
				return err
			}
			if err := a.Out.Result(u.Raw, func(w io.Writer) { renderUsage(w, a, u) }); err != nil {
				return err
			}
			if threshold && !u.RemainingKnown {
				return &output.Error{
					Code:    "unexpected_response",
					Message: "the AudD account service did not report how many requests remain, so the threshold cannot be checked",
					Hint:    "update audd (audd update); report persistent problems at https://github.com/AudDMusic/audd-cli/issues",
					Exit:    output.ExitUnexpected,
				}
			}
			if threshold && u.Remaining < minRemaining {
				return &output.Error{
					Code:    "usage_threshold",
					Message: fmt.Sprintf("%s requests remaining, below the threshold of %s", formatInt(u.Remaining), formatInt(minRemaining)),
					Hint:    "audd billing plans, or audd billing buy 10000",
					Exit:    output.ExitThreshold,
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "days of per-day history to show")
	cmd.Flags().BoolVar(&check, "check", false, "exit with code 8 when fewer than --min-remaining requests remain (needs --min-remaining)")
	cmd.Flags().IntVar(&minRemaining, "min-remaining", 0, "with --check, the fewest remaining requests that pass")
	return cmd
}

func renderUsage(w io.Writer, a *app.App, u *account.Usage) {
	st := a.Out.Styles()
	if a.Out.Quiet() {
		fmt.Fprintf(w, "%s remaining\n", remainingText(u))
		return
	}
	title := "This billing cycle"
	if u.CycleStart != "" && u.CycleEnd != "" {
		title += " (" + u.CycleStart + " to " + u.CycleEnd + ")"
	}
	fmt.Fprintln(w, st.Title.Render(title))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	used := formatInt(u.UsedThisCycle)
	if u.Allowance > 0 {
		used += " of " + formatInt(u.Allowance)
	}
	fmt.Fprintf(tw, "%s\t%s requests\n", st.Key.Render("Used"), used)
	fmt.Fprintf(tw, "%s\t%s\n", st.Key.Render("Remaining"), remainingText(u))
	tw.Flush()
	if u.Allowance > 0 {
		fmt.Fprintf(w, "%s\n", meter(u.UsedThisCycle, u.Allowance, 40, st.Accent.Render, st.Dim.Render))
	}
	if len(u.Days) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.Title.Render("Requests per day"))
	max := 0
	for _, d := range u.Days {
		if d.Requests > max {
			max = d.Requests
		}
	}
	width := 0
	for _, d := range u.Days {
		if n := len(formatInt(d.Requests)); n > width {
			width = n
		}
	}
	for _, d := range u.Days {
		line := fmt.Sprintf("%s  %*s", d.Date, width, formatInt(d.Requests))
		if b := bar(d.Requests, max, 30); b != "" {
			line += " " + st.Accent.Render(b)
		}
		fmt.Fprintln(w, line)
	}
}

func remainingText(u *account.Usage) string {
	if !u.RemainingKnown {
		return "unknown"
	}
	return formatInt(u.Remaining)
}

// bar draws n/max as up to width eighth-block characters.
func bar(n, max, width int) string {
	if max <= 0 || n <= 0 {
		return ""
	}
	eighths := n * width * 8 / max
	if eighths == 0 {
		eighths = 1
	}
	s := strings.Repeat("█", eighths/8)
	if r := eighths % 8; r > 0 {
		s += string([]rune("▏▎▍▌▋▊▉")[r-1])
	}
	return s
}

// meter draws a usage meter: filled part, empty part, and the percentage.
func meter(used, total, width int, fill, empty func(...string) string) string {
	if total <= 0 {
		return ""
	}
	n := used * width / total
	if n > width {
		n = width
	}
	if used > 0 && n == 0 {
		n = 1
	}
	pct := float64(used) * 100 / float64(total)
	return fill(strings.Repeat("█", n)) + empty(strings.Repeat("░", width-n)) + fmt.Sprintf(" %.0f%%", pct)
}
