package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

// playLine is the one-line human form of a play.
func playLine(p streamstore.Play) string {
	return fmt.Sprintf("%s  [%d] %s — %s", p.Timestamp.Local().Format("2006-01-02 15:04"), p.RadioID, p.Artist, p.Title)
}

func healthLine(h streamstore.HealthEvent) string {
	label := healthLabel(h.Running, &h)
	msg := h.Message
	if h.Code != 0 {
		msg = fmt.Sprintf("%s (%d)", msg, h.Code)
	}
	return fmt.Sprintf("%s  [%d] stream %s: %s", h.At.Local().Format("2006-01-02 15:04"), h.RadioID, label, msg)
}

// liveOutput prints plays and health events as they arrive: lines for
// people, JSONL "result" and "event" records for programs.
type liveOutput struct {
	a  *app.App
	mu sync.Mutex
}

func (o *liveOutput) play(p streamstore.Play) {
	if o.a.Out.IsHuman() {
		o.mu.Lock()
		fmt.Fprintln(o.a.Out.Stdout(), playLine(p))
		o.mu.Unlock()
		return
	}
	o.a.Out.Event("result", p)
}

func (o *liveOutput) health(h streamstore.HealthEvent) {
	if o.a.Out.IsHuman() {
		o.a.Out.Info("%s", healthLine(h))
		return
	}
	o.a.Out.Event("event", struct {
		Event string `json:"event"`
		streamstore.HealthEvent
	}{"health", h})
}

// runRecorder records until ctx ends.
func runRecorder(ctx context.Context, a *app.App, st *streamstore.Store, opts streams.RecorderOptions) error {
	return streams.NewRecorder(a, st, opts).Run(ctx)
}

// explorerStartDelay is how long `streams watch` waits for the explorer to
// open before recording alongside it.
var explorerStartDelay = 300 * time.Millisecond

func newStreamsWatchCmd(a *app.App) *cobra.Command {
	var forward string
	cmd := &cobra.Command{
		Use:   "watch [<id>...]",
		Short: "Show stream results live",
		Long: `Show stream results as they arrive, for every stream or only the radio IDs
you name. In a terminal this opens the explorer's Streams tab on those
streams; when piped it prints one JSON line per play ("type":"result") and
per stream status change ("type":"event").

--forward-to relays every result to a local URL as the same POST AudD sends
to a callback URL, so you can test a callback handler on your machine.
Results are also saved to the local stream store.`,
		Example: `  audd streams watch
  audd streams watch 1 2 | jq .
  audd streams watch --forward-to http://localhost:3000/audd-callback`,
		Annotations: map[string]string{AnnotationStreaming: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseRadioIDs(args)
			if err != nil {
				return err
			}
			if err := streams.EnsureCallbackURL(cmd.Context(), a); err != nil {
				return err
			}
			st, err := openStore(a)
			if err != nil {
				return err
			}
			defer st.Close()
			ensureRecorder(a)
			out := &liveOutput{a: a}
			opts := streams.RecorderOptions{RadioIDs: ids, ForwardTo: forward,
				Logf: func(f string, args ...any) { a.Out.Info(f, args...) }}

			if a.Out.IsHuman() && a.Out.Options().StdoutTTY {
				// The explorer reads the store; record alongside it. The
				// recorder starts once the explorer is up, so a build without
				// the explorer falls back to printing lines without having
				// made any requests for it.
				ctx, cancel := context.WithCancel(cmd.Context())
				done := make(chan error, 1)
				quiet := opts
				quiet.Logf = nil
				go func() {
					select {
					case <-time.After(explorerStartDelay):
						done <- runRecorder(ctx, a, st, quiet)
					case <-ctx.Done():
						done <- nil
					}
				}()
				tab := "streams"
				if len(ids) > 0 {
					parts := make([]string, len(ids))
					for i, id := range ids {
						parts[i] = strconv.Itoa(id)
					}
					tab += "/" + strings.Join(parts, ",")
				}
				err := app.RunExplorer(cmd.Context(), a, tab)
				cancel()
				<-done
				if err == nil || output.AsError(err).Code != "not_implemented" {
					return err
				}
			}

			opts.OnPlay, opts.OnHealth = out.play, out.health
			if a.Out.IsHuman() {
				a.Out.Info("Watching %s. Press Ctrl-C to stop.", describeStreams(ids))
				if forward != "" {
					a.Out.Info("Forwarding results to %s.", forward)
				}
			}
			ctx, stop := streams.WithStopFile(cmd.Context(), a.Profile.Name)
			defer stop()
			return runRecorder(ctx, a, st, opts)
		},
	}
	cmd.Flags().StringVar(&forward, "forward-to", "", "also POST each result to this URL, shaped like an AudD callback")
	return cmd
}

func describeStreams(ids []int) string {
	if len(ids) == 0 {
		return "all streams"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	if len(ids) == 1 {
		return "stream " + parts[0]
	}
	return "streams " + strings.Join(parts, ", ")
}

func newStreamsRecordCmd(a *app.App) *cobra.Command {
	var forward string
	var child bool
	cmd := &cobra.Command{
		Use:   "record [<id>...]",
		Short: "Run the stream recorder in the foreground",
		Long: `Run the stream recorder in the foreground: the same loop as the background
recorder, for Docker and service managers. It saves every play and stream
status change to the local stream store and prints each play. Only one
recorder runs per profile.`,
		Example: `  audd streams record
  audd streams record --forward-to http://localhost:3000/audd-callback`,
		Annotations: map[string]string{AnnotationStreaming: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseRadioIDs(args)
			if err != nil {
				return err
			}
			lock, err := streams.Acquire(a.Profile.Name)
			if err != nil {
				return err
			}
			defer lock.Release()
			st, err := openStore(a)
			if err != nil {
				return err
			}
			defer st.Close()
			if _, err := a.APIClient(); err != nil {
				return err
			}
			opts := streams.RecorderOptions{RadioIDs: ids, ForwardTo: forward, Owned: lock.Check}
			st.SetMeta("last_error", "")
			if child {
				if err := lock.MarkBackground(); err != nil {
					return err
				}
				logf, err := streams.OpenLog(a.Profile.Name)
				if err != nil {
					return err
				}
				defer logf.Close()
				var mu sync.Mutex
				logLine := func(format string, args ...any) {
					mu.Lock()
					defer mu.Unlock()
					fmt.Fprintf(logf, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
				}
				opts.Logf = logLine
				logLine("recorder started (pid %d, audd %s)", os.Getpid(), app.Version)
				defer logLine("recorder stopped")
				// The recorder itself notes a missing callback URL and
				// leaves the time as a gap until one is set.
			} else {
				if err := streams.EnsureCallbackURL(cmd.Context(), a); err != nil {
					return err
				}
				out := &liveOutput{a: a}
				opts.OnPlay, opts.OnHealth = out.play, out.health
				opts.Logf = func(f string, args ...any) { a.Out.Info(f, args...) }
				a.Out.Info("Recording %s to %s. Press Ctrl-C to stop.", describeStreams(ids), st.Path())
			}
			ctx, stop := streams.WithStopFile(cmd.Context(), a.Profile.Name)
			defer stop()
			return runRecorder(ctx, a, st, opts)
		},
	}
	cmd.Flags().StringVar(&forward, "forward-to", "", "also POST each result to this URL, shaped like an AudD callback")
	cmd.Flags().BoolVar(&child, streams.BackgroundChildFlag, false, "run as the background recorder (log to the recorder log, print nothing)")
	cmd.Flags().MarkHidden(streams.BackgroundChildFlag)
	return cmd
}

type recorderStatus struct {
	Profile            string     `json:"profile"`
	Running            bool       `json:"running"`
	PID                int        `json:"pid,omitempty"`
	Since              *time.Time `json:"since,omitempty"`
	BackgroundRecorder bool       `json:"background_recorder"`
	Store              string     `json:"store"`
	Plays              int        `json:"plays"`
	LastHeartbeat      *time.Time `json:"last_heartbeat,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
	Log                string     `json:"log"`
}

func readRecorderStatus(a *app.App) (recorderStatus, error) {
	s := recorderStatus{Profile: a.Profile.Name, BackgroundRecorder: a.Profile.RecorderEnabled(), Log: streams.LogPath(a.Profile.Name)}
	running, pid, since, err := streams.Status(a.Profile.Name)
	if err != nil {
		return s, err
	}
	s.Running, s.PID = running, pid
	if !since.IsZero() {
		s.Since = &since
	}
	st, err := openStore(a)
	if err != nil {
		return s, err
	}
	defer st.Close()
	s.Store = st.Path()
	s.Plays, _ = st.CountPlays()
	if hb, _ := st.LastHeartbeat(); !hb.IsZero() {
		s.LastHeartbeat = &hb
	}
	lastErr, _ := st.Meta("last_error")
	s.LastError = output.Redact(lastErr)
	return s, nil
}

func printRecorderStatus(a *app.App, s recorderStatus) error {
	return a.Out.Result(s, func(w io.Writer) {
		if s.Running {
			fmt.Fprintf(w, "The stream recorder is running (pid %d", s.PID)
			if s.Since != nil {
				fmt.Fprintf(w, ", since %s", s.Since.Local().Format("2006-01-02 15:04"))
			}
			fmt.Fprintln(w, ").")
		} else {
			fmt.Fprintln(w, "The stream recorder is not running. Start it with: audd streams recorder start")
		}
		if !s.BackgroundRecorder {
			fmt.Fprintln(w, "Automatic start is off (streams.background_recorder = false).")
		}
		fmt.Fprintf(w, "Plays recorded: %d\n", s.Plays)
		if s.LastHeartbeat != nil {
			fmt.Fprintf(w, "Last active:    %s\n", s.LastHeartbeat.Local().Format("2006-01-02 15:04:05"))
		}
		if s.LastError != "" {
			fmt.Fprintf(w, "Last problem:   %s\n", s.LastError)
		}
		fmt.Fprintf(w, "Store:          %s\nLog:            %s\n", s.Store, s.Log)
	})
}

func newStreamsRecorderCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recorder",
		Short: "Start, stop, or check the background stream recorder",
		Long: `The background recorder longpolls all of the profile's streams and saves
every play and stream status change to the local stream store. It starts
by itself the first time you use a streams command (unless
streams.background_recorder is false), survives closing the terminal, and
stops at reboot unless installed as a login service with --install-service.
The login service reads the token stored with audd login or audd config set
token, not --token or AUDD_API_TOKEN.`,
	}
	var install bool
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the background recorder",
		Long: `Start the background recorder for this profile, if it is not running. It
keeps running after the terminal closes and saves every result of every
stream on the account to the local stream store (audd streams history,
report, export, and now-playing read it). Its log is in the audd data
folder; audd streams recorder status shows where.

--install-service also writes a login service so the recorder starts
whenever you log in, and prints the command that turns it on.`,
		Example: `  audd streams recorder start
  audd streams recorder start --install-service`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if install {
				if err := requireStoredToken(a); err != nil {
					return err
				}
			}
			if err := streams.EnsureCallbackURL(cmd.Context(), a); err != nil {
				return err
			}
			res := map[string]any{"profile": a.Profile.Name}
			var notes []string
			if install {
				svc, err := streams.ServiceFor(a.Profile.Name)
				if err != nil {
					return err
				}
				path, err := streams.InstallService(a.Profile.Name)
				if err != nil {
					return err
				}
				res["service_file"], res["activate"] = path, svc.Activate
				if svc.StartsNow {
					// Turning the service on starts the recorder; one started
					// here would hold the lock and make the service fail.
					running, pid, _, _ := streams.Status(a.Profile.Name)
					res["started"], res["running"], res["pid"] = false, running, pid
					res["log"] = streams.LogPath(a.Profile.Name)
					return a.Out.Result(res, func(w io.Writer) {
						fmt.Fprintf(w, "Wrote %s.\n", path)
						if running {
							fmt.Fprintf(w, "A stream recorder is already running (pid %d). Stop it, then turn the service on:\n", pid)
							fmt.Fprintln(w, "  audd streams recorder stop")
						} else {
							fmt.Fprintln(w, "Turn it on (this also starts the recorder now):")
						}
						fmt.Fprintln(w, "  "+svc.Activate)
					})
				}
				notes = append(notes, "Wrote "+path+".", "Turn it on with: "+svc.Activate)
			}
			started, err := streams.StartBackground(a)
			if err != nil {
				return err
			}
			running, pid := false, 0
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
				running, pid, _, _ = streams.Status(a.Profile.Name)
				if running || !started || time.Now().After(deadline) {
					break
				}
			}
			res["started"], res["running"], res["pid"] = started, running, pid
			res["log"] = streams.LogPath(a.Profile.Name)
			if started && !running {
				return output.Errf(output.ExitUnexpected, "recorder_failed", "see "+streams.LogPath(a.Profile.Name),
					"the background recorder did not start")
			}
			return a.Out.Result(res, func(w io.Writer) {
				switch {
				case started:
					fmt.Fprintf(w, "Started the stream recorder (pid %d).\n", pid)
				default:
					fmt.Fprintf(w, "The stream recorder is already running (pid %d).\n", pid)
				}
				for _, n := range notes {
					fmt.Fprintln(w, n)
				}
			})
		},
	}
	start.Flags().BoolVar(&install, "install-service", false, "install a login service (systemd user unit, launchd agent, or scheduled task) that starts the recorder when you log in; it prints the command that turns the service on")

	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the background recorder",
		Long: `Stop the background recorder. It starts again the next time you use a
streams command unless you turn automatic start off:
audd config set streams.background_recorder false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := streams.Stop(a.Profile.Name)
			stopped := err == nil
			if err != nil && err != streams.ErrNotRunning {
				return err
			}
			return a.Out.Result(map[string]any{"profile": a.Profile.Name, "stopped": stopped, "running": false}, func(w io.Writer) {
				if stopped {
					fmt.Fprintln(w, "Stopped the stream recorder.")
				} else {
					fmt.Fprintln(w, "No stream recorder is running.")
				}
			})
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether the background recorder runs and what it saved",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := readRecorderStatus(a)
			if err != nil {
				return err
			}
			return printRecorderStatus(a, s)
		},
	}
	cmd.AddCommand(start, stop, status)
	return cmd
}

// requireStoredToken checks that the profile has a token the login service
// can read. The service starts without this shell's flags or environment, so
// a token given only with --token or AUDD_API_TOKEN would make it fail, and
// restart, at every login.
func requireStoredToken(a *app.App) error {
	if a.Secrets != nil {
		for _, key := range []string{"api_token", "login_api_token"} {
			if t, err := a.Secrets.Get(a.Profile.Name, key); err == nil && t != "" {
				return nil
			}
		}
	}
	profile := ""
	if a.Profile.Name != config.DefaultProfile {
		profile = " --profile " + a.Profile.Name
	}
	return output.Errf(output.ExitAuth, "no_token",
		"store one with: audd login"+profile+", or audd config set token"+profile+" your-api-token",
		"the login service needs a stored token; it does not see --token or AUDD_API_TOKEN")
}
