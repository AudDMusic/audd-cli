package tui

import (
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/art"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// cardText is a result card: cover art (when the terminal shows it) next
// to title, artist, album · label · year, and the song link.
func (h *home) cardText(v app.ResultView, details bool, w, ht int) string {
	st := h.st
	title := st.Title.Inherit(st.Accent)
	if c := h.arts.accent(v); c != "" && h.color {
		title = st.Title.Inherit(st.Renderer.NewStyle().Foreground(c))
	}
	text := strings.Join(cardLines(v, details, title, st.Dim, st.Key), "\n")
	if h.arts.proto == art.ProtoNone || v.NoArt {
		return text
	}
	rows := min(cardArtRows, ht)
	cols := h.arts.colsFor(rows)
	if rows < 4 || w-cols-3 < 30 {
		return text
	}
	cover := h.arts.render(v, cols, rows, st.Dim)
	return lipgloss.JoinHorizontal(lipgloss.Top, cover, "   ", fit(text, w-cols-3, max(rows, strings.Count(text, "\n")+1)))
}

// recognition is a recognize (or listen) result document, ready to show.
type recognition struct {
	view    *app.ResultView // one standard match
	text    string          // what to show when there is no card
	link    string          // the song link to open or copy
	matched bool
}

// parseRecognition reads the JSON document of audd recognize or listen.
func parseRecognition(doc map[string]any) recognition {
	if doc == nil {
		return recognition{text: "No result."}
	}
	if doc["enterprise"] == true {
		return enterpriseRecognition(doc)
	}
	raw, _ := json.Marshal(doc["result"])
	cached, _ := doc["cached"].(bool)
	v, ok := api.View(raw, cached)
	if !ok {
		return recognition{text: "No match.\nTry another moment of the file with Clip at, or scan more of it with Enterprise."}
	}
	return recognition{view: &v, link: v.SongLink, matched: true}
}

func enterpriseRecognition(doc map[string]any) recognition {
	var b strings.Builder
	r := recognition{}
	if tracks, ok := doc["tracks"].([]any); ok && len(tracks) > 0 {
		for _, t := range tracks {
			m, _ := t.(map[string]any)
			fmt.Fprintf(&b, "%-17s  %s — %s\n", str(m["start"])+" – "+str(m["end"]), str(m["artist"]), str(m["title"]))
			if r.link == "" {
				r.link = str(m["song_link"])
			}
		}
		r.matched = true
	} else {
		raw, _ := json.Marshal(doc["matches"])
		for _, m := range api.ParseMatches(raw) {
			at := m.Timecode
			if m.StartSeconds != nil {
				at = api.Clock(*m.StartSeconds)
			}
			fmt.Fprintf(&b, "%-8s  %s — %s\n", at, m.Artist, m.Title)
			if r.link == "" {
				r.link = m.SongLink
			}
			r.matched = true
		}
	}
	if !r.matched {
		r.text = "No match in the part scanned.\nRaise the Limit to scan more of the file."
		return r
	}
	if doc["cached"] == true {
		b.WriteString("(cached result, no requests used)\n")
	}
	r.text = strings.TrimRight(b.String(), "\n")
	return r
}

// batchState follows the JSON lines of a batch run (recognize on a
// folder, glob, or list, and jobs resume).
type batchState struct {
	jobID   string
	total   int
	results []map[string]any
	summary map[string]any

	// selectable: when done, the results are a list to move through;
	// detail shows the selected one.
	selectable  bool
	cursor, off int
	detail      bool
	title       string // a heading of its own ("3 matches in mix.mp3")
	inJobs      bool   // shown in Recognitions › Jobs, so it does not point there
}

// selected is the result under the cursor.
func (b *batchState) selected() map[string]any {
	if b == nil || b.cursor < 0 || b.cursor >= len(b.results) {
		return nil
	}
	return b.results[b.cursor]
}

func (b *batchState) move(d int) {
	b.cursor = max(0, min(len(b.results)-1, b.cursor+d))
}

// batchLink is the song link of a batch result line ("" for none).
func batchLink(m map[string]any) string {
	if m == nil {
		return ""
	}
	if o, ok := m["result"].(map[string]any); ok {
		return str(o["song_link"])
	}
	if arr, ok := m["result"].([]any); ok {
		for _, c := range arr {
			o, _ := c.(map[string]any)
			if songs, ok := o["songs"].([]any); ok && len(songs) > 0 {
				o, _ = songs[0].(map[string]any)
			}
			if l := str(o["song_link"]); l != "" {
				return l
			}
		}
	}
	return ""
}

// listView is a finished batch: the summary, then a row per file with
// the cursor (row draws a row, selected or not).
func (b *batchState) listView(st output.Styles, w, ht int, row func(label string, sel bool) string) string {
	var out strings.Builder
	head := b.view(st, w, 0, false)
	if b.title != "" {
		head = st.Bold.Render(b.title) + "\n"
	}
	if head != "" {
		out.WriteString(head + "\n")
	}
	room := max(1, ht-strings.Count(out.String(), "\n"))
	// Finished files are listed in the order of the input, not the order
	// they finished in.
	sort.SliceStable(b.results, func(i, j int) bool {
		return num(b.results[i]["index"]) < num(b.results[j]["index"])
	})
	if b.cursor < b.off {
		b.off = b.cursor
	}
	if b.cursor >= b.off+room {
		b.off = b.cursor - room + 1
	}
	inW := min(32, w/2)
	timed := len(b.results) > 0 && b.results[0]["start"] != nil
	if timed {
		// Matches in one file: where each starts and ends.
		out.WriteString(st.Dim.Render(truncate("  "+padRight("Starts at", 11)+padRight("Ends at", 10)+"Song", w)) + "\n")
		room--
	}
	for i := b.off; i < len(b.results) && i < b.off+room; i++ {
		m := b.results[i]
		label := padRight(truncate(fileName(str(m["input"])), inW-1), inW) + resultSong(m)
		if timed {
			label = padRight(str(m["start"]), 11) + padRight(str(m["end"]), 10) + resultSong(m)
		}
		out.WriteString(truncate(row(label, i == b.cursor), w) + "\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// fileName is the last part of a path (URLs stay whole).
func fileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 && !strings.Contains(name, "://") {
		return name[i+1:]
	}
	return name
}

func (b *batchState) add(line string) {
	if m := parseDoc(line); m != nil {
		b.addDoc(m)
	}
}

func (b *batchState) addDoc(m map[string]any) {
	switch m["type"] {
	case "event":
		if b.jobID == "" {
			b.jobID = str(m["job_id"])
		}
		if n := num(m["to_run"]); n > 0 {
			b.total = int(n)
		}
	case "result":
		if b.jobID == "" {
			b.jobID = str(m["job_id"])
		}
		b.results = append(b.results, m)
	case "summary":
		b.summary = m
		if id := str(m["job_id"]); id != "" {
			b.jobID = id
		}
	}
}

// resultSong is "Artist — Title" for a batch result line.
func resultSong(m map[string]any) string {
	switch m["status"] {
	case "failed":
		if e, ok := m["error"].(map[string]any); ok {
			msg := str(e["message"])
			if strings.HasPrefix(strings.ToLower(msg), "recognition failed") {
				return capFirst(msg)
			}
			return "Failed: " + msg
		}
		return "Failed"
	case "no_match":
		return "no match"
	}
	if arr, ok := m["result"].([]any); ok {
		var songs []string
		add := func(x any) {
			if o, ok := x.(map[string]any); ok && (str(o["artist"]) != "" || str(o["title"]) != "") {
				songs = append(songs, str(o["artist"])+" — "+str(o["title"]))
			}
		}
		for _, c := range arr {
			if o, ok := c.(map[string]any); ok {
				if inner, ok := o["songs"].([]any); ok {
					for _, x := range inner {
						add(x)
					}
					continue
				}
			}
			add(c)
		}
		switch len(songs) {
		case 0:
			return "no match"
		case 1:
			return songs[0]
		}
		return fmt.Sprintf("%s (+%d more)", songs[0], len(songs)-1)
	}
	raw, _ := json.Marshal(m["result"])
	if v, ok := api.View(raw, false); ok {
		return songLine(v)
	}
	return str(m["status"])
}

// view is the batch's progress, or its results when done.
func (b *batchState) view(st output.Styles, w, ht int, running bool) string {
	var out strings.Builder
	done := len(b.results)
	if running {
		head := fmt.Sprintf("Recognizing… %d", done)
		if b.total > 0 {
			head += fmt.Sprintf(" of %d", b.total)
		}
		out.WriteString(st.Bold.Render(head) + "\n")
		if b.total > 0 {
			barW := min(40, w-2)
			n := done * barW / b.total
			out.WriteString(st.Accent.Render(strings.Repeat("█", n)) + st.Dim.Render(strings.Repeat("░", barW-n)) + "\n")
		}
		out.WriteString("\n")
	} else if b.summary != nil {
		s := b.summary
		out.WriteString(st.Bold.Render(fmt.Sprintf("Job %s: %d recognized, %d no match, %d failed",
			b.jobID, int(num(s["recognized"])), int(num(s["no_match"])), int(num(s["failed"])))) + "\n")
		if n := int(num(s["requests_spent_this_run"])); n > 0 || s["requests_spent_this_run"] != nil {
			note := output.Plural(n, "request") + " used in this run."
			if !b.inJobs {
				note += " The job is in Recognitions › Jobs."
			}
			out.WriteString(st.Dim.Render(note) + "\n")
		}
		out.WriteString("\n")
	}
	if ht == 0 {
		// The heading alone (listView draws the rows).
		return strings.TrimRight(out.String(), "\n")
	}
	rows := b.results
	room := max(1, ht-strings.Count(out.String(), "\n")-1)
	if len(rows) > room {
		rows = rows[len(rows)-room:]
	}
	inW := min(32, w/2)
	for _, m := range rows {
		name := str(m["input"])
		if i := strings.LastIndexAny(name, `/\`); i >= 0 && !strings.Contains(name, "://") {
			name = name[i+1:]
		}
		out.WriteString(truncate(padRight(truncate(name, inW-1), inW)+resultSong(m), w) + "\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// tableFromDoc finds the list of objects in a document (items, plays,
// rows, plans, …) and lays it out as a table; ok is false when there is
// none.
func tableFromDoc(doc map[string]any, st output.Styles, w int) (string, bool) {
	var key string
	var list []any
	for k, v := range doc {
		if arr, isArr := v.([]any); isArr && len(arr) > 0 {
			if _, obj := arr[0].(map[string]any); obj {
				if list != nil {
					return "", false // more than one list: use the tree
				}
				key, list = k, arr
			}
		}
	}
	if list == nil {
		return "", false
	}
	return tableOf(list, st, w, key), true
}

// tableOf lays out objects as columns of their scalar fields.
func tableOf(list []any, st output.Styles, w int, title string) string {
	count := map[string]int{}
	var order []string
	for _, it := range list {
		m, _ := it.(map[string]any)
		for k, v := range m {
			switch v.(type) {
			case map[string]any, []any:
				continue
			}
			if count[k] == 0 {
				order = append(order, k)
			}
			count[k]++
		}
	}
	pref := map[string]int{"radio_id": 1, "id": 1, "plan": 1, "name": 2, "artist": 3, "title": 4, "key": 2, "plays": 5, "url": 9}
	sort.SliceStable(order, func(i, j int) bool {
		pi, pj := pref[order[i]], pref[order[j]]
		if pi == 0 {
			pi = 6
		}
		if pj == 0 {
			pj = 6
		}
		if pi != pj {
			return pi < pj
		}
		return order[i] < order[j]
	})
	cells := [][]string{}
	head := make([]string, len(order))
	for i, k := range order {
		head[i] = strings.ToUpper(strings.ReplaceAll(k, "_", " "))
	}
	for _, it := range list {
		m, _ := it.(map[string]any)
		row := make([]string, len(order))
		for i, k := range order {
			if m[k] != nil {
				row[i] = str(m[k])
				if b, ok := m[k].(bool); ok {
					row[i] = fmt.Sprint(b)
				}
			}
		}
		cells = append(cells, row)
	}
	widths := make([]int, len(order))
	for i := range order {
		widths[i] = len([]rune(head[i]))
		for _, r := range cells {
			widths[i] = max(widths[i], len([]rune(r[i])))
		}
		widths[i] = min(widths[i], 40)
	}
	var b strings.Builder
	if title != "" {
		b.WriteString(st.Dim.Render(fmt.Sprintf("%s · %d", title, len(list))) + "\n")
	}
	b.WriteString(truncate(st.Dim.Render(cellsLine(head, widths)), w) + "\n")
	for _, r := range cells {
		b.WriteString(truncate(cellsLine(r, widths), w) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// batchPage is a batch's results: a list to move through, or the details
// of the selected one.
func (h *home) batchPage(bs *batchState, w, ht int) string {
	st := h.st
	if bs.detail {
		if m := bs.selected(); m != nil {
			ds := detailStyles{title: st.Title.Inherit(st.Accent), dim: st.Dim, key: st.Key}
			out := st.Dim.Render(truncate(str(m["input"]), w)) + "\n\n"
			if m["start"] != nil {
				where := "Starts at " + str(m["start"])
				if e := str(m["end"]); e != "" {
					where += ", ends at " + e
				}
				where += " (in the file)"
				out = where + "\n\n"
			}
			if m["result"] != nil && m["status"] != "no_match" {
				raw, _ := json.Marshal(m["result"])
				return out + resultDetail(raw, ds)
			}
			if m["status"] == "failed" {
				// The file's own error, in full.
				return out + styleLines(st.Warn, output.Wrap("Failed: "+capFirst(failMessage(m)), w))
			}
			return out + resultSong(m)
		}
	}
	return bs.listView(st, w, ht, func(label string, sel bool) string { return h.listRow(label, sel) })
}

// batchButtons are the buttons of a batch result's details.
func (h *home) batchButtons(bs *batchState) []button {
	if bs == nil {
		return nil
	}
	link := batchLink(bs.selected())
	if link == "" {
		return nil
	}
	return []button{
		{label: "Open the song link", key: "o", do: func() tea.Cmd {
			return h.openLink(link)
		}},
		{label: "Copy the song link", key: "c", do: func() tea.Cmd { return h.copy(link, "the link "+link) }},
	}
}

// failMessage is the error of a failed batch result.
func failMessage(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		msg := str(e["message"])
		if RetryHelps(str(e["code"])) {
			return msg
		}
		return strings.TrimSuffix(msg, ".") + ". Retrying won't help"
	}
	return "failed"
}
