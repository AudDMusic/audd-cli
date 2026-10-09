package account

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/mcpclient"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// NewMCP returns a Backend that calls the AudD MCP server's tools.
func NewMCP(c *mcpclient.Client) Backend {
	return &mcpBackend{c: c}
}

type mcpBackend struct {
	c *mcpclient.Client
}

// shapeError reports a tool result the CLI cannot use at all.
func shapeError(tool, problem string) *output.Error {
	return &output.Error{
		Code:    "unexpected_response",
		Message: fmt.Sprintf("the AudD account service returned an unexpected %s result: %s", tool, problem),
		Hint:    "update audd (audd update); report persistent problems at https://github.com/AudDMusic/audd-cli/issues",
		Exit:    output.ExitUnexpected,
	}
}

// tool returns the published description of a tool, or nil when this
// sign-in does not list it (calling it then reports the missing permission).
func (b *mcpBackend) tool(ctx context.Context, name string) (*mcpclient.Tool, error) {
	tools, err := b.c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i], nil
		}
	}
	return nil, nil
}

// checkArgs makes sure the tool still takes the arguments the CLI sends.
func checkArgs(name string, t *mcpclient.Tool, args map[string]any) error {
	if t == nil || t.InputSchema == nil {
		return nil
	}
	props, hasProps := t.InputSchema["properties"].(map[string]any)
	for k := range args {
		if _, ok := props[k]; !ok && hasProps {
			return shapeError(name, fmt.Sprintf("the tool no longer takes a %q argument", k))
		}
	}
	for _, k := range requiredOf(t.InputSchema) {
		if _, ok := args[k]; !ok {
			return shapeError(name, fmt.Sprintf("the tool now needs a %q argument", k))
		}
	}
	return nil
}

// call runs a tool and returns its structured result. A result without
// structured content is the one shape problem that fails loudly: every
// account tool publishes an output schema, so plain text means the service
// changed in a way this version cannot read.
func (b *mcpBackend) call(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	t, err := b.tool(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := checkArgs(name, t, args); err != nil {
		return nil, err
	}
	st, _, err := b.c.CallTool(ctx, name, args)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, shapeError(name, "it has no structured content")
	}
	return st, nil
}

// The fields read below follow the output schemas the AudD MCP server
// publishes (recorded in internal/testutil/testdata/mcp/tools_list.json).

func (b *mcpBackend) Profile(ctx context.Context) (*Profile, error) {
	m, err := b.call(ctx, "get_profile", nil)
	if err != nil {
		return nil, err
	}
	p := &Profile{Raw: m, UserID: intOr0(m, "user_id"), Email: str(m, "email")}
	for _, s := range objects(m, "sign_in_methods") {
		p.SignInMethods = append(p.SignInMethods, SignInMethod{Provider: str(s, "provider"), Label: str(s, "label")})
	}
	return p, nil
}

func readPlan(m map[string]any) Plan {
	return Plan{
		Key:                    str(m, "plan"),
		ID:                     intOr0(m, "id"),
		Name:                   str(m, "name"),
		MonthlyPriceCents:      intOr0(m, "monthly_price_cents"),
		IncludedRequests:       intOr0(m, "included_requests"),
		ExtraPriceCentsPer1000: intOr0(m, "extra_price_cents_per_1000"),
		Raw:                    m,
	}
}

func (b *mcpBackend) Status(ctx context.Context) (*Status, error) {
	m, err := b.call(ctx, "get_account_status", nil)
	if err != nil {
		return nil, err
	}
	s := &Status{Raw: m, Status: str(m, "status"), PaidTill: str(m, "paid_till"), BonusRequests: intOr0(m, "bonus_requests")}
	s.AutoRenew, _ = boolean(m, "auto_renew")
	if pm := object(m, "plan"); pm != nil {
		p := readPlan(pm)
		s.Plan = &p
	}
	return s, nil
}

func (b *mcpBackend) Usage(ctx context.Context, days int) (*Usage, error) {
	var args map[string]any
	if days > 0 {
		args = map[string]any{"days": days}
	}
	m, err := b.call(ctx, "get_usage_stats", args)
	if err != nil {
		return nil, err
	}
	u := &Usage{Raw: m, CycleStart: str(m, "cycle_start"), CycleEnd: str(m, "cycle_end"), UsedThisCycle: intOr0(m, "used_this_cycle")}
	var hasAllowance bool
	u.Allowance, hasAllowance = integer(m, "allowance")
	u.Remaining, u.RemainingKnown = integer(m, "remaining")
	if !u.RemainingKnown && hasAllowance {
		u.Remaining = max(u.Allowance-u.UsedThisCycle, 0)
		u.RemainingKnown = true
	}
	for _, d := range objects(m, "daily") {
		u.Days = append(u.Days, DayUsage{Date: str(d, "date"), Requests: intOr0(d, "requests")})
	}
	sort.SliceStable(u.Days, func(i, j int) bool { return u.Days[i].Date < u.Days[j].Date })
	return u, nil
}

// tokenKeys are where a token tool may put the token. get_api_token
// publishes api_token. rotate_api_token is listed only for sign-ins with
// token:write and its output schema has not been recorded, so its result is
// read under the same key first and then under common alternatives.
var tokenKeys = []string{"api_token", "token", "new_api_token", "new_token"}

func (b *mcpBackend) apiToken(ctx context.Context, tool string) (string, error) {
	m, err := b.call(ctx, tool, nil)
	if err != nil {
		return "", err
	}
	for _, k := range tokenKeys {
		if t := strings.TrimSpace(str(m, k)); t != "" {
			return t, nil
		}
	}
	return "", shapeError(tool, "it has no API token")
}

func (b *mcpBackend) APIToken(ctx context.Context) (string, error) {
	return b.apiToken(ctx, "get_api_token")
}

func (b *mcpBackend) RotateAPIToken(ctx context.Context) (string, error) {
	return b.apiToken(ctx, "rotate_api_token")
}

func (b *mcpBackend) Plans(ctx context.Context) ([]Plan, error) {
	m, err := b.call(ctx, "list_plans", nil)
	if err != nil {
		return nil, err
	}
	plans := []Plan{}
	for _, it := range objects(m, "plans") {
		plans = append(plans, readPlan(it))
	}
	return plans, nil
}

func (b *mcpBackend) BillingHistory(ctx context.Context) ([]Payment, error) {
	m, err := b.call(ctx, "get_billing_history", nil)
	if err != nil {
		return nil, err
	}
	out := []Payment{}
	for _, it := range objects(m, "payments") {
		p := Payment{
			Raw: it, ID: intOr0(it, "id"), Kind: str(it, "kind"), AmountCents: intOr0(it, "amount_cents"),
			CreatedAt: str(it, "created_at"), PaidAt: str(it, "paid_at"), BillingMonth: str(it, "billing_month"),
			ExtraRequestsBilled: intOr0(it, "extra_requests_billed"), BonusRequestsGranted: intOr0(it, "bonus_requests_granted"),
		}
		p.Paid, _ = boolean(it, "paid")
		out = append(out, p)
	}
	return out, nil
}

func (b *mcpBackend) AmountOwed(ctx context.Context) (*Owed, error) {
	m, err := b.call(ctx, "get_amount_owed", nil)
	if err != nil {
		return nil, err
	}
	return &Owed{
		Raw: m, OwedRequests: intOr0(m, "owed_requests"), OwedAmountCents: intOr0(m, "owed_amount_cents"),
		UsedThisCycle: intOr0(m, "used_this_cycle"), Allowance: intOr0(m, "allowance"), Note: str(m, "note"),
	}, nil
}

// link reads a payment link. Without an https:// link there is nothing to
// show, so that fails loudly.
func (b *mcpBackend) link(ctx context.Context, tool string, args map[string]any) (*PaymentLink, error) {
	m, err := b.call(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	l := &PaymentLink{
		Raw: m, URL: strings.TrimSpace(str(m, "payment_url")), AmountCents: intOr0(m, "amount_cents"),
		Requests: intOr0(m, "requests"), IncludesExtrasCents: intOr0(m, "includes_extras_cents"),
	}
	l.HumanApprovalRequired, _ = boolean(m, "human_approval_required")
	if l.URL == "" {
		return nil, shapeError(tool, "it has no payment link")
	}
	if !strings.HasPrefix(l.URL, "https://") {
		return nil, shapeError(tool, "the payment link is not an https:// address")
	}
	return l, nil
}

func (b *mcpBackend) Subscribe(ctx context.Context, plan string) (*PaymentLink, error) {
	return b.link(ctx, "subscribe_to_plan", map[string]any{"plan": plan})
}

func (b *mcpBackend) Renew(ctx context.Context) (*PaymentLink, error) {
	return b.link(ctx, "create_renewal_payment", nil)
}

func (b *mcpBackend) BuyBonus(ctx context.Context, requests int) (*PaymentLink, error) {
	return b.link(ctx, "buy_bonus_requests", map[string]any{"requests": requests})
}

// DocsTopics maps CLI topics to the server's get_api_docs topics.
var DocsTopics = map[string]string{"api": "api", "streams": "streams", "enterprise": "enterprise", "upload": "file-upload", "file-upload": "file-upload"}

func (b *mcpBackend) Docs(ctx context.Context, topic string) (string, error) {
	if t, ok := DocsTopics[topic]; ok {
		topic = t
	}
	args := map[string]any{"topic": topic}
	t, err := b.tool(ctx, "get_api_docs")
	if err != nil {
		return "", err
	}
	if err := checkArgs("get_api_docs", t, args); err != nil {
		return "", err
	}
	st, text, err := b.c.CallTool(ctx, "get_api_docs", args)
	if err != nil {
		return "", err
	}
	if md := str(st, "documentation_markdown"); strings.TrimSpace(md) != "" {
		return md, nil
	}
	if strings.TrimSpace(text) == "" {
		return "", shapeError("get_api_docs", "it has no documentation")
	}
	return text, nil
}
