package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
)

var accountPages = []string{"Overview", "Usage", "Billing", "API token", "Profiles"}

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
	payWhat  string    // what the payment link is for
	payLink  string
	buying   bool
	buyIn    *form

	// API token
	token    *cmdPanel
	tokView  map[string]any
	revealed bool
	copyRun  *run

	// A different token, pasted on the API token page.
	tokPasting bool
	tokForm    *form
	tokSet     *cmdPanel

	// Profiles
	profCur  int
	prof     *cmdPanel
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
	tok := &field{kind: fText, name: "token", label: "API token", masked: true, required: true, input: newInput(),
		need: "Paste the token first", help: "From https://dashboard.audd.io."}
	s.tokForm = newForm(tok, buttonField("save", "Save the token"))
	s.tokForm.h = h
	s.tokSet = newPanel(h)
	s.buyIn.get("buy").disabled = func() string {
		if s.buyIn.value("requests") == "" {
			return "Enter how many requests first"
		}
		return ""
	}
	return s
}

func (s *accountSection) tabNames() []string      { return accountPages }
func (s *accountSection) tabIndex() int           { return s.page }
func (s *accountSection) selectTab(i int) tea.Cmd { s.buying = false; return s.openPage(i) }

// upExits: ↑ at the top of the page goes to the tab bar.
func (s *accountSection) upExits() bool {
	if s.needsLogin() {
		return s.signin.step == "" && s.signin.choice == 0
	}
	if !s.atTop() {
		return false
	}
	switch s.page {
	case 0:
		return s.overview.scroll.off == 0
	case 1:
		return s.usage.upExits()
	case 2:
		return s.planCur == 0
	case 4:
		return s.profCur == 0
	}
	return true
}

// crumbs name what is open on the page.
func (s *accountSection) crumbs() []string {
	if s.needsLogin() {
		return s.signin.crumbs()
	}
	switch s.page {
	case 2:
		switch {
		case s.buying:
			return []string{"Buy requests"}
		case s.payLink != "" || s.pay.running:
			return []string{"Payment link"}
		case s.showHist:
			return []string{"Payments"}
		}
	}
	return nil
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
	if !s.pay.running {
		// What a payment link showed is gone once the page is left.
		s.pay.res, s.pay.args, s.payLink = nil, nil, ""
	}
	s.page = max(0, min(len(accountPages)-1, p))
	s.checkLogin()
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
				s.h.useToken(false)
				s.checkLogin()
				return tea.Batch(s.h.setFlash("Signed out"), s.h.loadHeader())
			}
		}
		return nil
	}
	if mine, done := s.tokSet.handle(msg); mine {
		if done && s.tokSet.res.ok() {
			s.tokPasting = false
			s.tokForm.get("token").setValue("")
			s.h.useToken(false)
			return tea.Batch(s.h.setFlash("Token saved for profile "+s.h.profileName()), s.h.loadHeader(), s.showToken(false))
		}
		if done && s.tokSet.res.err != nil {
			s.tokForm.err = tokenSaveFailed(s.tokSet.res.err)
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
		return s.signin.update(k)
	}
	_, ht := s.h.contentSize()
	switch s.page {
	case 0:
		return s.overview.key(k, ht)
	case 1:
		if ks == "enter" || ks == "right" {
			return s.openPage(2)
		}
		return s.usage.update(msg)
	case 2:
		return s.billingKey(k)
	case 3:
		if s.token.pending != nil {
			return s.token.key(k, ht)
		}
		if s.tokPasting {
			if act, cmd := s.tokForm.update(k); act == "save" {
				return s.askToken()
			} else {
				return cmd
			}
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
		if k.Type == tea.KeyRunes || ks == "backspace" || ks == "ctrl+u" {
			s.pay.res = nil // an old error goes with the change
		}
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
			return s.h.openLink(s.payLink)
		case "c":
			return s.h.copy(s.payLink, "the payment link")
		case "backspace":
			s.payLink = ""
			s.pay.res = nil
			return nil
		}
	}
	switch ks {
	case "down":
		s.planCur = min(len(s.plans)-1, s.planCur+1)
	case "up":
		s.planCur = max(0, s.planCur-1)
	case "enter":
		// Enter on the current plan renews it; on another, subscribes.
		if s.planCur == s.currentPlan() {
			return s.startPay([]string{"billing", "renew"})
		}
		if p := s.selectedPlan(); p != "" {
			return s.startPay([]string{"billing", "subscribe", p})
		}
	case "b":
		s.buying = true
		s.buyIn.focusName("requests")
	case "m":
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
	// What the link is for, named on its page.
	name := ""
	if i := s.planCur; i >= 0 && i < len(s.plans) {
		name = planName(s.plans[i])
	}
	switch args[1] {
	case "subscribe":
		s.payWhat = "Subscribe to " + name
	case "renew":
		s.payWhat = "Renew " + name
	default:
		s.payWhat = "Buy " + fmtIntString(args[2]) + " requests"
	}
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

// currentPlan is the index of the plan the account is on (-1: not
// known).
func (s *accountSection) currentPlan() int {
	key, name := strings.ToLower(s.h.hdr.planKey), strings.ToLower(s.h.hdr.plan)
	if key == "" && name == "" {
		return -1
	}
	// The plan's identifier first; then its name ("Startup", or "Startup
	// plan" against the plan named Startup).
	for i, p := range s.plans {
		if k := strings.ToLower(str(p["plan"])); k != "" && (k == key || k == name) {
			return i
		}
	}
	for i, p := range s.plans {
		n := strings.ToLower(str(p["name"]))
		if n != "" && (n == name || strings.HasPrefix(name, n+" ") || strings.HasPrefix(key, n+"_")) {
			return i
		}
	}
	return -1
}

// extraPrice is the price of 1,000 extra requests on the account's plan,
// in cents (0 when not known).
func (s *accountSection) extraPrice() float64 {
	if i := s.currentPlan(); i >= 0 {
		return num(s.plans[i]["extra_price_cents_per_1000"])
	}
	return 0
}

// priceLine says what 1,000 extra requests cost, before any link.
func (s *accountSection) priceLine() string {
	if c := s.extraPrice(); c > 0 {
		return fmt.Sprintf("Extra requests cost %s per 1,000 on your plan.", money(c))
	}
	lo, hi := 0.0, 0.0
	for _, p := range s.plans {
		if c := num(p["extra_price_cents_per_1000"]); c > 0 {
			if lo == 0 || c < lo {
				lo = c
			}
			hi = max(hi, c)
		}
	}
	switch {
	case lo == 0:
		return "The price is shown on the payment page before you pay."
	case lo == hi:
		return fmt.Sprintf("Extra requests cost %s per 1,000.", money(lo))
	}
	return fmt.Sprintf("Extra requests cost %s to %s per 1,000, depending on your plan.", money(lo), money(hi))
}

// canSignOut reports whether the profile has anything to sign out of.
func (s *accountSection) canSignOut() bool { return !s.h.signedOut || s.loggedIn }

func (s *accountSection) askSignOut() tea.Cmd {
	cur := "default"
	if s.h.a.Profile != nil {
		cur = s.h.a.Profile.Name
	}
	return s.h.askFirst(&confirmState{
		owner:    "account",
		question: fmt.Sprintf("Sign out of profile %s?", cur),
		action:   "Sign out",
		notes:    []string{"Its saved sign-in and API token are removed from this computer. audd cannot recognize music with this profile until you sign in or set a token again."},
		command:  "audd auth logout",
		cancel:   "Cancelled. Sign-in unchanged.",
		yes: func() tea.Cmd {
			return s.prof.start(runReq{args: []string{"auth", "logout"}, tag: "logout"})
		},
	})
}

func (s *accountSection) profilesKey(ks string) tea.Cmd {
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
	case "l":
		if s.canSignOut() {
			return s.askSignOut()
		}
		return s.h.setFlash("This profile is signed out already")
	}
	return nil
}

// atTop reports whether the page shows nothing opened on it (a form, a
// question, a link, a sign-in step), so the arrows walk the pages.
func (s *accountSection) atTop() bool {
	if s.needsLogin() {
		return s.signin.step == ""
	}
	if s.capturing() {
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

// leftExits: left goes back, except inside typed text.
func (s *accountSection) leftExits() bool {
	switch {
	case s.needsLogin():
		return s.signin.leftExits()
	case s.page == 1:
		return s.usage.leftExits()
	case s.page == 2 && s.buying:
		return s.buyIn.leftExits()
	case s.page == 3 && s.tokPasting:
		return s.tokForm.leftExits()
	}
	return !s.capturing()
}

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
	bar := ""
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
		label := n
		if n == cur {
			label += st.Dim.Render("  (in use)")
		}
		b.WriteString(s.h.listRow(label, i == s.profCur) + "\n")
	}
	b.WriteString("\n" + styleLines(st.Dim, output.Wrap("Enter switches to a profile. To add one, sign in with it: audd login --profile NAME.", w)))
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
		b.WriteString(styleLines(st.Bold, output.Wrap(s.payWhat, w)) + "\n\n")
		b.WriteString(output.Wrap("Open this link to review and pay (nothing is charged until you confirm there):", w) + "\n\n  ")
		b.WriteString(st.Accent.Render(strings.Join(hardWrap([]string{s.payLink}, max(10, w-2)), "\n  ")) + "\n\n")
		b.WriteString(styleLines(st.Dim, output.Wrap("enter or o opens it in the browser, c copies it, esc closes it.", w)))
		return b.String()
	}
	if s.buying {
		b.WriteString(st.Bold.Render("Buy extra requests") + "\n\n")
		b.WriteString(s.priceLine() + "\n\n")
		b.WriteString(s.buyIn.view(w, st, s.h.color))
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
	cur := s.currentPlan()
	for i, p := range s.plans {
		// Columns line up: name, price, requests.
		line := fmt.Sprintf("%-9s %5s/mo  %4s requests", planName(p),
			shortMoney(num(p["monthly_price_cents"])), shortCount(int(num(p["included_requests"]))))
		act := "Subscribe"
		if i == cur {
			line += "  (your plan)"
			act = "Renew"
		}
		// The row's button: enter presses it.
		btn := st.Dim.Render("[ " + act + " ]")
		if i == s.planCur && s.h.pageHasKeys() && !s.h.btnActive {
			btn = "[>" + act + "<]"
			if s.h.color {
				btn = st.Bold.Reverse(true).Render(" " + act + " ")
			}
		}
		line += "  " + btn
		b.WriteString(truncate(s.h.listRow(line, i == s.planCur), w) + "\n")
	}
	b.WriteString("\n" + styleLines(st.Dim, output.Wrap("Enter gets a payment link for the selected plan (on your plan: renews it). Payment links never charge anything: you review and pay in the browser.", w)))
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
	if s.tokPasting {
		b.WriteString("\n" + st.Bold.Render("Paste a different token") + "\n\n" + s.tokForm.view(w, st, s.h.color))
		return b.String()
	}
	b.WriteString("\n" + styleLines(st.Dim, output.Wrap("New token makes a new one: the current token stops working everywhere it is used.", w)))
	return b.String()
}

func (s *accountSection) keys() []keyHelp {
	if s.needsLogin() {
		return s.signin.keys()
	}
	switch s.page {
	case 0:
		return []keyHelp{{"↑/↓", "scroll"}}
	case 1:
		return []keyHelp{{"↑/↓", "scroll"}, {"enter", "billing"}}
	case 2:
		switch {
		case s.pay.running && s.pay.pending != nil:
			return s.signin.keysFor(s.pay)
		case s.buying:
			return s.buyIn.rowKeys("get the link")
		case s.payLink != "":
			return []keyHelp{{"esc", "close"}}
		case s.showHist:
			return []keyHelp{{"↑/↓", "scroll"}}
		}
		act := "subscribe"
		if s.planCur == s.currentPlan() {
			act = "renew"
		}
		return []keyHelp{{"↑/↓", "plan"}, {"enter", act}}
	case 3:
		if s.tokPasting {
			return s.tokForm.rowKeys("save the token")
		}
		return nil
	}
	return []keyHelp{{"↑/↓", "profile"}, {"enter", "switch to it"}}
}

// actions are the buttons of each page.
func (s *accountSection) actions() []button {
	if s.needsLogin() {
		return nil
	}
	refresh := func(fn func() tea.Cmd) button {
		return button{label: "Refresh", key: "r", do: func() tea.Cmd { return tea.Batch(fn(), s.h.setFlash("Refreshed")) }}
	}
	switch s.page {
	case 0:
		return []button{refresh(func() tea.Cmd { return s.openPage(0) })}
	case 1:
		return append([]button{{label: "Billing", key: "", do: func() tea.Cmd { return s.h.selectTab(2) }}}, s.usage.actions()...)
	case 2:
		switch {
		case s.pay.running || s.buying:
			return nil
		case s.payLink != "":
			link := s.payLink
			return []button{
				{label: "Open in browser", key: "o", do: func() tea.Cmd {
					return s.h.openLink(link)
				}},
				{label: "Copy link", key: "c", do: func() tea.Cmd { return s.h.copy(link, "the payment link") }},
				{label: "Close", key: "", do: func() tea.Cmd { s.payLink, s.pay.res = "", nil; return nil }},
			}
		case s.showHist:
			return []button{{label: "Plans", key: "", do: func() tea.Cmd { s.showHist = false; return nil }}}
		}
		return []button{
			{label: "Buy requests", key: "", do: func() tea.Cmd { s.buying = true; s.buyIn.focusName("requests"); return nil }},
			{label: "Payments", key: "", do: func() tea.Cmd {
				s.showHist = true
				if s.history.res == nil {
					return s.history.start(runReq{args: []string{"billing", "history"}, format: "table"})
				}
				return nil
			}},
			refresh(func() tea.Cmd { s.billing.res, s.owed.res = nil, nil; return s.openPage(2) }),
		}
	case 3:
		if s.tokPasting || s.token.pending != nil {
			return nil
		}
		show := button{label: "Show", key: "", token: true, do: func() tea.Cmd { return s.showToken(true) }}
		if s.revealed {
			show = button{label: "Hide", key: "", token: true, do: func() tea.Cmd { return s.showToken(false) }}
		}
		return []button{show,
			{label: "Copy", key: "c", token: true, do: func() tea.Cmd {
				return s.token.start(runReq{args: []string{"token", "show", "--reveal"}, tag: "copy"})
			}},
			{label: "New token…", key: "", token: true, do: func() tea.Cmd {
				return s.token.start(runReq{args: []string{"token", "rotate"}, typed: "rotate", tag: "rotate"})
			}},
			{label: "Paste a different token…", key: "", do: func() tea.Cmd {
				s.tokPasting = true
				s.tokForm.focusName("token")
				s.tokForm.resume()
				return nil
			}},
		}
	case 4:
		if s.canSignOut() {
			return []button{{label: "Sign out", key: "", do: s.askSignOut}}
		}
	}
	return nil
}

// askToken asks before replacing the API token with the one pasted.
func (s *accountSection) askToken() tea.Cmd {
	v := s.tokForm.value("token")
	old := "not set"
	if s.tokView != nil {
		old = str(s.tokView["token"])
	}
	q := "Replace the API token of profile " + s.h.profileName() + "?"
	if why := suspiciousToken(v); why != "" {
		q = why + " Save it anyway?"
	}
	return s.h.askFirst(&confirmState{
		owner:    "account",
		question: q,
		notes:    []string{"Now: " + old, "New: " + config.MaskToken(v), "Commands in this profile use the new token from now on."},
		command:  "audd config set token -",
		action:   "Replace the token",
		cancel:   "Not saved · esc closes the form",
		onCancel: func() {
			// Back in the field, typing, the token as it was.
			s.tokForm.focusName("token")
			s.tokForm.resume()
			s.tokForm.get("token").input.CursorEnd()
		},
		yes: func() tea.Cmd {
			return s.tokSet.start(runReq{args: []string{"config", "set", "token", "-"}, stdin: v})
		},
	})
}

// downExits: ↓ at the end of the page goes to its buttons.
func (s *accountSection) downExits() bool {
	switch s.page {
	case 2:
		return !s.buying && (s.showHist || s.payLink != "" || s.planCur >= len(s.plans)-1)
	case 3:
		return !s.tokPasting
	case 4:
		return s.profCur >= len(s.profiles())-1
	}
	return true
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
				return ""
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
		return s.tokPasting && s.tokForm.capturing() || s.token.capturing()
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
			if !s.buyIn.leaveField() {
				s.buying = false
			}
			return true
		case s.payLink != "":
			s.payLink = ""
			return true
		case s.showHist:
			s.showHist = false
			return true
		}
	case 3:
		if s.tokPasting {
			if !s.tokForm.leaveField() {
				s.tokPasting = false
			}
			return true
		}
		if s.token.back() {
			return true
		}
	}
	return false
}

// shortMoney is a price without cents when it has none: $450, $4.50.
func shortMoney(cents float64) string {
	if int(cents)%100 == 0 {
		return fmt.Sprintf("$%s", fmtInt(int(cents)/100))
	}
	return money(cents)
}

// shortCount is a count in thousands when it is round: 100k.
func shortCount(n int) string {
	if n >= 1000 && n%1000 == 0 {
		return fmtInt(n/1000) + "k"
	}
	return fmtInt(n)
}

// fmtIntString formats a number given as text ("10000" → "10,000").
func fmtIntString(v string) string {
	if n, err := strconv.Atoi(v); err == nil {
		return fmtInt(n)
	}
	return v
}

// planName is a plan's name, or its key in words when it has none
// ("indie_plan_outside" → "Indie plan outside").
func planName(p map[string]any) string {
	if n := str(p["name"]); n != "" {
		return n
	}
	return plainName(str(p["plan"]))
}
