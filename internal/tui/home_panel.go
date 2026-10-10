package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/output"
)

func parseDurationLoose(s string) (time.Duration, error) { return time.ParseDuration(s) }

// cmdPanel runs a command and shows what it printed: its text output (or
// a rendering of its JSON), its notes, and its error with the hint.
type cmdPanel struct {
	h       *home
	run     *run
	args    []string
	res     *runResult
	running bool
	lines   []string       // streamed stdout lines
	pending map[string]any // a sign-in the run waits for
	scroll  scrollView
	// render turns a finished run into text for width w; nil shows
	// stdout as is.
	render func(res runResult, w int) string
	// live renders streamed lines while running; nil shows a spinner line.
	live func(lines []string, w int) string
	// empty is shown before the first run.
	empty string

	// While a sign-in is pending in the browser: the redirect address
	// pasted from another machine (p), finished with auth login --complete.
	pasting  bool
	pasteIn  textinput.Model
	complete *run
}

// capturing reports whether the paste field is open.
func (p *cmdPanel) capturing() bool { return p.pasting }

// back closes the paste field.
func (p *cmdPanel) back() bool {
	if p.pasting {
		p.pasting = false
		return true
	}
	return false
}

func newPanel(h *home) *cmdPanel { return &cmdPanel{h: h} }

// start runs req, replacing what the panel shows.
func (p *cmdPanel) start(req runReq) tea.Cmd {
	if p.running && p.run != nil {
		p.h.cancelRun(p.run)
	}
	p.args = req.args
	p.res = nil
	p.lines = nil
	p.pending = nil
	p.running = true
	p.scroll.off = 0
	cmd := p.h.start(req)
	p.run = p.h.lastRun()
	return cmd
}

// stop cancels the run, as Ctrl-C would.
func (p *cmdPanel) stop() {
	if p.running {
		p.h.cancelRun(p.run)
	}
}

// handle takes the panel's run events; it reports whether msg was one
// and whether the run finished with it.
func (p *cmdPanel) handle(msg tea.Msg) (mine, done bool) {
	switch m := msg.(type) {
	case runLineMsg:
		if m.run != p.run {
			return false, false
		}
		if m.pending != nil {
			p.pending = m.pending
			return true, false
		}
		p.lines = append(p.lines, m.line)
		return true, false
	case runDoneMsg:
		if p.complete != nil && m.run == p.complete {
			p.complete = nil
			if m.res.err != nil {
				p.h.flash = "Could not finish the sign-in: " + m.res.err.Message
			}
			return true, false
		}
		if m.run != p.run {
			return false, false
		}
		p.running = false
		res := m.res
		p.res = &res
		return true, true
	}
	return false, false
}

func (p *cmdPanel) command() string {
	if p.args == nil {
		return ""
	}
	return displayCommand(p.args)
}

// key scrolls the output; enter on an error whose hint is a command
// opens it in the palette.
func (p *cmdPanel) key(k tea.KeyMsg, h int) tea.Cmd {
	s := k.String()
	if p.pasting {
		switch s {
		case "esc":
			p.pasting = false
			return nil
		case "enter":
			p.pasting = false
			v := strings.TrimSpace(p.pasteIn.Value())
			if v == "" {
				return nil
			}
			cmd := p.h.start(runReq{args: []string{"auth", "login", "--complete", v}})
			p.complete = p.h.lastRun()
			return cmd
		}
		var cmd tea.Cmd
		p.pasteIn, cmd = p.pasteIn.Update(k)
		return cmd
	}
	if p.running && p.pending != nil {
		u := pendingURL(p.pending)
		switch s {
		case "o":
			if err := openURL(u); err != nil {
				return p.h.setFlash("Could not open the browser: " + err.Error())
			}
			return p.h.setFlash("Opened " + u)
		case "c":
			return p.h.copy(u, "the sign-in address")
		case "p":
			if p.pending["method"] != "device" {
				p.pasting = true
				p.pasteIn = newInput()
				p.pasteIn.Prompt = "Address: "
				return p.pasteIn.Focus()
			}
		}
		return nil
	}
	if s == "enter" && p.res != nil && p.res.err != nil && strings.HasPrefix(p.res.err.Hint, "audd ") {
		return p.h.openPaletteLine(p.res.err.Hint)
	}
	p.scroll.key(s, h)
	return nil
}

func (p *cmdPanel) text(w int) string {
	st := p.h.st
	if p.running && p.pending != nil {
		t := pendingText(st, p.pending, w)
		if p.pasting {
			t += "\n\n" + p.pasteIn.View()
		}
		return t
	}
	if p.running {
		if p.live != nil {
			return p.live(p.lines, w)
		}
		out := st.Dim.Render("Running " + displayCommand(p.args) + " …")
		if len(p.lines) > 0 {
			out += "\n\n" + strings.Join(p.lines, "\n")
		}
		return out
	}
	if p.res == nil {
		return p.empty
	}
	return p.resultText(*p.res, w)
}

func (p *cmdPanel) resultText(res runResult, w int) string {
	st := p.h.st
	var b strings.Builder
	if res.err != nil {
		b.WriteString(errorText(st, res.err, w))
		if len(res.notes) > 0 {
			b.WriteString("\n" + st.Dim.Render(output.Wrap(strings.Join(res.notes, "\n"), w)))
		}
		return b.String()
	}
	if p.render != nil {
		b.WriteString(p.render(res, w))
	} else {
		b.WriteString(strings.TrimRight(res.stdout, "\n"))
	}
	if len(res.notes) > 0 {
		b.WriteString("\n\n" + st.Dim.Render(output.Wrap(strings.Join(res.notes, "\n"), w)))
	}
	return b.String()
}

// errorText is an error as the command line shows it, for a pane.
func errorText(st output.Styles, e *runError, w int) string {
	var b strings.Builder
	b.WriteString(st.Warn.Render(output.Wrap("Error: "+e.Message, w)))
	if e.Hint != "" {
		b.WriteString("\n" + output.Wrap("Try: "+e.Hint, w))
		if strings.HasPrefix(e.Hint, "audd ") {
			b.WriteString("\n" + st.Dim.Render("Press enter to open it."))
		}
	}
	return b.String()
}

func (p *cmdPanel) view(w, h int) string {
	p.scroll.lines = strings.Split(p.text(w), "\n")
	return p.scroll.view(w, h)
}

// lastRun is the run start created most recently.
func (h *home) lastRun() *run { return h.last }

// pendingText tells how to approve a sign-in the run is waiting for.
func pendingText(st output.Styles, doc map[string]any, w int) string {
	var b strings.Builder
	if doc["method"] == "device" {
		uri := str(doc["verification_uri_complete"])
		if uri == "" {
			uri = str(doc["verification_uri"])
		}
		b.WriteString("To sign in, open this page on any device:\n\n  " + st.Accent.Render(uri) + "\n\n")
		b.WriteString("Check that it shows this code, then approve:\n\n    " + st.Bold.Render(spaced(str(doc["user_code"]))) + "\n\n")
		b.WriteString(st.Dim.Render(output.Wrap("Only approve a sign-in you started yourself. o opens the page, c copies it.", w)))
		return b.String()
	}
	b.WriteString("Sign in to AudD in your browser. If it did not open, visit:\n\n  " + st.Accent.Render(str(doc["url"])) + "\n\n")
	b.WriteString(st.Dim.Render(output.Wrap("Waiting for the browser. o opens the page, c copies it. If the browser is on another machine, approve there and paste the address it was sent to with p.", w)))
	return b.String()
}

// pendingURL is the page to open for a sign-in.
func pendingURL(doc map[string]any) string {
	for _, k := range []string{"verification_uri_complete", "verification_uri", "url"} {
		if s := str(doc[k]); s != "" {
			return s
		}
	}
	return ""
}

func spaced(code string) string { return strings.Join(strings.Split(code, ""), " ") }
