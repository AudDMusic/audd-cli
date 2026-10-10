package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func (m *explorer) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.purpose != inputNone {
		return m.inputKey(k)
	}
	if m.confirm != "" {
		m.confirm = ""
		if s == "y" || s == "Y" {
			return m, m.onYes()
		}
		return m, m.setFlash("Cancelled")
	}
	if m.exportAs {
		m.exportAs = false
		switch s {
		case "c":
			return m, m.export("csv")
		case "j":
			return m, m.export("json")
		}
		return m, m.setFlash("Export cancelled")
	}
	if m.help {
		m.help = false
		if s == "q" || s == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	l := m.top()
	if m.embedded && (s == "left" || s == "right") {
		// Embedded, left and right walk the tabs without wrapping, and
		// left first closes details and drill-downs.
		if s == "left" && l != nil && (l.detail || len(m.tabs[m.tab]) > 1) {
			s = "esc"
		} else {
			d := 1
			if s == "left" {
				d = -1
			}
			if next := m.nextTab(d); (d > 0) == (next > m.tab) && next != m.tab {
				return m, m.switchTab(next)
			}
			return m, nil
		}
	}
	switch s {
	case "q", "ctrl+c":
		if m.embedded {
			return m, nil
		}
		return m, tea.Quit
	case "?":
		if m.embedded {
			return m, nil
		}
		m.help = true
		return m, nil
	case "tab", "right":
		return m, m.switchTab(m.nextTab(1))
	case "shift+tab", "left":
		return m, m.switchTab(m.nextTab(-1))
	case "1", "2", "3", "4":
		if m.embedded {
			return m, nil
		}
		return m, m.switchTab(tabID(s[0] - '1'))
	}
	if m.tab == tabUsage {
		switch s {
		case "r":
			return m, m.load(tabUsage, nil)
		case "e":
			m.exportAs = true
		}
		return m, nil
	}
	if l == nil {
		return m, nil
	}
	if l.detail {
		switch s {
		case "esc", "backspace", "enter":
			l.detail = false
		case "down", "j":
			l.dscroll++
		case "up", "k":
			if l.dscroll > 0 {
				l.dscroll--
			}
		case "o", "c", "i", "u":
			return m, m.rowAction(s, l.selected())
		}
		return m, nil
	}
	n := len(l.visible())
	switch s {
	case "down", "j":
		if l.cursor < n-1 {
			l.cursor++
		}
	case "up", "k":
		if l.cursor > 0 {
			l.cursor--
		}
	case "pgdown", " ":
		l.cursor = min(n-1, l.cursor+m.listHeight())
		if l.cursor < 0 {
			l.cursor = 0
		}
	case "pgup":
		l.cursor = max(0, l.cursor-m.listHeight())
	case "home", "g":
		l.cursor = 0
	case "end", "G":
		l.cursor = max(0, n-1)
	case "/":
		return m, m.startInput(inputFilter, "Filter: ", l.filter)
	case "esc", "backspace":
		switch {
		case l.filter != "":
			l.filter, l.cursor = "", 0
		case len(m.tabs[m.tab]) > 1:
			m.tabs[m.tab] = m.tabs[m.tab][:len(m.tabs[m.tab])-1]
		}
	case "enter":
		return m, m.enter(l)
	case "o", "c", "i", "u":
		if s == "u" && l.kind == "streams" {
			if r := l.selected(); r != nil {
				m.pending = map[string]string{"id": r.key}
				return m, m.startInput(inputSetURL, fmt.Sprintf("New URL for stream %s: ", r.key), "")
			}
			return m, nil
		}
		return m, m.rowAction(s, l.selected())
	case "e":
		m.exportAs = true
		return m, nil
	case "r":
		if l.kind == "jobs" || l.kind == "items" {
			return m, m.resumeJob(l, false)
		}
		return m, m.load(m.tab, l)
	case "R":
		if l.kind == "jobs" || l.kind == "items" {
			return m, m.resumeJob(l, true)
		}
	case "a":
		if l.kind == "streams" {
			m.pending = map[string]string{}
			return m, m.startInput(inputAddURL, "Stream URL to add: ", "")
		}
	case "d":
		if l.kind == "streams" {
			if r := l.selected(); r != nil && m.d.RemoveStream != nil {
				id, _ := strconv.Atoi(r.key)
				m.confirm = fmt.Sprintf("Remove stream %d? Its stored results stay on this computer. [y/N]", id)
				m.onYes = func() tea.Cmd {
					return m.streamAction(func(ctx context.Context) error { return m.d.RemoveStream(ctx, id) }, fmt.Sprintf("Removed stream %d", id))
				}
			}
		}
	}
	return m, nil
}

func (m *explorer) enter(l *level) tea.Cmd {
	r := l.selected()
	if r == nil {
		return nil
	}
	switch l.kind {
	case "jobs":
		nl := &level{kind: "items", arg: r.key, title: "Job " + r.key, cols: jobItemCols, loading: true}
		m.tabs[m.tab] = append(m.tabs[m.tab], nl)
		return m.load(m.tab, nl)
	case "streams":
		nl := &level{kind: "plays", arg: r.key, title: "Stream " + r.key + " · recent plays", cols: playCols, loading: true}
		m.tabs[m.tab] = append(m.tabs[m.tab], nl)
		return m.load(m.tab, nl)
	}
	if r.detail != nil {
		l.detail, l.dscroll = true, 0
		if r.view != nil {
			return m.arts.want(m.ctx, *r.view)
		}
	}
	return nil
}

func (m *explorer) rowAction(s string, r *row) tea.Cmd {
	if r == nil {
		return nil
	}
	var val, what string
	switch s {
	case "o":
		if r.link == "" {
			return m.setFlash("No link to open")
		}
		if err := openURL(r.link); err != nil {
			return m.setFlash("Could not open the browser: " + err.Error())
		}
		return m.setFlash("Opened " + r.link)
	case "c":
		val, what = r.link, "link"
	case "i":
		val, what = r.copies["i"], "ISRC"
	case "u":
		val, what = r.copies["u"], "UPC"
	}
	if val == "" {
		return m.setFlash("No " + what + " to copy")
	}
	m.osc = oscCopy(val)
	return tea.Batch(m.setFlash("Copied "+what+" "+val),
		tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return clearCopyMsg{} }))
}

func (m *explorer) resumeJob(l *level, retry bool) tea.Cmd {
	id := l.arg
	if l.kind == "jobs" {
		r := l.selected()
		if r == nil {
			return nil
		}
		id = r.key
	}
	if m.onResume != nil {
		return m.onResume(id, retry)
	}
	a, ctx := m.a, m.ctx
	// The command's context: Ctrl-C stops the resumed job the way it stops
	// audd jobs resume.
	m.after = func() error {
		_, err := app.RunBatch(ctx, a, app.BatchOptions{ResumeID: id, RetryFailed: retry})
		return err
	}
	return tea.Quit
}

func (m *explorer) streamAction(f func(ctx context.Context) error, done string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		if err := f(ctx); err != nil {
			return actionMsg{flash: "Error: " + output.AsError(err).Message}
		}
		return actionMsg{flash: done, reload: true, tab: tabStreams}
	}
}

func (m *explorer) startInput(p inputPurpose, prompt, value string) tea.Cmd {
	m.purpose = p
	m.input.Prompt = prompt
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m.input.Focus()
}

func (m *explorer) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "ctrl+c":
		if m.purpose == inputFilter {
			if l := m.top(); l != nil {
				l.filter = ""
			}
		}
		m.purpose = inputNone
		m.input.Blur()
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.input.Value())
		p := m.purpose
		m.purpose = inputNone
		m.input.Blur()
		return m, m.submit(p, val)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.purpose == inputFilter {
		if l := m.top(); l != nil {
			l.filter = m.input.Value()
			l.cursor = 0
		}
	}
	return m, cmd
}

func (m *explorer) submit(p inputPurpose, val string) tea.Cmd {
	switch p {
	case inputFilter:
		if l := m.top(); l != nil {
			l.filter, l.cursor = val, 0
		}
	case inputAddURL:
		if val == "" {
			return m.setFlash("Cancelled")
		}
		m.pending["url"] = val
		return m.startInput(inputAddID, "Radio ID for it (a number you choose): ", "")
	case inputAddID:
		id, err := strconv.Atoi(val)
		if err != nil || id <= 0 {
			return m.setFlash("The radio ID must be a positive number")
		}
		if m.d.AddStream == nil {
			return m.setFlash("Adding streams is not available in this build")
		}
		u := m.pending["url"]
		return m.streamAction(func(ctx context.Context) error { return m.d.AddStream(ctx, u, id) }, fmt.Sprintf("Added stream %d", id))
	case inputSetURL:
		id, _ := strconv.Atoi(m.pending["id"])
		if val == "" || m.d.SetStreamURL == nil {
			return m.setFlash("Cancelled")
		}
		return m.streamAction(func(ctx context.Context) error { return m.d.SetStreamURL(ctx, id, val) }, fmt.Sprintf("Updated the URL of stream %d", id))
	}
	return nil
}
