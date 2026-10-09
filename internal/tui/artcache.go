package tui

import (
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/art"
)

// artMsg delivers a fetched cover to a full-screen view.
type artMsg struct {
	key string
	img image.Image
	err error
}

type artEntry struct {
	img      image.Image
	loading  bool
	failed   bool
	rendered map[[2]int]string
	accent   lipgloss.Color
}

// artStore fetches covers in the background for full-screen views, keeps
// them rendered per size, and tracks the images on screen (graphics.go).
type artStore struct {
	proto   art.Protocol
	cell    art.CellSize
	entries map[string]*artEntry

	out io.Writer // what the program renders to
	tty *os.File  // the terminal, when images are drawn with escapes

	frame []pendingBox // boxes reserved by the view being built

	mu        sync.Mutex // guards the fields below, shared with artOut
	wanted    []placedBox
	drawn     []placedBox
	kittyIDs  map[string]uint32
	kittyByID map[uint32]string
	kittyTx   []string
	kittyBase uint32
	kittyNext uint32
}

func newArtStore(proto art.Protocol) *artStore {
	return &artStore{proto: proto, cell: art.DefaultCell, entries: map[string]*artEntry{},
		kittyIDs: map[string]uint32{}, kittyByID: map[uint32]string{}}
}

func artKey(v app.ResultView) string {
	return art.SourceURL(v.SongLink, v.AppleArtwork, 300)
}

// want returns a command fetching v's cover, or nil when it is known,
// loading, unavailable, or art is off.
func (s *artStore) want(ctx context.Context, v app.ResultView) tea.Cmd {
	if s.proto == art.ProtoNone {
		return nil
	}
	key := artKey(v)
	if key == "" || s.entries[key] != nil {
		return nil
	}
	s.entries[key] = &artEntry{loading: true}
	link, apple := v.SongLink, v.AppleArtwork
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		img, err := fetchArt(c, link, apple, 300)
		return artMsg{key: key, img: img, err: err}
	}
}

func (s *artStore) handle(m artMsg) {
	e := s.entries[m.key]
	if e == nil {
		e = &artEntry{}
		s.entries[m.key] = e
	}
	e.loading = false
	if m.err != nil || m.img == nil {
		e.failed = true
		return
	}
	e.img = m.img
}

// render returns cols×rows cells for v's cover, else a quiet placeholder
// of the same size so layouts do not jump. The cells are half-blocks,
// Kitty placeholders, or a reserved box that the image is drawn over
// after the frame (iTerm2, sixel). Call it between beginView and
// finishView.
func (s *artStore) render(v app.ResultView, cols, rows int, dim lipgloss.Style) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	key := artKey(v)
	e := s.entries[key]
	if e == nil || e.img == nil {
		return placeholder(cols, rows, dim)
	}
	id := fmt.Sprintf("%s|%dx%d|%dx%d", key, cols, rows, s.cell.W, s.cell.H)
	if s.proto == art.ProtoKitty {
		return art.KittyPlaceholders(s.kittyID(e, id, cols, rows), cols, rows)
	}
	if e.rendered == nil {
		e.rendered = map[[2]int]string{}
	}
	size := [2]int{cols, rows}
	r, ok := e.rendered[size]
	if !ok {
		r = art.RenderCell(e.img, cols, rows, s.proto, s.cell)
		e.rendered[size] = r
	}
	if s.proto.IsGraphics() {
		if box := s.reserve(id, r, cols, rows); box != "" {
			return box
		}
		return placeholder(cols, rows, dim)
	}
	return r
}

func placeholder(cols, rows int, dim lipgloss.Style) string {
	lines := make([]string, rows)
	for i := range lines {
		lines[i] = strings.Repeat(" ", cols)
	}
	mid := rows / 2
	note := "♪"
	pad := (cols - 1) / 2
	lines[mid] = strings.Repeat(" ", pad) + dim.Render(note) + strings.Repeat(" ", cols-pad-1)
	return strings.Join(lines, "\n")
}

// accent is the cover's accent color, or "" when unknown.
func (s *artStore) accent(v app.ResultView) lipgloss.Color {
	e := s.entries[artKey(v)]
	if e == nil || e.img == nil {
		if v.AppleBG != "" {
			return art.Accent(nil, v.AppleBG)
		}
		return ""
	}
	if e.accent == "" {
		e.accent = art.Accent(e.img, v.AppleBG)
	}
	return e.accent
}
