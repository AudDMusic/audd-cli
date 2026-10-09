package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// ResultLine is one item in JSON/JSONL output (type "result").
type ResultLine struct {
	JobID  string          `json:"job_id"`
	Index  int             `json:"index"`
	Input  string          `json:"input"`
	Status string          `json:"status"` // matched, no_match, failed, pending
	Cached bool            `json:"cached"`
	Result json.RawMessage `json:"result"`
	// BudgetLimit is the enterprise limit --max-requests lowered for this
	// file of unknown length: the result covers at most that many
	// 12-second chunks from the start, so it may be partial.
	BudgetLimit int        `json:"budget_limit,omitempty"`
	Error       *LineError `json:"error,omitempty"`
}

// LineError is a failed item's error.
type LineError struct {
	Code      string `json:"code"`
	APICode   int    `json:"api_code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// CSVRow is one CSV row: one per standard result, one per enterprise match.
type CSVRow struct {
	JobID        string `json:"job_id"`
	Index        int    `json:"index"`
	Input        string `json:"input"`
	Status       string `json:"status"`
	Cached       bool   `json:"cached"`
	Artist       string `json:"artist"`
	Title        string `json:"title"`
	Album        string `json:"album"`
	ReleaseDate  string `json:"release_date"`
	Label        string `json:"label"`
	ISRC         string `json:"isrc"`
	UPC          string `json:"upc"`
	SongLink     string `json:"song_link"`
	Timecode     string `json:"timecode"`
	Score        string `json:"score"`
	StartSeconds string `json:"start_seconds"`
	EndSeconds   string `json:"end_seconds"`
	Error        string `json:"error"`
}

// ItemStatus is matched, no_match, failed, or pending.
func ItemStatus(it Item) string {
	switch it.State {
	case StateFailed:
		return StateFailed
	case StatePending:
		return StatePending
	}
	if isEmptyResult(it.Result) {
		return "no_match"
	}
	return "matched"
}

func isEmptyResult(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == "[]"
}

// Line converts an item to its JSON output form.
func Line(jobID string, it Item) ResultLine {
	l := ResultLine{JobID: jobID, Index: it.Index, Input: it.Input.Name(), Status: ItemStatus(it), Cached: it.Cached, Result: it.Result, BudgetLimit: it.BudgetLimit}
	if len(l.Result) == 0 {
		l.Result = json.RawMessage("null")
	}
	if it.State == StateFailed {
		l.Error = &LineError{Code: it.ErrCode, APICode: it.APICode, Message: it.Err, Retryable: it.SafeRetry}
	}
	return l
}

// matches returns the result as a list of match objects (one for the
// standard endpoint, any number for enterprise).
func matches(raw json.RawMessage) []map[string]any {
	if isEmptyResult(raw) {
		return nil
	}
	var list []map[string]any
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one map[string]any
	if json.Unmarshal(raw, &one) == nil && one != nil {
		return []map[string]any{one}
	}
	return nil
}

func str(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// Rows converts an item to CSV rows.
func Rows(jobID string, it Item) []CSVRow {
	base := CSVRow{JobID: jobID, Index: it.Index, Input: it.Input.Name(), Status: ItemStatus(it), Cached: it.Cached, Error: it.Err}
	ms := matches(it.Result)
	if len(ms) == 0 {
		return []CSVRow{base}
	}
	rows := make([]CSVRow, 0, len(ms))
	for _, m := range ms {
		r := base
		r.Artist, r.Title, r.Album = str(m, "artist"), str(m, "title"), str(m, "album")
		r.ReleaseDate, r.Label, r.ISRC, r.UPC = str(m, "release_date"), str(m, "label"), str(m, "isrc"), str(m, "upc")
		r.SongLink, r.Timecode, r.Score = str(m, "song_link"), str(m, "timecode"), str(m, "score")
		r.StartSeconds, r.EndSeconds = str(m, "start_seconds"), str(m, "end_seconds")
		rows = append(rows, r)
	}
	return rows
}

// Describe is a short human summary of an item's outcome.
func Describe(it Item) string {
	switch ItemStatus(it) {
	case StateFailed:
		return "failed: " + it.Err
	case StatePending:
		return "not done yet"
	case "no_match":
		return "no match"
	}
	ms := matches(it.Result)
	song := func(m map[string]any) string {
		s := str(m, "artist") + " — " + str(m, "title")
		if v := str(m, "start_seconds"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				s += " at " + clock(int(f))
			}
		}
		return s
	}
	if len(ms) == 1 {
		return song(ms[0])
	}
	parts := make([]string, 0, 3)
	for i, m := range ms {
		if i == 3 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, song(m))
	}
	return fmt.Sprintf("%d matches: %s", len(ms), strings.Join(parts, ", "))
}

func clock(s int) string {
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// HumanLine writes one item as a line for people.
func HumanLine(w io.Writer, st output.Styles, it Item) {
	mark := st.OK.Render("✓")
	switch ItemStatus(it) {
	case StateFailed:
		mark = st.Err.Render("✗")
	case "no_match", StatePending:
		mark = st.Dim.Render("·")
	}
	desc := Describe(it)
	if it.BudgetLimit > 0 && it.State == StateDone {
		desc += st.Dim.Render(fmt.Sprintf(" (limited to the first %s by --max-requests)", clock(it.BudgetLimit*12)))
	}
	if it.Cached {
		desc += st.Dim.Render(" (cached)")
	}
	fmt.Fprintf(w, "%s %s  %s\n", mark, it.Input.Name(), desc)
}

// MatchFields are the fields of a recognition result (and of each
// enterprise match). A result lacks some of them at times (no ISRC on a
// plan without it, say); --fields accepts them all and prints a missing
// one as null.
var MatchFields = []string{"artist", "title", "album", "release_date", "label", "timecode", "song_link", "isrc", "upc", "score"}

// EnterpriseFields are the fields an enterprise match has besides
// MatchFields.
var EnterpriseFields = []string{"start_offset", "end_offset", "start_seconds", "end_seconds"}

// ProviderBlocks are the optional metadata blocks --return adds.
var ProviderBlocks = []string{"apple_music", "spotify", "deezer", "musicbrainz"}

// ResultFields are the --fields paths under prefix ("result" for a
// standard result, "" for a match row) that recognition output can have:
// the match fields, the enterprise fields, and anything inside a provider
// block.
func ResultFields(prefix string) []string {
	pre := ""
	if prefix != "" {
		pre = prefix + "."
	}
	var out []string
	for _, f := range MatchFields {
		out = append(out, pre+f)
	}
	for _, f := range EnterpriseFields {
		out = append(out, pre+f)
	}
	for _, b := range ProviderBlocks {
		out = append(out, pre+b+".*")
	}
	return out
}

// CheckFields checks --fields for the result lines of a batch before
// anything is sent.
func CheckFields(out *output.Printer) error {
	if out.Format() == output.FormatCSV {
		return out.CheckFieldsFor(CSVRow{})
	}
	return out.CheckFieldsFor(ResultLine{}, ResultFields("result")...)
}

// SingleRows is the CSV rows of one input recognized on its own: the same
// columns as a batch, with no job ID.
func SingleRows(name string, cached bool, raw json.RawMessage) []CSVRow {
	return Rows("", Item{Input: media.Input{Path: name}, State: StateDone, Cached: cached, Result: raw})
}

// emit prints one finished item in the active format. A failure to print
// (a closed stdout, say) stops the run.
func (r *runner) emit(it Item) {
	out := r.a.Out
	var err error
	switch {
	case out.Format() == output.FormatTable && len(out.Fields()) == 0:
		r.mu.Lock()
		HumanLine(out.Stdout(), out.Styles(), it)
		r.mu.Unlock()
	case out.Format() == output.FormatCSV:
		for _, row := range Rows(r.job.ID, it) {
			if err = out.Event("result", row); err != nil {
				break
			}
		}
	default:
		// With --fields, a table shows only those fields of each result.
		err = out.Event("result", Line(r.job.ID, it))
	}
	if err != nil {
		r.stop(err)
	}
}

// DryRunDoc is recognize --dry-run output, the same for one input, a batch,
// and jobs resume:
//
//	{"dry_run":true,"input":"song.mp3"|null,"job_id":"k3x9"|null,
//	 "endpoint":"standard"|"enterprise","plan":{"files","cached_files",
//	 "requests","approximate","cost_usd",...}}
//
// input names the file or URL of a single-input run and is null for
// batches; job_id is the job a batch would resume, null for a single input
// and for a new batch. Piped, both print this document. With --format jsonl
// it is one "type":"event" line with "event":"dry_run", so it is never
// counted as a file's result.
type DryRunDoc struct {
	DryRun   bool    `json:"dry_run"`
	Input    *string `json:"input"`
	JobID    *string `json:"job_id"`
	Endpoint string  `json:"endpoint"`
	Plan     any     `json:"plan"`
}

// PrintDryRun prints a DryRunDoc (input and jobID may be ""), with human for
// people.
func PrintDryRun(out *output.Printer, input, jobID, endpoint string, plan any, human func(w io.Writer)) error {
	doc := DryRunDoc{DryRun: true, Endpoint: endpoint, Plan: plan}
	if input != "" {
		doc.Input = &input
	}
	if jobID != "" {
		doc.JobID = &jobID
	}
	// A batch's automatic JSON lines become one JSON document, as for a
	// single input; an explicit --format jsonl gets an event line.
	out.SetDocument()
	if out.Format() == output.FormatJSONL {
		return out.Event("event", struct {
			Event string `json:"event"`
			DryRunDoc
		}{"dry_run", doc})
	}
	return out.Result(doc, human)
}
