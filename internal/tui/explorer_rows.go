package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/AudDMusic/audd-cli/internal/app"
)

// column is a list column; width 0 takes the remaining space.
type column struct {
	title string
	width int
}

// row is one line of a list, with what the shared keys need.
type row struct {
	key    string            // stable identity, keeps the cursor across reloads
	cells  []string          // one per column
	search string            // lower-cased text the filter matches
	link   string            // o opens it, c copies it
	copies map[string]string // extra copy targets: "i" ISRC, "u" UPC
	detail func(st detailStyles) string
	view   *app.ResultView // the song whose cover the detail shows
	data   any             // exported with e
}

type detailStyles struct {
	title, dim, key lipgloss.Style
}

func newRow(key string, cells ...string) row {
	return row{key: key, cells: cells, search: strings.ToLower(strings.Join(cells, " "))}
}

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(localZone).Format("Jan 02 15:04")
}

// --- recognition results ---

// match is one song found in a result, with its position when known.
type match struct {
	at   string
	view app.ResultView
}

// resultSummary describes any stored result: a single song, an enterprise
// result (chunks of songs), a tracklist, or no match.
func resultSummary(raw json.RawMessage) (headline string, matches []match, single bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "No match", nil, false
	}
	if strings.HasPrefix(trimmed, "{") {
		v, _, ok := ViewFromJSON(raw)
		if ok {
			return songLine(v), nil, true
		}
		// {"result": null} and similar.
		return "No match", nil, false
	}
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) != nil {
		return "Unreadable result", nil, false
	}
	for _, chunk := range arr {
		songs, isChunk := chunk["songs"].([]any)
		if !isChunk {
			songs = []any{chunk}
		}
		for _, s := range songs {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			b, _ := json.Marshal(m)
			v, _, ok := ViewFromJSON(b)
			if !ok {
				continue
			}
			at := str(m["timecode"])
			if st := str(m["start_seconds"]); st != "" {
				at = clock(time.Duration(num(m["start_seconds"])*float64(time.Second))) + "–" +
					clock(time.Duration(num(m["end_seconds"])*float64(time.Second)))
			} else if off := str(chunk["offset"]); off != "" && isChunk {
				at = off
				if tc := str(m["timecode"]); tc != "" {
					at += " +" + tc
				}
			}
			matches = append(matches, match{at: at, view: v})
		}
	}
	switch len(matches) {
	case 0:
		return "No match", nil, false
	case 1:
		return songLine(matches[0].view), matches, false
	}
	return fmt.Sprintf("%d matches", len(matches)), matches, false
}

// singleView is the song of a single-song result, or nil.
func singleView(raw json.RawMessage) *app.ResultView {
	if _, _, single := resultSummary(raw); !single {
		return nil
	}
	v, _, _ := ViewFromJSON(raw)
	return &v
}

// resultDetail renders a stored result for the detail pane.
func resultDetail(raw json.RawMessage, st detailStyles) string {
	if _, matches, single := resultSummary(raw); single {
		v, length, _ := ViewFromJSON(raw)
		lines := cardLines(v, true, st.title, st.dim, st.key)
		if length > 0 {
			lines = append(lines, st.key.Render(fmt.Sprintf("%-12s", "Length"))+clock(length))
		}
		return strings.Join(lines, "\n")
	} else if len(matches) > 0 {
		var b strings.Builder
		for _, m := range matches {
			fmt.Fprintf(&b, "%s  %s", st.dim.Render(fmt.Sprintf("%-15s", m.at)), songLine(m.view))
			if m.view.Album != "" {
				b.WriteString(st.dim.Render("  " + m.view.Album))
			}
			b.WriteByte('\n')
		}
		return strings.TrimRight(b.String(), "\n")
	}
	return st.dim.Render("No match.")
}

func shortSource(s string) string {
	if strings.Contains(s, "://") {
		return s
	}
	return filepath.Base(s)
}

func recentRows(items []RecentItem) []row {
	rows := make([]row, 0, len(items))
	for i, it := range items {
		head, _, _ := resultSummary(it.Result)
		v, _, _ := ViewFromJSON(it.Result)
		r := newRow(fmt.Sprintf("%d:%s", i, it.Source), when(it.At), head, v.Album, shortSource(it.Source))
		r.search += " " + strings.ToLower(it.Source+" "+v.ISRC+" "+v.UPC+" "+v.Label)
		r.link = v.SongLink
		r.copies = map[string]string{"i": v.ISRC, "u": v.UPC}
		r.view = singleView(it.Result)
		raw := it.Result
		r.detail = func(st detailStyles) string {
			return st.dim.Render(it.Source) + "\n\n" + resultDetail(raw, st)
		}
		r.data = it
		rows = append(rows, r)
	}
	return rows
}

var recentCols = []column{{"When", 12}, {"Song", 0}, {"Album", 24}, {"Source", 24}}

// --- jobs ---

var jobCols = []column{{"Job", 10}, {"Status", 11}, {"Done", 11}, {"Failed", 6}, {"Created", 12}, {"Command", 0}}

func jobRows(jobs []JobRow) []row {
	rows := make([]row, 0, len(jobs))
	for _, j := range jobs {
		r := newRow(j.ID, j.ID, j.Status, fmt.Sprintf("%d/%d", j.Done, j.Total), fmt.Sprint(j.Failed), when(j.Created), strings.Join(j.Command, " "))
		r.data = j
		rows = append(rows, r)
	}
	return rows
}

var jobItemCols = []column{{"#", 5}, {"State", 8}, {"Input", 30}, {"Result", 0}}

func jobItemRows(items []JobItem) []row {
	rows := make([]row, 0, len(items))
	for _, it := range items {
		res := ""
		switch {
		case it.Err != "":
			res = "Error: " + it.Err
		case len(it.Result) > 0:
			res, _, _ = resultSummary(it.Result)
		}
		r := newRow(fmt.Sprint(it.Index), fmt.Sprint(it.Index+1), it.State, shortSource(it.Input), res)
		v, _, _ := ViewFromJSON(it.Result)
		r.link = v.SongLink
		r.copies = map[string]string{"i": v.ISRC, "u": v.UPC}
		r.view = singleView(it.Result)
		it := it
		r.detail = func(st detailStyles) string {
			var b strings.Builder
			b.WriteString(st.dim.Render(it.Input) + "\n" + st.key.Render("State       ") + it.State + "\n")
			if it.Err != "" {
				b.WriteString(st.key.Render("Error       ") + it.Err + "\n")
			}
			if len(it.Result) > 0 {
				b.WriteString("\n" + resultDetail(it.Result, st))
			}
			return strings.TrimRight(b.String(), "\n")
		}
		r.data = it
		rows = append(rows, r)
	}
	return rows
}

// --- streams ---

var streamCols = []column{{"ID", 4}, {"Health", 19}, {"Latest song", 0}, {"When", 17}, {"URL", 22}}

func streamRows(data []stationData, now time.Time) []row {
	rows := make([]row, 0, len(data))
	for _, d := range data {
		song, since := "—", ""
		var link string
		if len(d.Plays) > 0 {
			p := d.Plays[0]
			song = songLine(p.ResultView)
			since = whenShort(p, now)
			link = p.SongLink
		}
		if d.Err != nil && len(d.Plays) == 0 {
			song = "Could not read results: " + d.Err.Error()
		}
		r := newRow(fmt.Sprint(d.RadioID), fmt.Sprint(d.RadioID), d.healthShort(), song, since, d.URL)
		r.link = link
		r.data = stationDoc(d, now)
		rows = append(rows, r)
	}
	return rows
}

var playCols = []column{{"Time", 12}, {"Song", 0}, {"Album", 28}, {"Played", 7}, {"Length", 7}}

// whenShort is the "When" cell of a stream: "now · 1:23" while a song
// plays, "ended 2 min ago" for a result sent when the song ended, else the
// age of the result.
func whenShort(p Play, now time.Time) string {
	switch {
	case p.State(now) == StatePlaying:
		if p.TrackLength > 0 {
			return "now · " + clock(p.Elapsed(now)) + " / " + clock(p.TrackLength)
		}
		return "now · " + clock(p.Elapsed(now))
	case p.PlayLength > 0:
		return "ended " + ago(p.Ago(now))
	}
	return ago(p.Ago(now))
}

func playRows(plays []Play) []row {
	rows := make([]row, 0, len(plays))
	for _, p := range plays {
		played, length := "", ""
		if p.PlayLength > 0 {
			played = clock(p.Played())
		}
		if p.TrackLength > 0 {
			length = clock(p.TrackLength)
		}
		r := newRow(p.key(), when(p.At), songLine(p.ResultView), p.Album, played, length)
		r.search += " " + strings.ToLower(p.Label+" "+p.ISRC)
		r.link = p.SongLink
		r.copies = map[string]string{"i": p.ISRC, "u": p.UPC}
		r.view = &p.ResultView
		p := p
		r.detail = func(st detailStyles) string {
			head := st.dim.Render(fmt.Sprintf("Stream %d · %s", p.RadioID, p.At.In(localZone).Format("2006-01-02 15:04:05")))
			lines := cardLines(p.ResultView, true, st.title, st.dim, st.key)
			if p.PlayLength > 0 {
				lines = append(lines, st.key.Render(fmt.Sprintf("%-12s", "Played"))+clock(p.Played()))
			}
			if p.TrackLength > 0 {
				lines = append(lines, st.key.Render(fmt.Sprintf("%-12s", "Length"))+clock(p.TrackLength))
			}
			return head + "\n\n" + strings.Join(lines, "\n")
		}
		r.data = p.JSON()
		rows = append(rows, r)
	}
	return rows
}
