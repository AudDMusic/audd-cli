package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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

var signinChoices = []struct{ title, help string }{
	{"Sign in", "Sign in to your AudD account in the browser (or with a code on another device). audd saves your API token."},
	{"Paste an API token", "Use the API token from your dashboard at https://dashboard.audd.io."},
	{"Try with the test token", "The public test token: 10 requests a day, standard endpoint only. Nothing is saved."},
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
		s.step = ""
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
	switch s.choice {
	case 0:
		return displayCommand(s.loginArgs())
	case 1:
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
		return []keyHelp{{"esc", "back"}}
	case "paste":
		return []keyHelp{{"enter", "save"}, {"esc", "back"}}
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
	if s.heading != "" {
		return 1
	}
	return len(signinChoices)
}

func (s *signinSection) update(msg tea.Msg) tea.Cmd {
	if mine, done := s.panel.handle(msg); mine {
		if done {
			if s.panel.res.ok() {
				switch s.panel.args[0] {
				case "login", "config":
					s.step = ""
					return s.h.signedIn(false)
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
		if k.String() == "left" && !s.panel.capturing() {
			s.back()
			return nil
		}
		return s.panel.key(k, s.h.h)
	case "paste":
		if k.String() == "left" && s.paste.leftExits() {
			s.step = ""
			return nil
		}
		act, cmd := s.paste.update(k)
		if act == "save" {
			v := s.paste.value("token")
			if v == "" {
				s.paste.err = "Paste the token first."
				return nil
			}
			s.paste.err = ""
			return s.panel.start(runReq{args: []string{"config", "set", "token", "-"}, stdin: v})
		}
		return cmd
	}
	switch k.String() {
	case "down", "j", "tab":
		s.choice = (s.choice + 1) % s.choices()
	case "up", "k", "shift+tab":
		s.choice = (s.choice + s.choices() - 1) % s.choices()
	case "enter":
		switch s.choice {
		case 0:
			s.step = "login"
			return s.panel.start(runReq{args: s.loginArgs(), format: "jsonl", stream: true})
		case 1:
			s.step = "paste"
			s.paste.focusName("token")
			return nil
		case 2:
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
	if s.heading != "" {
		b.WriteString(st.Bold.Render(s.heading) + "\n\n")
		b.WriteString(output.Wrap("Your account, usage, and billing need a sign-in to your AudD account.", w) + "\n\n")
	} else {
		b.WriteString(st.Bold.Render("Welcome to AudD") + "\n\n")
		b.WriteString(output.Wrap("audd recognizes music in files, URLs, and from the microphone, monitors streams, and manages your account. To start, choose how audd gets your API token:", w) + "\n\n")
	}
	for i, c := range signinChoices[:s.choices()] {
		mark := "  "
		title := c.title
		if i == s.choice {
			mark = "› "
			title = st.Bold.Render(title)
		}
		item := mark + title + "\n" + styleLines(st.Dim, indent(output.Wrap(c.help, max(10, w-4)), "    "))
		b.WriteString(s.h.mark(item, func() tea.Cmd {
			if s.choice == i {
				return s.h.sendKey(tea.KeyMsg{Type: tea.KeyEnter})
			}
			s.choice = i
			return nil
		}) + "\n\n")
	}
	if s.heading == "" {
		b.WriteString(styleLines(st.Dim, output.Wrap("You can also look around first: the sections on the left work once a token is set.", w)))
	}
	return b.String()
}

// leftExits: left leaves from the choices; inside a step it goes back
// to them.
func (s *signinSection) leftExits() bool { return s.step == "" }

func indent(s, pre string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pre + lines[i]
	}
	return strings.Join(lines, "\n")
}
