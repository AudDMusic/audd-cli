package tui

import (
	"context"
	"encoding/json"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/streams"
)

// Station is one monitored stream.
type Station struct {
	RadioID int     `json:"radio_id"`
	URL     string  `json:"url,omitempty"`
	Running bool    `json:"stream_running"`
	Health  *Health `json:"health,omitempty"` // latest health notification, if any
	// StatusUnknown is set when the stream list could not be read, so
	// whether the stream runs is not known.
	StatusUnknown bool `json:"-"`
}

// Health is a stream health notification (650: can't connect, 651: no music).
type Health struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Down reports whether the station is not delivering recognitions right now.
func (s Station) Down() bool {
	return s.Health != nil && s.Health.Code != 0
}

// HealthText describes a station's health for people.
func (s Station) HealthText() string {
	if s.StatusUnknown && s.Health == nil {
		return "Status unknown"
	}
	if s.Health == nil || s.Health.Code == 0 {
		if !s.Running {
			return "Stream is stopped"
		}
		return "Live"
	}
	switch s.Health.Code {
	case 650:
		return "Can't connect to the stream (650)"
	case 651:
		return "No music detected (651)"
	}
	if s.Health.Message != "" {
		return s.Health.Message
	}
	return "Stream problem"
}

// healthShort is HealthText for table cells.
func (s Station) healthShort() string {
	if s.Health != nil {
		switch s.Health.Code {
		case 650:
			return "Can't connect (650)"
		case 651:
			return "No music (651)"
		}
	}
	if s.StatusUnknown && s.Health == nil {
		return "Unknown"
	}
	if s.Health == nil || s.Health.Code == 0 {
		if !s.Running {
			return "Stopped"
		}
		return "Live"
	}
	return s.HealthText()
}

// Play is one song recognized on a stream.
type Play struct {
	RadioID int
	// At is when AudD detected the start of the song.
	At time.Time
	// PlayLength is how long the song had played when AudD reported it, in
	// seconds (from the callback's play_length).
	PlayLength int
	// TrackLength is the full song length, when metadata includes it.
	TrackLength time.Duration
	app.ResultView
	// Raw is the original song object from the API, when known.
	Raw json.RawMessage
}

// key identifies a play for change detection.
func (p Play) key() string {
	return p.At.UTC().Format(time.RFC3339) + "\x00" + p.Artist + "\x00" + p.Title
}

// Elapsed is the time since AudD detected the start of the song.
func (p Play) Elapsed(now time.Time) time.Duration {
	if p.At.IsZero() {
		return 0
	}
	return now.Sub(p.At)
}

// PlayState says what a stream result tells about the song now.
type PlayState string

const (
	// StatePlaying: the result arrived when the song started (a stream
	// added with --start) and the song is probably still on.
	StatePlaying PlayState = "playing"
	// StateJustPlayed: the result arrived when the song ended (the
	// default) a few minutes ago at most.
	StateJustPlayed PlayState = "just_played"
	// StateLastRecognized: the newest result is older than that.
	StateLastRecognized PlayState = "last_recognized"
)

// justPlayedFor is how long after a song ended it counts as just played.
const justPlayedFor = 5 * time.Minute

// liveFor is how long a song whose length is unknown counts as playing.
const liveFor = 10 * time.Minute

// Ended reports when the song ended, for results delivered at the end of
// the song (they carry play_length). ok is false otherwise.
func (p Play) Ended() (t time.Time, ok bool) {
	if p.PlayLength <= 0 || p.At.IsZero() {
		return time.Time{}, false
	}
	return p.At.Add(time.Duration(p.PlayLength) * time.Second), true
}

// Played is how long the song played before AudD reported it (play_length).
func (p Play) Played() time.Duration { return time.Duration(p.PlayLength) * time.Second }

// Ago is the time since the song was last heard: since it ended when the
// result says so, else since it started.
func (p Play) Ago(now time.Time) time.Duration {
	if end, ok := p.Ended(); ok {
		return now.Sub(end)
	}
	return p.Elapsed(now)
}

// State is what the result says about the song now. A result with
// play_length arrived when the song ended, so the song is over. A result
// without it arrived when the song started: the song plays for its track
// length when that is known (provider metadata), else for up to 10 minutes.
func (p Play) State(now time.Time) PlayState {
	if _, ok := p.Ended(); ok {
		if p.Ago(now) <= justPlayedFor {
			return StateJustPlayed
		}
		return StateLastRecognized
	}
	e := p.Elapsed(now)
	if p.TrackLength > 0 {
		if e <= p.TrackLength+30*time.Second {
			return StatePlaying
		}
		return StateLastRecognized
	}
	if e <= liveFor {
		return StatePlaying
	}
	return StateLastRecognized
}

// Current reports whether the song is probably still playing.
func (p Play) Current(now time.Time) bool { return p.State(now) == StatePlaying }

// Timing is the machine form of State: the state with the times that go
// with it. elapsed_seconds (since the song started) is set only while it
// plays; played_seconds and ended_at only for results delivered when the
// song ended; ago_seconds (since it ended, or since it started when the end
// is not known) only when it is not playing; length_seconds when the track
// length is known.
func (p Play) Timing(now time.Time) map[string]any {
	st := p.State(now)
	m := map[string]any{"state": string(st), "playing": st == StatePlaying}
	if st == StatePlaying {
		m["elapsed_seconds"] = secs(p.Elapsed(now))
	} else {
		m["ago_seconds"] = secs(p.Ago(now))
	}
	if end, ok := p.Ended(); ok {
		m["played_seconds"] = p.PlayLength
		m["ended_at"] = end.UTC().Format(time.RFC3339)
	}
	if p.TrackLength > 0 {
		m["length_seconds"] = secs(p.TrackLength)
	}
	return m
}

func secs(d time.Duration) int {
	if d < 0 {
		return 0
	}
	return int(d / time.Second)
}

// JSON is the machine form of a play, shaped like a stream callback result.
func (p Play) JSON() map[string]any {
	m := map[string]any{"radio_id": p.RadioID}
	if !p.At.IsZero() {
		m["timestamp"] = p.At.UTC().Format(time.RFC3339)
	}
	if p.PlayLength > 0 {
		m["play_length"] = p.PlayLength
	}
	var song any = ViewJSON(p.ResultView)
	if len(p.Raw) > 0 && json.Valid(p.Raw) {
		song = p.Raw
	}
	m["result"] = song
	return m
}

// Feed supplies stream data to now-playing and the explorer's Streams tab.
// Stations lists the account's streams with their latest health; Plays
// returns the recent plays of one stream, newest first. Plays reads the
// local stream store (kept complete by the background recorder) and falls
// back to the recent-results endpoint (the last ~30 results per stream).
type Feed interface {
	Stations(ctx context.Context) ([]Station, error)
	Plays(ctx context.Context, radioID int, limit int) ([]Play, error)
}

// FeedFuncs adapts two functions to a Feed.
type FeedFuncs struct {
	StationsFunc func(ctx context.Context) ([]Station, error)
	PlaysFunc    func(ctx context.Context, radioID int, limit int) ([]Play, error)
}

func (f FeedFuncs) Stations(ctx context.Context) ([]Station, error) {
	if f.StationsFunc == nil {
		return nil, app.NotImplemented("stream listing")
	}
	return f.StationsFunc(ctx)
}

func (f FeedFuncs) Plays(ctx context.Context, radioID int, limit int) ([]Play, error) {
	if f.PlaysFunc == nil {
		return nil, app.NotImplemented("stream results")
	}
	return f.PlaysFunc(ctx, radioID, limit)
}

// NewFeed builds the Feed for an invocation. The default lists streams with
// the API but has no plays; the integration replaces it with a StoreFeed
// over the local stream store and the recent-results endpoint (see the
// package comment).
var NewFeed = func(a *app.App) (Feed, error) {
	return FeedFuncs{StationsFunc: func(ctx context.Context) ([]Station, error) { return APIStations(ctx, a) }}, nil
}

// APIStations lists the account's streams with getStreams, healing a
// rejected login token like every other API call.
func APIStations(ctx context.Context, a *app.App) ([]Station, error) {
	list, err := streams.Do(ctx, a, func(c *audd.Client) ([]audd.Stream, error) { return c.Streams().ListContext(ctx) })
	if err != nil {
		return nil, err
	}
	out := make([]Station, 0, len(list))
	for _, s := range list {
		out = append(out, Station{RadioID: s.RadioID, URL: s.URL, Running: s.StreamRunning})
	}
	return out, nil
}
