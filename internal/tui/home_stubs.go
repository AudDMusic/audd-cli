package tui

import tea "github.com/charmbracelet/bubbletea"

// stubSection is a section not built yet.
type stubSection struct {
	h    *home
	name string
}

func (s *stubSection) title() string          { return s.name }
func (s *stubSection) init() tea.Cmd          { return nil }
func (s *stubSection) update(tea.Msg) tea.Cmd { return nil }
func (s *stubSection) view(w, h int) string   { return s.h.st.Bold.Render(s.name) }
func (s *stubSection) keys() []keyHelp        { return nil }
func (s *stubSection) command() string        { return "" }
func (s *stubSection) capturing() bool        { return false }
func (s *stubSection) back() bool             { return false }

func newListenSection(h *home) section     { return &stubSection{h, "Listen"} }
func newNowPlayingSection(h *home) section { return &stubSection{h, "Now playing"} }
func newStreamsSection(h *home) section    { return &stubSection{h, "Streams"} }
func newHistorySection(h *home) section    { return &stubSection{h, "History"} }
func newAccountSection(h *home) section    { return &stubSection{h, "Account"} }
func newSettingsSection(h *home) section   { return &stubSection{h, "Settings"} }
func newHelpSection(h *home) section       { return &stubSection{h, "Help"} }

type paletteOpenMsg struct{ path, line string }

type palette struct{ stubSection }

func newPalette(h *home) *palette { return &palette{stubSection{h, "Commands"}} }

func (h *home) openPaletteLine(line string) tea.Cmd {
	h.paletteO = true
	return h.send("palette", paletteOpenMsg{line: line})
}
