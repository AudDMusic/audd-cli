// Package app holds the state shared by every command (config, secrets,
// printer, flags), factories for API and account clients, and the hooks
// that let feature packages plug into each other without import cycles.
//
// Ownership: each hook and factory is assigned by exactly one package, in
// its init() or in its cli.Register callback. Everyone else only calls it.
//
//	RunBatch       internal/jobs     (batch recognition)
//	RunExplorer    internal/tui      (audd browse and friends)
//	RenderResult   internal/tui      (result card; plain text until then)
//	EnsureRecorder internal/streams  (background stream recorder)
//	RestartRecorder internal/streams (restart it after a token change)
//	App.APIClient  internal/api      (via cli.Register; also APIClientOnce)
//	App.Account    internal/account  (via cli.Register)
//
// The terminal screens read their data through two more hooks in
// internal/tui, which the integration assigns (until then the screens
// list streams from the API and show "not available" for the rest):
//
//	tui.NewFeed          tui.StoreFeed over internal/streamstore and
//	                     streams.RecentResults (now-playing, Streams tab)
//	tui.NewExplorerData  tui.DefaultExplorerData plus Recent (results
//	                     cache), Jobs and JobItems (jobs store)
//
// This package imports only output, config, secrets, account, and media.
package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
)

// Build information, set with -ldflags "-X github.com/AudDMusic/audd-cli/internal/app.Version=…".
// Builds without ldflags (go install) take it from the module build
// information instead; see buildinfo.go.
var (
	Version = defaultVersion
	Commit  = defaultCommit
	Date    = defaultDate
)

// App is the per-invocation state. The root command fills it in before any
// command runs.
type App struct {
	Cfg     *config.Config
	Profile *config.Profile
	Secrets secrets.Store
	Out     *output.Printer
	Flags   GlobalFlags
	In      io.Reader // stdin
	Now     func() time.Time

	// Factories. New sets defaults that return a not-implemented error; the
	// owning packages replace them (see the package comment).
	APIClient func() (*audd.Client, error)
	// APIClientOnce is APIClient for calls that must reach AudD at most
	// once (enterprise recognition, audd api): the client never retries a
	// request on its own. It defaults to APIClient.
	APIClientOnce func() (*audd.Client, error)
	Account       func() (account.Backend, error)
}

// GlobalFlags are the flags every command accepts.
type GlobalFlags struct {
	Token, Profile, Format string
	Fields                 []string
	Yes, Quiet, Debug      bool
	NoColor                bool
	MaxRequests            int
	// MaxRequestsFromConfig is set when MaxRequests came from the
	// max_requests setting, not --max-requests.
	MaxRequestsFromConfig bool
}

// MaxRequestsName names where the request ceiling came from, for messages:
// "--max-requests 50" or "the max_requests setting (50)".
func (f GlobalFlags) MaxRequestsName() string {
	if f.MaxRequestsFromConfig {
		return fmt.Sprintf("the max_requests setting (%d)", f.MaxRequests)
	}
	return fmt.Sprintf("--max-requests %d", f.MaxRequests)
}

// MaxRequestsHint is how to change the ceiling, for hints.
func (f GlobalFlags) MaxRequestsHint() string {
	if f.MaxRequestsFromConfig {
		return "pass a larger --max-requests N, or remove the setting with audd config unset max_requests"
	}
	return "raise --max-requests"
}

// New returns an App with default factories. The root command sets the rest.
func New() *App {
	a := &App{Now: time.Now}
	a.APIClient = func() (*audd.Client, error) { return nil, NotImplemented("API access") }
	a.APIClientOnce = func() (*audd.Client, error) { return a.APIClient() }
	a.Account = func() (account.Backend, error) { return nil, NotImplemented("account access") }
	return a
}

// NotImplemented is returned by hooks and factories nobody has assigned.
func NotImplemented(what string) *output.Error {
	return output.Errf(output.ExitUnexpected, "not_implemented", "", "%s is not available in this build", what)
}

// Cross-package hooks. Defaults report "not available in this build",
// except RenderResult, which defaults to PlainResult.
var (
	// RunBatch recognizes many inputs as a resumable job.
	RunBatch = func(ctx context.Context, a *App, opts BatchOptions) (BatchSummary, error) {
		return BatchSummary{}, NotImplemented("batch recognition")
	}
	// RunExplorer opens the interactive explorer on a tab
	// (recent|jobs|streams|usage); "jobs/<id>" opens that job.
	// ctx is the command's: Ctrl-C cancels it, including for a job resumed
	// from the explorer.
	RunExplorer = func(ctx context.Context, a *App, tab string) error {
		return NotImplemented("the interactive explorer")
	}
	// RenderResult draws one recognition result for people.
	RenderResult = PlainResult
	// EnsureRecorder starts the background stream recorder if it should run
	// and is not running. started reports whether this call started it.
	EnsureRecorder = func(a *App) (started bool, err error) {
		return false, NotImplemented("the background stream recorder")
	}
	// RestartRecorder restarts the profile's background stream recorder,
	// if one runs, so it uses the API token stored now. restarted reports
	// whether it did.
	RestartRecorder = func(a *App) (restarted bool, err error) { return false, nil }
)

// ResultView is a recognition result flattened for display.
type ResultView struct {
	Artist, Title, Album, Label, ReleaseDate string
	ISRC, UPC, SongLink                      string
	AppleArtwork, AppleBG                    string
	Timecode                                 string
	Score                                    int
	Cached                                   bool
	// NoArt is set by --no-art: the renderer must not draw cover art.
	NoArt bool
	// Extra holds the API's own fields beyond the ones above.
	Extra map[string]any
}

// PlainResult is the text rendering used until a richer renderer is set.
func PlainResult(w io.Writer, a *App, r ResultView, details bool) {
	fmt.Fprintf(w, "%s — %s\n", r.Artist, r.Title)
	var meta []string
	for _, s := range []string{r.Album, r.Label, r.ReleaseDate} {
		if s != "" {
			meta = append(meta, s)
		}
	}
	if len(meta) > 0 {
		fmt.Fprintln(w, strings.Join(meta, " · "))
	}
	if r.SongLink != "" {
		fmt.Fprintln(w, r.SongLink)
	}
	if details {
		for _, kv := range [][2]string{{"ISRC", r.ISRC}, {"UPC", r.UPC}, {"Timecode", r.Timecode}} {
			if kv[1] != "" {
				fmt.Fprintf(w, "%-9s %s\n", kv[0]+":", kv[1])
			}
		}
		if r.Score > 0 {
			fmt.Fprintf(w, "%-9s %d\n", "Score:", r.Score)
		}
	}
	if r.Cached {
		fmt.Fprintln(w, "(cached result, no request used)")
	}
}

// BatchOptions describe a batch recognition run.
type BatchOptions struct {
	Inputs         []media.Input
	Enterprise     bool
	Limit          *int // enterprise chunks per file; nil = no limit
	MaxFiles       *int // nil = no limit
	Return         string
	EnterpriseOpts map[string]string
	Concurrency    int
	DryRun         bool
	NoCache        bool
	Yes            bool
	RetryFailed    bool
	ResumeID       string
	// Resume and New answer "an identical earlier run did not finish":
	// continue that job, or start a new one. Without a terminal one of
	// them is required.
	Resume, New bool
}

// BatchSummary is the outcome of a batch run. The file counts are for the
// whole job; RequestsSpent and RequestsReserved are this run's.
type BatchSummary struct {
	JobID                                              string
	Recognized, NoMatch, Failed, Cached, RequestsSpent int
	// RequestsReserved is the most that enterprise calls for files of
	// unknown length may have used (not counted in RequestsSpent).
	RequestsReserved int
}
