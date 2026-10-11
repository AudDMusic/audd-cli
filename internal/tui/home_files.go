package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// fileBrowser picks an audio file or a folder. Folders are read in the
// background (fsListMsg), never while a key is handled.
type fileBrowser struct {
	dir     string
	entries []fsEntry
	cursor  int
	off     int
	err     error
	loading bool
	req     int // the folder read that counts; older ones are dropped
}

type fsEntry struct {
	name string
	dir  bool
}

// fsListMsg is a folder read for the file browser.
type fsListMsg struct {
	req     int
	dir     string
	entries []fsEntry
	err     error
}

// newFileBrowser opens at start (a folder, or the folder of a file); the
// returned command reads it.
func newFileBrowser(start string) (*fileBrowser, tea.Cmd) {
	if start == "" {
		start = "."
	}
	b := &fileBrowser{dir: start}
	return b, b.load(true)
}

// load reads b.dir in the background; first resolves a file to its
// folder and makes the path absolute.
func (b *fileBrowser) load(first bool) tea.Cmd {
	b.loading, b.err = true, nil
	b.req++
	req, dir := b.req, b.dir
	return func() tea.Msg {
		if first {
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				dir = filepath.Dir(dir)
			}
			if abs, err := filepath.Abs(dir); err == nil {
				dir = abs
			}
		}
		entries, err := readEntries(dir)
		return fsListMsg{req: req, dir: dir, entries: entries, err: err}
	}
}

// apply takes a folder read, when it is the latest one.
func (b *fileBrowser) apply(m fsListMsg) {
	if m.req != b.req {
		return
	}
	b.loading = false
	b.dir, b.entries, b.err = m.dir, m.entries, m.err
	b.cursor, b.off = 0, 0
}

func readEntries(dir string) ([]fsEntry, error) {
	list, err := os.ReadDir(dir)
	entries := []fsEntry{{name: "..", dir: true}}
	var dirs, files []fsEntry
	for _, e := range list {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = fi.IsDir()
			}
		}
		switch {
		case isDir:
			dirs = append(dirs, fsEntry{name: name, dir: true})
		case media.IsMediaFile(name):
			files = append(files, fsEntry{name: name})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].name) < strings.ToLower(dirs[j].name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].name) < strings.ToLower(files[j].name) })
	return append(append(entries, dirs...), files...), err
}

// key handles a key. It returns the picked path, or done with "" when
// the browser was closed; cmd reads another folder.
func (b *fileBrowser) key(k string, h int) (picked string, done bool, cmd tea.Cmd) {
	up := func() tea.Cmd {
		b.dir = filepath.Dir(b.dir)
		return b.load(false)
	}
	n := len(b.entries)
	if n == 0 || b.loading {
		switch k {
		case "left", "backspace":
			return "", false, up()
		case "esc":
			return "", true, nil
		case "s":
			return b.rel(b.dir), true, nil
		}
		return "", false, nil
	}
	switch k {
	case "down":
		b.cursor = min(n-1, b.cursor+1)
	case "up":
		b.cursor = max(0, b.cursor-1)
	case "pgdown":
		b.cursor = min(n-1, b.cursor+h)
	case "pgup":
		b.cursor = max(0, b.cursor-h)
	case "home":
		b.cursor = 0
	case "end":
		b.cursor = n - 1
	case "left", "backspace":
		return "", false, up()
	case "esc":
		return "", true, nil
	case "s":
		// The folder itself, as a batch.
		e := b.entries[b.cursor]
		if e.dir && e.name != ".." {
			return b.rel(filepath.Join(b.dir, e.name)), true, nil
		}
		return b.rel(b.dir), true, nil
	case "enter", "right":
		e := b.entries[b.cursor]
		if e.dir {
			if e.name == ".." {
				return "", false, up()
			}
			b.dir = filepath.Join(b.dir, e.name)
			return "", false, b.load(false)
		}
		return b.rel(filepath.Join(b.dir, e.name)), true, nil
	}
	return "", false, nil
}

// rel shortens p to a path relative to the working directory when it is
// inside it.
func (b *fileBrowser) rel(p string) string {
	wd, err := os.Getwd()
	if err != nil {
		return p
	}
	if r, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(r, "..") {
		if r == "." {
			return "."
		}
		return r
	}
	return p
}

func (b *fileBrowser) view(st output.Styles, w, h int, color bool) string {
	var out strings.Builder
	out.WriteString(st.Bold.Render(truncate(b.dir, w)) + "\n")
	if b.err != nil {
		out.WriteString(st.Warn.Render(truncate(b.err.Error(), w)) + "\n")
	}
	if b.loading {
		out.WriteString(st.Dim.Render("Reading the folder…") + "\n")
	}
	listH := max(1, h-3)
	if b.cursor < b.off {
		b.off = b.cursor
	}
	if b.cursor >= b.off+listH {
		b.off = b.cursor - listH + 1
	}
	for i := b.off; i < len(b.entries) && i < b.off+listH; i++ {
		e := b.entries[i]
		name := e.name
		if e.dir {
			name += "/"
		}
		line := "  " + name
		if i == b.cursor {
			line = "› " + name
			if color {
				line = st.Bold.Reverse(true).Render(line)
			}
		}
		out.WriteString(truncate(line, w) + "\n")
	}
	out.WriteString(st.Dim.Render(truncate("enter open or pick  s pick this folder (a batch)  ← up  esc close", w)))
	return out.String()
}
