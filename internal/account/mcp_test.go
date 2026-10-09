package account

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/mcpclient"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

var allScopes = []string{"profile:read", "account:read", "usage:read", "billing:read", "billing:pay", "token:read", "token:write"}

func backend(t *testing.T) (*testutil.FakeMCP, Backend) {
	t.Helper()
	as := testutil.NewFakeOAuth(t)
	srv := testutil.NewFakeMCP(t, as)
	tok := as.IssueAccess(allScopes...)
	c := mcpclient.New(srv.URL(), func(context.Context) (string, error) { return tok, nil })
	return srv, NewMCP(c)
}

var ctx = context.Background()

// The fake server answers with responses recorded from the live service.
func TestTypedResults(t *testing.T) {
	srv, b := backend(t)
	p, err := b.Profile(ctx)
	if err != nil || p.UserID != 12345 || p.Email != "user@example.com" || len(p.SignInMethods) != 6 ||
		p.SignInMethods[2] != (SignInMethod{Provider: "github", Label: "example-user"}) || p.Raw["email"] == nil {
		t.Fatalf("profile %+v %v", p, err)
	}
	s, err := b.Status(ctx)
	if err != nil || s.Status != "active" || s.Plan == nil || s.Plan.Name != "Indie" || s.Plan.ID != 1 ||
		s.Plan.MonthlyPriceCents != 500 || s.Plan.IncludedRequests != 1000 || s.Plan.ExtraPriceCentsPer1000 != 500 ||
		s.PaidTill != "2028-10-01" || s.BonusRequests != 3000 || !s.AutoRenew {
		t.Fatalf("status %+v %v", s, err)
	}
	u, err := b.Usage(ctx, 2)
	if err != nil || u.CycleStart != "2028-09-01" || u.CycleEnd != "2028-10-01" || u.UsedThisCycle != 0 ||
		u.Allowance != 4000 || u.Remaining != 4000 || !u.RemainingKnown ||
		len(u.Days) != 2 || u.Days[0] != (DayUsage{Date: "2026-10-07", Requests: 313}) {
		t.Fatalf("usage %+v %v", u, err)
	}
	if c := srv.CallsTo("get_usage_stats"); c[0].Args["days"] != float64(2) {
		t.Fatalf("days arg: %v", c[0].Args)
	}
	if tok, err := b.APIToken(ctx); err != nil || tok != testutil.FakeAPIToken {
		t.Fatal(tok, err)
	}
	if tok, err := b.RotateAPIToken(ctx); err != nil || tok != testutil.FakeRotatedToken {
		t.Fatal(tok, err)
	}
	plans, err := b.Plans(ctx)
	if err != nil || len(plans) != 3 || plans[1].Key != "startup_plan" || plans[1].Name != "Startup" ||
		plans[1].MonthlyPriceCents != 45000 || plans[1].IncludedRequests != 100000 || plans[1].ExtraPriceCentsPer1000 != 450 {
		t.Fatalf("plans %+v %v", plans, err)
	}
	pays, err := b.BillingHistory(ctx)
	if err != nil || len(pays) != 20 || pays[0].ID != 34356 || pays[0].Kind != "subscription_start" || pays[0].AmountCents != 85000 ||
		pays[0].Paid || pays[0].CreatedAt != "2026-10-08 08:26:34" || pays[0].PaidAt != "" || pays[0].BillingMonth != "2026-10-08" {
		t.Fatalf("history %+v %v", pays, err)
	}
	if pays[6].Kind != "bonus_requests_purchase" || pays[6].BonusRequestsGranted != 1000 {
		t.Fatalf("bonus purchase %+v", pays[6])
	}
	o, err := b.AmountOwed(ctx)
	if err != nil || o.OwedRequests != 0 || o.OwedAmountCents != 0 || o.Allowance != 4000 || o.Note == "" {
		t.Fatalf("owed %+v %v", o, err)
	}
	l, err := b.Subscribe(ctx, "pro_plan")
	if err != nil || l.URL != testutil.FakePaymentURL || l.AmountCents != 85000 || !l.HumanApprovalRequired {
		t.Fatalf("subscribe %+v %v", l, err)
	}
	if c := srv.CallsTo("subscribe_to_plan"); c[0].Args["plan"] != "pro_plan" {
		t.Fatalf("subscribe args: %v", c[0].Args)
	}
	if _, err := b.Renew(ctx); err != nil {
		t.Fatal(err)
	}
	if l, err := b.BuyBonus(ctx, 5000); err != nil || l.Requests != 5000 || l.AmountCents != 2500 {
		t.Fatalf("buy %+v %v", l, err)
	}
	if d, err := b.Docs(ctx, "upload"); err != nil || d != "# file-upload\n\nDocs." {
		t.Fatalf("docs %q %v", d, err)
	}
}

func TestRenamedArgumentFailsLoudly(t *testing.T) {
	srv, b := backend(t)
	srv.SetTool(testutil.FakeTool{
		Name: "subscribe_to_plan", Scope: "billing:pay",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"plan_key": map[string]any{"type": "string"}}, "required": []string{"plan_key"}},
		OutputSchema: testutil.FakeOutputSchemas["subscribe_to_plan"],
		Handle: func(args map[string]any) (map[string]any, string, bool) {
			return map[string]any{"payment_url": "https://checkout.stripe.com/c/pay/x"}, "", false
		},
	})
	_, err := b.Subscribe(ctx, "startup_plan")
	wantShape(t, err)
	if n := len(srv.CallsTo("subscribe_to_plan")); n != 0 {
		t.Fatalf("payment tool called %d times with an argument it does not take", n)
	}
}

func wantShape(t *testing.T, err error) {
	t.Helper()
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "unexpected_response" || oe.Exit != output.ExitUnexpected {
		t.Fatalf("want a loud unexpected_response error, got %#v", err)
	}
}

// withResult replaces a default tool's result, keeping its live schemas.
func withResult(srv *testutil.FakeMCP, name, scope, result string) {
	srv.SetTool(testutil.FakeTool{
		Name: name, Scope: scope, InputSchema: testutil.LiveInputSchemas[name],
		OutputSchema: testutil.FakeOutputSchemas[name],
		Handle: func(map[string]any) (map[string]any, string, bool) {
			m := map[string]any{}
			if err := json.Unmarshal([]byte(result), &m); err != nil {
				panic(err)
			}
			return m, "", false
		},
	})
}

// Without structured content there is nothing to read: that fails loudly.
func TestTextOnlyResultFailsLoudly(t *testing.T) {
	srv, b := backend(t)
	textOnly := func(map[string]any) (map[string]any, string, bool) {
		return nil, "Your email is user@example.com", false
	}
	for _, tool := range []struct{ name, scope string }{
		{"get_profile", "profile:read"}, {"get_account_status", "account:read"}, {"get_usage_stats", "usage:read"},
		{"list_plans", "billing:read"}, {"get_billing_history", "billing:read"}, {"get_amount_owed", "billing:read"},
	} {
		srv.SetTool(testutil.FakeTool{Name: tool.name, Scope: tool.scope, Handle: textOnly})
	}
	for name, call := range map[string]func() error{
		"profile": func() error { _, err := b.Profile(ctx); return err },
		"status":  func() error { _, err := b.Status(ctx); return err },
		"usage":   func() error { _, err := b.Usage(ctx, 0); return err },
		"plans":   func() error { _, err := b.Plans(ctx); return err },
		"history": func() error { _, err := b.BillingHistory(ctx); return err },
		"owed":    func() error { _, err := b.AmountOwed(ctx); return err },
	} {
		t.Run(name, func(t *testing.T) { wantShape(t, call()) })
	}
}

// Missing and wrong-typed fields read as zero values; convertible values
// are converted. None of it fails the command.
func TestLenientFields(t *testing.T) {
	srv, b := backend(t)
	withResult(srv, "get_profile", "profile:read", `{"user_id":"12345","sign_in_methods":[{"provider":"github"},"junk",{"label":42}]}`)
	p, err := b.Profile(ctx)
	if err != nil || p.UserID != 12345 || p.Email != "" || len(p.SignInMethods) != 2 ||
		p.SignInMethods[0].Provider != "github" || p.SignInMethods[1].Label != "42" {
		t.Fatalf("profile %+v %v", p, err)
	}
	withResult(srv, "get_account_status", "account:read", `{"status":"inactive","plan":null,"paid_till":null,"bonus_requests":"abc","auto_renew":"maybe"}`)
	s, err := b.Status(ctx)
	if err != nil || s.Status != "inactive" || s.Plan != nil || s.PaidTill != "" || s.BonusRequests != 0 || s.AutoRenew {
		t.Fatalf("status %+v %v", s, err)
	}
	withResult(srv, "get_account_status", "account:read", `{"plan":"Indie","bonus_requests":12.7,"auto_renew":1}`)
	s, err = b.Status(ctx)
	if err != nil || s.Plan != nil || s.BonusRequests != 12 || !s.AutoRenew {
		t.Fatalf("status %+v %v", s, err)
	}
	withResult(srv, "get_usage_stats", "usage:read", `{"used_this_cycle":"10","daily":{"date":"x"}}`)
	u, err := b.Usage(ctx, 0)
	if err != nil || u.UsedThisCycle != 10 || u.RemainingKnown || len(u.Days) != 0 {
		t.Fatalf("usage %+v %v", u, err)
	}
	withResult(srv, "list_plans", "billing:read", `{"plans":[{"plan":"startup_plan","monthly_price_cents":"45000"},7]}`)
	plans, err := b.Plans(ctx)
	if err != nil || len(plans) != 1 || plans[0].Key != "startup_plan" || plans[0].Name != "" || plans[0].MonthlyPriceCents != 45000 {
		t.Fatalf("plans %+v %v", plans, err)
	}
	withResult(srv, "get_billing_history", "billing:read", `{"payments":"none"}`)
	if pays, err := b.BillingHistory(ctx); err != nil || len(pays) != 0 {
		t.Fatalf("history %+v %v", pays, err)
	}
	withResult(srv, "get_billing_history", "billing:read", `{"payments":[{"amount_cents":500,"paid":"true"}]}`)
	if pays, err := b.BillingHistory(ctx); err != nil || len(pays) != 1 || !pays[0].Paid || pays[0].AmountCents != 500 {
		t.Fatalf("history %+v %v", pays, err)
	}
	withResult(srv, "get_amount_owed", "billing:read", `{}`)
	if o, err := b.AmountOwed(ctx); err != nil || o.OwedRequests != 0 || o.Note != "" {
		t.Fatalf("owed %+v %v", o, err)
	}
}

// A token or payment tool without the one value the command needs fails.
func TestEssentialValuesFailLoudly(t *testing.T) {
	srv, b := backend(t)
	withResult(srv, "get_api_token", "token:read", `{"token_hint":"0123"}`)
	_, err := b.APIToken(ctx)
	wantShape(t, err)
	withResult(srv, "create_renewal_payment", "billing:pay", `{"amount_cents":500}`)
	_, err = b.Renew(ctx)
	wantShape(t, err)
	withResult(srv, "create_renewal_payment", "billing:pay", `{"payment_url":"http://example.com/pay"}`)
	_, err = b.Renew(ctx)
	wantShape(t, err)
}

// rotate_api_token's output schema is not recorded; the new token is read
// under the get_api_token key or common alternatives.
func TestRotatedTokenKeys(t *testing.T) {
	for _, key := range []string{"api_token", "token", "new_api_token", "new_token"} {
		srv, b := backend(t)
		withResult(srv, "rotate_api_token", "token:write", `{"`+key+`":"your-api-token"}`)
		if tok, err := b.RotateAPIToken(ctx); err != nil || tok != "your-api-token" {
			t.Fatalf("%s: %q %v", key, tok, err)
		}
	}
}

func TestUsageRemaining(t *testing.T) {
	for _, c := range []struct {
		result string
		known  bool
		want   int
	}{
		{`{"used_this_cycle":10,"remaining":7,"daily":[]}`, true, 7},
		{`{"used_this_cycle":10,"allowance":100,"daily":[]}`, true, 90},
		{`{"used_this_cycle":200,"allowance":100,"daily":[]}`, true, 0},
		{`{"used_this_cycle":10,"daily":[{"date":"2026-10-01","requests":10}]}`, false, 0},
	} {
		srv, b := backend(t)
		withResult(srv, "get_usage_stats", "usage:read", c.result)
		u, err := b.Usage(ctx, 0)
		if err != nil || u.RemainingKnown != c.known || u.Remaining != c.want {
			t.Fatalf("%s: %+v %v", c.result, u, err)
		}
	}
}

// Docs read documentation_markdown, or the text content when a server
// sends only text.
func TestDocsTextFallback(t *testing.T) {
	srv, b := backend(t)
	srv.SetTool(testutil.FakeTool{Name: "get_api_docs", InputSchema: testutil.LiveInputSchemas["get_api_docs"],
		Handle: func(map[string]any) (map[string]any, string, bool) { return nil, "# api\n\nText docs.", false }})
	if d, err := b.Docs(ctx, "api"); err != nil || d != "# api\n\nText docs." {
		t.Fatalf("%q %v", d, err)
	}
}
