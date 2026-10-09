package output

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Styles are the shared text styles for human output.
type Styles struct {
	Title    lipgloss.Style // headings, song titles
	Bold     lipgloss.Style
	Dim      lipgloss.Style // secondary text, labels
	Accent   lipgloss.Style // highlights, links
	OK       lipgloss.Style
	Warn     lipgloss.Style
	Err      lipgloss.Style
	Key      lipgloss.Style // key/label column
	Renderer *lipgloss.Renderer
}

// Styles returns lipgloss styles that honor --no-color, NO_COLOR, and FORCE_COLOR.
func (p *Printer) Styles() Styles {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.styles != nil {
		return *p.styles
	}
	r := lipgloss.NewRenderer(p.stdout)
	switch {
	case p.opts.NoColor:
		r.SetColorProfile(termenv.Ascii)
	case !p.opts.StdoutTTY:
		r.SetColorProfile(termenv.ANSI256) // FORCE_COLOR without a terminal
	}
	s := Styles{
		Title:    r.NewStyle().Bold(true),
		Bold:     r.NewStyle().Bold(true),
		Dim:      r.NewStyle().Faint(true),
		Accent:   r.NewStyle().Foreground(lipgloss.Color("39")),
		OK:       r.NewStyle().Foreground(lipgloss.Color("42")),
		Warn:     r.NewStyle().Foreground(lipgloss.Color("214")),
		Err:      r.NewStyle().Foreground(lipgloss.Color("196")),
		Key:      r.NewStyle().Faint(true),
		Renderer: r,
	}
	if p.opts.NoColor {
		plain := r.NewStyle()
		s.Title, s.Bold, s.Dim, s.Accent, s.OK, s.Warn, s.Err, s.Key = plain, plain, plain, plain, plain, plain, plain, plain
	}
	p.styles = &s
	return s
}
