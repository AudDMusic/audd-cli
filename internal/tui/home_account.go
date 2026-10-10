package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/output"
)

var accountPages = []string{"Account", "Usage", "Billing", "API token", "Profiles"}

// accountSection is the account, usage, billing, API token, and profiles.
type accountSection struct {
	h        *home
	page     int
	loggedIn bool
	signin   *signinSection

	overview *cmdPanel
	usage    *explorerPane

	// Billing
	plans    []map[string]any
	planCur  int
	billing  *cmdPanel // plans (json)
	owed     *cmdPanel // billing owed (table)
	history  *cmdPanel // billing history (table)
	showHist bool
	pay      *cmdPanel // subscribe, renew, buy
	payLink  string
	buying   bool
	buyIn    *form

	// API token
	token    *cmdPanel
	tokView  map[string]any
	revealed bool
	copyRun  *run

	// Profiles
	profCur  int
	prof     *cmdPanel
	confirm  string // "logout" while asking
	switchTo string
}

func newAccountSection(h *home) section {
	s := &accountSection{h: h, signin: newSignin(h, "Sign in to see this"),
		overview: newPanel(h), usage: newExplorerPane(h, tabUsage),
		billing: newPanel(h), owed: newPanel(h), history: newPanel(h), pay: newPanel(h),
		token: newPanel(h), prof: newPanel(h)}
	n := &field{kind: fInt, name: "requests", label: "Requests", help: "Extra requests, in multiples of 1,000", required: true, input: newInput()}
	s.buyIn = newForm(n, buttonField("buy", "Get the payment link"))
	s.buyIn.h = h
	return s
}

func (s *accountSection) title() string { return "Account" }

// needsLogin reports whether the current page needs an account sign-in.
func (s *accountSection) needsLogin() bool { return s.page <= 2 && !s.loggedIn }

func (s *accountSection) checkLogin() {
	_, err := s.h.a.Account()
	s.loggedIn = err == nil
}

func (s *accountSection) init() tea.Cmd {
	s.checkLogin()
	return s.openPage(s.page)
}

func (s *accountSection) openPage(p int) tea.Cmd {
	s.page = (p + len(accountPages)) % len(accountPages)
	s.checkLogin()
	s.confirm = ""
	if s.needsLogin() {
		return nil
	}
	switch s.page {
	case 0:
		return s.overview.start(runReq{args: []string{"account"}, format: "table"})
	case 1:
		if s.usage.ex == nil {
			return s.usage.init()
		}
		return s.usage.reload(tabUsage)
	case 2:
		if s.billing.res == nil && !s.billing.running {
			return tea.Batch(s.billing.start(runReq{args: []string{"billing", "plans"}}),
				s.owed.start(runReq{args: []string{"billing", "owed"}, format: "table"}))
		}
	case 3:
		if s.token.res == nil && !s.token.running {
			return s.showToken(false)
		}
	}
	return nil
}

func (s *accountSection) showToken(reveal bool) tea.Cmd {
	args := []string{"token", "show"}
	if reveal {
		args = append(args, "--reveal")
	}
	s.revealed = reveal
	return s.token.start(runReq{args: args, tag: "show"})
}

func (s *accountSection) selectedPlan() string {
	if s.planCur < 0 || s.planCur >= len(s.plans) {
		return ""
	}
	return str(s.plans[s.planCur]["plan"])
}

func (s *accountSection) update(msg tea.Msg) tea.Cmd {
	if mine, done := s.billing.handle(msg); mine {
		if done && s.billing.res.ok() {
			s.plans = nil
			if doc := parseDoc(s.billing.res.stdout); doc != nil {
				if list, ok := doc["plans"].([]any); ok {
					for _, p := range list {
						if m, ok := p.(map[string]any); ok {
							s.plans = append(s.plans, m)
						}
					}
				}
			}
		}
		return nil
	}
	if mine, done := s.pay.handle(msg); mine {
		if done && s.pay.res.ok() {
			s.payLink = str(parseDoc(s.pay.res.stdout)["url"])
		}
		return nil
	}
	if mine, done := s.token.handle(msg); mine {
		if done && s.token.res.ok() {
			doc := parseDoc(s.token.res.stdout)
			switch s.token.run.req.tag {
			case "copy":
				s.token.res = nil // keep showing the masked token
				if t := str(doc["token"]); t != "" {
					return tea.Batch(s.h.copy(t, "the API token"), s.showToken(false))
				}
				return s.showToken(false)
			case "rotate":
				s.tokView = doc
				s.revealed = false
				return s.h.setFlash("Rotated. The old token no longer works.")
			default:
				s.tokView = doc
			}
		}
		return nil
	}
	if mine, done := s.prof.handle(msg); mine {
		if done && s.prof.res.ok() {
			switch s.prof.run.req.tag {
			case "switch":
				return s.h.switchProfile(s.switchTo)
			case "logout":
				s.h.signedOut = s.h.noToken()
				s.checkLogin()
				return tea.Batch(s.h.setFlash("Signed out"), s.h.loadHeader())
			}
		}
		return nil
	}
	for _, p := range []*cmdPanel{s.overview, s.owed, s.history} {
		if mine, _ := p.handle(msg); mine {
			return nil
		}
	}
	if mine, done := s.signin.panel.handle(msg); mine {
		if done && s.signin.panel.res.ok() {
			s.signin.step = ""
			s.checkLogin()
			return tea.Batch(s.h.signedIn(false), s.h.show("account", true), s.openPage(s.page))
		}
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s.usage.update(msg)
	}
	ks := k.String()
	if s.needsLogin() {
		if s.signin.step == "" {
			switch ks {
			case "]", "right":
				return s.openPage(s.page + 1)
			case "[":
				return s.openPage(s.page - 1)
			case "left":
				if s.page > 0 {
					return s.openPage(s.page - 1)
				}
			}
		}
		return s.signin.update(k)
	}
	if s.atTop() {
		switch ks {
		case "]":
			return s.openPage(s.page + 1)
		case "[":
			return s.openPage(s.page - 1)
		case "left":
			if s.page > 0 {
				return s.openPage(s.page - 1)
			}
		case "right":
			if s.page < len(accountPages)-1 {
				return s.openPage(s.page + 1)
			}
		}
	}
	if ks == "left" && s.page == 2 && s.buying && s.buyIn.leftExits() {
		s.buying = false
		return nil
	}
	_, ht := s.h.contentSize()
	switch s.page {
	case 0:
		if ks == "r" {
			return s.openPage(0)
		}
		return s.overview.key(k, ht)
	case 1:
		return s.usage.update(msg)
	case 2:
		return s.billingKey(k)
	case 3:
		if s.token.pending != nil {
			return s.token.key(k, ht)
		}
		switch ks {
		case "v":
			return s.showToken(true)
		case "c":
			return s.token.start(runReq{args: []string{"token", "show", "--reveal"}, tag: "copy"})
		case "R":
			return s.token.start(runReq{args: []string{"token", "rotate"}, typed: "rotate", tag: "rotate"})
		case "f":
			return s.token.start(runReq{args: []string{"token", "refresh-local"}, tag: "refresh"})
		case "h":
			return s.showToken(false)
		}
		return nil
	case 4:
		return s.profilesKey(ks)
	}
	return nil
}

func (s *accountSection) billingKey(k tea.KeyMsg) tea.Cmd {
	ks := k.String()
	if s.pay.running && s.pay.pending != nil || s.pay.pasting {
		_, ht := s.h.contentSize()
		return s.pay.key(k, ht)
	}
	if s.buying {
		act, cmd := s.buyIn.update(k)
		if act == "buy" {
			v := s.buyIn.value("requests")
			n, err := strconv.Atoi(strings.ReplaceAll(v, ",", ""))
			if err != nil || n <= 0 || n%1000 != 0 {
				s.buyIn.err = "Enter a positive multiple of 1,000."
				return nil
			}
			s.buyIn.err = ""
			s.buying = false
			return s.startPay([]string{"billing", "buy", strconv.Itoa(n)})
		}
		return cmd
	}
	if s.payLink != "" {
		switch ks {
		case "enter", "o":
			if err := openURL(s.payLink); err != nil {
				return s.h.setFlash("Could not open the browser: " + err.Error())
			}
			return s.h.setFlash("Opened the payment page")
		case "c":
			return s.h.copy(s.payLink, "the payment link")
		case "backspace", "x":
			s.payLink = ""
			s.pay.res = nil
			return nil
		}
	}
	switch ks {
	case "down", "j":
		s.planCur = min(len(s.plans)-1, s.planCur+1)
	case "up", "k":
		s.planCur = max(0, s.planCur-1)
	case "s":
		if p := s.selectedPlan(); p != "" {
			return s.startPay([]string{"billing", "subscribe", p})
		}
	case "n":
		return s.startPay([]string{"billing", "renew"})
	case "b":
		s.buying = true
		s.buyIn.focusName("requests")
	case "h":
		s.showHist = !s.showHist
		if s.showHist && s.history.res == nil {
			return s.history.start(runReq{args: []string{"billing", "history"}, format: "table"})
		}
	case "r":
		s.billing.res, s.owed.res = nil, nil
		return s.openPage(2)
	}
	return nil
}

// startPay asks for a payment link. It never charges anything and never
// adds --open: the link is shown, and opened only on a keypress.
func (s *accountSection) startPay(args []string) tea.Cmd {
	s.payLink = ""
	return s.pay.start(runReq{args: args})
}

func (s *accountSection) profiles() []string {
	if s.h.a.Cfg == nil {
		return nil
	}
	names := s.h.a.Cfg.ProfileNames()
	cur := ""
	if s.h.a.Profile != nil {
		cur = s.h.a.Profile.Name
	}
	found := false
	for _, n := range names {
		if n == cur {
			found = true
		}
	}
	if !found && cur != "" {
		names = append([]string{cur}, names...)
	}
	return names
}

func (s *accountSection) profilesKey(ks string) tea.Cmd {
	if s.confirm != "" {
		s.confirm = ""
		if ks == "y" || ks == "Y" {
			return s.prof.start(runReq{args: []string{"auth", "logout"}, tag: "logout"})
		}
		return s.h.setFlash("Cancelled")
	}
	names := s.profiles()
	switch ks {
	case "down", "j":
		s.profCur = min(len(names)-1, s.profCur+1)
	case "up", "k":
		s.profCur = max(0, s.profCur-1)
	case "enter":
		if s.profCur < len(names) {
			s.switchTo = names[s.profCur]
			return s.prof.start(runReq{args: []string{"auth", "switch", s.switchTo}, tag: "switch"})
		}
	case "L":
		s.confirm = "logout"
	}
	return nil
}

func (s *accountSection) pageBar(w int) string {
	return s.h.pageStrip(accountPages, s.page, w, func(i int) tea.Cmd {
		s.buying = false
		return s.openPage(i)
	})
}

// atTop reports whether the page shows nothing opened on it (a form, a
// question, a link, a sign-in step), so the arrows walk the pages.
func (s *accountSection) atTop() bool {
	if s.needsLogin() {
		return s.signin.step == ""
	}
	if s.capturing() || s.confirm != "" {
		return false
	}
	switch s.page {
	case 1:
		return s.usage.atTop()
	case 2:
		return !s.buying && s.payLink == "" && !s.showHist && !s.pay.running
	case 3:
		return s.token.pending == nil
	}
	return true
}

// leftExits: left walks back through the pages, and leaves from the
// first one.
func (s *accountSection) leftExits() bool { return s.page == 0 && s.atTop() }

func (s *accountSection) wheel(dir int) tea.Cmd {
	k := tea.KeyMsg{Type: tea.KeyDown}
	if dir < 0 {
		k = tea.KeyMsg{Type: tea.KeyUp}
	}
	if s.needsLogin() || !s.atTop() {
		return nil
	}
	switch s.page {
	case 1:
		return s.usage.wheel(dir)
	case 2, 4:
		return s.update(k)
	}
	return nil
}

func (s *accountSection) view(w, h int) string {
	st := s.h.st
	bar := s.pageBar(w) + "\n"
	h--
	if s.needsLogin() {
		return bar + s.signin.view(w, h)
	}
	switch s.page {
	case 0:
		return bar + s.overview.view(w, h)
	case 1:
		return bar + s.usage.view(w, h)
	case 2:
		return bar + s.billingView(w, h)
	case 3:
		return bar + s.tokenView(w, h)
	}
	var b strings.Builder
	b.WriteString(st.Bold.Render("Profiles") + "\n\n")
	names := s.profiles()
	cur := ""
	if s.h.a.Profile != nil {
		cur = s.h.a.Profile.Name
	}
	for i, n := range names {
		mark := "  "
		if i == s.profCur {
			mark = "› "
		}
		label := n
		if n == cur {
			label += st.Dim.Render("  (in use)")
		}
		b.WriteString(mark + label + "\n")
	}
	b.WriteString("\n" + styleLines(st.Dim, output.Wrap("Enter switches to a profile. To add one, sign in with it: audd login --profile NAME.", w)))
	if s.confirm != "" {
		b.WriteString("\n\n" + st.Warn.Render(fmt.Sprintf("Sign out of profile %s? Its saved sign-in and API token are removed. [y/N]", cur)))
	}
	if s.prof.res != nil && s.prof.res.err != nil {
		b.WriteString("\n\n" + errorText(st, s.prof.res.err, w))
	}
	return bar + b.String()
}

func (s *accountSection) billingView(w, h int) string {
	st := s.h.st
	var b strings.Builder
	switch {
	case s.pay.running && s.pay.pending != nil:
		return s.pay.view(w, h)
	case s.pay.running:
		return st.Dim.Render("Getting the payment link…")
	case s.pay.res != nil && s.pay.res.err != nil:
		b.WriteString(errorText(st, s.pay.res.err, w) + "\n\n")
	case s.payLink != "":
		doc := parseDoc(s.pay.res.stdout)
		if c := num(doc["amount_cents"]); c > 0 {
			b.WriteString(fmt.Sprintf("Amount: $%.2f\n\n", c/100))
		}
		b.WriteString("Open this link to review and pay (nothing is charged until you confirm there):\n\n  ")
		b.WriteString(st.Accent.Render(s.payLink) + "\n\n")
		b.WriteString(st.Dim.Render("enter opens it in the browser, c copies it, x closes it."))
		return b.String()
	}
	if s.buying {
		b.WriteString(st.Bold.Render("Buy extra requests") + "\n\n" + s.buyIn.view(w, st, s.h.color))
		return b.String()
	}
	if s.showHist {
		b.WriteString(st.Bold.Render("Payments") + "\n\n" + s.history.view(w, h-3))
		return b.String()
	}
	if s.owed.res != nil {
		b.WriteString(s.owed.view(w, 4) + "\n\n")
	}
	b.WriteString(st.Bold.Render("Plans") + "\n")
	switch {
	case s.billing.running:
		b.WriteString(st.Dim.Render("Loading…"))
	case s.billing.res != nil && s.billing.res.err != nil:
		b.WriteString(errorText(st, s.billing.res.err, w))
	case len(s.plans) == 0:
		b.WriteString(st.Dim.Render("No plans available."))
	}
	for i, p := range s.plans {
		mark := "  "
		if i == s.planCur {
			mark = "› "
		}
		line := fmt.Sprintf("%-16s %-14s %s/month  %s requests", str(p["plan"]), str(p["name"]),
			money(num(p["monthly_price_cents"])), fmtInt(int(num(p["included_requests"]))))
		if i == s.planCur {
			line = st.Bold.Render(line)
		}
		b.WriteString(truncate(mark+line, w) + "\n")
	}
	b.WriteString("\n" + styleLines(st.Dim, output.Wrap("Payment links never charge anything: you review and pay in the browser.", w)))
	return b.String()
}

func money(cents float64) string { return fmt.Sprintf("$%.2f", cents/100) }

func (s *accountSection) tokenView(w, h int) string {
	st := s.h.st
	var b strings.Builder
	b.WriteString(st.Bold.Render("API token") + "\n\n")
	if s.token.running && s.token.pending != nil {
		return b.String() + s.token.view(w, h-2)
	}
	if s.token.res != nil && s.token.res.err != nil {
		b.WriteString(errorText(st, s.token.res.err, w) + "\n\n")
	}
	if s.tokView != nil {
		b.WriteString("  " + st.Bold.Render(str(s.tokView["token"])) + "\n")
		if d := str(s.tokView["source_description"]); d != "" {
			b.WriteString("  " + st.Dim.Render("From the "+d) + "\n")
		}
	} else if s.token.running {
		b.WriteString(st.Dim.Render("Loading…") + "\n")
	}
	b.WriteString("\n" + styleLines(st.Dim, output.Wrap("v shows the whole token, h hides it again, c copies it without showing it. R rotates it: the current token stops working everywhere. f fetches it from your account again.", w)))
	return b.String()
}

func (s *accountSection) keys() []keyHelp {
	pages := keyHelp{"←/→ [ ]", "pages"}
	if s.needsLogin() {
		return append(s.signin.keys(), pages)
	}
	switch s.page {
	case 0:
		return []keyHelp{{"r", "refresh"}, pages}
	case 1:
		return append(s.usage.keys(), pages)
	case 2:
		switch {
		case s.pay.running && s.pay.pending != nil:
			return s.signin.keysFor(s.pay)
		case s.buying:
			return []keyHelp{{"enter", "get the link"}, {"esc", "cancel"}}
		case s.payLink != "":
			return []keyHelp{{"enter", "open"}, {"c", "copy"}, {"x", "close"}}
		}
		return []keyHelp{{"↑/↓", "plan"}, {"s", "subscribe"}, {"n", "renew"}, {"b", "buy requests"}, {"h", "payments"}, pages}
	case 3:
		return []keyHelp{{"v", "reveal"}, {"c", "copy"}, {"R", "rotate"}, {"f", "fetch again"}, pages}
	}
	return []keyHelp{{"↑/↓", "profile"}, {"enter", "switch"}, {"L", "sign out"}, pages}
}

func (s *accountSection) command() string {
	if s.needsLogin() {
		return s.signin.command()
	}
	switch s.page {
	case 0:
		return "audd account"
	case 1:
		return "audd usage"
	case 2:
		switch {
		case s.buying:
			n := s.buyIn.value("requests")
			if n == "" {
				n = "10000"
			}
			return "audd billing buy " + n
		case s.pay.args != nil && (s.pay.running || s.pay.res != nil):
			return displayCommand(s.pay.args)
		case s.showHist:
			return "audd billing history"
		case s.selectedPlan() != "":
			return "audd billing subscribe " + s.selectedPlan()
		}
		return "audd billing plans"
	case 3:
		if s.token.args != nil {
			if s.token.run != nil && s.token.run.req.tag == "copy" {
				return "audd token show"
			}
			return displayCommand(s.token.args)
		}
		return "audd token show"
	}
	if s.confirm != "" {
		return "audd auth logout"
	}
	if names := s.profiles(); s.profCur < len(names) {
		return "audd auth switch " + names[s.profCur]
	}
	return "audd auth status"
}

func (s *accountSection) capturing() bool {
	if s.needsLogin() {
		return s.signin.capturing()
	}
	switch s.page {
	case 1:
		return s.usage.capturing()
	case 2:
		return s.buying && s.buyIn.capturing() || s.pay.capturing()
	case 3:
		return s.token.capturing()
	case 4:
		return s.confirm != ""
	}
	return false
}

func (s *accountSection) back() bool {
	if s.needsLogin() {
		return s.signin.back()
	}
	switch s.page {
	case 2:
		switch {
		case s.pay.back():
			return true
		case s.pay.running:
			s.pay.stop()
			return true
		case s.buying:
			s.buying = false
			return true
		case s.payLink != "":
			s.payLink = ""
			return true
		case s.showHist:
			s.showHist = false
			return true
		}
	case 3:
		if s.token.back() {
			return true
		}
	case 4:
		if s.confirm != "" {
			s.confirm = ""
			return true
		}
	}
	return false
}
