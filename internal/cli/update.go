package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/update"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newUpdateCmd(a))
	})
}

// executablePath finds the running binary (replaced in tests).
var executablePath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// installMethod is how this binary was installed.
func installMethod() update.Method {
	exe, err := executablePath()
	if err != nil {
		return update.Method{Name: "binary", SelfUpdate: true}
	}
	return update.DetectMethod(exe, runtime.GOOS, os.Getenv)
}

type updateResult struct {
	Current  string `json:"current"`
	Latest   string `json:"latest"`
	UpToDate bool   `json:"up_to_date"`
	Updated  bool   `json:"updated"`
	update.Method
	ReleaseURL string `json:"release_url,omitempty"`
}

func newUpdateCmd(a *app.App) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update audd to the latest release",
		Long: `Check for a newer audd release and install it.

When audd was installed with a package manager (Homebrew, npm, pip, pipx, uv,
Scoop, winget, go install, or Docker) or runs through npx or uvx, this prints
the command that updates it instead, so the package manager stays in
charge. Binaries installed with the install script or by hand are replaced
in place, after the download is checked against the release checksums.

--check only reports whether an update is available.

audd also mentions a new release at most once a day on a terminal. Turn that
off with AUDD_NO_UPDATE_CHECK=1; it is always off in CI.`,
		Example: `  audd update
  audd update --check`,
		GroupID:     GroupOther,
		Annotations: map[string]string{annotationNoConfig: "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client := accountHTTPClient(a, 2*time.Minute)
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			rel, err := update.Latest(cctx, client, update.APIBase())
			cancel()
			if err != nil {
				return &output.Error{Code: "network", Exit: output.ExitNetwork, Retryable: true,
					Message: "could not check for updates: " + err.Error(),
					Hint:    "see https://github.com/" + update.Repo + "/releases"}
			}
			_ = update.SaveState(config.CacheDir(), update.State{CheckedAt: a.Now(), Latest: rel.Version, URL: rel.URL})
			m := installMethod()
			res := updateResult{Current: app.Version, Latest: rel.Version, Method: m, ReleaseURL: rel.URL}
			res.UpToDate = !update.Newer(rel.Version, app.Version)
			if res.UpToDate || check || !m.SelfUpdate {
				return a.Out.Result(res, func(w io.Writer) {
					switch {
					case res.UpToDate:
						fmt.Fprintf(w, "audd %s is the latest release.\n", app.Version)
					case !m.SelfUpdate:
						fmt.Fprintf(w, "audd %s is available (you have %s). You installed audd with %s; update it with:\n  %s\n", rel.Version, app.Version, m.Name, m.Command)
					default:
						fmt.Fprintf(w, "audd %s is available (you have %s). Install it with: audd update\n", rel.Version, app.Version)
					}
				})
			}
			exe, err := executablePath()
			if err != nil {
				return output.Errf(output.ExitUnexpected, "update_failed", "", "could not find the audd binary: %v", err)
			}
			a.Out.Info("Downloading audd %s…", rel.Version)
			if err := update.Apply(ctx, client, rel, exe, runtime.GOOS, runtime.GOARCH); err != nil {
				e := &output.Error{Code: "update_failed", Exit: output.ExitUnexpected, Message: "could not update audd: " + err.Error(),
					Hint: "download it from " + rel.URL}
				if errors.Is(err, os.ErrPermission) {
					e.Hint = "run audd update as a user who can write to " + filepath.Dir(exe) + ", or reinstall from " + rel.URL
				}
				var ne interface{ Timeout() bool }
				if errors.As(err, &ne) {
					e.Code, e.Exit, e.Retryable = "network", output.ExitNetwork, true
				}
				return e
			}
			res.Updated = true
			return a.Out.Result(res, func(w io.Writer) {
				fmt.Fprintf(w, "Updated audd %s → %s.\n", app.Version, rel.Version)
			})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release is available")
	return cmd
}

// noticeSkip lists commands that never show the update notice.
var noticeSkip = []string{"update", "mcp", "completion", "__complete", "__completeNoDesc", "record"}

// noticeWait bounds how long a finished command waits for the daily release
// check before exiting without the notice.
const noticeWait = 400 * time.Millisecond

// updateChecks tracks background release checks (tests wait on it).
var updateChecks sync.WaitGroup

// startUpdateNotice runs the daily release check alongside the command and
// returns a function that prints the notice (to stderr) when a newer
// release is known. It does nothing off a terminal, in CI, or with
// AUDD_NO_UPDATE_CHECK set.
func startUpdateNotice(root *cobra.Command, args []string, stdio IO) func() {
	if !update.NoticeAllowed(update.NoticeEnv{StderrTTY: stdio.StderrTTY, Getenv: os.Getenv}) {
		return func() {}
	}
	if c, _, err := root.Find(args); err == nil && c != nil && (slices.Contains(noticeSkip, c.Name()) || c == root) {
		return func() {}
	}
	for _, a := range args {
		if a == "--quiet" || a == "-q" || a == "--help" || a == "-h" {
			return func() {}
		}
	}
	dir := config.CacheDir()
	st := update.LoadState(dir)
	now := time.Now()
	var done chan struct{}
	if st.Due(now) {
		// Record the check before making it, so a slow or unreachable
		// release API is tried at most once a day rather than on every run.
		_ = update.SaveState(dir, update.State{CheckedAt: now, Latest: st.Latest, URL: st.URL, NoticedAt: st.NoticedAt})
		done = make(chan struct{})
		updateChecks.Add(1)
		go func() {
			defer updateChecks.Done()
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ns := update.State{CheckedAt: now, Latest: st.Latest, URL: st.URL}
			if rel, err := update.Latest(ctx, &http.Client{Timeout: 5 * time.Second}, update.APIBase()); err == nil {
				ns.Latest, ns.URL = rel.Version, rel.URL
			}
			// Keep the time the notice was last shown, which may have
			// changed while the check ran.
			ns.NoticedAt = update.LoadState(dir).NoticedAt
			_ = update.SaveState(dir, ns)
		}()
	}
	return func() {
		if done != nil {
			select {
			case <-done:
				st = update.LoadState(dir)
			case <-time.After(noticeWait):
			}
		}
		if !st.NoticeDue(time.Now()) {
			return
		}
		if msg := update.Notice(app.Version, st, installMethod()); msg != "" {
			fmt.Fprintln(stdio.Err, msg)
			st.NoticedAt = time.Now()
			_ = update.SaveState(dir, st)
		}
	}
}
