package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cache"
	"github.com/AudDMusic/audd-cli/internal/jobs"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
	"github.com/AudDMusic/audd-cli/internal/tui"
)

// This file connects packages that were built side by side and only know
// each other through hooks: batch recognition goes through the same API
// layer and results cache as single recognitions, the terminal screens read
// the local stream store and the results cache, and the jobs tab reads the
// jobs store.

var (
	mediaToolsOnce sync.Once
	mediaTools     media.Tools
)

// findMedia locates ffmpeg, ffprobe and sox once per process.
func findMedia() media.Tools {
	mediaToolsOnce.Do(func() { mediaTools = media.Find() })
	return mediaTools
}

func init() {
	trimMedia = func(ctx context.Context, src string, at, dur time.Duration) (string, func(), error) {
		return media.Trim(ctx, findMedia(), src, at, dur)
	}
	probeDuration = func(path string) (time.Duration, bool) {
		return media.Duration(findMedia(), path)
	}

	jobs.Recognize = batchRecognize
	jobs.CacheGet = batchCacheGet
	jobs.CachePut = batchCachePut
	jobs.Durations = func(path string) (time.Duration, bool) { return probeDuration(path) }

	// The recent-results endpoint and --forward-to relays use the same
	// proxy settings and user agent as the API.
	streams.HTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: api.Transport()}
	streams.LongpollHTTPClient = &http.Client{Transport: api.Transport()}

	tui.NewFeed = newStoreFeed
	tui.NewExplorerData = newExplorerData
}

// requestFor is the API request a job's settings describe.
func requestFor(p jobs.Params) api.Request {
	return api.Request{Enterprise: p.Enterprise, Return: p.Return, Limit: p.Limit, EnterpriseOpts: p.Options}
}

// batchRecognize is jobs.Recognize: the API layer (token self-healing, error
// mapping), with the SDK error kept so the runner can tell safe retries.
func batchRecognize(ctx context.Context, a *app.App, in media.Input, p jobs.Params) (json.RawMessage, error) {
	return api.RecognizeWithCause(ctx, a, in, requestFor(p))
}

func batchCacheKey(in media.Input, p jobs.Params) (string, error) {
	r := requestFor(p)
	return cache.KeyForInput(in, r.Endpoint(), r.CacheParams())
}

// batchCacheGet is jobs.CacheGet: the same key as `audd recognize <file>`.
func batchCacheGet(a *app.App, in media.Input, p jobs.Params) (json.RawMessage, bool) {
	key, err := batchCacheKey(in, p)
	if err != nil {
		return nil, false
	}
	c, err := cache.Open()
	if err != nil {
		return nil, false
	}
	defer c.Close()
	return c.Get(key)
}

// batchCachePut is jobs.CachePut.
func batchCachePut(a *app.App, in media.Input, p jobs.Params, result json.RawMessage) {
	key, err := batchCacheKey(in, p)
	if err != nil {
		return
	}
	c, err := cache.Open()
	if err != nil {
		return
	}
	defer c.Close()
	if err := c.PutEntry(cache.Entry{Key: key, Source: in.Name(), Endpoint: requestFor(p).Endpoint(), Result: result}, in.URL != ""); err != nil && a.Out != nil {
		a.Out.Info("Could not save the result for %s to the cache: %v", in.Name(), err)
	}
}

// storePlays adapts the profile's stream store to tui.PlayStore. Each call
// opens the database briefly, so a long-running screen never holds it.
type storePlays struct{ a *app.App }

func (s storePlays) open() (*streamstore.Store, error) { return openStore(s.a) }

func (s storePlays) Plays(radioID int, limit int) ([]tui.Play, error) {
	st, err := s.open()
	if err != nil {
		return nil, err
	}
	defer st.Close()
	plays, err := st.History(&radioID, time.Time{}, limit)
	if err != nil {
		return nil, err
	}
	out := make([]tui.Play, 0, len(plays))
	for _, p := range plays {
		out = append(out, tuiPlay(p))
	}
	return out, nil
}

func (s storePlays) AddPlay(p tui.Play) error {
	st, err := s.open()
	if err != nil {
		return err
	}
	defer st.Close()
	_, err = st.AddPlay(storePlay(p))
	return err
}

// tuiPlay converts a stored play for the screens.
func tuiPlay(p streamstore.Play) tui.Play {
	if len(p.Raw) > 0 {
		if v, length, ok := tui.ViewFromJSON(p.Raw); ok {
			if v.Score == 0 {
				v.Score = p.Score
			}
			return tui.Play{RadioID: p.RadioID, At: p.Timestamp, PlayLength: p.PlayLength, TrackLength: length, ResultView: v, Raw: p.Raw}
		}
	}
	return tui.Play{RadioID: p.RadioID, At: p.Timestamp, PlayLength: p.PlayLength, Raw: p.Raw, ResultView: app.ResultView{
		Artist: p.Artist, Title: p.Title, Album: p.Album, Label: p.Label, ReleaseDate: p.ReleaseDate,
		ISRC: p.ISRC, UPC: p.UPC, SongLink: p.SongLink, Score: p.Score,
	}}
}

// storePlay converts a play the screens fetched for the store.
func storePlay(p tui.Play) streamstore.Play {
	return streamstore.Play{
		RadioID: p.RadioID, Timestamp: p.At, PlayLength: p.PlayLength,
		Artist: p.Artist, Title: p.Title, Album: p.Album, Label: p.Label, ReleaseDate: p.ReleaseDate,
		ISRC: p.ISRC, UPC: p.UPC, SongLink: p.SongLink, Score: p.Score, Raw: p.Raw,
	}
}

// newStoreFeed is tui.NewFeed: stations from the API with their latest
// health from the store, plays from the store, and the recent-results
// endpoint when the store has nothing current for a stream.
func newStoreFeed(a *app.App) (tui.Feed, error) {
	store := storePlays{a: a}
	return &tui.StoreFeed{
		Store: store,
		Now:   a.Now,
		StationsFunc: func(ctx context.Context) ([]tui.Station, error) {
			list, err := tui.APIStations(ctx, a)
			if err != nil {
				return nil, streams.MapError(a, err)
			}
			st, err := store.open()
			if err != nil {
				return list, nil
			}
			defer st.Close()
			for i := range list {
				h, err := st.LatestHealth(list[i].RadioID)
				if err != nil || h == nil {
					continue
				}
				// A stream that is running again is healthy, unless AudD
				// reports that it hears no music (651).
				code := h.Code
				if h.Running && code != 651 {
					code = 0
				}
				list[i].Health = &tui.Health{Code: code, Message: h.Message, At: h.At}
			}
			return list, nil
		},
		Recent: func(ctx context.Context, radioID int) ([]tui.Play, error) {
			c, err := a.APIClient()
			if err != nil {
				return nil, err
			}
			plays, err := streams.RecentResults(ctx, radioID, audd.DeriveLongpollCategory(c.APIToken(), radioID))
			if err != nil {
				return nil, err
			}
			out := make([]tui.Play, 0, len(plays))
			for _, p := range plays {
				p.RadioID = radioID
				out = append(out, tuiPlay(p))
			}
			return out, nil
		},
		RecorderRunning: func(radioID int) bool {
			st, err := store.open()
			if err != nil {
				return false
			}
			defer st.Close()
			hb, err := st.LastHeartbeat(radioID)
			if err != nil || hb.IsZero() {
				return false
			}
			return a.Now().Sub(hb) <= tui.HeartbeatWindow
		},
	}, nil
}

// newExplorerData is tui.NewExplorerData: the default sources plus the
// results cache (Recent tab) and the jobs store (Jobs tab).
func newExplorerData(a *app.App) (tui.ExplorerData, error) {
	d, err := tui.DefaultExplorerData(a)
	if err != nil {
		return d, err
	}
	d.Recent = func(ctx context.Context, limit int) ([]tui.RecentItem, error) {
		return recentResults(limit)
	}
	d.Jobs = func(ctx context.Context) ([]tui.JobRow, error) {
		st, err := jobs.Open()
		if err != nil {
			return nil, err
		}
		defer st.Close()
		list, err := jobs.List(st)
		if err != nil {
			return nil, err
		}
		out := make([]tui.JobRow, 0, len(list))
		for _, j := range list {
			out = append(out, tui.JobRow{ID: j.ID, Command: j.Command, Created: j.Created, Total: j.Total, Done: j.Done, Failed: j.Failed, Status: j.Status})
		}
		return out, nil
	}
	d.JobItems = func(ctx context.Context, id string) ([]tui.JobItem, error) {
		st, err := jobs.Open()
		if err != nil {
			return nil, err
		}
		defer st.Close()
		items, err := st.Items(id)
		if err != nil {
			return nil, err
		}
		out := make([]tui.JobItem, 0, len(items))
		for _, it := range items {
			out = append(out, tui.JobItem{Index: it.Index, Input: it.Input.Name(), State: it.State, Result: it.Result, Err: it.Err})
		}
		return out, nil
	}
	return d, nil
}

// recentResults is the explorer's Recent tab: everything recognized through
// the CLI, from the results cache and the jobs store, newest first. A result
// in both (a batch result is also cached) is listed once.
func recentResults(limit int) ([]tui.RecentItem, error) {
	if limit <= 0 {
		limit = 100
	}
	var (
		out  []tui.RecentItem
		errs []error
	)
	seen := map[string]bool{}
	add := func(source string, at time.Time, result json.RawMessage) {
		k := source + "\x00" + string(result)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, tui.RecentItem{Source: source, At: at, Result: result})
	}
	if c, err := cache.Open(); err != nil {
		errs = append(errs, err)
	} else {
		entries, err := c.Recent(limit)
		c.Close()
		if err != nil {
			errs = append(errs, err)
		}
		for _, e := range entries {
			add(e.Source, e.At, e.Result)
		}
	}
	if st, err := jobs.Open(); err != nil {
		errs = append(errs, err)
	} else {
		items, err := st.RecentResults(limit)
		st.Close()
		if err != nil {
			errs = append(errs, err)
		}
		for _, it := range items {
			add(it.Source, it.At, it.Result)
		}
	}
	if len(errs) == 2 {
		return nil, errs[0]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
