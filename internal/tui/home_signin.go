package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/oauth"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// signinMethod is the sign-in method for this machine: the browser on a
// desktop, a code over SSH or in a container (as audd login picks on a
// terminal). Tests replace it.
var signinMethod = func() string {
	return string(oauth.DetectEnvironment(true, true).Method())
}

// signinSection is the signed-out screen: sign in, paste an API token,
// or try the public test token. The Account section shows it too, as
// "sign in to see this".
type signinSection struct {
	h *home
	// heading replaces the welcome text (Account: "Sign in to see this").
	heading string
	choice  int
	step    string // "", "login", "paste"
	paste   *form
	panel   *cmdPanel
}

func newSigninSection(h *home) section { return newSignin(h, "") }

func newSignin(h *home, heading string) *signinSection {
	s := &signinSection{h: h, heading: heading, panel: newPanel(h)}
	tok := &field{kind: fText, name: "token", label: "API token", masked: true, input: newInput(),
		help: "From https://dashboard.audd.io. Saved like audd config set token."}
	s.paste = newForm(tok, buttonField("save", "Save"))
	s.paste.h = h
	return s
}

var signinChoices = []struct{ title, help, step string }{
	{"Sign in", "Sign in to your AudD account in the browser (or with a code on another device). audd saves your API token.", "login"},
	{"Paste an API token", "Use the API token from your dashboard at https://dashboard.audd.io.", "paste"},
	{"Try with the test token", "The public test token: 10 requests a day, standard endpoint only. Nothing is saved.", "test"},
}

// tokenSetChoices are Account's choices when an API token is set but
// the account is not signed in.
var tokenSetChoices = []struct{ title, help, step string }{
	{"Sign in to your account", "Sign in to your AudD account in the browser (or with a code on another device) to see your plan, usage, and billing.", "login"},
	{"Paste a different token", "Replace the API token of this profile with one from https://dashboard.audd.io.", "paste"},
}

// choiceList is what the screen offers.
func (s *signinSection) choiceList() []struct{ title, help, step string } {
	if s.heading != "" && !s.h.signedOut {
		return tokenSetChoices
	}
	return signinChoices
}

func (s *signinSection) title() string { return "Sign in" }
func (s *signinSection) init() tea.Cmd { return nil }

func (s *signinSection) capturing() bool {
	return s.step == "paste" && s.paste.capturing() || s.step == "login" && s.panel.capturing()
}

func (s *signinSection) back() bool {
	switch s.step {
	case "login":
		if s.panel.back() {
			return true
		}
		s.panel.stop()
		s.step = ""
		return true
	case "paste":
		if !s.paste.leaveField() {
			s.step = ""
		}
		return true
	}
	return false
}

func (s *signinSection) loginArgs() []string {
	if signinMethod() == string(oauth.MethodBrowser) {
		return []string{"login", "--browser"}
	}
	return []string{"login", "--device"}
}

func (s *signinSection) command() string {
	switch s.step {
	case "login":
		return displayCommand(s.loginArgs())
	case "paste":
		return "audd config set token -"
	}
	switch s.choiceList()[min(s.choice, s.choices()-1)].step {
	case "login":
		return displayCommand(s.loginArgs())
	case "paste":
		return "audd config set token -"
	}
	return "audd recognize song.mp3 --token test"
}

func (s *signinSection) keys() []keyHelp {
	switch s.step {
	case "login":
		if s.panel.running && s.panel.pending != nil {
			return s.keysFor(s.panel)
		}
		return nil
	case "paste":
		return s.paste.rowKeys("save")
	}
	return []keyHelp{{"↑/↓", "choose"}, {"enter", "go"}}
}

// keysFor are the keys while p waits for a sign-in to be approved.
func (s *signinSection) keysFor(p *cmdPanel) []keyHelp {
	k := []keyHelp{{"o", "open the page"}, {"c", "copy it"}}
	if p.pending["method"] != "device" {
		k = append(k, keyHelp{"p", "paste the address"})
	}
	return append(k, keyHelp{"esc", "cancel"})
}

// choices is how many choices the screen offers: the Account section
// only offers the sign-in (a token alone does not reach the account).
func (s *signinSection) choices() int {
	return len(s.choiceList())
}

func (s *signinSection) update(msg tea.Msg) tea.Cmd {
	if mine, done := s.panel.handle(msg); mine {
		if done {
			if s.panel.res.ok() {
				switch s.panel.args[0] {
				case "login":
					s.step = ""
					return s.h.signedIn(false)
				case "config":
					s.step = ""
					s.paste.get("token").setValue("")
					return tea.Batch(s.h.signedIn(false), s.h.setFlash("Token saved for profile "+s.h.profileName()))
				}
			}
			if s.step == "paste" && s.panel.res.err != nil {
				s.paste.err = s.panel.res.err.Message
			}
		}
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch s.step {
	case "login":
		return s.panel.key(k, s.h.h)
	case "paste":
		act, cmd := s.paste.update(k)
		if act == "save" {
			v := s.paste.value("token")
			if v == "" {
				s.paste.err = "Paste the token first."
				return nil
			}
			s.paste.err = ""
			save := func() tea.Cmd {
				return s.panel.start(runReq{args: []string{"config", "set", "token", "-"}, stdin: v})
			}
			if s.h.signedOut {
				return save()
			}
			// A token is set: replacing it is asked first.
			owner := "signin"
			if s.heading != "" {
				owner = "account"
			}
			return s.h.askFirst(&confirmState{
				owner:    owner,
				question: "Replace the API token of profile " + s.h.profileName() + "?",
				notes:    []string{"New: " + config.MaskToken(v), "Commands in this profile use the new token from now on."},
				command:  "audd config set token -",
				action:   "Replace the token",
				cancel:   "Cancelled. The token is unchanged.",
				yes:      save,
			})
		}
		return cmd
	}
	switch k.String() {
	case "down", "tab":
		s.choice = (s.choice + 1) % s.choices()
	case "up", "shift+tab":
		s.choice = (s.choice + s.choices() - 1) % s.choices()
	case "enter":
		switch s.choiceList()[min(s.choice, s.choices()-1)].step {
		case "login":
			s.step = "login"
			return s.panel.start(runReq{args: s.loginArgs(), format: "jsonl", stream: true})
		case "paste":
			s.step = "paste"
			s.paste.focusName("token")
			s.paste.resume()
			return nil
		case "test":
			return tea.Batch(s.h.signedIn(true), s.h.setFlash("Using the test token: 10 requests a day, standard endpoint only"))
		}
	}
	return nil
}

func (s *signinSection) view(w, h int) string {
	st := s.h.st
	var b strings.Builder
	switch s.step {
	case "login":
		b.WriteString(st.Bold.Render("Sign in") + "\n\n")
		if s.panel.res != nil && s.panel.res.err != nil {
			b.WriteString(errorText(st, s.panel.res.err, w) + "\n\n" + st.Dim.Render("Press esc to go back."))
			return b.String()
		}
		if s.panel.pending == nil {
			b.WriteString(st.Dim.Render("Starting the sign-in…"))
			return b.String()
		}
		b.WriteString(s.panel.view(w, h-2))
		return b.String()
	case "paste":
		b.WriteString(st.Bold.Render("Paste an API token") + "\n\n")
		b.WriteString(s.paste.view(w, st, s.h.color))
		return b.String()
	}
	switch {
	case s.heading != "" && !s.h.signedOut:
		b.WriteString(st.Bold.Render(s.heading) + "\n\n")
		b.WriteString(output.Wrap("An API token is set for profile "+s.h.profileName()+", so audd can recognize music. Your account, usage, and billing need a sign-in to your AudD account.", w) + "\n\n")
	case s.heading != "":
		b.WriteString(st.Bold.Render(s.heading) + "\n\n")
		b.WriteString(output.Wrap("Your account, usage, and billing need a sign-in to your AudD account. To only recognize music, an API token is enough:", w) + "\n\n")
	default:
		b.WriteString(st.Bold.Render("Welcome to AudD") + "\n\n")
		b.WriteString(output.Wrap("Recognize music in files, URLs, and from the microphone, and monitor streams. Choose how audd gets your API token:", w) + "\n\n")
	}
	for i, c := range s.choiceList() {
		item := s.h.listRow(c.title, i == s.choice) + "\n" + styleLines(st.Dim, indent(output.Wrap(c.help, max(10, w-4)), "    "))
		b.WriteString(s.h.mark(item, func() tea.Cmd {
			// A click opens the choice, as enter does.
			s.choice = i
			return s.h.sendKey(tea.KeyMsg{Type: tea.KeyEnter})
		}) + "\n\n")
	}
	if s.heading == "" {
		b.WriteString(styleLines(st.Dim, output.Wrap("You can also look around first: the sections on the left work once a token is set.", w)))
	}
	return b.String()
}

// leftExits: left goes back, from a step to the choices too, except
// inside typed text.
func (s *signinSection) leftExits() bool {
	switch s.step {
	case "login":
		return !s.panel.capturing()
	case "paste":
		return s.paste.leftExits()
	}
	return true
}

// crumbs: the step chosen.
func (s *signinSection) crumbs() []string {
	switch s.step {
	case "login":
		return []string{"Sign in to AudD"}
	case "paste":
		return []string{"Paste an API token"}
	}
	return nil
}

func indent(s, pre string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pre + lines[i]
	}
	return strings.Join(lines, "\n")
}
