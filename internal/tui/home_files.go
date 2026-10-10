package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// fileBrowser picks an audio file or a folder.
type fileBrowser struct {
	dir     string
	entries []fsEntry
	cursor  int
	off     int
	err     error
}

type fsEntry struct {
	name string
	dir  bool
}

func newFileBrowser(start string) *fileBrowser {
	if start == "" {
		start = "."
	}
	if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
		start = filepath.Dir(start)
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		abs = start
	}
	b := &fileBrowser{dir: abs}
	b.load()
	return b
}

func (b *fileBrowser) load() {
	b.entries, b.err = nil, nil
	b.cursor, b.off = 0, 0
	list, err := os.ReadDir(b.dir)
	if err != nil {
		b.err = err
	}
	b.entries = append(b.entries, fsEntry{name: "..", dir: true})
	var dirs, files []fsEntry
	for _, e := range list {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(b.dir, name)); err == nil {
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
	b.entries = append(append(b.entries, dirs...), files...)
}

// key handles a key. It returns the picked path, or done with "" when
// the browser was closed.
func (b *fileBrowser) key(k string, h int) (picked string, done bool) {
	n := len(b.entries)
	switch k {
	case "down", "j":
		b.cursor = min(n-1, b.cursor+1)
	case "up", "k":
		b.cursor = max(0, b.cursor-1)
	case "pgdown":
		b.cursor = min(n-1, b.cursor+h)
	case "pgup":
		b.cursor = max(0, b.cursor-h)
	case "home", "g":
		b.cursor = 0
	case "end", "G":
		b.cursor = n - 1
	case "left", "backspace", "h":
		b.dir = filepath.Dir(b.dir)
		b.load()
	case "esc", "q":
		return "", true
	case "s", ".":
		// The folder itself, as a batch.
		e := b.entries[b.cursor]
		if e.dir && e.name != ".." {
			return b.rel(filepath.Join(b.dir, e.name)), true
		}
		return b.rel(b.dir), true
	case "enter", "right", "l":
		e := b.entries[b.cursor]
		if e.dir {
			if e.name == ".." {
				b.dir = filepath.Dir(b.dir)
			} else {
				b.dir = filepath.Join(b.dir, e.name)
			}
			b.load()
			return "", false
		}
		return b.rel(filepath.Join(b.dir, e.name)), true
	}
	return "", false
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
