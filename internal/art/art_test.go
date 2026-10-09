package art

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// fixture is a 4×4 image: red, green, blue, white columns on top; black,
// gray, yellow, transparent on the bottom half.
func fixture() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	top := []color.NRGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 255, 255}}
	bottom := []color.NRGBA{{0, 0, 0, 255}, {128, 128, 128, 255}, {255, 255, 0, 255}, {0, 0, 0, 0}}
	for x := 0; x < 4; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, top[x])
			img.Set(x, y+2, bottom[x])
		}
	}
	return img
}

func TestHalfBlockGolden(t *testing.T) {
	testutil.Golden(t, "halfblock_truecolor", []byte(Render(fixture(), 4, 2, ProtoHalfBlock)))
	testutil.Golden(t, "halfblock_256", []byte(Render(fixture(), 4, 2, ProtoHalfBlock256)))
}

func TestHalfBlockShape(t *testing.T) {
	out := Render(fixture(), 4, 2, ProtoHalfBlock)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(out, "")
	if plain != "▀▀▀▀\n▀▀▀▀" {
		t.Fatalf("cells: %q", plain)
	}
	if !strings.Contains(lines[0], "\x1b[38;2;255;0;0m") {
		t.Fatalf("first cell should be red: %q", lines[0])
	}
	// Downscaling a large image still yields the requested box.
	big := image.NewRGBA(image.Rect(0, 0, 600, 600))
	if got := strings.Count(Render(big, 20, 10, ProtoHalfBlock256), "\n"); got != 9 {
		t.Fatalf("want 10 rows, got %d newlines", got+1)
	}
}

func TestRenderNoneAndEmpty(t *testing.T) {
	if Render(fixture(), 4, 2, ProtoNone) != "" || Render(nil, 4, 2, ProtoHalfBlock) != "" || Render(fixture(), 0, 2, ProtoHalfBlock) != "" {
		t.Fatal("expected empty output")
	}
}

func TestTo256(t *testing.T) {
	cases := map[[3]uint8]int{
		{0, 0, 0}:       16,
		{255, 255, 255}: 231,
		{255, 0, 0}:     196,
		{128, 128, 128}: 244,
		{0, 95, 135}:    24,
	}
	for c, want := range cases {
		if got := To256(c[0], c[1], c[2]); got != want {
			t.Errorf("To256(%v) = %d, want %d", c, got, want)
		}
	}
}

func TestKittyFraming(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 300, 300))
	for i := range big.Pix {
		big.Pix[i] = uint8(i * 7)
	}
	out := Render(big, 20, 10, ProtoKitty)
	re := regexp.MustCompile("\x1b_G([^;]*);([^\x1b]*)\x1b\\\\")
	parts := re.FindAllStringSubmatch(out, -1)
	if len(parts) < 2 {
		t.Fatalf("expected several chunks, got %d", len(parts))
	}
	if re.ReplaceAllString(out, "") != "" {
		t.Fatal("unexpected bytes outside APC sequences")
	}
	if parts[0][1] != "a=T,f=100,q=2,c=20,r=10,m=1" {
		t.Fatalf("first header: %q", parts[0][1])
	}
	var data strings.Builder
	for i, p := range parts {
		if i > 0 {
			want := "m=1"
			if i == len(parts)-1 {
				want = "m=0"
			}
			if p[1] != want {
				t.Fatalf("chunk %d header %q, want %q", i, p[1], want)
			}
		}
		if len(p[2]) > 4096 {
			t.Fatalf("chunk %d too long: %d", i, len(p[2]))
		}
		data.WriteString(p[2])
	}
	raw, err := base64.StdEncoding.DecodeString(data.String())
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 200 || b.Dy() != 200 {
		t.Fatalf("pixel size %v", b)
	}
}

func TestITerm2Framing(t *testing.T) {
	out := Render(fixture(), 8, 4, ProtoITerm2)
	m := regexp.MustCompile("^\x1b]1337;File=inline=1;size=(\\d+);width=8;height=4;preserveAspectRatio=1:([A-Za-z0-9+/=]+)\a$").FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("bad framing: %q", out[:min(80, len(out))])
	}
	raw, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		t.Fatal(err)
	}
	if m[1] != strconv.Itoa(len(raw)) {
		t.Fatalf("size %s, payload %d", m[1], len(raw))
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestSixelFraming(t *testing.T) {
	out := Render(fixture(), 2, 1, ProtoSixel)
	if !strings.HasPrefix(out, "\x1bPq\"1;1;20;18#") || !strings.HasSuffix(out, "-\x1b\\") {
		t.Fatalf("bad framing: %q", out)
	}
	if strings.Count(out, "-") != 3 {
		t.Fatalf("want 3 sixel bands: %q", out)
	}
	// Pure red is palette entry 5*36 = 180.
	if !strings.Contains(out, "#180;2;100;0;0") {
		t.Fatalf("missing red palette entry: %q", out)
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		tty  bool
		want Protocol
	}{
		{"not a tty", map[string]string{"KITTY_WINDOW_ID": "1"}, false, ProtoNone},
		{"dumb", map[string]string{"TERM": "dumb"}, true, ProtoNone},
		{"kitty", map[string]string{"TERM": "xterm-kitty"}, true, ProtoKitty},
		{"kitty window", map[string]string{"KITTY_WINDOW_ID": "3", "TERM": "xterm-256color"}, true, ProtoKitty},
		{"ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, true, ProtoKitty},
		{"ghostty resources", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x"}, true, ProtoKitty},
		{"wezterm", map[string]string{"WEZTERM_PANE": "0"}, true, ProtoKitty},
		{"konsole", map[string]string{"KONSOLE_VERSION": "240202", "TERM": "xterm-256color"}, true, ProtoKitty},
		{"iterm", map[string]string{"TERM_PROGRAM": "iTerm.app"}, true, ProtoITerm2},
		{"iterm over ssh", map[string]string{"LC_TERMINAL": "iTerm2"}, true, ProtoITerm2},
		{"foot", map[string]string{"TERM": "foot"}, true, ProtoSixel},
		{"xterm sixel", map[string]string{"TERM": "xterm-sixel"}, true, ProtoSixel},
		{"tmux keeps cells", map[string]string{"TMUX": "/tmp/x", "KITTY_WINDOW_ID": "1", "COLORTERM": "truecolor"}, true, ProtoHalfBlock},
		{"screen 256", map[string]string{"TERM": "screen-256color"}, true, ProtoHalfBlock256},
		{"truecolor", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, true, ProtoHalfBlock},
		{"apple terminal", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal"}, true, ProtoHalfBlock256},
		{"override none", map[string]string{"AUDD_ART": "none", "TERM": "xterm-kitty"}, true, ProtoNone},
		{"override sixel", map[string]string{"AUDD_ART": "sixel"}, true, ProtoSixel},
		{"override blocks", map[string]string{"AUDD_ART": "blocks", "TERM": "xterm-kitty"}, true, ProtoHalfBlock},
	}
	for _, c := range cases {
		got := Detect(func(k string) string { return c.env[k] }, c.tty)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAccent(t *testing.T) {
	if got := Accent(nil, "e4a64c"); got != "#e4a64c" {
		t.Fatalf("apple bg: %q", got)
	}
	if got := Accent(fixture(), "#E4A64C"); got != "#e4a64c" {
		t.Fatalf("apple bg with #: %q", got)
	}
	if got := Accent(nil, ""); got != "" {
		t.Fatalf("no source: %q", got)
	}
	// Near-black Apple backgrounds are lifted to stay readable.
	if got := Accent(nil, "050505"); got == "#050505" {
		t.Fatal("dark accent was not lifted")
	}
	red := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for i := 0; i < 100; i++ {
		c := color.NRGBA{200, 30, 30, 255}
		if i%10 < 3 {
			c = color.NRGBA{40, 40, 40, 255} // some dark gray
		}
		red.Set(i%10, i/10, c)
	}
	r, g, b, _ := parseHex(string(Accent(red, "")))
	if r < 180 || g > 50 || b > 50 {
		t.Fatalf("dominant color should be red: %d,%d,%d", r, g, b)
	}
}

func TestSourceURL(t *testing.T) {
	cases := []struct{ link, apple, want string }{
		{"https://lis.tn/Abc", "", "https://lis.tn/Abc?thumb"},
		{"https://lis.tn/Abc?x=1", "", "https://lis.tn/Abc?x=1&thumb"},
		{"https://www.youtube.com/watch?v=1", "", ""},
		{"", "", ""},
		{"https://lis.tn/Abc", "https://is1.mzstatic.com/a/{w}x{h}bb.jpg", "https://is1.mzstatic.com/a/300x300bb.jpg"},
		{"", "https://is1.mzstatic.com/a/{w}x{h}{c}.{f}", "https://is1.mzstatic.com/a/300x300bb.jpg"},
	}
	for _, c := range cases {
		if got := SourceURL(c.link, c.apple, 300); got != c.want {
			t.Errorf("SourceURL(%q,%q) = %q, want %q", c.link, c.apple, got, c.want)
		}
	}
}

func TestFetchCachesOnDisk(t *testing.T) {
	testutil.Isolate(t)
	var hits atomic.Int32
	var buf bytes.Buffer
	png.Encode(&buf, fixture())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/art/64x64bb.jpg":
			w.Write(buf.Bytes())
		case "/text/64x64bb.jpg":
			w.Write([]byte("not an image"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		img, err := Fetch(ctx, "", srv.URL+"/art/{w}x{h}bb.jpg", 64)
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != 4 {
			t.Fatalf("bounds %v", img.Bounds())
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("want 1 download, got %d", hits.Load())
	}
	if _, err := Fetch(ctx, "", srv.URL+"/text/{w}x{h}bb.jpg", 64); err == nil {
		t.Fatal("expected a decode error")
	}
	if _, err := Fetch(ctx, "", srv.URL+"/missing/{w}x{h}bb.jpg", 64); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected HTTP 404, got %v", err)
	}
	if _, err := Fetch(ctx, "https://www.youtube.com/watch?v=1", "", 64); !errors.Is(err, ErrNoArt) {
		t.Fatalf("expected ErrNoArt, got %v", err)
	}
}
