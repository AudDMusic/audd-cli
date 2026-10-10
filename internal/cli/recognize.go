package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cache"
	"github.com/AudDMusic/audd-cli/internal/jobs"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/safety"
)

// maxStandardBytes is the largest file the standard endpoint accepts.
const maxStandardBytes = 10 << 20

// standardSeconds is how much audio the standard endpoint analyzes.
const standardSeconds = 12

// Media helpers recognize needs from internal/media (ffmpeg and ffprobe).
// They are assigned where the media tools are built in; until then --at
// reports that trimming is unavailable and lengths are estimated from file
// sizes.
var (
	// trimMedia cuts dur of audio starting at `at` out of src (a file path
	// or URL) into a temporary file; cleanup deletes it.
	trimMedia = func(ctx context.Context, src string, at, dur time.Duration) (path string, cleanup func(), err error) {
		return "", func() {}, app.NotImplemented("trimming with --at")
	}
	// probeDuration returns a media file's length when ffprobe can read it.
	probeDuration = func(path string) (time.Duration, bool) { return 0, false }
)

type recognizeFlags struct {
	enterprise             bool
	limit, maxFiles        string
	at, duration           string
	tracklist              bool
	ret                    string
	skip, every, skipFirst int
	useTimecode, accurate  bool
	details, browse, noArt bool
	failOnNoMatch, dryRun  bool
	noCache                bool
	concurrency            int
	resume, startNew       bool
	// inputName replaces the input's name in the output (audd listen
	// reports its recording as "microphone").
	inputName string
}

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newRecognizeCmd(a))
	})
}

func newRecognizeCmd(a *app.App) *cobra.Command {
	f := &recognizeFlags{}
	cmd := &cobra.Command{
		Use:     "recognize <file|url|dir|glob|->",
		Short:   "Identify music in files, URLs, and folders",
		GroupID: GroupRecognize,
		Long: `Identify the music in a file, a URL, a folder, a glob, or a list of files.

The standard endpoint analyzes up to the first 12 seconds of audio. To pick
another moment, send a clip with --at (needs ffmpeg). To scan a whole
recording, use --enterprise: it returns every match with its position and is
billed per 12-second chunk, so it needs --limit (chunks per file, or none).

A folder, glob, several arguments, or a list on stdin (-) runs as a batch:
it needs --max-files, shows a plan, and can be resumed with audd jobs resume.
A batch prints one JSON line per file and a summary line when piped (also
with --format json). Running the same batch again after an interruption asks
whether to continue it; --resume or --new answers without asking.

Results are cached by file contents (URLs for 24 hours), so recognizing the
same audio again is free. --no-cache sends it anyway.

No match is not an error: the result is null and the exit code is 0, unless
you pass --fail-on-no-match (exit 1; in a batch, when any file has no match).`,
		Example: `  audd recognize song.mp3
  audd recognize https://audd.tech/example.mp3 --return apple_music,spotify
  audd recognize mix.mp3 --at 1:30
  audd recognize mix.mp3 --enterprise --limit 20 --tracklist
  audd recognize ./recordings --max-files 200 --dry-run
  ls *.mp3 | audd recognize - --max-files 50 --yes`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecognize(cmd, a, f, args)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.enterprise, "enterprise", false, "scan the whole file and return every match with timestamps (billed per 12-second chunk; needs --limit)")
	fl.StringVar(&f.limit, "limit", "", "with --enterprise: at most N 12-second chunks per file, or none")
	fl.StringVar(&f.maxFiles, "max-files", "", "for folders, globs, and lists: at most N files, or none")
	agent.SetRequiredWhen(fl, "limit", "with --enterprise (N chunks per file, or none)")
	agent.SetRequiredWhen(fl, "max-files", "for a batch: a folder, a glob, several arguments, or a list on stdin (N files, or none)")
	fl.StringVar(&f.at, "at", "", "send a clip starting at this time, such as 90, 1:30, or 1m30s (needs ffmpeg)")
	fl.StringVar(&f.duration, "duration", "", "clip length for --at (default 12s)")
	fl.BoolVar(&f.tracklist, "tracklist", false, "with --enterprise: merge consecutive matches into tracks with start and end times")
	fl.StringVar(&f.ret, "return", "", "extra metadata: "+strings.Join(api.Providers, ", ")+" (comma-separated)")
	fl.IntVar(&f.skip, "skip", 0, "with --enterprise: chunks to skip after each scanned run")
	fl.IntVar(&f.every, "every", 0, "with --enterprise: chunks to scan in a row")
	fl.IntVar(&f.skipFirst, "skip-first-seconds", 0, "with --enterprise: seconds to skip at the start of the file")
	fl.BoolVar(&f.useTimecode, "use-timecode", false, "with --enterprise: start at the time in the URL (t=, start=)")
	fl.BoolVar(&f.accurate, "accurate-offsets", true, "with --enterprise: exact start and end offsets for each match")
	fl.BoolVarP(&f.details, "details", "v", false, "show every field of the result")
	fl.BoolVar(&f.browse, "browse", false, "open the results in the interactive explorer afterwards")
	fl.BoolVar(&f.noArt, "no-art", false, "do not show cover art")
	fl.BoolVar(&f.failOnNoMatch, "fail-on-no-match", false, "exit 1 when there is no match (in a batch: when any file has no match)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "show the plan and cost without sending anything")
	fl.BoolVar(&f.noCache, "no-cache", false, "send the audio even if the result is cached")
	fl.IntVar(&f.concurrency, "concurrency", 0, "parallel requests for batches (default 4, or audd config concurrency)")
	fl.BoolVar(&f.resume, "resume", false, "for a batch that ran before and did not finish: continue it")
	fl.BoolVar(&f.startNew, "new", false, "for a batch that ran before and did not finish: start over as a new job")
	fl.StringVar(&f.inputName, "input-name", "", "name to show for the input")
	_ = fl.MarkHidden("input-name")
	return cmd
}

// enterpriseOpts collects the enterprise passthrough flags that were set.
func (f *recognizeFlags) enterpriseOpts(cmd *cobra.Command) map[string]string {
	opts := map[string]string{}
	changed := cmd.Flags().Changed
	if changed("skip") {
		opts["skip"] = strconv.Itoa(f.skip)
	}
	if changed("every") {
		opts["every"] = strconv.Itoa(f.every)
	}
	if changed("skip-first-seconds") {
		opts["skip_first_seconds"] = strconv.Itoa(f.skipFirst)
	}
	if changed("use-timecode") {
		opts["use_timecode"] = strconv.FormatBool(f.useTimecode)
	}
	if changed("accurate-offsets") {
		opts["accurate_offsets"] = strconv.FormatBool(f.accurate)
	}
	return opts
}

func usageErr(hint, format string, args ...any) error {
	return output.Errf(output.ExitUsage, "invalid_argument", hint, format, args...)
}

func runRecognize(cmd *cobra.Command, a *app.App, f *recognizeFlags, args []string) error {
	ctx := cmd.Context()
	ret, err := api.NormalizeReturn(f.ret)
	if err != nil {
		return err
	}
	eopts := f.enterpriseOpts(cmd)
	if !f.enterprise {
		for _, name := range []string{"skip", "every", "skip-first-seconds", "use-timecode", "accurate-offsets", "tracklist"} {
			if cmd.Flags().Changed(name) {
				return usageErr("add --enterprise --limit N", "--%s applies to --enterprise recognition only", name)
			}
		}
	}
	if f.enterprise && ret != "" {
		return usageErr("drop --return, or recognize a clip without --enterprise", "--return isn't available with --enterprise: the enterprise endpoint doesn't return Apple Music, Spotify, Deezer, or MusicBrainz metadata")
	}
	if f.duration != "" && f.at == "" {
		return usageErr("add --at, e.g. --at 1:30 --duration 20", "--duration sets the clip length for --at")
	}

	inputs, batch, err := media.ExpandInputs(args, a.In)
	if err != nil {
		return err
	}
	defer func() {
		for _, in := range inputs {
			in.Cleanup()
		}
	}()
	// --dry-run sends nothing, so it plans without --limit or --max-files
	// (the plan then shows the uncapped cost).
	limitFlag, maxFilesFlag := f.limit, f.maxFiles
	if f.dryRun {
		if strings.TrimSpace(limitFlag) == "" && f.enterprise {
			limitFlag = "none"
		}
		if strings.TrimSpace(maxFilesFlag) == "" && batch {
			maxFilesFlag = "none"
		}
	}
	limit, err := safety.RequireLimit(f.enterprise, limitFlag)
	if err != nil {
		return err
	}
	maxFiles, err := safety.RequireMaxFiles(batch, maxFilesFlag)
	if err != nil {
		return err
	}
	req := api.Request{Enterprise: f.enterprise, Return: ret, Limit: limit, EnterpriseOpts: eopts}
	if f.enterprise {
		// Validate the passthrough values before anything is sent.
		if _, err := req.EnterpriseOptions(); err != nil {
			return err
		}
	}

	if !batch && (f.resume || f.startNew) {
		name := "--resume"
		if f.startNew {
			name = "--new"
		}
		return usageErr("use audd jobs resume <id> to continue a batch job", "%s applies to batches (folders, globs, lists) only", name)
	}
	if batch {
		if f.inputName != "" {
			return usageErr("", "--input-name works on a single input")
		}
		if f.at != "" {
			return usageErr("run audd recognize <file> --at T for one file at a time", "--at works on a single file or URL")
		}
		if f.tracklist {
			return usageErr("run audd recognize <file> --enterprise --tracklist for one file at a time", "--tracklist works on a single file or URL")
		}
		if !f.dryRun {
			if err := requireToken(a); err != nil {
				return err
			}
		}
		a.Out.SetStreaming()
		sum, err := app.RunBatch(ctx, a, app.BatchOptions{
			Inputs: inputs, Enterprise: f.enterprise, Limit: limit, MaxFiles: maxFiles,
			Return: ret, EnterpriseOpts: eopts, Concurrency: f.concurrency,
			DryRun: f.dryRun, NoCache: f.noCache, Yes: a.Flags.Yes,
			Resume: f.resume, New: f.startNew,
		})
		if err != nil {
			return err
		}
		if f.browse && !f.dryRun && a.Out.IsHuman() {
			if err := app.RunExplorer(ctx, a, "jobs/"+sum.JobID); err != nil {
				return err
			}
		}
		if sum.Failed > 0 {
			return &output.Error{
				Code:    "partial_failure",
				Message: fmt.Sprintf("%d of the files failed", sum.Failed),
				Hint:    "audd jobs resume " + sum.JobID + " --retry-failed",
				Exit:    output.ExitPartial,
			}
		}
		if f.failOnNoMatch && !f.dryRun && sum.NoMatch > 0 {
			total := sum.Recognized + sum.NoMatch
			return &output.Error{
				Code:    "no_match",
				Message: fmt.Sprintf("no music was recognized in %d of the %s", sum.NoMatch, output.Plural(total, "file")),
				Hint:    "see which with audd jobs show " + sum.JobID + "; try another moment of a file with --at 1:00, or scan whole files with --enterprise --limit N",
				Exit:    output.ExitUnexpected,
			}
		}
		return nil
	}
	return recognizeOne(ctx, a, f, inputs[0], req)
}

// requireToken returns the no_token error (exit 3) when no API token is
// set, so commands that send audio say so before planning, confirming, or
// recording anything. --dry-run needs no token.
func requireToken(a *app.App) error {
	if _, err := a.APIClient(); err != nil && output.AsError(err).Code == "no_token" {
		return err
	}
	return nil
}

func recognizeOne(ctx context.Context, a *app.App, f *recognizeFlags, in media.Input, req api.Request) error {
	if !f.dryRun {
		if err := requireToken(a); err != nil {
			return err
		}
		// A wrong --fields fails here, before anything is sent.
		if err := checkOneFields(a, f, req.Enterprise); err != nil {
			return err
		}
	}
	name := in.Name()
	if f.inputName != "" {
		name = f.inputName
	}
	var clip *clipInfo
	if f.at != "" {
		at, err := parseClock(f.at)
		if err != nil {
			return usageErr("use seconds (90), m:ss (1:30), h:mm:ss, or a duration (1m30s)", "invalid --at value %q", f.at)
		}
		dur := standardSeconds * time.Second
		if f.duration != "" {
			if dur, err = parseClock(f.duration); err != nil || dur <= 0 {
				return usageErr("use seconds (20) or a duration (20s)", "invalid --duration value %q", f.duration)
			}
		}
		src := in.URL
		if src == "" {
			src = in.Path
		}
		path, cleanup, err := trimMedia(ctx, src, at, dur)
		if err != nil {
			return err
		}
		if cleanup != nil {
			defer cleanup()
		}
		in = media.Input{Path: path}
		clip = &clipInfo{Start: at.Seconds(), Length: dur.Seconds()}
		req.Offset = at.Seconds()
	}

	// What the standard endpoint can take.
	var length time.Duration
	var knowLength bool
	if in.Path != "" {
		fi, err := os.Stat(in.Path)
		if err != nil {
			return usageErr("check the path", "cannot read %s: %v", name, err)
		}
		if !req.Enterprise && fi.Size() > maxStandardBytes {
			return &output.Error{
				Code:    "file_too_large",
				Message: fmt.Sprintf("%s is %s; the standard endpoint accepts files up to 10 MB", name, humanBytes(fi.Size())),
				Hint:    "send a 12-second clip with --at 1:00, or scan the whole file with --enterprise --limit N",
				Exit:    output.ExitUsage,
			}
		}
		length, knowLength = probeDuration(in.Path)
	}

	// --max-requests caps the enterprise chunks of a single call. The plan
	// is estimated with the --limit asked for and says where the ceiling
	// stops it.
	planLimit := req.Limit
	if req.Enterprise && a.Flags.MaxRequests > 0 && (req.Limit == nil || *req.Limit > a.Flags.MaxRequests) {
		n := a.Flags.MaxRequests
		req.Limit = &n
	}

	c, err := cache.Open()
	if err != nil {
		a.Out.Info("The results cache is unavailable (%v); continuing without it.", err)
		c = nil
	} else {
		defer c.Close()
	}
	params := req.CacheParams()
	key, err := cache.KeyForInput(in, req.Endpoint(), params)
	if err != nil {
		return usageErr("check the path", "cannot read %s: %v", name, err)
	}
	var (
		raw    json.RawMessage
		cached bool
	)
	if c != nil && !f.noCache {
		raw, cached = c.Get(key)
	}

	if f.dryRun {
		var pc *cache.Cache
		if !f.noCache {
			pc = c
		}
		plan := safety.Estimate([]media.Input{in}, req.Enterprise, planLimit, intOpt(req.EnterpriseOpts, "skip"), intOpt(req.EnterpriseOpts, "every"), pc, probeDuration, params)
		if req.Enterprise {
			plan.Cap(a.Flags.MaxRequests, a.Flags.MaxRequestsName())
		}
		safety.FillAllowance(ctx, a, &plan)
		endpoint := "standard"
		if req.Enterprise {
			endpoint = "enterprise"
		}
		return jobs.PrintDryRun(a.Out, name, "", endpoint, plan, func(w io.Writer) {
			fmt.Fprintf(w, "Plan: %s.\nNothing was sent (--dry-run).\n", plan.String())
		})
	}

	if !cached {
		if req.Enterprise {
			plan := safety.Estimate([]media.Input{in}, true, planLimit, intOpt(req.EnterpriseOpts, "skip"), intOpt(req.EnterpriseOpts, "every"), nil, probeDuration, params)
			plan.Cap(a.Flags.MaxRequests, a.Flags.MaxRequestsName())
			// --max-requests already lowered req.Limit, so the limit is the
			// most this call can spend. When the length is only a guess, the
			// estimate can be too low; decide on the worst case instead.
			worst := plan.Requests
			if plan.MaxRequests > 0 && worst > plan.MaxRequests {
				worst = plan.MaxRequests
			}
			if plan.Approximate && req.Limit != nil && *req.Limit > worst {
				worst = *req.Limit
			}
			if req.Limit == nil || worst > 1 {
				safety.FillAllowance(ctx, a, &plan)
			}
			plan.Render(a.Out)
			if req.Limit == nil || worst > 1 {
				// A run that can spend more than one request: ask first.
				q := fmt.Sprintf("Scan up to %d chunks of %s with the enterprise endpoint?", worst, name)
				if req.Limit == nil {
					q = fmt.Sprintf("Scan all of %s with the enterprise endpoint?", name)
				}
				if err := a.Out.Confirm(q, a.Flags.Yes); err != nil {
					return err
				}
			}
		} else if knowLength && length > standardSeconds*time.Second && clip == nil {
			a.Out.Info("Only up to the first 12 seconds are analyzed. Use --at or --enterprise to recognize later parts.")
		}
		prog := a.Out.Progress()
		prog.Start(1, "Recognizing "+name)
		raw, err = api.Recognize(ctx, a, in, req)
		prog.Done()
		if err != nil {
			return err
		}
		if c != nil {
			if err := c.PutEntry(cache.Entry{Key: key, Source: name, Endpoint: req.Endpoint(), Result: raw}, in.URL != ""); err != nil {
				a.Out.Info("Could not save the result to the cache: %v", err)
			}
		}
	}

	var (
		matched bool
		perr    error
	)
	a.Out.SeparateNotes() // an empty line between the notes and the result
	if req.Enterprise {
		skip := 0
		if p := intOpt(req.EnterpriseOpts, "skip"); p != nil {
			skip = *p
		}
		matched, perr = printEnterprise(a, f, name, cached, clip, raw, skip)
	} else {
		matched, perr = printStandard(a, f, name, cached, clip, raw)
	}
	if perr != nil {
		return perr
	}
	if f.browse && a.Out.IsHuman() {
		if err := app.RunExplorer(ctx, a, "recent"); err != nil {
			return err
		}
	}
	if !matched && f.failOnNoMatch {
		return &output.Error{Code: "no_match", Message: "no music was recognized in " + name,
			Hint: "try another moment with --at 1:00, or scan the whole file with --enterprise --limit N", Exit: output.ExitUnexpected}
	}
	return nil
}

type clipInfo struct {
	Start  float64 `json:"start_seconds"`
	Length float64 `json:"length_seconds"`
}

type standardDoc struct {
	Input  string          `json:"input"`
	Clip   *clipInfo       `json:"clip,omitempty"`
	Cached bool            `json:"cached"`
	Result json.RawMessage `json:"result"`
}

type enterpriseDoc struct {
	Input      string          `json:"input"`
	Clip       *clipInfo       `json:"clip,omitempty"`
	Cached     bool            `json:"cached"`
	Enterprise bool            `json:"enterprise"`
	Matches    json.RawMessage `json:"matches"`
	Tracks     []api.Track     `json:"tracks,omitempty"`
}

// trackRow is a track as a CSV/JSONL row.
type trackRow struct {
	Input  string `json:"input"`
	Cached bool   `json:"cached"`
	api.Track
}

// checkOneFields checks --fields against the output of one recognition
// (see printStandard and printEnterprise).
func checkOneFields(a *app.App, f *recognizeFlags, enterprise bool) error {
	format := a.Out.Format()
	switch {
	case enterprise && f.tracklist && (format == output.FormatCSV || format == output.FormatJSONL):
		return a.Out.CheckFieldsFor(trackRow{})
	case format == output.FormatCSV:
		return a.Out.CheckFieldsFor(jobs.CSVRow{})
	case enterprise && format == output.FormatJSONL:
		return a.Out.CheckFieldsFor(struct {
			Input  string `json:"input"`
			Cached bool   `json:"cached"`
		}{}, jobs.ResultFields("")...)
	case enterprise:
		return a.Out.CheckFieldsFor(enterpriseDoc{}, append(jobs.ResultFields("matches"), "tracks.*")...)
	}
	return a.Out.CheckFieldsFor(standardDoc{}, jobs.ResultFields("result")...)
}

func printStandard(a *app.App, f *recognizeFlags, name string, cached bool, clip *clipInfo, raw json.RawMessage) (bool, error) {
	view, ok := api.View(raw, cached)
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	if a.Out.Format() == output.FormatCSV {
		// The same columns as a batch.
		return ok, a.Out.Result(jobs.SingleRows(name, cached, raw), nil)
	}
	doc := standardDoc{Input: name, Clip: clip, Cached: cached, Result: raw}
	return ok, a.Out.Result(doc, func(w io.Writer) {
		if !ok {
			fmt.Fprintln(w, "No match.")
			return
		}
		if a.Out.Quiet() {
			fmt.Fprintln(w, api.Essential(view))
			return
		}
		view.NoArt = f.noArt
		app.RenderResult(w, a, view, f.details)
	})
}

func printEnterprise(a *app.App, f *recognizeFlags, name string, cached bool, clip *clipInfo, raw json.RawMessage, skip int) (bool, error) {
	matches := api.ParseMatches(raw)
	if raw == nil || len(matches) == 0 && string(raw) != "[]" {
		raw = json.RawMessage("[]")
	}
	var tracks []api.Track
	if f.tracklist {
		tracks = api.Tracklist(matches, skip)
		if tracks == nil {
			tracks = []api.Track{}
		}
	}
	matched := len(matches) > 0
	human := func(w io.Writer) {
		if !matched {
			fmt.Fprintln(w, "No match.")
			return
		}
		st := a.Out.Styles()
		if f.tracklist {
			for _, t := range tracks {
				line := fmt.Sprintf("%s – %s", t.Start, t.End)
				if a.Out.Quiet() {
					fmt.Fprintf(w, "%s  %s — %s\n", line, t.Artist, t.Title)
					continue
				}
				fmt.Fprintf(w, "%s  %s\n", st.Dim.Render(fmt.Sprintf("%-17s", line)), st.Bold.Render(t.Artist+" — "+t.Title))
				if f.details {
					printTrackDetails(w, st, t.Album, t.Label, t.ReleaseDate, t.ISRC, t.UPC, t.SongLink)
				}
			}
		} else {
			for _, m := range matches {
				at := m.Timecode
				if m.StartSeconds != nil {
					at = api.Clock(*m.StartSeconds)
				}
				if a.Out.Quiet() {
					fmt.Fprintf(w, "%s  %s — %s\n", at, m.Artist, m.Title)
					continue
				}
				score := ""
				if m.Score > 0 {
					score = st.Dim.Render(fmt.Sprintf("  (score %d)", m.Score))
				}
				fmt.Fprintf(w, "%s  %s%s\n", st.Dim.Render(fmt.Sprintf("%-8s", at)), st.Bold.Render(m.Artist+" — "+m.Title), score)
				if f.details {
					printTrackDetails(w, st, m.Album, m.Label, m.ReleaseDate, m.ISRC, m.UPC, m.SongLink)
				}
			}
		}
		if cached && !a.Out.Quiet() {
			fmt.Fprintln(w, st.Dim.Render("(cached result, no requests used)"))
		}
	}
	switch a.Out.Format() {
	case output.FormatCSV, output.FormatJSONL:
		// One row per match (or track), each naming the input. CSV rows
		// have the same columns as a batch.
		if a.Out.Format() == output.FormatCSV && !f.tracklist {
			return matched, a.Out.Result(jobs.SingleRows(name, cached, raw), nil)
		}
		if f.tracklist {
			rows := make([]trackRow, len(tracks))
			for i, t := range tracks {
				rows[i] = trackRow{Input: name, Cached: cached, Track: t}
			}
			return matched, a.Out.Result(rows, nil)
		}
		return matched, a.Out.Result(matchRows(name, cached, raw), nil)
	}
	return matched, a.Out.Result(enterpriseDoc{Input: name, Clip: clip, Cached: cached, Enterprise: true, Matches: raw, Tracks: tracks}, human)
}

// matchRows turns each match object into a row led by the input name and
// the cached flag. Any "input" or "cached" keys in the match itself are
// dropped so the row has no duplicate keys; other keys keep their order.
func matchRows(name string, cached bool, raw json.RawMessage) []json.RawMessage {
	var items []json.RawMessage
	_ = json.Unmarshal(raw, &items)
	in, _ := json.Marshal(name)
	head := `{"input":` + string(in) + `,"cached":` + strconv.FormatBool(cached)
	rows := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		var b strings.Builder
		b.WriteString(head)
		if !appendMembers(&b, it, "input", "cached") {
			continue
		}
		b.WriteString("}")
		rows = append(rows, json.RawMessage(b.String()))
	}
	return rows
}

// appendMembers writes ",key:value" for each member of the JSON object obj,
// in order, skipping the given keys. It reports false if obj is not an object.
func appendMembers(b *strings.Builder, obj json.RawMessage, skip ...string) bool {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	var out strings.Builder
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return false
		}
		key, _ := t.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return false
		}
		if slices.Contains(skip, key) {
			continue
		}
		k, _ := json.Marshal(key)
		out.WriteString(",")
		out.Write(k)
		out.WriteString(":")
		out.Write(val)
	}
	b.WriteString(out.String())
	return true
}

func printTrackDetails(w io.Writer, st output.Styles, album, label, date, isrc, upc, link string) {
	for _, kv := range [][2]string{{"Album", album}, {"Label", label}, {"Released", date}, {"ISRC", isrc}, {"UPC", upc}, {"Link", link}} {
		if kv[1] != "" {
			fmt.Fprintf(w, "          %s %s\n", st.Key.Render(fmt.Sprintf("%-9s", kv[0]+":")), kv[1])
		}
	}
}

func intOpt(opts map[string]string, k string) *int {
	v, ok := opts[k]
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

// parseClock reads "90", "90.5", "1:30", "1:02:03", or a Go duration ("1m30s").
func parseClock(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return 0, fmt.Errorf("negative")
		}
		return d, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("too many parts")
	}
	var total float64
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("bad number %q", p)
		}
		if i > 0 && v >= 60 {
			return 0, fmt.Errorf("%q is 60 or more", p)
		}
		if i < len(parts)-1 && v != float64(int(v)) {
			return 0, fmt.Errorf("fraction in %q", p)
		}
		total = total*60 + v
	}
	return time.Duration(total * float64(time.Second)), nil
}

func humanBytes(n int64) string {
	const mb = 1 << 20
	if n >= mb {
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}
