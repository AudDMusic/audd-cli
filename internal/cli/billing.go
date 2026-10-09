package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/oauth"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newBillingCmd(a))
	})
}

func newBillingCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "billing",
		Short: "Plans, payments, and payment links",
		Long: `See plans, payment history, and what you owe, and get payment links.

subscribe, renew, and buy never charge anything: they print a Stripe payment
link that you open and approve in the browser (--open opens it for you).
If you unticked the billing:pay permission when signing in, they ask you
to approve it.
subscribe takes a plan from the PLAN column of audd billing plans, such as
startup_plan. Needs audd login.`,
		Example: `  audd billing plans
  audd billing history
  audd billing owed
  audd billing subscribe startup_plan --open
  audd billing buy 10000`,
		GroupID: GroupAccount,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "plans",
			Short: "List the available plans",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				b, err := a.Account()
				if err != nil {
					return err
				}
				plans, err := b.Plans(cmd.Context())
				if err != nil {
					return err
				}
				raws := make([]map[string]any, len(plans))
				for i, p := range plans {
					raws[i] = p.Raw
				}
				return a.Out.Result(map[string]any{"plans": raws}, func(w io.Writer) {
					if len(plans) == 0 {
						fmt.Fprintln(w, "No plans available.")
						return
					}
					tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
					fmt.Fprintln(tw, "PLAN\tNAME\tPRICE\tINCLUDED REQUESTS\tOVER THE ALLOWANCE")
					for _, p := range plans {
						fmt.Fprintf(tw, "%s\t%s\t%s/month\t%s\t%s per 1,000\n", p.Key, p.Name,
							formatMoney(p.MonthlyPriceCents, ""), formatInt(p.IncludedRequests), formatMoney(p.ExtraPriceCentsPer1000, ""))
					}
					tw.Flush()
					a.Out.Info("Subscribe or switch with: audd billing subscribe <PLAN>")
				})
			},
		},
		&cobra.Command{
			Use:   "history",
			Short: "Show recent payments",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				b, err := a.Account()
				if err != nil {
					return err
				}
				pays, err := b.BillingHistory(cmd.Context())
				if err != nil {
					return err
				}
				raws := make([]map[string]any, len(pays))
				for i, p := range pays {
					raws[i] = p.Raw
				}
				return a.Out.Result(map[string]any{"payments": raws}, func(w io.Writer) {
					if len(pays) == 0 {
						fmt.Fprintln(w, "No payments yet.")
						return
					}
					tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
					fmt.Fprintln(tw, "DATE\tKIND\tAMOUNT\tSTATUS")
					for _, p := range pays {
						status := "unpaid"
						if p.Paid {
							status = "paid"
						}
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", dateOnly(p.CreatedAt), strings.ReplaceAll(p.Kind, "_", " "),
							formatMoney(p.AmountCents, ""), status)
					}
					tw.Flush()
				})
			},
		},
		&cobra.Command{
			Use:   "owed",
			Short: "Show over-allowance usage due at the next renewal",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				b, err := a.Account()
				if err != nil {
					return err
				}
				o, err := b.AmountOwed(cmd.Context())
				if err != nil {
					return err
				}
				return a.Out.Result(o.Raw, func(w io.Writer) {
					st := a.Out.Styles()
					tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
					fmt.Fprintf(tw, "%s\t%s\n", st.Key.Render("Requests over the allowance"), formatInt(o.OwedRequests))
					fmt.Fprintf(tw, "%s\t%s\n", st.Key.Render("Amount owed"), formatMoney(o.OwedAmountCents, ""))
					if _, ok := o.Raw["used_this_cycle"]; ok {
						used := formatInt(o.UsedThisCycle)
						if o.Allowance > 0 {
							used += " of " + formatInt(o.Allowance)
						}
						fmt.Fprintf(tw, "%s\t%s requests\n", st.Key.Render("Used this cycle"), used)
					}
					tw.Flush()
					if o.Note != "" {
						fmt.Fprintln(w, o.Note)
					}
				})
			},
		},
		paymentCmd(a, "subscribe <plan>", "Get a payment link to subscribe to or switch to a plan",
			"  audd billing subscribe startup_plan\n  audd billing subscribe pro_plan --open", cobra.ExactArgs(1),
			func(ctx context.Context, b account.Backend, args []string) (*account.PaymentLink, error) {
				return b.Subscribe(ctx, args[0])
			}),
		paymentCmd(a, "renew", "Get a payment link to renew the subscription, including any owed usage",
			"  audd billing renew --open", cobra.NoArgs,
			func(ctx context.Context, b account.Backend, args []string) (*account.PaymentLink, error) {
				return b.Renew(ctx)
			}),
		paymentCmd(a, "buy <requests>", "Get a payment link for extra requests (multiples of 1,000)",
			"  audd billing buy 10000", cobra.ExactArgs(1),
			func(ctx context.Context, b account.Backend, args []string) (*account.PaymentLink, error) {
				n, _ := strconv.Atoi(strings.ReplaceAll(args[0], ",", ""))
				return b.BuyBonus(ctx, n)
			}),
	)
	return cmd
}

type paymentFunc func(ctx context.Context, b account.Backend, args []string) (*account.PaymentLink, error)

func paymentCmd(a *app.App, use, short, example string, nargs cobra.PositionalArgs, call paymentFunc) *cobra.Command {
	var open, noConsent bool
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    short + ".\n\nThis never charges anything: it prints a Stripe link that you open and approve\nin the browser. --open opens it for you.",
		Example: example,
		Args:    nargs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.HasPrefix(use, "buy") {
				n, err := strconv.Atoi(strings.ReplaceAll(args[0], ",", ""))
				if err != nil || n <= 0 || n%1000 != 0 {
					return output.Errf(output.ExitUsage, "invalid_argument", "audd billing buy 10000",
						"the number of requests must be a positive multiple of 1,000, got %q", args[0])
				}
			}
			oc := newOAuth(a)
			if !oc.LoggedIn() {
				return notSignedIn(a)
			}
			if strings.HasPrefix(use, "subscribe") {
				key, err := planKey(cmd.Context(), a, args[0])
				if err != nil {
					return err
				}
				args = []string{key}
			}
			if noConsent {
				// Used by audd mcp: never start a browser sign-in; say what
				// the user has to run instead.
				t, err := oc.Token(cmd.Context())
				if err != nil {
					return err
				}
				if !slices.Contains(t.Scopes, "billing:pay") {
					return output.Errf(output.ExitAuth, "approval_required", strings.TrimSpace(cmd.CommandPath()+" "+strings.Join(args, " ")),
						"payment links need your approval of payment access once; run the command below in a terminal and approve it in the browser")
				}
			} else if _, err := oc.EnsureScopes(cmd.Context(), "billing:pay"); err != nil {
				return err
			}
			b, err := a.Account()
			if err != nil {
				return err
			}
			l, err := call(cmd.Context(), b, args)
			if err != nil {
				return err
			}
			view := map[string]any{}
			for k, v := range l.Raw {
				view[k] = v
			}
			view["url"] = l.URL
			view["charged"] = false
			err = a.Out.Result(view, func(w io.Writer) {
				if a.Out.Quiet() {
					fmt.Fprintln(w, l.URL)
					return
				}
				if l.AmountCents > 0 {
					amount := formatMoney(l.AmountCents, "")
					if l.Requests > 0 {
						amount += " for " + formatInt(l.Requests) + " requests"
					}
					if l.IncludesExtrasCents > 0 {
						amount += ", including " + formatMoney(l.IncludesExtrasCents, "") + " for requests over the allowance"
					}
					fmt.Fprintf(w, "Amount: %s\n", amount)
				}
				fmt.Fprintf(w, "Open this link to review and pay (nothing is charged until you confirm there):\n  %s\n", l.URL)
			})
			if err != nil {
				return err
			}
			if open {
				if err := oauth.OpenBrowser(l.URL); err != nil {
					a.Out.Warn("Could not open a browser; open the link above.")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&open, "open", false, "open the payment link in the browser")
	cmd.Flags().BoolVar(&noConsent, "no-consent-prompt", false, "fail instead of asking to approve payment access in the browser")
	_ = cmd.Flags().MarkHidden("no-consent-prompt")
	return cmd
}

// planKey checks a plan against list_plans and returns its key as the
// service spells it. Unknown plans fail before any payment step.
func planKey(ctx context.Context, a *app.App, arg string) (string, error) {
	b, err := a.Account()
	if err != nil {
		return "", err
	}
	plans, err := b.Plans(ctx)
	if err != nil {
		return "", err
	}
	want := strings.TrimSpace(arg)
	var keys []string
	for _, p := range plans {
		if p.Key == "" {
			continue
		}
		if strings.EqualFold(p.Key, want) {
			return p.Key, nil
		}
		keys = append(keys, p.Key)
	}
	if len(keys) == 0 {
		return "", output.Errf(output.ExitUsage, "invalid_argument", "audd billing plans", "no plans are available to subscribe to")
	}
	return "", output.Errf(output.ExitUsage, "invalid_argument", "audd billing subscribe "+keys[0],
		"unknown plan %q; choose one of: %s (see audd billing plans)", arg, strings.Join(keys, ", "))
}

// dateOnly trims "2026-10-08 08:26:34" to "2026-10-08".
func dateOnly(s string) string {
	if len(s) > 10 && (s[10] == ' ' || s[10] == 'T') {
		return s[:10]
	}
	return s
}
