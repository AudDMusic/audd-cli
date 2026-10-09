package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/art"
)

// How the full-screen views show real images:
//
//   - Kitty graphics: each image is sent once with a virtual placement
//     (U=1), and the view prints Unicode placeholder cells for it. The
//     image is then ordinary text to Bubble Tea: it moves, redraws, and
//     disappears with the cells.
//   - iTerm2 and sixel images cannot be cells. The view reserves a box of
//     blank cells, marked while the view is built with private-use runes
//     (boxRune onward) that finishView turns into cursor moves after
//     noting where each box ended up, so rewriting a row moves past the
//     image. The terminal output is wrapped (artOut) so that right after
//     Bubble Tea writes a frame, each box that is new or has moved is
//     drawn at its position (save cursor, move, image, restore), and
//     boxes that went away are blanked.
//
// Nothing is drawn unless stdout is a terminal.

// boxRune marks the cells of reserved box 0; box i uses boxRune+i.
const (
	boxRune  = '\U000F0000'
	maxBoxes = 64
)

// boxPrefix is the UTF-8 prefix shared by boxRune … boxRune+63.
var boxPrefix = string([]byte{0xf3, 0xb0, 0x80})

// pendingBox is a box reserved by the view being built.
type pendingBox struct {
	id         string // image key and size: equal ids draw the same thing
	seq        string // escape sequence that draws the image
	cols, rows int
}

// placedBox is a reserved box at its place on the screen (0-based).
type placedBox struct {
	pendingBox
	x, y int
}

// setupArt picks how a full-screen view draws cover art. It asks the
// terminal (DA1 and cell size) when stdin and stdout are terminals, and
// returns the store with the writer the program must render to: stdout
// itself, or a wrapper that draws images after each frame.
func setupArt(a *app.App, noArt bool) *artStore {
	out := a.Out.Stdout()
	opts := a.Out.Options()
	if noArt || opts.NoColor || noArtEnv() {
		s := newArtStore(art.ProtoNone)
		s.out = out
		return s
	}
	outF, _ := out.(*os.File)
	inF, _ := a.In.(*os.File)
	if outF != nil && !isTerminal(outF) {
		outF = nil
	}
	var t art.Terminal
	restore := func() {}
	if outF != nil && inF != nil {
		t, restore = openTTY(inF, outF)
	}
	proto, probe := art.DetectFullScreen(getenv, true, t)
	restore()
	cell := probe.Cell
	if c, ok := cellSizeOf(outF); ok {
		cell = c
	}
	if proto.IsGraphics() && outF == nil {
		proto = art.Cells(getenv)
	}
	// A sixel image is sized in pixels: with a guessed cell size it could
	// spill over the text next to it. Unless asked for, use half-blocks.
	if proto == art.ProtoSixel && !cell.Known() && !strings.EqualFold(strings.TrimSpace(getenv("AUDD_ART")), "sixel") {
		proto = art.Cells(getenv)
	}
	s := newArtStore(proto)
	s.cell = cell.OrDefault()
	s.out = out
	if proto.IsGraphics() {
		s.tty = outF
		s.out = &artOut{f: outF, arts: s}
	}
	return s
}

// Test seams for terminal access.
var (
	getpid     = os.Getpid
	isTerminal = func(f *os.File) bool { return term.IsTerminal(f.Fd()) }
	openTTY    = art.OpenTTY
	cellSizeOf = func(f *os.File) (art.CellSize, bool) {
		if f == nil {
			return art.CellSize{}, false
		}
		return art.CellSizeOf(f)
	}
)

// colsFor is the width in cells of a square box rows tall.
func (s *artStore) colsFor(rows int) int { return s.cell.ColsFor(rows) }

// resized re-reads the cell size (it changes with the font size) and
// drops images rendered for the old one. Images on screen are drawn
// again: a terminal may move or drop them when its window changes size.
func (s *artStore) resized() {
	s.mu.Lock()
	s.drawn = nil
	s.mu.Unlock()
	c, ok := cellSizeOf(s.tty)
	if !ok || c == s.cell {
		return
	}
	s.cell = c
	for _, e := range s.entries {
		e.rendered = nil
	}
}

// beginView starts a view: boxes reserved from here on belong to it.
func (s *artStore) beginView() { s.frame = s.frame[:0] }

// reserve returns a cols×rows box of marker cells for an image drawn
// after the frame, or "" when there is no room for another box.
func (s *artStore) reserve(id, seq string, cols, rows int) string {
	if len(s.frame) >= maxBoxes {
		return ""
	}
	r := string(rune(boxRune + len(s.frame)))
	s.frame = append(s.frame, pendingBox{id: id, seq: seq, cols: cols, rows: rows})
	line := strings.Repeat(r, cols)
	lines := make([]string, rows)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// finishView turns the marker cells in page, a screen width cells wide,
// into blank space and records where each reserved box ended up. A box
// cut by the edge of the screen is left blank: an image would spill out
// of it.
//
// The blank space is a cursor move (CUF) rather than spaces, so a frame
// that rewrites a row (a clock ticking next to the cover) moves past the
// image instead of writing over it, and the image need not be sent again.
// Bubble Tea does not count the moves, so it would end a row that fills
// the whole width with an erase that takes its last cell. Such a row ends
// with eolTag, and artOut drops the tag with the erase after it.
func (s *artStore) finishView(page string, width int) string {
	if !s.proto.IsGraphics() {
		return page
	}
	type seen struct {
		x, y  int
		rows  int
		ok    bool
		begun bool
	}
	found := make([]seen, len(s.frame))
	lines := strings.Split(page, "\n")
	for y, line := range lines {
		if !strings.Contains(line, boxPrefix) {
			continue
		}
		// Pieces of the line: text, then a run of marker cells.
		type piece struct {
			text  string
			cells int
		}
		var pieces []piece
		col, last := 0, 0
		cur, run, runX := -1, 0, 0
		end := func() {
			if cur < 0 || cur >= len(found) {
				return
			}
			f := &found[cur]
			want := s.frame[cur]
			switch {
			case !f.begun:
				*f = seen{x: runX, y: y, rows: 1, ok: run == want.cols, begun: true}
			case f.y+f.rows == y && f.x == runX && run == want.cols:
				f.rows++
			default:
				f.ok = false
			}
		}
		for i := 0; i < len(line); {
			r, size := utf8.DecodeRuneInString(line[i:])
			if r < boxRune || r >= boxRune+maxBoxes {
				i += size
				continue
			}
			col += ansi.StringWidth(line[last:i])
			if n := len(pieces); n > 0 && last == i {
				pieces[n-1].cells++
			} else {
				pieces = append(pieces, piece{text: line[last:i], cells: 1})
			}
			idx := int(r - boxRune)
			if idx != cur || col != runX+run {
				end()
				cur, run, runX = idx, 0, col
			}
			run++
			col++
			i += size
			last = i
		}
		end()
		tail := line[last:]
		var b strings.Builder
		for _, p := range pieces {
			b.WriteString(p.text)
			b.WriteString(ansi.CursorForward(p.cells))
		}
		b.WriteString(tail)
		if width > 0 && col+ansi.StringWidth(tail) >= width {
			b.WriteString(eolTag)
		}
		lines[y] = b.String()
	}
	var want []placedBox
	for i, f := range found {
		if f.begun && f.ok && f.rows == s.frame[i].rows {
			want = append(want, placedBox{pendingBox: s.frame[i], x: f.x, y: f.y})
		}
	}
	s.mu.Lock()
	s.wanted = want
	s.mu.Unlock()
	return strings.Join(lines, "\n")
}

var (
	frameTail   = regexp.MustCompile(`(\x1b\[J)?\x1b\[[0-9;]*H$`)
	frameHome   = []byte(ansi.CursorHomePosition)
	clearScreen = []byte(ansi.EraseEntireScreen)
	leaveAlt    = []byte("\x1b[?1049l")
)

// rewrittenRows returns the screen rows that a Bubble Tea frame rewrote.
// In the alternate screen a frame starts at the home position and holds
// one "\n"-separated segment per row, empty for a row it skipped
// because it did not change.
func rewrittenRows(frame []byte) map[int]bool {
	body := bytes.TrimPrefix(frame, frameHome)
	rows := map[int]bool{}
	segs := bytes.Split(body, []byte("\n"))
	for i, seg := range segs {
		if i == len(segs)-1 {
			seg = frameTail.ReplaceAll(seg, nil)
		}
		if len(seg) > 0 {
			rows[i] = true
		}
	}
	return rows
}

// afterWrite is called with everything Bubble Tea writes. It returns what
// to write before it (pending Kitty uploads) and after it (iTerm2 and
// sixel images for the frame, blanking for boxes that went away).
func (s *artStore) afterWrite(p []byte) (pre, post string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pre = strings.Join(s.kittyTx, "")
	s.kittyTx = nil
	if bytes.Contains(p, clearScreen) || bytes.Contains(p, leaveAlt) {
		s.drawn = nil
	}
	if !bytes.HasPrefix(p, frameHome) || len(p) == len(frameHome) {
		return pre, ""
	}
	rows := rewrittenRows(p)
	var b strings.Builder
	has := func(list []placedBox, x placedBox) bool {
		for _, l := range list {
			if l.id == x.id && l.x == x.x && l.y == x.y && l.cols == x.cols && l.rows == x.rows {
				return true
			}
		}
		return false
	}
	sameRect := func(d placedBox) bool {
		for _, w := range s.wanted {
			if w.x == d.x && w.y == d.y && w.cols == d.cols && w.rows == d.rows {
				return true // the new image covers it
			}
		}
		return false
	}
	for _, d := range s.drawn {
		if sameRect(d) {
			continue
		}
		for r := 0; r < d.rows; r++ {
			if !rows[d.y+r] {
				fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[%dX", d.y+r+1, d.x+1, d.cols)
			}
		}
	}
	// An image is sent only when it is new or has moved, or after the
	// screen was cleared or resized: a rewritten row moves past it.
	for _, w := range s.wanted {
		if has(s.drawn, w) {
			continue
		}
		// Blank the cells first: a cursor move left whatever was there,
		// and an image with transparent parts would show it.
		for r := 0; r < w.rows; r++ {
			fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[%dX", w.y+r+1, w.x+1, w.cols)
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH%s", w.y+1, w.x+1, w.seq)
	}
	s.drawn = append(s.drawn[:0], s.wanted...)
	if b.Len() > 0 {
		post = "\x1b7" + b.String() + "\x1b8"
	}
	return pre, post
}

// kittyID returns the image id for key at cols×rows, queueing the upload
// the first time. Ids are unique to this process (its pid in the upper
// bits) and recycled after 255 images, deleting the old image first.
func (s *artStore) kittyID(e *artEntry, id string, cols, rows int) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.kittyIDs[id]; ok {
		return n
	}
	if s.kittyBase == 0 {
		s.kittyBase = (uint32(getpid())&0xffff | 1) << 8
	}
	s.kittyNext = s.kittyNext%255 + 1
	n := s.kittyBase | s.kittyNext
	if old, ok := s.kittyByID[n]; ok {
		s.kittyTx = append(s.kittyTx, art.KittyDelete(n))
		delete(s.kittyIDs, old)
	}
	s.kittyIDs[id] = n
	s.kittyByID[n] = id
	s.kittyTx = append(s.kittyTx, art.KittyTransmit(e.img, n, cols, rows, s.cell))
	return n
}

// cleanup is what to write after the program ends: deletes for every
// Kitty image sent.
func (s *artStore) cleanup() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for n := range s.kittyByID {
		b.WriteString(art.KittyDelete(n))
	}
	s.kittyIDs, s.kittyByID, s.kittyTx = map[string]uint32{}, map[uint32]string{}, nil
	return b.String()
}

// finish writes the cleanup to the terminal once the program has ended.
func (s *artStore) finish() {
	if c := s.cleanup(); c != "" && s.tty != nil {
		_, _ = io.WriteString(s.tty, c)
	}
}

// artOut is the terminal as Bubble Tea sees it, with images drawn around
// each write. Writes are serialized so an image never lands inside a
// frame. It keeps the file's descriptor so Bubble Tea still finds the
// terminal (size, raw mode, resize signals).
type artOut struct {
	f    *os.File
	mu   sync.Mutex
	arts *artStore
}

func (o *artOut) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	if bytes.Contains(p, []byte(eolTag)) {
		p = bytes.ReplaceAll(p, []byte(eolTag+ansi.EraseLineRight), nil)
		p = bytes.ReplaceAll(p, []byte(eolTag), nil)
	}
	pre, post := o.arts.afterWrite(p)
	if pre == "" && post == "" {
		if _, err := o.f.Write(p); err != nil {
			return 0, err
		}
		return n, nil
	}
	// One write, as a synchronized update where the terminal supports it
	// (others ignore the mode), so the frame and the images that go with
	// it show up together.
	var b bytes.Buffer
	b.WriteString(pre)
	if post != "" {
		b.WriteString(syncBegin)
	}
	b.Write(p)
	if post != "" {
		b.WriteString(post + syncEnd)
	}
	if _, err := o.f.Write(b.Bytes()); err != nil {
		return 0, err
	}
	return n, nil
}

// eolTag ends a view row that fills the screen width and holds cursor
// moves over an image (finishView). It is an APC string, which Bubble Tea
// counts as no width and terminals ignore; artOut removes it.
const eolTag = "\x1b_audd-eol\x1b\\"

// Synchronized output (DEC private mode 2026).
const (
	syncBegin = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
)

func (o *artOut) WriteString(s string) (int, error) { return o.Write([]byte(s)) }
func (o *artOut) Read(p []byte) (int, error)        { return o.f.Read(p) }
func (o *artOut) Close() error                      { return nil }
func (o *artOut) Fd() uintptr                       { return o.f.Fd() }
