// Package tui is the terminal interface: the result card printed after a
// recognition, the interactive explorer (audd browse), and the now-playing
// screen. Every screen also has a plain, non-interactive output so it works
// when piped.
//
// Integration hooks, assigned where the stream store, results cache, and
// jobs store are wired in:
//
//   - NewFeed must return a StoreFeed backed by internal/streamstore and
//     streams.RecentResults. The default only lists stations, so
//     now-playing reports that stream results are not available.
//   - NewExplorerData must extend DefaultExplorerData with Recent
//     (cache.Recent) and Jobs/JobItems (jobs.List and the job's results).
package tui

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
)

// ViewFromJSON flattens one AudD song object (a recognition result, a stream
// callback song, or a cached result) into a ResultView. Parsing is lenient:
// missing or wrong-typed fields are left empty. It also returns the track
// length when Apple Music (or Spotify or Deezer) metadata carries it. ok is
// false when raw is not an object or has neither artist nor title.
func ViewFromJSON(raw json.RawMessage) (v app.ResultView, trackLength time.Duration, ok bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return v, 0, false
	}
	// Accept wrappers: {"result": {...}} and stream callbacks {"results": [...]}.
	if inner, isObj := m["result"].(map[string]any); isObj {
		m = inner
	} else if arr, isArr := m["results"].([]any); isArr && len(arr) > 0 {
		if inner, isObj := arr[0].(map[string]any); isObj {
			m = inner
		}
	}
	v = app.ResultView{
		Artist:      str(m["artist"]),
		Title:       str(m["title"]),
		Album:       str(m["album"]),
		Label:       str(m["label"]),
		ReleaseDate: str(m["release_date"]),
		ISRC:        str(m["isrc"]),
		UPC:         str(m["upc"]),
		SongLink:    str(m["song_link"]),
		Timecode:    str(m["timecode"]),
		Score:       int(num(m["score"])),
	}
	if c, isBool := m["cached"].(bool); isBool {
		v.Cached = c
	}
	if am, isObj := m["apple_music"].(map[string]any); isObj {
		if aw, isObj := am["artwork"].(map[string]any); isObj {
			v.AppleArtwork = str(aw["url"])
			v.AppleBG = str(aw["bgColor"])
		}
		if ms := num(am["durationInMillis"]); ms > 0 {
			trackLength = time.Duration(ms) * time.Millisecond
		}
	}
	if trackLength == 0 {
		if sp, isObj := m["spotify"].(map[string]any); isObj {
			if ms := num(sp["duration_ms"]); ms > 0 {
				trackLength = time.Duration(ms) * time.Millisecond
			}
		}
	}
	if trackLength == 0 {
		if dz, isObj := m["deezer"].(map[string]any); isObj {
			if s := num(dz["duration"]); s > 0 {
				trackLength = time.Duration(s) * time.Second
			}
		}
	}
	extra := map[string]any{}
	for k, val := range m {
		switch k {
		case "artist", "title", "album", "label", "release_date", "isrc", "upc", "song_link", "timecode", "score", "cached":
		default:
			extra[k] = val
		}
	}
	if len(extra) > 0 {
		v.Extra = extra
	}
	return v, trackLength, v.Artist != "" || v.Title != ""
}

// ViewJSON is the snake_case form of a ResultView for JSON output when the
// original API object is not at hand.
func ViewJSON(v app.ResultView) map[string]any {
	m := map[string]any{}
	for k, val := range v.Extra {
		m[k] = val
	}
	set := func(k, val string) {
		if val != "" {
			m[k] = val
		}
	}
	set("artist", v.Artist)
	set("title", v.Title)
	set("album", v.Album)
	set("label", v.Label)
	set("release_date", v.ReleaseDate)
	set("isrc", v.ISRC)
	set("upc", v.UPC)
	set("song_link", v.SongLink)
	set("timecode", v.Timecode)
	if v.Score > 0 {
		m["score"] = v.Score
	}
	return m
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	}
	return ""
}

func num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err == nil {
			return f
		}
	}
	return 0
}

// songLine is "Artist — Title", or whichever half is known.
func songLine(v app.ResultView) string {
	switch {
	case v.Artist != "" && v.Title != "":
		return v.Artist + " — " + v.Title
	case v.Title != "":
		return v.Title
	}
	return v.Artist
}

// metaLine is "Album · Label · Year" with empty parts left out.
func metaLine(v app.ResultView) string {
	var parts []string
	for _, s := range []string{v.Album, v.Label, year(v.ReleaseDate)} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

func year(date string) string {
	if len(date) >= 4 {
		if _, err := strconv.Atoi(date[:4]); err == nil {
			return date[:4]
		}
	}
	return date
}

// clock formats a duration as m:ss or h:mm:ss.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// ago is a short "3 min ago" style age.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}

// localZone is the zone clock times are shown in (tests use UTC).
var localZone = time.Local
