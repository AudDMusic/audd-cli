package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func testImage() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.NRGBA{uint8(x * 30), uint8(y * 30), 120, 255})
		}
	}
	return img
}

// withEnv sets the environment seen by the tui package for one test.
func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	old := getenv
	getenv = func(k string) string { return env[k] }
	t.Cleanup(func() { getenv = old })
}

// withArt makes fetchArt return img and counts calls.
func withArt(t *testing.T, img image.Image) *int {
	t.Helper()
	calls := 0
	var mu sync.Mutex
	old := fetchArt
	fetchArt = func(ctx context.Context, songLink, apple string, size int) (image.Image, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return img, nil
	}
	t.Cleanup(func() { fetchArt = old })
	return &calls
}

func testApp(out *bytes.Buffer, opts output.PrinterOptions) *app.App {
	a := app.New()
	if opts.Format == "" {
		opts.Format = output.FormatTable
	}
	a.Out = output.NewPrinter(out, &bytes.Buffer{}, opts)
	a.Now = func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }
	return a
}

func sampleView() app.ResultView {
	raw := json.RawMessage(`{"artist":"Imagine Dragons","title":"Warriors","album":"Warriors","release_date":"2014-09-18","label":"Universal Music","timecode":"00:40","song_link":"https://lis.tn/Warriors","isrc":"USUM71414186","upc":"00602547034463","score":100,
	 "apple_music":{"url":"https://music.apple.com/us/album/warriors/1440831203?i=1440831624","durationInMillis":170667,"artwork":{"url":"https://is1-ssl.mzstatic.com/image/thumb/x/{w}x{h}bb.jpg","bgColor":"e4a64c"}},
	 "spotify":{"uri":"spotify:track:1lgN0A2Vki2FTON5PYq42m"}}`)
	v, _, ok := ViewFromJSON(raw)
	if !ok {
		panic("sample")
	}
	return v
}

func TestCardTextOnly(t *testing.T) {
	withEnv(t, map[string]string{"AUDD_ART": "none"})
	calls := withArt(t, testImage())
	var out bytes.Buffer
	a := testApp(&out, output.PrinterOptions{StdoutTTY: true})
	RenderResultCard(&out, a, sampleView(), false)
	testutil.Golden(t, "card_plain", out.Bytes())
	out.Reset()
	RenderResultCard(&out, a, sampleView(), true)
	testutil.Golden(t, "card_details", out.Bytes())
	if *calls != 0 {
		t.Fatalf("art fetched with AUDD_ART=none")
	}
}

func TestCardHalfBlockArt(t *testing.T) {
	withEnv(t, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"})
	withArt(t, testImage())
	var out bytes.Buffer
	a := testApp(&out, output.PrinterOptions{StdoutTTY: true})
	RenderResultCard(&out, a, sampleView(), false)
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != cardArtRows {
		t.Fatalf("want %d lines, got %d:\n%s", cardArtRows, len(lines), out.String())
	}
	if !strings.Contains(lines[0], "▀") || !strings.Contains(lines[0], "Warriors") {
		t.Fatalf("art and title should share the first line: %q", lines[0])
	}
	testutil.Golden(t, "card_halfblock", out.Bytes())
}

func TestCardKittyArt(t *testing.T) {
	withEnv(t, map[string]string{"TERM": "xterm-kitty"})
	withArt(t, testImage())
	var out bytes.Buffer
	a := testApp(&out, output.PrinterOptions{StdoutTTY: true})
	RenderResultCard(&out, a, sampleView(), false)
	s := out.String()
	if !strings.HasPrefix(s, strings.Repeat("\n", cardArtRows)+"\x1b[10A\x1b7\x1b_Ga=T,f=100,q=2,c=20,r=10") {
		t.Fatalf("bad prefix: %q", s[:40])
	}
	if !strings.Contains(s, "\x1b8\r\x1b[22CWarriors\n\r\x1b[22CImagine Dragons\n") {
		t.Fatalf("text should be placed right of the image: %q", s[len(s)-200:])
	}
}

func TestCardNoArtCases(t *testing.T) {
	withEnv(t, map[string]string{"TERM": "xterm-kitty"})
	calls := withArt(t, testImage())
	cases := []struct {
		name string
		opts output.PrinterOptions
		off  bool
	}{
		{"not a terminal", output.PrinterOptions{StdoutTTY: false}, false},
		{"no color", output.PrinterOptions{StdoutTTY: true, NoColor: true}, false},
		{"--no-art", output.PrinterOptions{StdoutTTY: true}, true},
	}
	for _, c := range cases {
		var out bytes.Buffer
		v := sampleView()
		v.NoArt = c.off
		RenderResultCard(&out, testApp(&out, c.opts), v, false)
		if strings.Contains(out.String(), "\x1b_G") || *calls != 0 {
			t.Errorf("%s: art drawn", c.name)
		}
		if !strings.Contains(out.String(), "Warriors") || !strings.Contains(out.String(), "\nImagine Dragons\n") {
			t.Errorf("%s: want the text card, got %q", c.name, out.String())
		}
	}
}

func TestCardQuietAndHookAssigned(t *testing.T) {
	var out bytes.Buffer
	a := testApp(&out, output.PrinterOptions{StdoutTTY: true, Quiet: true})
	RenderResultCard(&out, a, sampleView(), true)
	if out.String() != "Imagine Dragons — Warriors\n" {
		t.Fatalf("quiet: %q", out.String())
	}
	out.Reset()
	withEnv(t, map[string]string{"AUDD_ART": "none"})
	app.RenderResult(&out, testApp(&out, output.PrinterOptions{NoColor: true}), sampleView(), false)
	if !strings.HasPrefix(out.String(), "Warriors\nImagine Dragons\n") {
		t.Fatalf("app.RenderResult should be the card: %q", out.String())
	}
}

func TestViewFromJSONLenient(t *testing.T) {
	v, length, ok := ViewFromJSON(json.RawMessage(`{"artist":"A","title":7,"score":"88","apple_music":{"durationInMillis":"200000"},"extra_field":"x"}`))
	if !ok || v.Artist != "A" || v.Title != "7" || v.Score != 88 || length != 200*time.Second || v.Extra["extra_field"] != "x" {
		t.Fatalf("%+v %v %v", v, length, ok)
	}
	if _, _, ok := ViewFromJSON(json.RawMessage(`[1,2]`)); ok {
		t.Fatal("array accepted")
	}
	v, _, ok = ViewFromJSON(json.RawMessage(`{"radio_id":3,"results":[{"artist":"B","title":"C","deezer":{"duration":180}}]}`))
	if !ok || v.Artist != "B" {
		t.Fatalf("callback shape: %+v", v)
	}
}

func TestCardDetailsLongKeysStaySeparated(t *testing.T) {
	withEnv(t, map[string]string{"AUDD_ART": "none"})
	var out bytes.Buffer
	a := testApp(&out, output.PrinterOptions{StdoutTTY: true})
	v := sampleView()
	v.Extra = map[string]any{"release_date_extra": "x"}
	RenderResultCard(&out, a, v, true)
	if !strings.Contains(out.String(), "release_date_extra x") {
		t.Fatalf("long keys need a separator:\n%s", out.String())
	}
}
