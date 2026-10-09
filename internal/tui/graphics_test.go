package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/AudDMusic/audd-cli/internal/art"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// visible makes escape sequences readable in golden files.
func visible(s string) string {
	return strings.NewReplacer("\x1b", `\e`, "\a", `\a`).Replace(s)
}

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// npArtModel is the single-station now-playing model drawing art with p.
func npArtModel(t *testing.T, p art.Protocol) *npModel {
	t.Helper()
	m := npTestModel(t, sampleFeed(), []int{1}, false)
	m.arts.proto = p
	return m
}

func boxes(s *artStore) []placedBox {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]placedBox(nil), s.wanted...)
}

func TestNowPlayingReservesBoxForITerm2(t *testing.T) {
	for _, p := range []art.Protocol{art.ProtoITerm2, art.ProtoSixel} {
		m := npArtModel(t, p)
		view := runModel(t, m, "Warriors")
		if strings.ContainsAny(view, "▀\U000F0000") || strings.Contains(view, "\x1b]1337") || strings.Contains(view, "\x1bP") {
			t.Fatalf("%v: the view must hold blank cells only:\n%s", p, view)
		}
		lines := strings.Split(view, "\n")
		for y := 2; y <= 15; y++ {
			if !strings.HasPrefix(lines[y], "\x1b[28C   ") {
				t.Fatalf("%v: row %d is not reserved: %q", p, y, lines[y])
			}
		}
		if p == art.ProtoITerm2 {
			testutil.Golden(t, "nowplaying_single_reserved", []byte(visible(view)))
		}
		got := boxes(m.arts)
		if len(got) != 1 || got[0].x != 0 || got[0].y != 2 || got[0].cols != 28 || got[0].rows != 14 {
			t.Fatalf("%v: boxes %+v", p, got)
		}
		prefix := map[art.Protocol]string{art.ProtoITerm2: "\x1b]1337;File=inline=1;", art.ProtoSixel: "\x1bPq"}[p]
		if !strings.HasPrefix(got[0].seq, prefix) || !strings.Contains(got[0].seq, "width=28;height=14") && p == art.ProtoITerm2 {
			t.Fatalf("%v: image %q", p, got[0].seq[:30])
		}
	}
}

func TestNowPlayingGridReservesABoxPerCard(t *testing.T) {
	m := npTestModel(t, sampleFeed(), nil, false)
	m.arts.proto = art.ProtoITerm2
	// Wait for the covers: until they load, the cards show blank space
	// where the boxes go.
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("Feeling Good")) },
		teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	for deadline := time.Now().Add(5 * time.Second); len(boxes(m.arts)) < 3 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	view := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).View()
	testutil.Golden(t, "nowplaying_grid_reserved", []byte(visible(view)))
	got := boxes(m.arts)
	// Stations 1 and 2 have covers; station 3 is down and shows its last
	// song's cover too.
	if len(got) != 3 {
		t.Fatalf("boxes %+v", got)
	}
	// Two cards per row, 50 columns each: the cover sits inside the border
	// and padding.
	want := [][2]int{{2, 3}, {52, 3}, {2, 11}}
	for i, b := range got {
		if b.cols != 12 || b.rows != 6 || b.x != want[i][0] || b.y != want[i][1] {
			t.Fatalf("box %d: %d,%d %dx%d", i, b.x, b.y, b.cols, b.rows)
		}
	}
}

func TestNowPlayingKittyPlaceholders(t *testing.T) {
	oldPid := getpid
	getpid = func() int { return 0x1234 }
	t.Cleanup(func() { getpid = oldPid })
	m := npArtModel(t, art.ProtoKitty)
	view := runModel(t, m, "Warriors")
	testutil.Golden(t, "nowplaying_single_kitty", []byte(visible(view)))
	lines := strings.Split(view, "\n")
	cells := strings.Count(sgr.ReplaceAllString(lines[2], ""), "\U0010EEEE")
	if cells != 28 || strings.Count(view, "\U0010EEEE") != 28*14 {
		t.Fatalf("placeholder grid: %d per row, %d in all", cells, strings.Count(view, "\U0010EEEE"))
	}
	if len(boxes(m.arts)) != 0 {
		t.Fatal("Kitty images need no boxes")
	}
	// The image was queued once for upload with its id and size.
	pre, post := m.arts.afterWrite([]byte("\x1b[Hframe"))
	if post != "" || !strings.HasPrefix(pre, "\x1b_Ga=T,U=1,i=") || !strings.Contains(pre, ",c=28,r=14,") {
		t.Fatalf("upload %q", visible(pre[:min(60, len(pre))]))
	}
	id := regexp.MustCompile(`i=(\d+)`).FindStringSubmatch(pre)[1]
	if id != "1193217" { // 0x123501: the pid with the low bit set, then image 1
		t.Fatalf("image id %s", id)
	}
	if pre, _ := m.arts.afterWrite([]byte("\x1b[Hframe")); pre != "" {
		t.Fatal("uploaded twice")
	}
	m.View()
	if pre, _ := m.arts.afterWrite([]byte("\x1b[Hframe")); pre != "" {
		t.Fatal("redrawing must not upload again")
	}
	if c := m.arts.cleanup(); c != "\x1b_Ga=d,d=I,i="+id+",q=2\x1b\\" {
		t.Fatalf("cleanup %q", visible(c))
	}
}

// frame builds a Bubble Tea alt-screen frame that rewrites the given rows
// of a screen h rows tall.
func frame(h int, rows ...int) []byte {
	set := map[int]bool{}
	for _, r := range rows {
		set[r] = true
	}
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i := 0; i < h; i++ {
		if set[i] {
			b.WriteString("text\x1b[K")
			if i < h-1 {
				b.WriteString("\r\n")
			}
		} else if i < h-1 {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\x1b[6;1H")
	return []byte(b.String())
}

func TestRewrittenRows(t *testing.T) {
	cases := []struct {
		rows []int
	}{{[]int{0, 1, 2, 3, 4}}, {[]int{2}}, {[]int{4}}, {[]int{0, 4}}, {nil}}
	for _, c := range cases {
		got := rewrittenRows(frame(5, c.rows...))
		if len(got) != len(c.rows) {
			t.Fatalf("%v: got %v", c.rows, got)
		}
		for _, r := range c.rows {
			if !got[r] {
				t.Fatalf("%v: got %v", c.rows, got)
			}
		}
	}
	// The first frame starts each row with \r; a cleared tail is not a row.
	if got := rewrittenRows([]byte("\x1b[H\rA\x1b[K\r\n\n\x1b[J\x1b[3;1H")); len(got) != 1 || !got[0] {
		t.Fatalf("first frame: %v", got)
	}
}

func TestDrawAfterFrame(t *testing.T) {
	s := newArtStore(art.ProtoITerm2)
	place := func(bs ...placedBox) {
		s.mu.Lock()
		s.wanted = bs
		s.mu.Unlock()
	}
	img := placedBox{pendingBox: pendingBox{id: "a", seq: "<IMG>", cols: 3, rows: 2}, x: 2, y: 1}
	const drawA = "\x1b[2;3H\x1b[3X\x1b[3;3H\x1b[3X\x1b[2;3H<IMG>"
	place(img)
	steps := []struct {
		name  string
		write []byte
		post  string
	}{
		{"new box is blanked and drawn", frame(5, 0, 1, 2, 3, 4), "\x1b7" + drawA + "\x1b8"},
		{"untouched rows keep the image", frame(5, 0, 4), ""},
		{"a rewritten row moves past the image", frame(5, 1, 2), ""},
		{"other writes are not frames", []byte("\x1b[?25l"), ""},
		{"home alone is not a frame", []byte("\x1b[H"), ""},
	}
	for _, st := range steps {
		if _, post := s.afterWrite(st.write); post != st.post {
			t.Fatalf("%s: post %q, want %q", st.name, visible(post), visible(st.post))
		}
	}
	// A cleared screen loses every image.
	s.afterWrite([]byte("\x1b[2J"))
	if _, post := s.afterWrite(frame(5, 0)); post != "\x1b7"+drawA+"\x1b8" {
		t.Fatalf("after clear: %q", visible(post))
	}
	// So may a resize.
	s.resized()
	if _, post := s.afterWrite(frame(5, 0)); post != "\x1b7"+drawA+"\x1b8" {
		t.Fatalf("after resize: %q", visible(post))
	}
	// A box that moves is drawn at its new place; the old rows that the
	// frame did not rewrite are blanked.
	moved := img
	moved.x, moved.y = 10, 2
	place(moved)
	if _, post := s.afterWrite(frame(5, 2, 3)); post != "\x1b7\x1b[2;3H\x1b[3X\x1b[3;11H\x1b[3X\x1b[4;11H\x1b[3X\x1b[3;11H<IMG>\x1b8" {
		t.Fatalf("moved: %q", visible(post))
	}
	// A new song is a new image in the same place.
	next := moved
	next.id, next.seq = "b", "<NEXT>"
	place(next)
	if _, post := s.afterWrite(frame(5, 0)); post != "\x1b7\x1b[3;11H\x1b[3X\x1b[4;11H\x1b[3X\x1b[3;11H<NEXT>\x1b8" {
		t.Fatalf("next song: %q", visible(post))
	}
	// Leaving the view blanks the box where the frame left the rows alone.
	place()
	if _, post := s.afterWrite(frame(5, 3)); post != "\x1b7\x1b[3;11H\x1b[3X\x1b8" {
		t.Fatalf("gone: %q", visible(post))
	}
}

func TestArtOutWritesAroundFrames(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "tty"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := newArtStore(art.ProtoITerm2)
	s.kittyTx = []string{"<UPLOAD>"}
	s.wanted = []placedBox{{pendingBox: pendingBox{id: "a", seq: "<IMG>", cols: 2, rows: 1}, x: 0, y: 1}}
	o := &artOut{f: f, arts: s}
	if _, err := o.WriteString(string(frame(3, 0, 1, 2))); err != nil {
		t.Fatal(err)
	}
	if o.Fd() != f.Fd() {
		t.Fatal("artOut must keep the terminal's descriptor")
	}
	got, _ := os.ReadFile(f.Name())
	want := "<UPLOAD>\x1b[?2026h" + string(frame(3, 0, 1, 2)) + "\x1b7\x1b[2;1H\x1b[2X\x1b[2;1H<IMG>\x1b8\x1b[?2026l"
	if string(got) != want {
		t.Fatalf("wrote %q\nwant %q", visible(string(got)), visible(want))
	}
}

func TestFinishViewSkipsCutBoxes(t *testing.T) {
	s := newArtStore(art.ProtoSixel)
	s.beginView()
	box := s.reserve("a", "<IMG>", 4, 3)
	page := fit("head\n"+box, 100, 3) // the last box row falls off
	out := s.finishView(page, 100)
	if strings.ContainsRune(out, boxRune) || !strings.Contains(out, "\n\x1b[4C\n\x1b[4C") {
		t.Fatalf("markers left: %q", out)
	}
	if len(boxes(s)) != 0 {
		t.Fatalf("a cut box must not be drawn: %+v", boxes(s))
	}
	s.beginView()
	box = s.reserve("a", "<IMG>", 4, 3)
	s.finishView("\x1b[1mhé\x1b[0m "+strings.ReplaceAll(box, "\n", "\n   "), 100)
	if b := boxes(s); len(b) != 1 || b[0].x != 3 || b[0].y != 0 {
		t.Fatalf("box %+v", b)
	}
}

func TestResizeChangesTheBox(t *testing.T) {
	m := npArtModel(t, art.ProtoITerm2)
	m.arts.tty = os.Stdout
	old := cellSizeOf
	cellSizeOf = func(*os.File) (art.CellSize, bool) { return art.CellSize{W: 9, H: 21}, true }
	t.Cleanup(func() { cellSizeOf = old })
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(artMsg{key: artKey(sampleFeed().plays[1][0].ResultView), img: testImage()})
	m.applyPoll(npPollMsg{data: loadAll(context.Background(), m.feed, []Station{m.stations[0].Station}, historyLimit)})
	m.View()
	// 14 rows of 21 px are 294 px: 33 columns of 9 px.
	if b := boxes(m.arts); len(b) != 1 || b[0].cols != 33 || b[0].rows != 14 || !strings.Contains(b[0].seq, "width=33;height=14") {
		t.Fatalf("boxes %+v", b)
	}
}

func TestSetupArtNeedsATerminal(t *testing.T) {
	for _, mode := range []string{"kitty", "iterm2", "sixel"} {
		withEnv(t, map[string]string{"AUDD_ART": mode, "COLORTERM": "truecolor"})
		var out bytes.Buffer
		a := npApp(&out, &bytes.Buffer{}, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, StdinTTY: true})
		s := setupArt(a, false)
		if s.proto != art.ProtoHalfBlock || s.out != &out {
			t.Fatalf("%s: proto %v without a terminal", mode, s.proto)
		}
		if setupArt(a, true).proto != art.ProtoNone {
			t.Fatalf("%s: --no-art", mode)
		}
	}
}

// replyTerm is a terminal that answers every query with reply.
type replyTerm struct {
	reply string
	asked bool
	done  bool
}

func (r *replyTerm) Write(p []byte) (int, error) { r.asked = true; return len(p), nil }

func (r *replyTerm) ReadTimeout(p []byte, d time.Duration) (int, error) {
	if !r.asked || r.done {
		return 0, nil
	}
	r.done = true
	return copy(p, r.reply), nil
}

func TestSetupArtOnATerminal(t *testing.T) {
	dir := t.TempDir()
	tty, err := os.Create(filepath.Join(dir, "tty"))
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	oldIs, oldOpen, oldCell := isTerminal, openTTY, cellSizeOf
	t.Cleanup(func() { isTerminal, openTTY, cellSizeOf = oldIs, oldOpen, oldCell })
	isTerminal = func(*os.File) bool { return true }
	cases := []struct {
		name  string
		env   map[string]string
		reply string
		ioctl art.CellSize
		want  art.Protocol
		cell  art.CellSize
	}{
		{"sixel from DA1", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, "\x1b[6;18;9t\x1b[?62;4c", art.CellSize{}, art.ProtoSixel, art.CellSize{W: 9, H: 18}},
		{"cell size from the window size wins", map[string]string{"TERM": "xterm-256color"}, "\x1b[6;18;9t\x1b[?62;4c", art.CellSize{W: 8, H: 16}, art.ProtoSixel, art.CellSize{W: 8, H: 16}},
		{"sixel needs a cell size", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, "\x1b[?62;4c", art.CellSize{}, art.ProtoHalfBlock, art.DefaultCell},
		{"forced sixel with a guessed size", map[string]string{"AUDD_ART": "sixel"}, "\x1b[?62;4c", art.CellSize{}, art.ProtoSixel, art.DefaultCell},
		{"kitty", map[string]string{"TERM": "xterm-kitty"}, "\x1b[?62c", art.CellSize{}, art.ProtoKitty, art.DefaultCell},
		{"no sixel", map[string]string{"TERM": "xterm-256color"}, "\x1b[?62c", art.CellSize{}, art.ProtoHalfBlock256, art.DefaultCell},
	}
	for _, c := range cases {
		withEnv(t, c.env)
		ft := &replyTerm{reply: c.reply}
		restored := false
		openTTY = func(in, out *os.File) (art.Terminal, func()) { return ft, func() { restored = true } }
		cellSizeOf = func(*os.File) (art.CellSize, bool) { return c.ioctl, c.ioctl.Known() }
		a := npApp(&bytes.Buffer{}, &bytes.Buffer{}, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, StdinTTY: true})
		a.Out = output.NewPrinter(tty, &bytes.Buffer{}, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, StdinTTY: true})
		a.In = tty
		s := setupArt(a, false)
		if s.proto != c.want || s.cell != c.cell || !ft.asked || !restored {
			t.Errorf("%s: proto %v cell %+v asked %v restored %v", c.name, s.proto, s.cell, ft.asked, restored)
		}
		if _, wrapped := s.out.(*artOut); wrapped != c.want.IsGraphics() {
			t.Errorf("%s: output wrapped = %v", c.name, wrapped)
		}
	}
}

func TestExplorerDetailShowsCover(t *testing.T) {
	withEnv(t, map[string]string{"COLORTERM": "truecolor"})
	calls := withArt(t, testImage())
	warriors := singleView([]byte(`{"title":"Warriors","song_link":"https://lis.tn/Warriors"}`))

	// Opening a detail fetches its cover.
	m := explorerModel(t, "recent", &fakeStreamsAPI{})
	m.arts = newArtStore(art.ProtoHalfBlock)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(m.load(tabRecent, m.top())())
	_, cmd := m.key(key("enter"))
	if cmd == nil {
		t.Fatal("no cover fetch")
	}
	m.Update(cmd())
	if *calls != 1 || !strings.Contains(m.View(), "▀") {
		t.Fatalf("cover not shown after the fetch (%d calls)", *calls)
	}

	for _, p := range []art.Protocol{art.ProtoHalfBlock, art.ProtoITerm2} {
		m := explorerModel(t, "recent", &fakeStreamsAPI{})
		m.arts = newArtStore(p)
		m.arts.entries[artKey(*warriors)] = &artEntry{img: testImage()}
		view := runModelUntil(t, m, "Warriors", "KIDinaKORNER", key("enter"))
		name := map[art.Protocol]string{art.ProtoHalfBlock: "explorer_detail_art", art.ProtoITerm2: "explorer_detail_reserved"}[p]
		if p == art.ProtoITerm2 {
			view = visible(view)
		}
		testutil.Golden(t, name, []byte(view))
		lines := strings.Split(view, "\n")
		switch p {
		case art.ProtoHalfBlock:
			if !strings.Contains(lines[1], "▀") || !strings.Contains(sgr.ReplaceAllString(lines[1], ""), "   /music/warriors.mp3") {
				t.Fatalf("cover next to the detail:\n%s", view)
			}
		case art.ProtoITerm2:
			if !strings.HasPrefix(lines[1], `\e[20C   /music/warriors.mp3`) {
				t.Fatalf("reserved cover:\n%s", view)
			}
			if b := boxes(m.arts); len(b) != 1 || b[0].x != 0 || b[0].y != 1 || b[0].cols != 20 || b[0].rows != 10 {
				t.Fatalf("boxes %+v", b)
			}
		}
	}
}

// TestImagesFollowRealFrames runs the now-playing screen through Bubble
// Tea's renderer into a file and checks that the image is written after
// the frame that reserves its box, at the box's place.
func TestImagesFollowRealFrames(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "tty"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := npArtModel(t, art.ProtoITerm2)
	m.arts.tty = f
	m.arts.out = &artOut{f: f, arts: m.arts}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithInput(nil), tea.WithOutput(m.arts.out))
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	p.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(f.Name())
		if bytes.Contains(b, []byte("\x1b]1337;File=")) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m.arts.finish()
	b, _ := os.ReadFile(f.Name())
	out := string(b)
	img := strings.Index(out, "\x1b[3;1H\x1b]1337;File=inline=1;")
	if img < 0 {
		t.Fatalf("no image at row 3, column 1:\n%q", visible(out[:min(400, len(out))]))
	}
	// It follows a frame (the song may have been drawn by an earlier one,
	// before the cover arrived).
	if frame := strings.LastIndex(out[:img], "\x1b[H"); frame < 0 || !strings.Contains(out[:img], "Warriors") {
		t.Fatalf("the image must follow a frame that shows the song:\n%q", visible(out[:img+20]))
	}
	if !strings.Contains(out[img:], "width=28;height=14;preserveAspectRatio=1:") {
		t.Fatal("image size in cells")
	}
}

// TestTicksDoNotResendImages runs now-playing through Bubble Tea's
// renderer and ticks the clock: the elapsed time of a live song next to the cover (and
// in the grid, rows that fill the width) is rewritten each second, but
// the image is written once.
func TestTicksDoNotResendImages(t *testing.T) {
	for _, grid := range []bool{false, true} {
		f, err := os.Create(filepath.Join(t.TempDir(), "tty"))
		if err != nil {
			t.Fatal(err)
		}
		var m *npModel
		if grid {
			m = npTestModel(t, startFeed(), nil, false)
		} else {
			m = npTestModel(t, startFeed(), []int{1}, false)
		}
		m.arts.proto = art.ProtoITerm2
		var offset atomic.Int64
		base := m.now()
		m.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
		m.arts.tty = f
		m.arts.out = &artOut{f: f, arts: m.arts}
		p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithInput(nil), tea.WithOutput(m.arts.out))
		done := make(chan error, 1)
		go func() { _, err := p.Run(); done <- err }()
		p.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
		read := func() string { b, _ := os.ReadFile(f.Name()); return string(b) }
		waitFor := func(what string, ok func(string) bool) {
			deadline := time.Now().Add(5 * time.Second)
			for !ok(read()) {
				if time.Now().After(deadline) {
					p.Kill()
					t.Fatalf("grid %v: timed out waiting for %s:\n%q", grid, what, visible(read()))
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		const img = "\x1b]1337;File=inline=1;"
		images := map[bool]int{false: 1, true: 3}[grid]
		waitFor("the images", func(s string) bool { return strings.Count(s, img) >= images })
		for i := 1; i <= 3; i++ {
			offset.Store(int64(i) * int64(time.Second))
			p.Send(npTickMsg(base))
			label := clock(startFeed().plays[1][0].Elapsed(m.now()))
			waitFor("tick "+label, func(s string) bool { return strings.Count(s, label) > 0 })
		}
		p.Quit()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		out := read()
		f.Close()
		if n := strings.Count(out, img); n != images {
			t.Errorf("grid %v: %d images written, want %d", grid, n, images)
		}
		if strings.Contains(out, "audd-eol") {
			t.Errorf("grid %v: the row tag reached the terminal", grid)
		}
	}
}

// A row that fills the width ends with a tag that artOut removes together
// with the erase Bubble Tea adds, which would take the row's last cell.
func TestFullRowsKeepTheirLastCell(t *testing.T) {
	s := newArtStore(art.ProtoITerm2)
	s.beginView()
	box := s.reserve("a", "<IMG>", 2, 1)
	if got := s.finishView("ab"+box+"cd", 6); got != "ab\x1b[2Ccd"+eolTag {
		t.Fatalf("full row %q", visible(got))
	}
	s.beginView()
	box = s.reserve("a", "<IMG>", 2, 1)
	if got := s.finishView("ab"+box+"cd", 7); got != "ab\x1b[2Ccd" {
		t.Fatalf("short row %q", visible(got))
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "tty"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	o := &artOut{f: f, arts: s}
	in := "\x1b[H\x1b[1Cab\x1b[2Ccd" + eolTag + "\x1b[K\r\nx" + eolTag + "\x1b[2;1H"
	if n, err := o.WriteString(in); err != nil || n != len(in) {
		t.Fatalf("wrote %d, %v", n, err)
	}
	got, _ := os.ReadFile(f.Name())
	if want := "\x1b[H\x1b[1Cab\x1b[2Ccd\r\nx\x1b[2;1H"; !strings.HasPrefix(string(got), "\x1b[?2026h"+want) {
		t.Fatalf("wrote %q", visible(string(got)))
	}
}
