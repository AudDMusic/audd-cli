package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *npModel) setFlash(s string) tea.Cmd {
	m.flash = s
	m.flashN++
	id := m.flashN
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return clearFlashMsg{id: id} })
}

func (m *npModel) current() *stationData {
	if len(m.stations) == 0 {
		return nil
	}
	return &m.stations[m.sel]
}

func (m *npModel) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.help {
		m.help = false
		if k.String() == "q" || k.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	switch k.String() {
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
	case "right", "l", "tab":
		if len(m.stations) > 0 {
			m.sel = (m.sel + 1) % len(m.stations)
		}
	case "left", "shift+tab":
		if len(m.stations) > 0 {
			m.sel = (m.sel + len(m.stations) - 1) % len(m.stations)
		}
	case "down", "j":
		if !m.single() {
			if c := m.gridCols(); m.sel+c < len(m.stations) {
				m.sel += c
			}
		}
	case "up", "k":
		if !m.single() {
			if c := m.gridCols(); m.sel-c >= 0 {
				m.sel -= c
			}
		}
	case "enter":
		if len(m.stations) > 1 {
			m.zoom = !m.zoom
		}
	case "esc":
		switch {
		case m.history:
			m.history = false
		case m.zoom:
			m.zoom = false
		}
	case "h":
		m.history = !m.history
		if m.history && len(m.stations) > 1 {
			m.zoom = true
		}
	case "r":
		return m, m.poll(true)
	case "o":
		if s := m.current(); s != nil && len(s.Plays) > 0 && s.Plays[0].SongLink != "" {
			if err := openURL(s.Plays[0].SongLink); err != nil {
				return m, m.setFlash("Could not open the browser: " + err.Error())
			}
			return m, m.setFlash("Opened " + s.Plays[0].SongLink)
		}
		return m, m.setFlash("No link to open")
	case "c":
		if s := m.current(); s != nil && len(s.Plays) > 0 && s.Plays[0].SongLink != "" {
			m.osc = oscCopy(s.Plays[0].SongLink)
			return m, tea.Batch(m.setFlash("Copied "+s.Plays[0].SongLink),
				tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return clearCopyMsg{} }))
		}
		return m, m.setFlash("No link to copy")
	}
	return m, nil
}

func (m *npModel) single() bool { return len(m.stations) <= 1 || m.zoom }

func (m *npModel) gridCols() int {
	c := m.w / 44
	if c < 1 {
		c = 1
	}
	if c > len(m.stations) {
		c = len(m.stations)
	}
	return c
}
