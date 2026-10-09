package cli

import (
	"cmp"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newAccountCmd(a))
	})
}

func newAccountCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "account",
		Short: "Show your AudD account, plan, and subscription",
		Long: `Show the signed-in AudD account: email, sign-in methods, plan, subscription
status, paid-until date, auto-renew, and bonus requests.

JSON output carries the account service's full results under "account" and
"status". Needs audd login.`,
		Example: "  audd account\n  audd account --format json",
		GroupID: GroupAccount,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := a.Account()
			if err != nil {
				return err
			}
			p, err := b.Profile(cmd.Context())
			if err != nil {
				return err
			}
			s, err := b.Status(cmd.Context())
			if err != nil {
				return err
			}
			if p.Email != "" && p.Email != a.Profile.Account {
				a.Profile.Account = p.Email
				_ = a.Cfg.Save()
			}
			view := map[string]any{"account": p.Raw, "status": s.Raw}
			return a.Out.Result(view, func(w io.Writer) {
				st := a.Out.Styles()
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				row := func(k, v string) {
					if v != "" {
						fmt.Fprintf(tw, "%s\t%s\n", st.Key.Render(k), v)
					}
				}
				row("Account", p.Email)
				signInRows(row, p.SignInMethods)
				plan := "none"
				if s.Plan != nil {
					plan = planSummary(*s.Plan)
				}
				if s.Status != "" {
					plan += " (" + s.Status + ")"
				}
				row("Plan", plan)
				row("Paid until", s.PaidTill)
				if _, ok := s.Raw["auto_renew"]; ok || s.AutoRenew {
					row("Auto-renew", onOff(s.AutoRenew))
				}
				row("Bonus requests", formatInt(s.BonusRequests))
				tw.Flush()
			})
		},
	}
}

// signInRows writes one "provider: label" row per sign-in method.
func signInRows(row func(k, v string), methods []account.SignInMethod) {
	key := "Sign-in"
	for _, m := range methods {
		v := m.Provider
		if m.Label != "" {
			if v != "" {
				v += ": "
			}
			v += m.Label
		}
		if v == "" {
			continue
		}
		row(key, v)
		key = ""
	}
}

// planSummary is "Startup, $450.00/month, 100,000 requests included".
func planSummary(p account.Plan) string {
	parts := []string{}
	if name := cmp.Or(p.Name, p.Key); name != "" {
		parts = append(parts, name)
	}
	if p.MonthlyPriceCents > 0 {
		parts = append(parts, formatMoney(p.MonthlyPriceCents, "")+"/month")
	}
	if p.IncludedRequests > 0 {
		parts = append(parts, formatInt(p.IncludedRequests)+" requests included")
	}
	if len(parts) == 0 {
		return "unnamed plan"
	}
	return strings.Join(parts, ", ")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// formatInt writes n with thousands separators: 12345 → "12,345".
func formatInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// formatMoney renders minor units: 2500 USD → "$25.00".
func formatMoney(cents int, currency string) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	amount := fmt.Sprintf("%s.%02d", formatInt(cents/100), cents%100)
	var s string
	switch strings.ToUpper(currency) {
	case "", "USD":
		s = "$" + amount
	case "EUR":
		s = "€" + amount
	case "GBP":
		s = "£" + amount
	default:
		s = amount + " " + strings.ToUpper(currency)
	}
	if neg {
		return "-" + s
	}
	return s
}
