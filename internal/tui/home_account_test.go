package tui

import (
	"strings"
	"testing"
	"time"
)

func accountFake() *fakeRun {
	f := &fakeRun{}
	f.reply("account", "Account         user@example.com\nPlan            Startup, $450.00/month, 100,000 requests included (active)\nPaid until      2026-11-01\n", "", 0)
	f.reply("billing plans", `{"schema_version":1,"plans":[{"plan":"startup_plan","name":"Startup","monthly_price_cents":45000,"included_requests":100000},{"plan":"pro_plan","name":"Pro","monthly_price_cents":90000,"included_requests":250000}]}`, "", 0)
	f.reply("billing owed", "Requests over the allowance  0\nAmount owed                  $0.00\n", "", 0)
	f.reply("billing subscribe", `{"schema_version":1,"url":"https://checkout.stripe.example/c/pay_123","charged":false,"amount_cents":90000}`, "", 0)
	f.reply("token show", `{"schema_version":1,"token":"0123…cdef","masked":true,"source":"config","source_description":"config file (audd config set token)"}`, "", 0)
	f.reply("token show --reveal", `{"schema_version":1,"token":"0123456789abcdef0123456789abcdef","masked":false,"source":"config","source_description":"config file (audd config set token)"}`, "", 0)
	return f
}

func TestAccountSignedOutPanel(t *testing.T) {
	useSigninMethod(t, "browser")
	snap(t, "account_signin", func() *home { return newTestHome(t, &fakeRun{}, homeOpts{start: "account"}) }, "Sign in to see this")
}

func TestAccountPages(t *testing.T) {
	f := accountFake()
	snap(t, "account_overview", func() *home { return newTestHome(t, f, homeOpts{start: "account", loggedIn: true}) }, "Paid until")
	snap(t, "account_billing", func() *home { return newTestHome(t, f, homeOpts{start: "account", loggedIn: true}) }, "Paid until", key("]"), key("]"), "pro_plan", "Amount owed")
}

// A payment link is shown and opened only on a keypress; nothing passes
// --open, and nothing is charged.
func TestAccountBillingLink(t *testing.T) {
	f := accountFake()
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	h := newTestHome(t, f, homeOpts{start: "account", loggedIn: true})
	v := drive(t, h, 120, 40, "Paid until", key("]"), key("]"), "pro_plan", key("down"),
		"audd billing subscribe pro_plan", key("s"), "https://checkout.stripe.example/c/pay_123")
	if !strings.Contains(v, "nothing is charged until you confirm there") || len(opened) != 0 {
		t.Fatalf("link:\n%s\nopened %v", v, opened)
	}
	h = newTestHome(t, f, homeOpts{start: "account", loggedIn: true})
	drive(t, h, 120, 40, "Paid until", key("]"), key("]"), "pro_plan", key("s"), "checkout.stripe.example", key("enter"), "Opened the payment page")
	if len(opened) != 1 || opened[0] != "https://checkout.stripe.example/c/pay_123" {
		t.Fatalf("enter opens the link: %v", opened)
	}
	for _, c := range f.commands() {
		if strings.Contains(c, "--open") || strings.Contains(c, "--yes") {
			t.Fatalf("payment run with %q", c)
		}
	}
}

// The token is masked until v; rotating needs the word typed.
func TestAccountTokenRevealAndRotate(t *testing.T) {
	f := accountFake()
	answered := make(chan bool, 1)
	f.on("token rotate", func(args []string, io RunIO) int {
		ok := io.Ask("Rotate your AudD API token? The current token stops working immediately, everywhere it is used.")
		answered <- ok
		if !ok {
			io.Stderr.Write([]byte(`{"error":{"code":"declined","message":"cancelled"}}` + "\n"))
			return 6
		}
		io.Stdout.Write([]byte(`{"schema_version":1,"token":"fedc…3210","masked":true,"source":"login"}`))
		return 0
	})
	h := newTestHome(t, f, homeOpts{start: "account", loggedIn: true})
	drive(t, h, 120, 40, "Paid until", key("["), key("["), "0123…cdef")
	for _, c := range f.commands() {
		if strings.Contains(c, "--reveal") {
			t.Fatalf("revealed without a keypress: %q", c)
		}
	}
	h = newTestHome(t, f, homeOpts{start: "account", loggedIn: true})
	v := drive(t, h, 120, 40, "Paid until", key("["), key("["), "0123…cdef", key("v"), "0123456789abcdef0123456789abcdef",
		key("R"), "Type rotate", typeText("rota"), key("enter"), "That does not match", typeText("te"), key("enter"), "fedc…3210")
	if ok := <-answered; !ok {
		t.Fatal("typing rotate confirms")
	}
	if !strings.Contains(v, "Rotated") {
		t.Fatalf("rotate:\n%s", v)
	}
}

func TestAccountProfilesSwitch(t *testing.T) {
	f := &fakeRun{}
	f.reply("auth switch", `{"schema_version":1,"active_profile":"work","existing":true}`, "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "account"}), 120, 40)
	h.a.Cfg.Profile("work")
	h.a.Cfg.Profiles["work"] = h.a.Cfg.Profile("work")
	drain(h, h.activate(), 0)
	press(h, key("["))
	if !strings.Contains(h.View(), "work") {
		t.Fatalf("profiles:\n%s", h.View())
	}
	names := h.subs["account"].(*accountSection).profiles()
	for range names {
		if h.command() == "audd auth switch work" {
			break
		}
		press(h, key("down"))
	}
	press(h, key("enter"))
	f.waitFor(t, "auth switch work")
	waitView(t, h, "profile work")
	if h.flags.Profile != "work" {
		t.Fatalf("session profile: %q", h.flags.Profile)
	}
}

// waitView presses nothing and waits until the view shows s, running
// pending messages.
func waitView(t *testing.T, h *home, s string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v := h.View(); strings.Contains(v, s) {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("view never showed %q:\n%s", s, h.View())
	return ""
}
