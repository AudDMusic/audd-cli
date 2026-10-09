package tui

import (
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/art"
)

func init() {
	app.RenderResult = RenderResultCard
}

// Test seams.
var (
	getenv   = os.Getenv
	fetchArt = art.Fetch
)

// noArtEnv reports whether AUDD_NO_ART turns cover art off. AUDD_ART=none
// does the same through art.Detect.
func noArtEnv() bool {
	v := getenv("AUDD_NO_ART")
	return v != "" && v != "0" && v != "false"
}

// Card art size in cells (square on screen).
const (
	cardArtCols = 20
	cardArtRows = 10
)

// RenderResultCard prints a recognition result for people: cover art next
// to title, artist, album · label · year, and the song link. details adds
// ISRC, UPC, score, timecode, and every metadata provider's link. Art is
// drawn only on a terminal, with color on, and not when r.NoArt is set or
// AUDD_NO_ART / AUDD_ART=none turn it off.
func RenderResultCard(w io.Writer, a *app.App, r app.ResultView, details bool) {
	if a == nil || a.Out == nil {
		app.PlainResult(w, a, r, details)
		return
	}
	if a.Out.Quiet() {
		fmt.Fprintln(w, songLine(r))
		return
	}
	opts := a.Out.Options()
	proto := art.ProtoNone
	if !r.NoArt && !noArtEnv() && !opts.NoColor {
		proto = art.Detect(getenv, opts.StdoutTTY)
	}
	var img image.Image
	if proto != art.ProtoNone && (r.AppleArtwork != "" || art.SourceURL(r.SongLink, "", 0) != "") {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		img, _ = fetchArt(ctx, r.SongLink, r.AppleArtwork, 300)
		cancel()
	}
	st := a.Out.Styles()
	accent := st.Accent
	if img != nil || r.AppleBG != "" {
		if c := art.Accent(img, r.AppleBG); c != "" && !opts.NoColor {
			accent = st.Renderer.NewStyle().Foreground(c)
		}
	}
	lines := cardLines(r, details, st.Title.Inherit(accent), st.Dim, st.Key)
	if img == nil {
		fmt.Fprintln(w, strings.Join(lines, "\n"))
		return
	}
	pic := art.Render(img, cardArtCols, cardArtRows, proto)
	if !proto.IsGraphics() {
		text := strings.Join(lines, "\n")
		joined := strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, pic, "  ", text), "\n")
		for i := range joined {
			joined[i] = strings.TrimRight(joined[i], " ")
		}
		fmt.Fprintln(w, strings.Join(joined, "\n"))
		return
	}
	// Reserve the rows, draw the image at the top left, and print the text
	// to its right, so it works whichever way the terminal moves the cursor
	// after an image.
	var b strings.Builder
	b.WriteString(strings.Repeat("\n", cardArtRows))
	fmt.Fprintf(&b, "\x1b[%dA\x1b7%s\x1b8", cardArtRows, pic)
	for _, l := range lines {
		fmt.Fprintf(&b, "\r\x1b[%dC%s\n", cardArtCols+2, l)
	}
	if n := cardArtRows - len(lines); n > 0 {
		b.WriteString(strings.Repeat("\n", n))
	}
	io.WriteString(w, b.String())
}

func cardLines(r app.ResultView, details bool, title, dim, key lipgloss.Style) []string {
	var lines []string
	if r.Title != "" {
		lines = append(lines, title.Render(r.Title))
	}
	if r.Artist != "" {
		lines = append(lines, r.Artist)
	}
	if m := metaLine(r); m != "" {
		lines = append(lines, dim.Render(m))
	}
	if r.SongLink != "" {
		lines = append(lines, "", r.SongLink)
	}
	if details {
		var rows [][2]string
		for _, kv := range [][2]string{{"ISRC", r.ISRC}, {"UPC", r.UPC}, {"Released", r.ReleaseDate}, {"Timecode", r.Timecode}} {
			if kv[1] != "" {
				rows = append(rows, kv)
			}
		}
		if r.Score > 0 {
			rows = append(rows, [2]string{"Score", fmt.Sprint(r.Score)})
		}
		rows = append(rows, providerLinks(r.Extra)...)
		if len(rows) > 0 {
			width := 12 // at least one space after the longest key
			for _, kv := range rows {
				width = max(width, len(kv[0])+1)
			}
			lines = append(lines, "")
			for _, kv := range rows {
				lines = append(lines, key.Render(fmt.Sprintf("%-*s", width, kv[0]))+kv[1])
			}
		}
	}
	if r.Cached {
		lines = append(lines, dim.Render("Cached result, no request used"))
	}
	return lines
}

// providerLinks pulls a link (or ID) from each metadata provider block.
func providerLinks(extra map[string]any) [][2]string {
	var out [][2]string
	get := func(block, field string) string {
		if m, ok := extra[block].(map[string]any); ok {
			return str(m[field])
		}
		return ""
	}
	if u := get("apple_music", "url"); u != "" {
		out = append(out, [2]string{"Apple Music", u})
	}
	if sp, ok := extra["spotify"].(map[string]any); ok {
		u := ""
		if ext, ok := sp["external_urls"].(map[string]any); ok {
			u = str(ext["spotify"])
		}
		if u == "" {
			u = str(sp["uri"])
		}
		if u != "" {
			out = append(out, [2]string{"Spotify", u})
		}
	}
	if u := get("deezer", "link"); u != "" {
		out = append(out, [2]string{"Deezer", u})
	}
	if mb, ok := extra["musicbrainz"].([]any); ok && len(mb) > 0 {
		if m, ok := mb[0].(map[string]any); ok && str(m["id"]) != "" {
			out = append(out, [2]string{"MusicBrainz", "https://musicbrainz.org/recording/" + str(m["id"])})
		}
	}
	// Other scalar fields the API added, so nothing is hidden.
	var keys []string
	for k, v := range extra {
		switch v.(type) {
		case string, float64, bool:
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s := str(extra[k]); s != "" {
			out = append(out, [2]string{k, s})
		} else if b, ok := extra[k].(bool); ok {
			out = append(out, [2]string{k, fmt.Sprint(b)})
		}
	}
	return out
}
