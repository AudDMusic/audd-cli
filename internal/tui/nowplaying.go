package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/template"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams"
)

// NowPlayingOptions configure audd now-playing.
type NowPlayingOptions struct {
	NoArt, Notify bool
	// Once prints the current song of each station and exits.
	Once bool
	// Format is a text/template over NowPlayingLine, printed per song
	// change (or once with Once), for status bars.
	Format string
	// Timeout stops the run after this long (0: run until interrupted).
	Timeout time.Duration
	// Interval is how often stream results are re-read (default 5 s).
	Interval time.Duration
	// Context cancels the run (Ctrl-C). Defaults to context.Background().
	Context context.Context
}

// NowPlayingLine is the data a --format template sees: the song fields
// ({{.Artist}}, {{.Title}}, {{.Album}}, {{.Label}}, {{.ReleaseDate}},
// {{.SongLink}}, {{.ISRC}}, {{.UPC}}), the stream ({{.RadioID}}, {{.URL}}),
// and the timing: {{.State}} (playing, just_played, or last_recognized),
// {{.Playing}}, {{.At}} (when the song started), {{.Elapsed}} (since it
// started, while it plays), {{.Played}} and {{.Ended}} (for results sent
// when the song ended), {{.Ago}} (since it ended, or since it started when
// the end is not known), and {{.Length}} (when the track length is known).
type NowPlayingLine struct {
	app.ResultView
	RadioID int
	URL     string
	State   string
	Playing bool
	At      time.Time
	Elapsed time.Duration
	Played  time.Duration
	Ended   time.Time
	Ago     time.Duration
	Length  time.Duration
}

// historyLimit is how many plays per station the screens read.
const historyLimit = 30

// stationsRefresh is how often the station list (and health) is re-read.
const stationsRefresh = time.Minute

// ensureRecorder starts the background recorder when it should run. A
// recorder that cannot start never blocks the screen. The note that it
// started is shown once per profile.
func ensureRecorder(a *app.App) {
	note, err := streams.EnsureRecorder(a)
	switch {
	case err != nil:
		var oe *output.Error
		if errors.As(err, &oe) && oe.Code == "not_implemented" {
			return
		}
		a.Out.Info("Could not start the background recorder: %v", err)
	case note != "":
		a.Out.Info("%s", note)
	}
}

// RunNowPlaying shows what is playing on the given streams (all streams
// when radioIDs is empty). On a terminal it opens the full-screen view;
// piped, it prints a JSONL line per song change; with Once it prints the
// current songs and exits.
func RunNowPlaying(a *app.App, radioIDs []int, opts NowPlayingOptions) error {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Second
	}
	var tmpl *template.Template
	if opts.Format != "" {
		t, err := template.New("format").Parse(opts.Format)
		if err != nil {
			return output.Errf(output.ExitUsage, "invalid_argument", `audd now-playing --format "{{.Artist}} - {{.Title}}"`, "invalid --format template: %v", err)
		}
		tmpl = t
	}
	// Every mode starts the recorder, --once and piped included, so the
	// local store keeps every result from now on. The note goes to stderr.
	ensureRecorder(a)
	if !opts.Once {
		if opts.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
			defer cancel()
		}
	}
	feed, err := NewFeed(a)
	if err != nil {
		return err
	}
	stations, err := selectStations(ctx, feed, radioIDs)
	if err != nil {
		return err
	}
	now := a.Now
	if now == nil {
		now = time.Now
	}
	interactive := !opts.Once && tmpl == nil && a.Out.IsHuman() && a.Out.Options().StdoutTTY && a.Out.Options().StdinTTY
	switch {
	case opts.Once:
		limit := 1
		if a.Out.IsHuman() && tmpl == nil {
			limit = 6
		}
		data := loadAll(ctx, feed, stations, limit)
		// A station that cannot be read is shown with its error; the
		// command fails only when no station could be read.
		if err := allFailed(data); err != nil {
			return err
		}
		if tmpl != nil {
			for _, d := range data {
				if d.Err != nil && len(d.Plays) == 0 {
					a.Out.Warn("%v", d.Err)
					continue
				}
				if len(d.Plays) > 0 {
					if err := writeLine(a.Out.Stdout(), tmpl, d.Station, d.Plays[0], now()); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if a.Out.Format() == output.FormatCSV {
			return a.Out.Result(stationRows(data, now()), nil)
		}
		return a.Out.Result(onceDoc(data, now()), func(w io.Writer) { printOnce(w, a, data, now(), opts.NoArt) })
	case interactive:
		m := newNowPlayingModel(ctx, a, feed, stations, radioIDs, opts)
		p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx),
			tea.WithInput(a.In), tea.WithOutput(m.arts.out))
		_, err := p.Run()
		m.arts.finish()
		if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
			return nil
		}
		return err
	default:
		return streamNowPlaying(ctx, a, feed, stations, radioIDs, tmpl, opts, now)
	}
}

// selectStations returns the requested stations in the given order, or all
// of the account's stations sorted by radio ID. A requested ID that is not
// on the account is an unknown_stream error; when the stream list cannot be
// read, requested IDs are shown from stored results with an unknown status.
func selectStations(ctx context.Context, feed Feed, ids []int) ([]Station, error) {
	all, err := feed.Stations(ctx)
	listed := err == nil
	if err != nil {
		if len(ids) == 0 {
			return nil, err
		}
		all = nil // still show the requested IDs from stored results
	}
	byID := map[int]Station{}
	for _, s := range all {
		byID[s.RadioID] = s
	}
	if len(ids) == 0 {
		if len(all) == 0 {
			return nil, output.Errf(output.ExitUsage, "no_streams", "audd streams add <url> --id 1", "there are no streams on this account")
		}
		sort.Slice(all, func(i, j int) bool { return all[i].RadioID < all[j].RadioID })
		return all, nil
	}
	out := make([]Station, 0, len(ids))
	seen := map[int]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		s, ok := byID[id]
		if !ok {
			if listed {
				return nil, output.Errf(output.ExitUsage, "unknown_stream", "audd streams list",
					"there is no stream with radio ID %d on this account", id)
			}
			s = Station{RadioID: id, Running: true, StatusUnknown: true}
		}
		out = append(out, s)
	}
	return out, nil
}

// stationData is one station with its recent plays, newest first.
type stationData struct {
	Station
	Plays []Play
	Err   error
}

func loadAll(ctx context.Context, feed Feed, stations []Station, limit int) []stationData {
	out := make([]stationData, len(stations))
	for i, s := range stations {
		plays, err := feed.Plays(ctx, s.RadioID, limit)
		sort.SliceStable(plays, func(a, b int) bool { return plays[a].At.After(plays[b].At) })
		out[i] = stationData{Station: s, Plays: plays, Err: err}
	}
	return out
}

func firstError(data []stationData) error {
	for _, d := range data {
		if d.Err != nil {
			return d.Err
		}
	}
	return nil
}

// allFailed returns the first station's error when no station could be
// read, else nil.
func allFailed(data []stationData) error {
	if len(data) == 0 {
		return nil
	}
	for _, d := range data {
		if d.Err == nil {
			return nil
		}
	}
	return data[0].Err
}

func lineData(s Station, p Play, now time.Time) NowPlayingLine {
	st := p.State(now)
	l := NowPlayingLine{
		ResultView: p.ResultView, RadioID: s.RadioID, URL: s.URL,
		State: string(st), Playing: st == StatePlaying, At: p.At, Length: p.TrackLength,
	}
	if st == StatePlaying {
		l.Elapsed = p.Elapsed(now).Round(time.Second)
	} else {
		l.Ago = p.Ago(now).Round(time.Second)
	}
	if end, ok := p.Ended(); ok {
		l.Played, l.Ended = p.Played(), end
	}
	return l
}

// playStatus describes a play for people. head is the status ("Now playing
// · 1:23", "Just played · ended 2 min ago", "Last played · ended 25 min
// ago", "Last recognized 25 min ago"); detail is what goes under it ("played
// 3:12", "played 3:12 of 3:20"), empty when there is nothing more to say;
// bar is set when a progress bar can be drawn (the song plays and its length
// is known), in which case head is just "Now playing".
func playStatus(p Play, now time.Time) (head, detail string, bar bool) {
	switch p.State(now) {
	case StatePlaying:
		if p.TrackLength > 0 {
			return "Now playing", "", true
		}
		return "Now playing · " + clock(p.Elapsed(now)), "", false
	case StateJustPlayed:
		head = "Just played · ended " + ago(p.Ago(now))
	default:
		if _, ok := p.Ended(); !ok {
			return "Last recognized " + ago(p.Ago(now)), "", false
		}
		head = "Last played · ended " + ago(p.Ago(now))
	}
	detail = "played " + clock(p.Played())
	if p.TrackLength > 0 {
		detail += " of " + clock(p.TrackLength)
	}
	return head, detail, false
}

// statusLine is playStatus on one line, for --once.
func statusLine(p Play, now time.Time) string {
	head, detail, bar := playStatus(p, now)
	switch {
	case bar:
		return "● " + head + " · " + clock(p.Elapsed(now)) + " / " + clock(p.TrackLength)
	case p.State(now) == StatePlaying:
		return "● " + head
	case detail != "":
		// "Just played · ended 2 min ago" → "Just played · played 3:12 · ended 2 min ago"
		if i := strings.Index(head, " · "); i >= 0 {
			return head[:i] + " · " + detail + head[i:]
		}
		return head + " · " + detail
	}
	return head
}

func writeLine(w io.Writer, t *template.Template, s Station, p Play, now time.Time) error {
	var b strings.Builder
	if err := t.Execute(&b, lineData(s, p, now)); err != nil {
		return output.Errf(output.ExitUsage, "invalid_argument", "", "--format template failed: %v", err)
	}
	_, err := fmt.Fprintln(w, strings.TrimRight(b.String(), "\n"))
	return err
}

// stationDoc is the JSON form of one station in --once output.
func stationDoc(d stationData, now time.Time) map[string]any {
	m := map[string]any{"radio_id": d.RadioID, "stream_running": d.Running}
	if d.StatusUnknown {
		m["stream_running"] = nil // the stream list could not be read
	}
	if d.URL != "" {
		m["url"] = d.URL
	}
	if d.Health != nil && d.Health.Code != 0 {
		m["health"] = d.Health
	}
	if d.Err != nil {
		e := output.AsError(d.Err)
		m["error"] = map[string]any{"code": e.Code, "message": e.Message, "retryable": e.Retryable}
	}
	if len(d.Plays) == 0 {
		m["now_playing"] = nil
		return m
	}
	p := d.Plays[0]
	np := p.JSON()
	delete(np, "radio_id")
	for k, v := range p.Timing(now) {
		np[k] = v
	}
	m["now_playing"] = np
	return m
}

func onceDoc(data []stationData, now time.Time) map[string]any {
	items := make([]map[string]any, len(data))
	for i, d := range data {
		items[i] = stationDoc(d, now)
	}
	return map[string]any{"stations": items}
}

// printOnce is the human --once view: a card per station.
func printOnce(w io.Writer, a *app.App, data []stationData, now time.Time, noArt bool) {
	st := a.Out.Styles()
	for i, d := range data {
		if i > 0 {
			fmt.Fprintln(w)
		}
		head := fmt.Sprintf("Stream %d", d.RadioID)
		if d.URL != "" {
			head += "  " + st.Dim.Render(d.URL)
		}
		fmt.Fprintln(w, st.Bold.Render(head))
		if d.Down() {
			fmt.Fprintln(w, st.Err.Render(d.HealthText()))
		}
		if len(d.Plays) == 0 {
			if d.Err != nil {
				fmt.Fprintln(w, st.Err.Render(output.AsError(d.Err).Message))
				continue
			}
			fmt.Fprintln(w, st.Dim.Render("No songs recognized yet."))
			continue
		}
		p := d.Plays[0]
		status := statusLine(p, now)
		fmt.Fprintln(w, st.Dim.Render(status))
		v := p.ResultView
		v.NoArt = v.NoArt || noArt
		RenderResultCard(w, a, v, false)
		if len(d.Plays) > 1 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, st.Dim.Render("Earlier on this station"))
			for _, e := range d.Plays[1:] {
				fmt.Fprintf(w, "  %s  %s\n", e.At.In(localZone).Format("15:04"), songLine(e.ResultView))
			}
		}
	}
}

// streamNowPlaying prints a line (JSONL, CSV, table, or --format) for the
// current song of each station and then for every change, until ctx ends
// or the timeout passes.
func streamNowPlaying(ctx context.Context, a *app.App, feed Feed, stations []Station, radioIDs []int, tmpl *template.Template, opts NowPlayingOptions, now func() time.Time) error {
	a.Out.SetStreaming()
	last := map[int]string{}
	lastHealth := map[int]int{}
	lastStations := now()
	warned := false
	first := true
	for {
		if !first && now().Sub(lastStations) >= stationsRefresh {
			if fresh, err := selectStations(ctx, feed, radioIDs); err == nil {
				stations = fresh
			}
			lastStations = now()
		}
		data := loadAll(ctx, feed, stations, 1)
		if ctx.Err() != nil {
			return nil
		}
		if err := firstError(data); err != nil {
			if first {
				if err := allFailed(data); err != nil {
					return err
				}
			}
			if !warned {
				a.Out.Info("Could not read stream results (will keep trying): %v", err)
				warned = true
			}
		}
		for _, d := range data {
			code := 0
			if d.Health != nil {
				code = d.Health.Code
			}
			if code != lastHealth[d.RadioID] && (!first || code != 0) && tmpl == nil {
				if err := a.Out.Event("event", map[string]any{"radio_id": d.RadioID, "event": "health", "health": d.Health, "message": d.HealthText()}); err != nil {
					return err
				}
			}
			lastHealth[d.RadioID] = code
			if len(d.Plays) == 0 {
				continue
			}
			p := d.Plays[0]
			if p.key() == last[d.RadioID] {
				continue
			}
			last[d.RadioID] = p.key()
			if opts.Notify && !first {
				_ = notify(songLine(p.ResultView), fmt.Sprintf("Stream %d", d.RadioID))
			}
			var err error
			if tmpl != nil {
				err = writeLine(a.Out.Stdout(), tmpl, d.Station, p, now())
			} else {
				ev := p.JSON()
				for k, v := range p.Timing(now()) {
					ev[k] = v
				}
				err = a.Out.Event("result", ev)
			}
			if err != nil {
				return err
			}
		}
		first = false
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(opts.Interval):
		}
	}
}
