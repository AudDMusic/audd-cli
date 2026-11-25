// Package termbg tells Lip Gloss the terminal's background before Bubble
// Tea's init asks the terminal for it. That query waits up to five seconds
// for an answer, so on a terminal that never answers (docker run -t from a
// script, ssh -tt, a CI runner's pty) every command would start five
// seconds late. audd's styles use fixed colors, so the answer is not needed.
//
// cmd/audd imports this package. Packages are initialized in import-path
// order once their imports are: this one needs only lipgloss, and
// github.com/AudDMusic/... sorts before github.com/charmbracelet/bubbletea,
// so its init runs first.
package termbg

import "github.com/charmbracelet/lipgloss"

func init() { lipgloss.SetHasDarkBackground(true) }
