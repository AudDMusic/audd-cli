package tui

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// PlayStore is the part of the local stream store the screens use.
type PlayStore interface {
	// Plays returns up to limit plays of a stream, newest first.
	Plays(radioID int, limit int) ([]Play, error)
	// AddPlay saves a play; duplicates (same stream and timestamp) are ignored.
	AddPlay(p Play) error
}

// HeartbeatWindow is how old the newest stored play may be, while the
// recorder is not running, before readers also ask the recent-results
// endpoint. The recorder writes a heartbeat every 30 seconds.
const HeartbeatWindow = 2 * time.Minute

// recentMinGap limits recent-results requests per stream.
const recentMinGap = 15 * time.Second

// StoreFeed reads plays from the local stream store first. It asks the
// recent-results endpoint (the last ~30 results of a stream) only when the
// store has nothing for the stream, or when no recorder keeps the stream
// current and the newest stored play is older than HeartbeatWindow; what it fetches
// is written into the store before it is shown.
type StoreFeed struct {
	StationsFunc func(ctx context.Context) ([]Station, error)
	Store        PlayStore
	// Recent fetches the recent-results endpoint for a stream.
	Recent func(ctx context.Context, radioID int) ([]Play, error)
	// RecorderRunning reports whether a recorder keeps a stream current
	// (it has written a heartbeat for the stream within HeartbeatWindow).
	RecorderRunning func(radioID int) bool
	Now             func() time.Time

	mu        sync.Mutex
	lastFetch map[int]time.Time
}

func (f *StoreFeed) Stations(ctx context.Context) ([]Station, error) {
	if f.StationsFunc == nil {
		return nil, nil
	}
	return f.StationsFunc(ctx)
}

func (f *StoreFeed) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Plays implements Feed.
func (f *StoreFeed) Plays(ctx context.Context, radioID int, limit int) ([]Play, error) {
	plays, err := f.Store.Plays(radioID, limit)
	if err != nil {
		return nil, err
	}
	if !f.stale(radioID, plays) || f.Recent == nil || !f.mayFetch(radioID) {
		return plays, nil
	}
	recent, rerr := f.Recent(ctx, radioID)
	if rerr != nil {
		if len(plays) > 0 {
			return plays, nil // show what the store has
		}
		return nil, rerr
	}
	for _, p := range recent {
		p.RadioID = radioID
		if err := f.Store.AddPlay(p); err != nil {
			return nil, err
		}
	}
	return f.Store.Plays(radioID, limit)
}

func (f *StoreFeed) stale(radioID int, plays []Play) bool {
	if len(plays) == 0 {
		return true
	}
	if f.RecorderRunning != nil && f.RecorderRunning(radioID) {
		return false
	}
	newest := plays[0].At
	for _, p := range plays {
		if p.At.After(newest) {
			newest = p.At
		}
	}
	return f.now().Sub(newest) > HeartbeatWindow
}

func (f *StoreFeed) mayFetch(radioID int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastFetch == nil {
		f.lastFetch = map[int]time.Time{}
	}
	now := f.now()
	if last, ok := f.lastFetch[radioID]; ok && now.Sub(last) < recentMinGap {
		return false
	}
	f.lastFetch[radioID] = now
	return true
}

// PlayFromRaw builds a Play from a stored or fetched song object.
func PlayFromRaw(radioID int, at time.Time, playLength int, raw json.RawMessage) Play {
	v, length, _ := ViewFromJSON(raw)
	return Play{RadioID: radioID, At: at, PlayLength: playLength, TrackLength: length, ResultView: v, Raw: raw}
}
