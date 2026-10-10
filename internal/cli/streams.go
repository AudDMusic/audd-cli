package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/AudDMusic/audd-go"
	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newStreamsCmd(a))
	})
}

var validReturn = []string{"apple_music", "spotify", "deezer", "musicbrainz"}

func newStreamsCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "streams",
		GroupID: GroupStreams,
		Short:   "Manage stream monitoring, watch it live, record it, and report on it",
		Long: `AudD monitors radio and other live streams on your account and identifies the
music that plays. These commands manage the streams, show results as they
arrive, and keep a local record of every play.

The first time you use a streams command, audd starts a background recorder
that saves every result to a local database, so history and reports stay
complete. Turn that off with: audd config set streams.background_recorder false`,
		Example: `  audd streams add https://radio.example/stream.mp3 --id 1
  audd streams list
  audd streams watch
  audd streams history --since 24h
  audd streams report --by artist --since 30d`,
	}
	cmd.AddCommand(
		newStreamsListCmd(a),
		newStreamsAddCmd(a),
		newStreamsRemoveCmd(a),
		newStreamsSetURLCmd(a),
		newStreamsCallbackCmd(a),
		newStreamsWatchCmd(a),
		newStreamsRecordCmd(a),
		newStreamsRecorderCmd(a),
		newStreamsHistoryCmd(a),
		newStreamsReportCmd(a),
		newStreamsExportCmd(a),
	)
	return cmd
}

// ensureRecorder starts the background recorder when it should run, and
// the first time it starts one for a profile it says so. Problems are not
// fatal: the command the user ran still worked.
func ensureRecorder(a *app.App) {
	if note, _ := streams.EnsureRecorder(a); note != "" {
		a.Out.Info("%s", note)
	}
}

func parseRadioID(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, output.Errf(output.ExitUsage, "invalid_argument", "stream IDs are whole numbers, as shown by audd streams list",
			"%q is not a stream ID", s)
	}
	return n, nil
}

func parseRadioIDs(args []string) ([]int, error) {
	ids := make([]int, 0, len(args))
	for _, s := range args {
		for _, part := range strings.Split(s, ",") {
			if strings.TrimSpace(part) == "" {
				continue
			}
			id, err := parseRadioID(part)
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func openStore(a *app.App) (*streamstore.Store, error) {
	st, err := streamstore.Open(a.Profile.Name)
	if err != nil {
		return nil, fmt.Errorf("opening the stream store: %w", err)
	}
	if a.Now != nil {
		st.Now = a.Now
	}
	return st, nil
}

// streamRow is one stream in `streams list`: the API's fields as returned,
// plus the latest play and health from the local store.
func streamRow(s audd.Stream, category string, last *streamstore.Play, health *streamstore.HealthEvent) map[string]any {
	row := map[string]any{}
	if len(s.RawResponse) > 0 {
		_ = json.Unmarshal(s.RawResponse, &row)
	}
	row["radio_id"] = s.RadioID
	row["url"] = s.URL
	row["stream_running"] = s.StreamRunning
	if _, ok := row["longpoll_category"]; !ok && category != "" {
		row["longpoll_category"] = category
	}
	if last != nil {
		row["last_play"] = last
	} else {
		row["last_play"] = nil
	}
	if health != nil {
		row["health"] = health
	}
	return row
}

func healthLabel(running bool, h *streamstore.HealthEvent) string {
	if h != nil && !h.Running {
		switch h.Code {
		case 650:
			return "can't connect"
		case 651:
			return "no music"
		}
	}
	if h != nil && h.Running && h.Code == 651 {
		return "no music"
	}
	if running {
		return "running"
	}
	return "stopped"
}

func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return t.Local().Format("2006-01-02")
}

func newStreamsListCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the streams on your account with what played last",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var c *audd.Client
			list, err := streams.Do(cmd.Context(), a, func(cl *audd.Client) ([]audd.Stream, error) {
				c = cl
				return cl.Streams().ListContext(cmd.Context())
			})
			if err != nil {
				return err
			}
			st, err := openStore(a)
			if err != nil {
				return err
			}
			defer st.Close()
			rows := make([]map[string]any, 0, len(list))
			type humanRow struct {
				id, status, last, url string
			}
			var human []humanRow
			now := a.Now()
			for _, s := range list {
				last, _ := st.Latest(s.RadioID)
				health, _ := st.LatestHealth(s.RadioID)
				rows = append(rows, streamRow(s, c.Streams().DeriveLongpollCategory(s.RadioID), last, health))
				hr := humanRow{id: strconv.Itoa(s.RadioID), status: healthLabel(s.StreamRunning, health), last: "-", url: s.URL}
				if last != nil {
					// Default streams report a song when it ends (with play_length);
					// --start streams report it when it starts.
					when := "started " + ago(now, last.Timestamp)
					if last.PlayLength > 0 {
						when = "ended " + ago(now, last.Timestamp.Add(time.Duration(last.PlayLength)*time.Second))
					}
					hr.last = fmt.Sprintf("%s — %s (%s)", last.Artist, last.Title, when)
				}
				human = append(human, hr)
			}
			err = a.Out.Result(rows, func(w io.Writer) {
				if len(rows) == 0 {
					fmt.Fprintln(w, "No streams yet. Add one with: audd streams add <url> --id 1")
					return
				}
				table := [][]string{{"ID", "STATUS", "LAST SONG", "URL"}}
				for _, r := range human {
					table = append(table, []string{r.id, r.status, r.last, r.url})
				}
				// On a narrow terminal, long URLs and song names are cut
				// with "…" rather than wrapped.
				output.WriteTable(w, table, a.Out.StdoutWidth(), 3, 2)
			})
			ensureRecorder(a)
			return err
		},
	}
}

func newStreamsAddCmd(a *app.App) *cobra.Command {
	var id int
	var start bool
	cmd := &cobra.Command{
		Use:   "add <url> --id N",
		Short: "Add a stream to monitor",
		Long: `Add a stream for AudD to monitor. The URL can be a direct stream (Icecast,
HLS, DASH, m3u/m3u8) or a shortcut: twitch:<channel>, youtube:<video_id>, or
youtube-ch:<channel_id>. The ID is a number you choose; use it with the other
streams commands.

By default AudD sends a stream's result when the song ends, with when it
started and how long it played (play_length). With --start, results arrive
when songs start instead: audd now-playing can then show the song that is on
right now, but results no longer include the play length, so reports cannot
count airtime for that stream.

Stream results include the track length, used for the progress bar, only
with Apple Music, Spotify, or Deezer metadata. Turn it on with
audd streams callback set <url> --return apple_music.`,
		Example: `  audd streams add https://radio.example/stream.mp3 --id 1
  audd streams add twitch:somechannel --id 2 --start`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("id") {
				return output.Errf(output.ExitUsage, "invalid_argument", "audd streams add <url> --id 1",
					"choose an ID for the stream with --id")
			}
			if id < 0 {
				return output.Errf(output.ExitUsage, "invalid_argument", "", "--id must be 0 or more")
			}
			req := audd.AddStreamRequest{URL: args[0], RadioID: id}
			if start {
				req.Callbacks = "before"
			}
			var c *audd.Client
			if _, err := streams.Do(cmd.Context(), a, func(cl *audd.Client) (struct{}, error) {
				c = cl
				return struct{}{}, cl.Streams().AddContext(cmd.Context(), req)
			}); err != nil {
				return err
			}
			res := map[string]any{"radio_id": id, "url": args[0], "added": true, "results_at_song_start": start}
			err := a.Out.Result(res, func(w io.Writer) {
				fmt.Fprintf(w, "Added stream %d (%s).\nSee results as they arrive: audd streams watch %d\n", id, args[0], id)
			})
			if missing, cerr := streams.CallbackMissing(cmd.Context(), c); cerr == nil && missing {
				a.Out.Info("Live results need a callback URL on your account. Set a placeholder with: audd streams callback set %s", streams.EmptyCallbackURL)
			}
			ensureRecorder(a)
			return err
		},
	}
	cmd.Flags().IntVar(&id, "id", 0, "the stream's ID (a number you choose)")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().BoolVar(&start, "start", false, "send each result when a song starts instead of when it ends (live now-playing, no play length)")
	return cmd
}

func newStreamsRemoveCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <id>",
		Aliases: []string{"rm", "delete"},
		Short:   "Stop monitoring a stream",
		Long:    "Stop monitoring a stream. Its plays stay in the local stream store.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseRadioID(args[0])
			if err != nil {
				return err
			}
			if err := a.Out.Confirm(fmt.Sprintf("Stop monitoring stream %d?", id), a.Flags.Yes); err != nil {
				return err
			}
			if _, err := streams.Do(cmd.Context(), a, func(c *audd.Client) (struct{}, error) {
				return struct{}{}, c.Streams().DeleteContext(cmd.Context(), id)
			}); err != nil {
				return err
			}
			err = a.Out.Result(map[string]any{"radio_id": id, "removed": true}, func(w io.Writer) {
				fmt.Fprintf(w, "Removed stream %d.\n", id)
			})
			ensureRecorder(a)
			return err
		},
	}
}

func newStreamsSetURLCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "set-url <id> <url>",
		Short: "Change a stream's URL",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseRadioID(args[0])
			if err != nil {
				return err
			}
			if _, err := streams.Do(cmd.Context(), a, func(c *audd.Client) (struct{}, error) {
				return struct{}{}, c.Streams().SetURLContext(cmd.Context(), id, args[1])
			}); err != nil {
				return err
			}
			err = a.Out.Result(map[string]any{"radio_id": id, "url": args[1], "updated": true}, func(w io.Writer) {
				fmt.Fprintf(w, "Stream %d now uses %s.\n", id, args[1])
			})
			ensureRecorder(a)
			return err
		},
	}
}

func newStreamsCallbackCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "callback",
		Short: "Show or set the URL AudD sends stream results to",
		Long: `AudD sends each stream result to your account's callback URL as a POST.
Live results in audd (watch, the recorder, now-playing) also need a callback
URL to be set; if you have no receiver, use the placeholder
` + streams.EmptyCallbackURL + `, which discards callbacks.`,
	}
	get := &cobra.Command{
		Use:   "get",
		Short: "Show the callback URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A missing callback URL (AudD error 19) is an answer, not
			// an error.
			u, err := streams.Do(cmd.Context(), a, func(c *audd.Client) (*string, error) {
				u, err := c.Streams().GetCallbackUrlContext(cmd.Context())
				if streams.IsNoCallback(err) {
					return nil, nil
				}
				return &u, err
			})
			if err != nil {
				return err
			}
			res := map[string]any{"url": nil}
			if u != nil {
				res["url"] = *u
			}
			err = a.Out.Result(res, func(w io.Writer) {
				if res["url"] == nil {
					fmt.Fprintf(w, "No callback URL is set.\nSet one with: audd streams callback set <url>   (or %s as a placeholder)\n", streams.EmptyCallbackURL)
					return
				}
				fmt.Fprintln(w, *u)
			})
			ensureRecorder(a)
			return err
		},
	}
	var ret string
	set := &cobra.Command{
		Use:   "set <url>",
		Short: "Set the callback URL",
		Long: `Set the URL AudD sends each stream result to as a POST. --return adds
Apple Music, Spotify, Deezer, or MusicBrainz data to every stream result,
including the ones audd reads (with Apple Music, Spotify, or Deezer data,
now-playing shows the track length and a progress bar).

audd's own live results (watch, the background recorder, now-playing) work
only while some callback URL is set. If you have no receiver of your own,
set ` + streams.EmptyCallbackURL + `, which discards the callbacks.`,
		Example: "  audd streams callback set https://example.com/audd-callback --return apple_music,spotify\n  audd streams callback set " + streams.EmptyCallbackURL,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ret != "" {
				for _, r := range strings.Split(ret, ",") {
					if !contains(validReturn, strings.TrimSpace(r)) {
						return output.Errf(output.ExitUsage, "invalid_argument", "--return accepts "+strings.Join(validReturn, ","),
							"unknown metadata provider %q", r)
					}
				}
			}
			opts := &audd.SetCallbackUrlOptions{ReturnMetadata: ret}
			if _, err := streams.Do(cmd.Context(), a, func(c *audd.Client) (struct{}, error) {
				return struct{}{}, c.Streams().SetCallbackUrlContext(cmd.Context(), args[0], opts)
			}); err != nil {
				return err
			}
			res := map[string]any{"url": args[0], "return": ret, "updated": true}
			err := a.Out.Result(res, func(w io.Writer) {
				fmt.Fprintf(w, "Callback URL set to %s.\n", args[0])
			})
			ensureRecorder(a)
			return err
		},
	}
	set.Flags().StringVar(&ret, "return", "", "extra metadata in each callback: "+strings.Join(validReturn, ", ")+" (comma-separated)")
	cmd.AddCommand(get, set)
	return cmd
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
