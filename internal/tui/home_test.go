package tui

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
)

// fakeRun stands in for HomeRun: it records each run and answers from
// handlers matched by the start of the command ("streams add").
type fakeRun struct {
	mu       sync.Mutex
	calls    [][]string
	stdins   []string
	handlers []fakeHandler
}

type fakeHandler struct {
	prefix string
	fn     func(args []string, io RunIO) int
}

func (f *fakeRun) on(prefix string, fn func(args []string, io RunIO) int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append([]fakeHandler{{prefix, fn}}, f.handlers...)
}

// reply answers runs starting with prefix with fixed output.
func (f *fakeRun) reply(prefix, stdout, stderr string, code int) {
	f.on(prefix, func(args []string, io RunIO) int {
		io.Stdout.Write([]byte(stdout))
		io.Stderr.Write([]byte(stderr))
		return code
	})
}

func (f *fakeRun) run(ctx context.Context, args []string, io RunIO) int {
	var in bytes.Buffer
	if io.Stdin != nil {
		in.ReadFrom(io.Stdin)
	}
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.stdins = append(f.stdins, in.String())
	hs := append([]fakeHandler(nil), f.handlers...)
	f.mu.Unlock()
	line := strings.Join(args, " ")
	for _, h := range hs {
		if strings.HasPrefix(line, h.prefix) {
			return h.fn(args, io)
		}
	}
	io.Stderr.Write([]byte(`{"schema_version":1,"error":{"code":"unexpected","message":"no fake for ` + line + `","hint":""}}` + "\n"))
	return 1
}

// commands are the runs so far, without --format and session flags.
func (f *fakeRun) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(stripRunFlags(c), " "))
	}
	return out
}

func stripRunFlags(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format", "--profile", "--token", "--max-requests":
			i++
			continue
		case "--debug":
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func (f *fakeRun) waitFor(t *testing.T, cmd string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range f.commands() {
			if c == cmd {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no run of %q; runs: %q", cmd, f.commands())
}

// fakeBackend is an account backend with fixed answers.
type fakeBackend struct{ err error }

func (b fakeBackend) Profile(ctx context.Context) (*account.Profile, error) {
	return &account.Profile{Email: "user@example.com"}, b.err
}
func (b fakeBackend) Status(ctx context.Context) (*account.Status, error) {
	return &account.Status{Status: "active", Plan: &account.Plan{Name: "Startup"}}, b.err
}
func (b fakeBackend) Usage(ctx context.Context, days int) (*account.Usage, error) {
	return &account.Usage{UsedThisCycle: 1234, Allowance: 5000, Remaining: 3766, RemainingKnown: true,
		Days: []account.DayUsage{{Date: "2026-10-07", Requests: 800}, {Date: "2026-10-08", Requests: 434}}}, b.err
}
func (b fakeBackend) APIToken(ctx context.Context) (string, error)       { return "", b.err }
func (b fakeBackend) RotateAPIToken(ctx context.Context) (string, error) { return "", b.err }
func (b fakeBackend) Plans(ctx context.Context) ([]account.Plan, error)  { return nil, b.err }
func (b fakeBackend) BillingHistory(ctx context.Context) ([]account.Payment, error) {
	return nil, b.err
}
func (b fakeBackend) AmountOwed(ctx context.Context) (*account.Owed, error) { return nil, b.err }
func (b fakeBackend) Subscribe(ctx context.Context, plan string) (*account.PaymentLink, error) {
	return nil, b.err
}
func (b fakeBackend) Renew(ctx context.Context) (*account.PaymentLink, error) { return nil, b.err }
func (b fakeBackend) BuyBonus(ctx context.Context, n int) (*account.PaymentLink, error) {
	return nil, b.err
}
func (b fakeBackend) Docs(ctx context.Context, topic string) (string, error) { return "", b.err }

type homeOpts struct {
	signedOut bool // no API token
	loggedIn  bool // signed in to the account
	start     string
}

// newTestHome is interactive mode over fakes: f answers the runs.
func newTestHome(t *testing.T, f *fakeRun, o homeOpts) *home {
	t.Helper()
	oldRun, oldApp := HomeRun, HomeApp
	HomeRun = f.run
	t.Cleanup(func() { HomeRun, HomeApp = oldRun, oldApp })
	oldZone := localZone
	localZone = time.UTC
	t.Cleanup(func() { localZone = oldZone })
	d := fakeExplorerData(&fakeStreamsAPI{})
	oldData, oldFeed := NewExplorerData, NewFeed
	NewExplorerData = func(a *app.App) (ExplorerData, error) { return d, nil }
	NewFeed = func(a *app.App) (Feed, error) { return sampleFeed(), nil }
	t.Cleanup(func() { NewExplorerData, NewFeed = oldData, oldFeed })
	t.Setenv("AUDD_API_TOKEN", "")
	t.Setenv("AUDD_FORMAT", "")

	var out, errb bytes.Buffer
	a := npApp(&out, &errb, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, StdinTTY: true, NoColor: true})
	a.Cfg = config.Defaults(filepath.Join(t.TempDir(), "config.toml"))
	a.Profile = a.Cfg.Profile("default")
	mem := secrets.NewMemory()
	if !o.signedOut {
		mem.Set("default", "api_token", "0123456789abcdef0123456789abcdef")
	}
	a.Secrets = mem
	a.Account = func() (account.Backend, error) {
		if !o.loggedIn {
			return nil, &output.Error{Code: "login_required", Message: "this needs you to sign in to your AudD account", Hint: "audd login", Exit: output.ExitAuth}
		}
		return fakeBackend{}, nil
	}
	HomeApp = func(flags app.GlobalFlags) (*app.App, error) {
		n := *a
		n.Flags = flags
		return &n, nil
	}
	idx, err := sectionIndex(o.start)
	if err != nil {
		t.Fatal(err)
	}
	return newHome(context.Background(), sessionApp(a), idx)
}

// runHome drives h on a w×ht terminal: waits for wait, sends keys, waits
// for after (when set), quits, and returns the last frame.
func runHome(t *testing.T, h *home, w, ht int, wait, after string, keys ...tea.KeyMsg) string {
	t.Helper()
	tm := teatest.NewTestModel(t, h, teatest.WithInitialTermSize(w, ht))
	out := tm.Output()
	waitFor := func(s string) {
		teatest.WaitFor(t, out, func(b []byte) bool { return bytes.Contains(b, []byte(s)) },
			teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	}
	if wait != "" {
		waitFor(wait)
	}
	for _, k := range keys {
		tm.Send(k)
	}
	if after != "" {
		waitFor(after)
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	return fm.View()
}

// typeText is the key messages for typing s.
func typeText(s string) []tea.KeyMsg {
	var out []tea.KeyMsg
	for _, r := range s {
		out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return out
}

func keys(ks ...any) []tea.KeyMsg {
	var out []tea.KeyMsg
	for _, k := range ks {
		switch v := k.(type) {
		case tea.KeyMsg:
			out = append(out, v)
		case tea.KeyType:
			out = append(out, tea.KeyMsg{Type: v})
		case string:
			out = append(out, key(v))
		}
	}
	return out
}

// sized gives a model its size and returns it, for direct Update tests.
func sized(h *home, w, ht int) *home {
	h.Update(tea.WindowSizeMsg{Width: w, Height: ht})
	return h
}

// press sends keys straight to Update and runs the commands they return
// until nothing is left (ticks excluded), for tests that do not need a
// program.
func press(h *home, ks ...tea.KeyMsg) {
	for _, k := range ks {
		_, cmd := h.Update(k)
		drain(h, cmd, 0)
	}
}

func drain(h *home, cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 50 {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(300 * time.Millisecond):
		return // a tick or a long wait
	}
	switch m := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range m {
			drain(h, c, depth+1)
		}
	case tea.QuitMsg:
	default:
		_, next := h.Update(m)
		drain(h, next, depth+1)
	}
}

func TestHomeShellNavigation(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{}), 120, 40)
	v := h.View()
	for _, s := range []string{"AudD", "profile default", "1 Recognize", "8 Help", "ctrl+k"} {
		if !strings.Contains(v, s) {
			t.Fatalf("missing %q:\n%s", s, v)
		}
	}
	press(h, key("esc"))
	if h.focus != focusSidebar {
		t.Fatal("esc goes back to the sidebar")
	}
	press(h, key("down"), key("down"))
	if h.activeID() != "now-playing" {
		t.Fatalf("down moves the sidebar: %s", h.activeID())
	}
	press(h, key("enter"))
	if h.focus != focusContent {
		t.Fatal("enter opens the section")
	}
	press(h, key("7"))
	if h.activeID() != "settings" {
		t.Fatalf("7 jumps to Settings: %s", h.activeID())
	}
	press(h, tea.KeyMsg{Type: tea.KeyF1})
	if h.activeID() != "help" {
		t.Fatalf("F1 opens Help: %s", h.activeID())
	}
	press(h, key("?"))
	if !h.keysO || !strings.Contains(h.View(), "Everywhere") {
		t.Fatalf("? shows the keys:\n%s", h.View())
	}
	press(h, key("x"))
	if h.keysO {
		t.Fatal("any key closes the keys")
	}
	_, cmd := h.Update(key("q"))
	if cmd == nil {
		t.Fatal("q quits")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q quits")
	}
}

func TestHomeNarrowTabStrip(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "streams"}), 70, 24)
	v := h.View()
	if strings.Contains(v, "│") || !strings.Contains(v, "[4 Streams]") {
		t.Fatalf("narrow terminals show a tab strip:\n%s", v)
	}
	if n := strings.Count(v, "\n") + 1; n != 24 {
		t.Fatalf("%d lines", n)
	}
}

func TestHomeHeader(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{loggedIn: true}), 120, 40)
	drain(h, h.loadHeader(), 0)
	if v := h.View(); !strings.Contains(v, "user@example.com · Startup") || !strings.Contains(v, "3,766 requests left") {
		t.Fatalf("header:\n%s", v)
	}
	h = sized(newTestHome(t, &fakeRun{}, homeOpts{}), 120, 40)
	drain(h, h.loadHeader(), 0)
	if v := h.View(); strings.Contains(v, "requests left") {
		t.Fatalf("no account line when not signed in:\n%s", v)
	}
	_ = errors.New
}
