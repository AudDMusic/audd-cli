package cli

import (
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/tui"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newNowPlayingCmd(a))
	})
}

func newNowPlayingCmd(a *app.App) *cobra.Command {
	var (
		once, notify, noArt bool
		timeout             time.Duration
		format              string
	)
	cmd := &cobra.Command{
		Use:     "now-playing [radio-id...]",
		Short:   "Show the latest songs on your streams",
		GroupID: GroupStreams,
		Long: `Show the latest song on your streams, with cover art and up to 30 songs
played before it, like the AudD widget (widget.audd.tech). Several streams are
shown as a grid; pick one with the arrow keys and press Enter to zoom in.
Press ? for all keys.

What the screen can say depends on when AudD sends a stream's results:

  By default, when the song ends. The song is shown as "Just played" (later
  "Last played") with how long it played and when it ended.

  With streams added with --start (audd streams add <url> --id N --start),
  when the song starts. The song is shown as "Now playing" with the time since
  it started, and after 10 minutes with no new result as "Last recognized".
  Results sent at the start carry no play length, so reports cannot count
  airtime for those streams.

A progress bar ("1:23 / 3:40") needs the track length, which stream results
include only with Apple Music, Spotify, or Deezer metadata. Turn it on with
audd streams callback set <url> --return apple_music.

Results come from the local stream store, which the background recorder keeps
complete by longpolling your streams and saving every result. When the store
has nothing recent for a stream, audd reads the last results AudD keeps for
it (about 30) and saves those too.

--once prints the latest songs and exits. --format takes a Go template for
status bars, printed each time the song changes (or once with --once). It
sees the song fields Artist, Title, Album, Label, ReleaseDate, SongLink, ISRC,
and UPC; RadioID and URL; and the timing: State (playing, just_played, or
last_recognized), Playing, At (when the song started), Elapsed (since it
started, while playing), Played and Ended (for results sent when the song
ended), Ago (since it ended, or since it started when the end is unknown), and
Length (when the track length is known). --format also accepts the output
formats table, json, jsonl, and csv; JSON output has the same timing as
state, playing, elapsed_seconds, played_seconds, ended_at, ago_seconds, and
length_seconds. When piped, now-playing prints a JSON line each time the song
changes.`,
		Example: `  audd now-playing
  audd now-playing 3 7
  audd now-playing --once
  audd now-playing 3 --once --format "{{.Artist}} - {{.Title}}"
  audd now-playing 3 --format "{{if .Playing}}▶ {{end}}{{.Artist}} - {{.Title}}"
  audd now-playing --notify
  audd now-playing --timeout 1h > plays.jsonl`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids := make([]int, 0, len(args))
			for _, s := range args {
				id, err := strconv.Atoi(strings.TrimSpace(s))
				if err != nil || id <= 0 {
					return output.Errf(output.ExitUsage, "invalid_argument", "audd streams list", "%q is not a radio ID (a positive number)", s)
				}
				ids = append(ids, id)
			}
			if timeout < 0 {
				return output.Errf(output.ExitUsage, "invalid_argument", "", "--timeout must be positive")
			}
			tmpl := format
			if f, err := output.ParseFormat(format); format != "" && err == nil {
				// --format with an output format name selects that format.
				opts := a.Out.Options()
				opts.Format = f
				a.Out = output.NewPrinter(a.Out.Stdout(), a.Out.Stderr(), opts)
				tmpl = ""
			}
			return tui.RunNowPlaying(a, ids, tui.NowPlayingOptions{
				NoArt: noArt, Notify: notify, Once: once, Format: tmpl, Timeout: timeout,
				Context: cmd.Context(),
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&once, "once", false, "print the latest song of each stream and exit")
	f.DurationVar(&timeout, "timeout", 0, "stop after this long (for example 30m)")
	f.StringVar(&format, "format", "", `template for each line, such as "{{.Artist}} - {{.Title}}" (or table, json, jsonl, csv)`)
	f.BoolVar(&notify, "notify", false, "show a desktop notification when the song changes")
	f.BoolVar(&noArt, "no-art", false, "do not show cover art")
	return cmd
}
