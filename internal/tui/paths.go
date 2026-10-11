package tui

import (
	"github.com/charmbracelet/bubbles/textinput"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
)

var homeDirs sync.Map // user name → home folder ("" for the current user)

// expandHome expands a leading ~ or ~user the way a shell does, so a
// path typed in interactive mode works like one typed after audd; the
// command shown then has the full path and works when pasted.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") || strings.Contains(p, "://") {
		return p
	}
	name, rest, _ := strings.Cut(p[1:], string(filepath.Separator))
	if i := strings.IndexByte(name, '/'); i >= 0 { // "/" works on every system
		name, rest = name[:i], name[i+1:]+rest
	}
	home, ok := homeDirs.Load(name)
	if !ok {
		var dir string
		if name == "" {
			dir, _ = os.UserHomeDir()
		} else if u, err := user.Lookup(name); err == nil {
			dir = u.HomeDir
		}
		homeDirs.Store(name, dir)
		home = dir
	}
	if home.(string) == "" {
		return p
	}
	if rest == "" && !strings.HasSuffix(p, "/") {
		return home.(string)
	}
	return filepath.Join(home.(string), rest)
}

// shortKeepName shortens a long path in its middle and keeps its file
// name whole (where a typo usually is): "/home/me/…/Musc/song.mp3".
func shortKeepName(p string, w int) string {
	r := []rune(p)
	if len(r) <= w {
		return p
	}
	base := []rune(filepath.Base(p))
	if len(base)+6 >= w {
		return truncMiddle(p, w)
	}
	head := w - len(base) - 2
	return string(r[:head]) + "…" + string(filepath.Separator) + string(base)
}

// inputView draws a text input. With color it is the input's own view
// (the caret in reverse video); without color the caret is a "|" where
// it is, so it shows also mid-text.
func inputView(ti textinput.Model, color bool) string {
	if color || !ti.Focused() {
		return ti.View()
	}
	v := []rune(ti.Value())
	if ti.EchoMode == textinput.EchoPassword {
		for i := range v {
			v[i] = '•'
		}
	}
	pos := max(0, min(ti.Position(), len(v)))
	if len(v) == 0 && ti.Placeholder != "" {
		return ti.Prompt + "|" + ti.Placeholder
	}
	text := string(v[:pos]) + "|" + string(v[pos:])
	// Keep the caret in view in a narrow field: the end of the text
	// before it.
	if w := ti.Width; w > 0 && len([]rune(text)) > w {
		r := []rune(text)
		start := max(0, min(pos+2-w, len(r)-w))
		text = string(r[start : start+w])
	}
	return ti.Prompt + text
}
