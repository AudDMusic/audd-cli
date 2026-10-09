package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// CSV rows for now-playing --once and browse: one flat row per song, so a
// spreadsheet gets a column per field instead of JSON in a cell.

// songCSV is the song part of a CSV row.
type songCSV struct {
	Artist      string `json:"artist"`
	Title       string `json:"title"`
	Album       string `json:"album"`
	Label       string `json:"label"`
	ReleaseDate string `json:"release_date"`
	ISRC        string `json:"isrc"`
	UPC         string `json:"upc"`
	SongLink    string `json:"song_link"`
}

func songOf(v app.ResultView) songCSV {
	return songCSV{Artist: v.Artist, Title: v.Title, Album: v.Album, Label: v.Label,
		ReleaseDate: v.ReleaseDate, ISRC: v.ISRC, UPC: v.UPC, SongLink: v.SongLink}
}

// stationCSV is one stream's current song.
type stationCSV struct {
	RadioID        int    `json:"radio_id"`
	URL            string `json:"url"`
	StreamRunning  string `json:"stream_running"` // true, false, or empty when unknown
	Health         string `json:"health"`
	State          string `json:"state"`
	Playing        string `json:"playing"`
	Timestamp      string `json:"timestamp"`
	ElapsedSeconds string `json:"elapsed_seconds"`
	PlayedSeconds  string `json:"played_seconds"`
	EndedAt        string `json:"ended_at"`
	AgoSeconds     string `json:"ago_seconds"`
	LengthSeconds  string `json:"length_seconds"`
	songCSV
	Error string `json:"error"`
}

func stationRows(data []stationData, now time.Time) []stationCSV {
	rows := make([]stationCSV, 0, len(data))
	for _, d := range data {
		r := stationCSV{RadioID: d.RadioID, URL: d.URL}
		if !d.StatusUnknown {
			r.StreamRunning = boolText(d.Running)
		}
		if d.Health != nil && d.Health.Code != 0 {
			r.Health = d.Health.Message
		}
		if d.Err != nil {
			r.Error = output.AsError(d.Err).Message
		}
		if len(d.Plays) > 0 {
			p := d.Plays[0]
			st := p.State(now)
			r.State, r.Playing = string(st), boolText(st == StatePlaying)
			if !p.At.IsZero() {
				r.Timestamp = p.At.UTC().Format(time.RFC3339)
			}
			if st == StatePlaying {
				r.ElapsedSeconds = strconv.Itoa(secs(p.Elapsed(now)))
			} else {
				r.AgoSeconds = strconv.Itoa(secs(p.Ago(now)))
			}
			if end, ok := p.Ended(); ok {
				r.PlayedSeconds = strconv.Itoa(p.PlayLength)
				r.EndedAt = end.UTC().Format(time.RFC3339)
			}
			if p.TrackLength > 0 {
				r.LengthSeconds = strconv.Itoa(secs(p.TrackLength))
			}
			r.songCSV = songOf(p.ResultView)
		}
		rows = append(rows, r)
	}
	return rows
}

// recentCSV is one song recognized with audd; an enterprise result gives a
// row per match.
type recentCSV struct {
	Source string `json:"source"`
	At     string `json:"at"`
	Status string `json:"status"` // matched or no_match
	// Position is where in the file an enterprise match is.
	Position string `json:"position"`
	songCSV
}

func recentCSVRows(items []RecentItem) []recentCSV {
	rows := make([]recentCSV, 0, len(items))
	for _, it := range items {
		base := recentCSV{Source: it.Source, Status: "no_match"}
		if !it.At.IsZero() {
			base.At = it.At.UTC().Format(time.RFC3339)
		}
		for _, r := range songRows(it.Result) {
			row := base
			row.Status, row.Position, row.songCSV = "matched", r.at, songOf(r.view)
			rows = append(rows, row)
		}
		if len(songRows(it.Result)) == 0 {
			rows = append(rows, base)
		}
	}
	return rows
}

// jobItemCSV is one input of a job; an enterprise result gives a row per
// match.
type jobItemCSV struct {
	Job      string `json:"job_id"`
	Index    int    `json:"index"`
	Input    string `json:"input"`
	State    string `json:"state"`
	Position string `json:"position"`
	songCSV
	Error string `json:"error"`
}

func jobItemCSVRows(job string, items []JobItem) []jobItemCSV {
	rows := make([]jobItemCSV, 0, len(items))
	for _, it := range items {
		base := jobItemCSV{Job: job, Index: it.Index, Input: it.Input, State: it.State, Error: it.Err}
		songs := songRows(it.Result)
		for _, r := range songs {
			row := base
			row.Position, row.songCSV = r.at, songOf(r.view)
			rows = append(rows, row)
		}
		if len(songs) == 0 {
			rows = append(rows, base)
		}
	}
	return rows
}

// jobCSV is a batch job.
type jobCSV struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Created string `json:"created"`
	Command string `json:"command"`
}

func jobCSVRows(jobs []JobRow) []jobCSV {
	rows := make([]jobCSV, 0, len(jobs))
	for _, j := range jobs {
		r := jobCSV{ID: j.ID, Status: j.Status, Total: j.Total, Done: j.Done, Failed: j.Failed, Command: strings.Join(j.Command, " ")}
		if !j.Created.IsZero() {
			r.Created = j.Created.UTC().Format(time.RFC3339)
		}
		rows = append(rows, r)
	}
	return rows
}

// songRows is every song in a stored result: one for a standard result,
// one per match for enterprise, none for no match.
func songRows(raw json.RawMessage) []match {
	_, matches, single := resultSummary(raw)
	if single {
		v, _, _ := ViewFromJSON(raw)
		return []match{{view: v}}
	}
	return matches
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
