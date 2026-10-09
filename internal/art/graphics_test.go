package art

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// visible makes escape sequences readable in golden files.
func visible(s string) string {
	return strings.NewReplacer("\x1b", `\e`, "\a", `\a`).Replace(s)
}

var payload = regexp.MustCompile(`;[A-Za-z0-9+/=]{16,}(\\e\\)`)

// elide replaces base64 image data, which depends on the PNG encoder,
// with a marker.
func elide(s string) string { return payload.ReplaceAllString(s, ";<base64>$1") }

func TestKittyTransmitGolden(t *testing.T) {
	out := KittyTransmit(fixture(), 0x01e240, 4, 2, CellSize{W: 8, H: 16})
	testutil.Golden(t, "kitty_transmit", []byte(elide(visible(out))+"\n"))

	re := regexp.MustCompile("\x1b_G([^;]*);([^\x1b]*)\x1b\\\\")
	parts := re.FindAllStringSubmatch(out, -1)
	if len(parts) != 1 || parts[0][1] != "a=T,U=1,i=123456,f=100,q=2,c=4,r=2,m=0" {
		t.Fatalf("framing: %q", visible(out))
	}
	raw, err := base64.StdEncoding.DecodeString(parts[0][2])
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// 4×2 cells of 8×16 pixels.
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Fatalf("pixel size %v", b)
	}
	if KittyTransmit(nil, 1, 4, 2, DefaultCell) != "" || KittyTransmit(fixture(), 1, 0, 2, DefaultCell) != "" {
		t.Fatal("expected nothing for an empty image or box")
	}
}

func TestKittyPlaceholdersGolden(t *testing.T) {
	out := KittyPlaceholders(0x01e240, 3, 2)
	testutil.Golden(t, "kitty_placeholders", []byte(visible(out)+"\n"))
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 rows: %q", out)
	}
	// The image id 123456 is 0x01e240: the color 1;226;64.
	if !strings.HasPrefix(lines[0], "\x1b[38;2;1;226;64m\U0010EEEE̅̅\U0010EEEE̅̍") {
		t.Fatalf("first row: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "\x1b[38;2;1;226;64m\U0010EEEE̍̅") {
		t.Fatalf("second row: %q", lines[1])
	}
	if KittyDelete(123456) != "\x1b_Ga=d,d=I,i=123456,q=2\x1b\\" {
		t.Fatalf("delete: %q", KittyDelete(123456))
	}
}

func TestITerm2Golden(t *testing.T) {
	out := RenderCell(fixture(), 6, 3, ProtoITerm2, CellSize{W: 9, H: 18})
	got := regexp.MustCompile(`size=\d+`).ReplaceAllString(visible(out), "size=<n>")
	got = regexp.MustCompile(`:[A-Za-z0-9+/=]+\\a$`).ReplaceAllString(got, ":<base64>\\a")
	testutil.Golden(t, "iterm2", []byte(got+"\n"))
	raw, _ := base64.StdEncoding.DecodeString(out[strings.Index(out, ":")+1 : len(out)-1])
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 54 || b.Dy() != 54 {
		t.Fatalf("pixel size %v", b)
	}
}

func TestSixelGolden(t *testing.T) {
	out := RenderCell(fixture(), 2, 1, ProtoSixel, CellSize{W: 8, H: 16})
	testutil.Golden(t, "sixel", []byte(visible(out)+"\n"))
	// 16×16 pixels rounded down to whole six-pixel bands: 12.
	if !strings.HasPrefix(out, "\x1bPq\"1;1;16;12#") {
		t.Fatalf("header: %q", out[:20])
	}
}

func TestCellSize(t *testing.T) {
	cases := []struct {
		cell CellSize
		rows int
		want int
	}{
		{DefaultCell, 10, 20},
		{CellSize{W: 8, H: 17}, 14, 30},
		{CellSize{W: 9, H: 18}, 6, 12},
		{CellSize{W: 10, H: 10}, 6, 6},
		{CellSize{}, 7, 14},
		{CellSize{W: 8, H: 16}, 0, 0},
	}
	for _, c := range cases {
		if got := c.cell.ColsFor(c.rows); got != c.want {
			t.Errorf("%v.ColsFor(%d) = %d, want %d", c.cell, c.rows, got, c.want)
		}
	}
	if w, h := pixelSize(30, 14, CellSize{W: 8, H: 17}); w != 240 || h != 238 {
		t.Errorf("pixelSize = %d×%d", w, h)
	}
	if w, h := pixelSize(60, 30, CellSize{W: 10, H: 20}); w != 480 || h != 480 {
		t.Errorf("capped pixelSize = %d×%d", w, h)
	}
}

// fakeTerm answers queries like a terminal: reply is what it sends back
// once it has been asked something, in chunks of chunk bytes.
type fakeTerm struct {
	reply   string
	chunk   int
	written bytes.Buffer
	sent    int
}

func (f *fakeTerm) Write(p []byte) (int, error) { return f.written.Write(p) }

func (f *fakeTerm) ReadTimeout(p []byte, d time.Duration) (int, error) {
	if f.written.Len() == 0 || f.sent >= len(f.reply) {
		return 0, nil // nothing asked, or nothing more to say: a timeout
	}
	n := len(f.reply) - f.sent
	if f.chunk > 0 && n > f.chunk {
		n = f.chunk
	}
	n = copy(p, f.reply[f.sent:f.sent+n])
	f.sent += n
	return n, nil
}

func TestQuery(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		chunk int
		want  Probe
	}{
		{"sixel and cell size", "\x1b[6;17;8t\x1b[?62;4;22c", 0, Probe{Answered: true, Sixel: true, Cell: CellSize{W: 8, H: 17}}},
		{"in small pieces", "\x1b[6;20;10t\x1b[?64;1;2;4;6c", 3, Probe{Answered: true, Sixel: true, Cell: CellSize{W: 10, H: 20}}},
		{"no sixel", "\x1b[?62;22c", 0, Probe{Answered: true}},
		{"class 4 is not sixel", "\x1b[?4;6c", 0, Probe{Answered: true}},
		{"silent", "", 0, Probe{}},
		{"cell size zero", "\x1b[6;0;0t\x1b[?1;2c", 0, Probe{Answered: true}},
	}
	for _, c := range cases {
		ft := &fakeTerm{reply: c.reply, chunk: c.chunk}
		got := Query(ft, QueryTimeout)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if ft.written.String() != "\x1b[16t\x1b[c" {
			t.Errorf("%s: asked %q", c.name, ft.written.String())
		}
	}
	if (Query(nil, QueryTimeout) != Probe{}) {
		t.Fatal("nil terminal")
	}
}

func TestDetectFullScreen(t *testing.T) {
	const sixelDA1 = "\x1b[6;18;9t\x1b[?62;4;22c"
	const plainDA1 = "\x1b[?62;22c"
	cases := []struct {
		name    string
		env     map[string]string
		tty     bool
		reply   string // "-" means stdin is not a terminal (nothing to ask)
		want    Protocol
		asked   bool
		wantCel CellSize
	}{
		{"not a tty", map[string]string{"TERM": "xterm-kitty"}, false, plainDA1, ProtoNone, false, CellSize{}},
		{"kitty", map[string]string{"TERM": "xterm-kitty"}, true, plainDA1, ProtoKitty, true, CellSize{}},
		{"ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, true, plainDA1, ProtoKitty, true, CellSize{}},
		{"wezterm uses iterm2", map[string]string{"WEZTERM_PANE": "0", "TERM_PROGRAM": "WezTerm"}, true, sixelDA1, ProtoITerm2, true, CellSize{W: 9, H: 18}},
		{"iterm2", map[string]string{"TERM_PROGRAM": "iTerm.app"}, true, plainDA1, ProtoITerm2, true, CellSize{}},
		{"konsole uses sixel", map[string]string{"KONSOLE_VERSION": "240202", "TERM": "xterm-256color"}, true, sixelDA1, ProtoSixel, true, CellSize{W: 9, H: 18}},
		{"vscode with images", map[string]string{"TERM_PROGRAM": "vscode"}, true, sixelDA1, ProtoITerm2, true, CellSize{W: 9, H: 18}},
		{"vscode without images", map[string]string{"TERM_PROGRAM": "vscode"}, true, plainDA1, ProtoHalfBlock, true, CellSize{}},
		{"sixel from DA1", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, true, sixelDA1, ProtoSixel, true, CellSize{W: 9, H: 18}},
		{"no sixel in DA1", map[string]string{"TERM": "xterm-256color"}, true, plainDA1, ProtoHalfBlock256, true, CellSize{}},
		{"silent terminal", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, true, "", ProtoHalfBlock, true, CellSize{}},
		{"stdin not a terminal", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, true, "-", ProtoHalfBlock, false, CellSize{}},
		{"sixel from TERM without asking", map[string]string{"TERM": "foot"}, true, "-", ProtoSixel, false, CellSize{}},
		{"tmux is not asked", map[string]string{"TMUX": "/tmp/t", "TERM": "screen-256color"}, true, sixelDA1, ProtoHalfBlock256, false, CellSize{}},
		{"forced blocks is not asked", map[string]string{"AUDD_ART": "blocks", "TERM": "xterm-256color"}, true, sixelDA1, ProtoHalfBlock, false, CellSize{}},
		{"forced none", map[string]string{"AUDD_ART": "none", "TERM": "xterm-kitty"}, true, sixelDA1, ProtoNone, false, CellSize{}},
		{"forced sixel asks for the cell size", map[string]string{"AUDD_ART": "sixel"}, true, sixelDA1, ProtoSixel, true, CellSize{W: 9, H: 18}},
		{"forced kitty on wezterm", map[string]string{"AUDD_ART": "kitty", "WEZTERM_PANE": "0"}, true, plainDA1, ProtoKitty, true, CellSize{}},
	}
	for _, c := range cases {
		var term Terminal
		ft := &fakeTerm{reply: c.reply}
		if c.reply != "-" {
			term = ft
		}
		got, probe := DetectFullScreen(func(k string) string { return c.env[k] }, c.tty, term)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if asked := ft.written.Len() > 0; asked != c.asked {
			t.Errorf("%s: asked = %v, want %v", c.name, asked, c.asked)
		}
		if probe.Cell != c.wantCel {
			t.Errorf("%s: cell %+v, want %+v", c.name, probe.Cell, c.wantCel)
		}
	}
}

func TestQueryTimeoutOverSSH(t *testing.T) {
	local := func(string) string { return "" }
	if got := queryTimeout(local); got != QueryTimeout {
		t.Fatalf("local: got %v, want %v", got, QueryTimeout)
	}
	ssh := func(k string) string {
		if k == "SSH_CONNECTION" {
			return "10.0.0.2 51234 10.0.0.1 22"
		}
		return ""
	}
	if got := queryTimeout(ssh); got != RemoteQueryTimeout {
		t.Fatalf("ssh: got %v, want %v", got, RemoteQueryTimeout)
	}
}
