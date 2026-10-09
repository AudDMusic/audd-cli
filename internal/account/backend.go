// Package account reads and manages the AudD account (profile, plan, usage,
// billing links, API token). The CLI talks to it only through Backend, so
// the transport (today the AudD MCP server) can be swapped in one file.
package account

import "context"

// Backend is every account operation the CLI uses.
type Backend interface {
	Profile(ctx context.Context) (*Profile, error)
	Status(ctx context.Context) (*Status, error)
	Usage(ctx context.Context, days int) (*Usage, error)
	APIToken(ctx context.Context) (string, error)
	RotateAPIToken(ctx context.Context) (string, error)
	Plans(ctx context.Context) ([]Plan, error)
	BillingHistory(ctx context.Context) ([]Payment, error)
	AmountOwed(ctx context.Context) (*Owed, error)
	Subscribe(ctx context.Context, plan string) (*PaymentLink, error)
	Renew(ctx context.Context) (*PaymentLink, error)
	BuyBonus(ctx context.Context, requests int) (*PaymentLink, error)
	Docs(ctx context.Context, topic string) (string, error)
}

// Each result keeps the typed fields the CLI renders plus Raw, the tool's
// full structured result; JSON output emits Raw in full. Fields the service
// leaves out (or sends with an unexpected type) are zero values.

// Profile is get_profile.
type Profile struct {
	UserID        int
	Email         string
	SignInMethods []SignInMethod
	Raw           map[string]any
}

// SignInMethod is one way the user signs in, such as a GitHub account.
type SignInMethod struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// Status is get_account_status.
type Status struct {
	Status        string
	Plan          *Plan // nil without a plan
	PaidTill      string
	BonusRequests int
	AutoRenew     bool
	Raw           map[string]any
}

// Usage is get_usage_stats.
type Usage struct {
	CycleStart, CycleEnd                string
	UsedThisCycle, Allowance, Remaining int
	// RemainingKnown is false when the service reported neither the
	// remaining requests nor an allowance to derive them from.
	RemainingKnown bool
	Days           []DayUsage
	Raw            map[string]any
}

type DayUsage struct {
	Date     string
	Requests int
}

// Plan is a plan from list_plans, or the current plan in get_account_status.
type Plan struct {
	// Key identifies the plan for subscribe_to_plan (the "plan" field of
	// list_plans, such as startup_plan). get_account_status has a numeric
	// ID instead.
	Key                    string
	ID                     int
	Name                   string
	MonthlyPriceCents      int
	IncludedRequests       int
	ExtraPriceCentsPer1000 int
	Raw                    map[string]any
}

// Payment is one row of get_billing_history.
type Payment struct {
	ID                   int
	Kind                 string
	AmountCents          int
	Paid                 bool
	CreatedAt, PaidAt    string
	BillingMonth         string
	ExtraRequestsBilled  int
	BonusRequestsGranted int
	Raw                  map[string]any
}

// Owed is get_amount_owed.
type Owed struct {
	OwedRequests, OwedAmountCents int
	UsedThisCycle, Allowance      int
	Note                          string
	Raw                           map[string]any
}

// PaymentLink is the result of subscribe_to_plan, create_renewal_payment,
// and buy_bonus_requests.
type PaymentLink struct {
	URL                   string
	AmountCents           int
	Requests              int // buy_bonus_requests only
	IncludesExtrasCents   int // create_renewal_payment only
	HumanApprovalRequired bool
	Raw                   map[string]any
}
