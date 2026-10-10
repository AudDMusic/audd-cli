package tui

import (
	"strings"
	"time"

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
	lines   []string // streamed stdout lines
	scroll  scrollView
	// render turns a finished run into text for width w; nil shows
	// stdout as is.
	render func(res runResult, w int) string
	// live renders streamed lines while running; nil shows a spinner line.
	live func(lines []string, w int) string
	// empty is shown before the first run.
	empty string
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
		p.lines = append(p.lines, m.line)
		return true, false
	case runDoneMsg:
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
	if s == "enter" && p.res != nil && p.res.err != nil && strings.HasPrefix(p.res.err.Hint, "audd ") {
		return p.h.openPaletteLine(p.res.err.Hint)
	}
	p.scroll.key(s, h)
	return nil
}

func (p *cmdPanel) text(w int) string {
	st := p.h.st
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
