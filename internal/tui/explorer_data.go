package tui

import (
	"context"
	"encoding/json"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/streams"
)

// RecentItem is one result recognized through the CLI (results cache and
// batch jobs).
type RecentItem struct {
	Source string          `json:"source"` // file path or URL
	At     time.Time       `json:"at"`
	Result json.RawMessage `json:"result"` // the API result (object, or array for enterprise)
}

// JobRow is a batch job.
type JobRow struct {
	ID      string    `json:"id"`
	Command []string  `json:"command"`
	Created time.Time `json:"created"`
	Total   int       `json:"total"`
	Done    int       `json:"done"`
	Failed  int       `json:"failed"`
	Status  string    `json:"status"`
}

// JobItem is one input of a batch job.
type JobItem struct {
	Index  int             `json:"index"`
	Input  string          `json:"input"`
	State  string          `json:"state"` // pending, done, failed
	Result json.RawMessage `json:"result,omitempty"`
	Err    string          `json:"error,omitempty"`
}

// ExplorerData supplies the explorer's tabs. A nil source shows a short
// note in its tab instead of data.
type ExplorerData struct {
	Recent   func(ctx context.Context, limit int) ([]RecentItem, error)
	Jobs     func(ctx context.Context) ([]JobRow, error)
	JobItems func(ctx context.Context, id string) ([]JobItem, error)
	Feed     Feed
	Usage    func(ctx context.Context, days int) (*account.Usage, error)

	// Stream management from the Streams tab.
	AddStream    func(ctx context.Context, url string, radioID int) error
	RemoveStream func(ctx context.Context, radioID int) error
	SetStreamURL func(ctx context.Context, radioID int, url string) error
}

// NewExplorerData builds the explorer's sources for an invocation. The
// default covers streams (NewFeed), stream management (API), and usage
// (account); the results cache and the jobs store are added where those
// packages are wired in.
var NewExplorerData = DefaultExplorerData

// DefaultExplorerData is the default NewExplorerData.
func DefaultExplorerData(a *app.App) (ExplorerData, error) {
	d := ExplorerData{
		Usage: func(ctx context.Context, days int) (*account.Usage, error) {
			b, err := a.Account()
			if err != nil {
				return nil, err
			}
			return b.Usage(ctx, days)
		},
		AddStream: func(ctx context.Context, url string, radioID int) error {
			_, err := streams.Do(ctx, a, func(c *audd.Client) (struct{}, error) {
				return struct{}{}, c.Streams().AddContext(ctx, audd.AddStreamRequest{URL: url, RadioID: radioID})
			})
			return err
		},
		RemoveStream: func(ctx context.Context, radioID int) error {
			_, err := streams.Do(ctx, a, func(c *audd.Client) (struct{}, error) {
				return struct{}{}, c.Streams().DeleteContext(ctx, radioID)
			})
			return err
		},
		SetStreamURL: func(ctx context.Context, radioID int, url string) error {
			_, err := streams.Do(ctx, a, func(c *audd.Client) (struct{}, error) {
				return struct{}{}, c.Streams().SetURLContext(ctx, radioID, url)
			})
			return err
		},
	}
	f, err := NewFeed(a)
	if err != nil {
		return d, err
	}
	d.Feed = f
	return d, nil
}
