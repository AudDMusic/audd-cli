package streams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
	"github.com/AudDMusic/audd-cli/internal/streams/streamstest"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

type harness struct {
	srv   *streamstest.Server
	store *streamstore.Store
	app   *app.App

	mu     sync.Mutex
	plays  []streamstore.Play
	health []streamstore.HealthEvent
	logs   []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	srv := streamstest.New(t)
	useFake(t, srv)
	st, err := streamstore.OpenPath(filepath.Join(t.TempDir(), "streams.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := app.New()
	a.APIClient = func() (*audd.Client, error) { return srv.Client(), nil }
	return &harness{srv: srv, store: st, app: a}
}

func (h *harness) recorder(opts RecorderOptions) *Recorder {
	opts.OnPlay = func(p streamstore.Play) {
		h.mu.Lock()
		h.plays = append(h.plays, p)
		h.mu.Unlock()
	}
	opts.OnHealth = func(e streamstore.HealthEvent) {
		h.mu.Lock()
		h.health = append(h.health, e)
		h.mu.Unlock()
	}
	opts.Logf = func(format string, args ...any) {
		h.mu.Lock()
		h.logs = append(h.logs, format)
		h.mu.Unlock()
	}
	r := NewRecorder(h.app, h.store, opts)
	r.pollTimeout = 1
	r.rescanEvery = 100 * time.Millisecond
	r.heartbeatEvery = 50 * time.Millisecond
	r.backoffMin = 10 * time.Millisecond
	r.backoffMax = 50 * time.Millisecond
	r.upAfter = 300 * time.Millisecond
	return r
}

// start runs the recorder in the background; the returned func stops it
// and returns Run's error.
func start(t *testing.T, r *Recorder) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var once sync.Once
	var err error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Error("recorder did not stop")
			}
		})
		return err
	}
	t.Cleanup(func() { stop() })
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) count() int {
	n, _ := h.store.CountPlays()
	return n
}

func (h *harness) livePlays() []streamstore.Play {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]streamstore.Play(nil), h.plays...)
}

func TestRecorderBackfillThenLiveDedupes(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, URL: "https://radio.example/1", Running: true})
	h.srv.SetRecent(1, streamstest.RecentBody(30,
		map[string]any{"artist": "A", "title": "One", "timestamp": "2026-10-08 14:00:00", "play_length": 200},
		map[string]any{"artist": "B", "title": "Two", "timestamp": "2026-10-08 14:04:00", "play_length": 180},
	))
	stop := start(t, h.recorder(RecorderOptions{}))
	waitFor(t, "backfill", func() bool { return h.count() == 2 })
	if len(h.livePlays()) != 0 {
		t.Fatal("the start-up backfill is not reported as live plays")
	}
	// The live feed repeats the last backfilled play, then a new one.
	h.srv.PushMatch(1, "2026-10-08 14:04:00", "B", "Two", 180)
	h.srv.PushMatch(1, "2026-10-08 14:07:00", "C", "Three", 150)
	waitFor(t, "live plays", func() bool { return h.count() == 3 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	// B was already stored by the backfill, so only C is reported.
	if live := h.livePlays(); len(live) != 1 || live[0].Title != "Three" {
		t.Fatalf("live plays %+v", live)
	}
	if h.count() != 3 {
		t.Fatalf("store has %d plays, want 3", h.count())
	}
	latest, _ := h.store.Latest(1)
	if latest.Title != "Three" || latest.Label != "Label" || latest.RadioID != 1 {
		t.Fatalf("latest %+v", latest)
	}
	// The backfill reached back to 14:00 (UTC+3), so the store is complete from then.
	gaps, _ := h.store.Gaps(time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC))
	for _, g := range gaps {
		if g.From.Before(time.Date(2026, 10, 8, 11, 0, 1, 0, time.UTC)) && g.To.After(time.Now().Add(-time.Minute)) {
			t.Fatalf("backfill coverage not recorded: %+v", gaps)
		}
	}
	if last, _ := h.store.LastHeartbeat(); time.Since(last) > time.Minute {
		t.Fatalf("heartbeat %v", last)
	}
}

func TestRecorderReconnectsAfterServerErrors(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 2, Running: true})
	h.srv.FailNextPolls(3)
	start(t, h.recorder(RecorderOptions{}))
	waitFor(t, "reconnects", func() bool { return h.srv.Count("longpoll") >= 4 })
	h.srv.PushMatch(2, "2026-10-08 14:07:00", "C", "Three", 150)
	waitFor(t, "play after reconnect", func() bool { return h.count() == 1 })
	// Every reconnect backfills from the recent-results endpoint.
	if n := h.srv.Count("getChannelById"); n < 3 {
		t.Fatalf("recent results fetched %d times", n)
	}
	if v, _ := h.store.Meta("last_error"); v == "" {
		t.Fatal("connection problems are noted for recorder status")
	}
}

func TestRecorderReconnectBackfillReportsMissedPlays(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 2, Running: true})
	h.srv.FailNextPolls(1)
	h.srv.FailNextRecent(1) // the start-up backfill fails
	// After the failed poll, the reconnect backfill finds a play.
	h.srv.SetRecent(2, streamstest.RecentBody(30, map[string]any{"artist": "M", "title": "Missed", "timestamp": "2026-10-08 14:00:00"}))
	start(t, h.recorder(RecorderOptions{}))
	waitFor(t, "missed play", func() bool { return len(h.livePlays()) == 1 })
	if p := h.livePlays()[0]; p.Title != "Missed" || p.RadioID != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestRecorderRescanPicksUpNewStreamsAndHealth(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	start(t, h.recorder(RecorderOptions{}))
	waitFor(t, "first poll", func() bool { return h.srv.Count("longpoll") >= 1 })
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true}, streamstest.Stream{RadioID: 9, Running: false})
	waitFor(t, "new stream polled", func() bool {
		for _, c := range h.srv.Calls() {
			if c.Method == "longpoll" && c.Params.Get("category") == streamstest.Category(9) {
				return true
			}
		}
		return false
	})
	h.srv.PushMatch(9, "2026-10-08 15:00:00", "N", "New", 100)
	waitFor(t, "play on new stream", func() bool { p, _ := h.store.Latest(9); return p != nil })
	hv, _ := h.store.LatestHealth(9)
	if hv == nil || hv.Running {
		t.Fatalf("a stream that is not running is recorded: %+v", hv)
	}
	h.srv.PushNotification(9, 650, "can't connect to the stream", false, 1791457320)
	waitFor(t, "notification", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, e := range h.health {
			if e.Code == 650 {
				return true
			}
		}
		return false
	})
	hv, _ = h.store.LatestHealth(9)
	if hv.Code != 650 || hv.Running || hv.At.Unix() != 1791457320 {
		t.Fatalf("health %+v", hv)
	}
}

func TestRecorderOnlySelectedStreams(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true}, streamstest.Stream{RadioID: 2, Running: true})
	start(t, h.recorder(RecorderOptions{RadioIDs: []int{2}}))
	waitFor(t, "poll", func() bool { return h.srv.Count("longpoll") >= 2 })
	for _, c := range h.srv.Calls() {
		if c.Method == "longpoll" && c.Params.Get("category") == streamstest.Category(1) {
			t.Fatal("stream 1 was not selected")
		}
	}
}

func TestRecorderPausesOnRejectedTokenAndResumes(t *testing.T) {
	t.Setenv("AUDD_API_TOKEN", "")
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	var mu sync.Mutex
	tok := "wrong"
	h.app.APIClient = func() (*audd.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		return audd.NewClient(tok, audd.WithHTTPClient(h.srv.HTTPClient()), audd.WithMaxAttempts(1)), nil
	}
	r := h.recorder(RecorderOptions{})
	stop := start(t, r)
	// It keeps running and says why nothing is recorded.
	waitFor(t, "last_error", func() bool {
		v, _ := h.store.Meta("last_error")
		return strings.Contains(v, "paused")
	})
	if v, _ := h.store.Meta("last_error"); !strings.Contains(v, "Wrong API token") {
		t.Fatalf("last_error %q", v)
	}
	if hb, _ := h.store.LastHeartbeat(1); !hb.IsZero() {
		t.Fatal("no heartbeat while AudD refuses the token")
	}
	mu.Lock()
	tok = streamstest.Token
	mu.Unlock()
	waitFor(t, "resumed", func() bool { return h.srv.Count("longpoll") >= 1 })
	waitFor(t, "last_error cleared", func() bool { v, _ := h.store.Meta("last_error"); return v == "" })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// fakeAccount hands out the account's current API token, like the login's
// account backend.
type fakeAccount struct {
	account.Backend
	mu    sync.Mutex
	token string
	calls int
}

func (f *fakeAccount) APIToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.token, nil
}

func (f *fakeAccount) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// loginHarness is a harness whose API token comes from the profile's
// stored tokens (as after audd login), resolved on every client.
func loginHarness(t *testing.T) (*harness, *fakeAccount) {
	t.Helper()
	t.Setenv("AUDD_API_TOKEN", "")
	h := newHarness(t)
	h.app.Profile = &config.Profile{Name: config.DefaultProfile}
	h.app.Secrets = secrets.NewMemory()
	acct := &fakeAccount{token: streamstest.Token}
	h.app.Account = func() (account.Backend, error) { return acct, nil }
	h.app.APIClient = func() (*audd.Client, error) {
		tok, _, err := config.ResolveToken(h.app.Flags.Token, config.DefaultProfile, h.app.Secrets)
		if err != nil {
			return nil, err
		}
		return audd.NewClient(tok, audd.WithHTTPClient(h.srv.HTTPClient()), audd.WithMaxAttempts(1)), nil
	}
	return h, acct
}

func TestRecorderHealsALoginToken(t *testing.T) {
	h, acct := loginHarness(t)
	_ = h.app.Secrets.Set(config.DefaultProfile, "login_api_token", "fedcba9876543210fedcba9876543210")
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	start(t, h.recorder(RecorderOptions{}))
	waitFor(t, "poll", func() bool { return h.srv.Count("longpoll") >= 1 })
	if got, _ := h.app.Secrets.Get(config.DefaultProfile, "login_api_token"); got != streamstest.Token {
		t.Fatalf("stored token %q", got)
	}
	if acct.Calls() != 1 {
		t.Fatalf("account asked %d times", acct.Calls())
	}
	// The longpoll uses the healed token's category.
	for _, c := range h.srv.Calls() {
		if c.Method == "longpoll" && c.Params.Get("category") != streamstest.Category(1) {
			t.Fatalf("category %s", c.Params.Get("category"))
		}
	}
}

func TestRecorderResumesAfterAHealAtRescan(t *testing.T) {
	h, acct := loginHarness(t)
	_ = h.app.Secrets.Set(config.DefaultProfile, "login_api_token", streamstest.Token)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	start(t, h.recorder(RecorderOptions{}))
	waitFor(t, "poll", func() bool { return h.srv.Count("longpoll") >= 1 })
	// The token is rotated on the dashboard: the stored one stops working.
	rotated := "fedcba9876543210fedcba9876543210"
	acct.mu.Lock()
	acct.token = rotated
	acct.mu.Unlock()
	h.srv.SetToken(rotated)
	waitFor(t, "healed", func() bool {
		got, _ := h.app.Secrets.Get(config.DefaultProfile, "login_api_token")
		return got == rotated
	})
	waitFor(t, "poll with the new category", func() bool {
		for _, c := range h.srv.Calls() {
			if c.Method == "longpoll" && c.Params.Get("category") == audd.DeriveLongpollCategory(rotated, 1) {
				return true
			}
		}
		return false
	})
}

func TestRecorderNeverReplacesAnEnvToken(t *testing.T) {
	h, acct := loginHarness(t)
	t.Setenv("AUDD_API_TOKEN", "wrong")
	_ = h.app.Secrets.Set(config.DefaultProfile, "login_api_token", streamstest.Token)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	err := h.recorder(RecorderOptions{}).Run(context.Background())
	if output.ExitCode(err) != output.ExitAuth {
		t.Fatalf("an env token AudD rejects stops the recorder with exit 3: %v", err)
	}
	if v, _ := h.store.Meta("last_error"); !strings.Contains(v, "AUDD_API_TOKEN") {
		t.Fatalf("last_error says why: %q", v)
	}
	if acct.Calls() != 0 || h.srv.Count("longpoll") != 0 {
		t.Fatalf("account calls %d, polls %d", acct.Calls(), h.srv.Count("longpoll"))
	}
}

func TestForwarderRelaysCallbackBodies(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var bodies []string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("forwarded %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	defer recv.Close()
	h.srv.SetStreams(streamstest.Stream{RadioID: 4, Running: true})
	start(t, h.recorder(RecorderOptions{ForwardTo: recv.URL}))
	waitFor(t, "poll", func() bool { return h.srv.Count("longpoll") >= 1 })
	h.srv.PushMatch(4, "2026-10-08 14:07:00", "C", "Three", 150)
	h.srv.PushNotification(4, 651, "no music", true, 1791457320)
	waitFor(t, "forwarded", func() bool { mu.Lock(); defer mu.Unlock(); return len(bodies) == 2 })
	m, _, err := audd.ParseCallback([]byte(bodies[0]))
	if err != nil || m == nil || m.Song.Title != "Three" || m.RadioID != 4 {
		t.Fatalf("live play is relayed as AudD sent it: %s", bodies[0])
	}
	_, n, err := audd.ParseCallback([]byte(bodies[1]))
	if err != nil || n == nil || n.NotificationCode != 651 {
		t.Fatalf("notification relayed: %s", bodies[1])
	}
}

func TestCallbackBodyGoldens(t *testing.T) {
	p := streamstore.Play{RadioID: 7, Timestamp: time.Date(2026, 10, 8, 11, 2, 0, 0, time.UTC), PlayLength: 180,
		Artist: "Artist", Title: "Title", Album: "Album", Label: "Label", ReleaseDate: "2024-01-01", Score: 99,
		SongLink: "https://lis.tn/x", ISRC: "USAAA2400001", UPC: "000000000001"}
	testutil.Golden(t, "forward_play", append(PlayCallbackBody(p), '\n'))
	p.Raw = json.RawMessage(`{"artist":"Artist","title":"Title","apple_music":{"url":"https://music.apple.com/x"}}`)
	testutil.Golden(t, "forward_play_raw", append(PlayCallbackBody(p), '\n'))
	h := streamstore.HealthEvent{RadioID: 7, At: time.Unix(1791457320, 0), Code: 650, Message: "can't connect", Running: false}
	testutil.Golden(t, "forward_health", append(HealthCallbackBody(h), '\n'))
	// What we build parses back with the SDK's callback parser.
	m, _, err := audd.ParseCallback(PlayCallbackBody(p))
	if err != nil || m.Song.Title != "Title" || m.Timestamp != "2026-10-08 14:02:00" {
		t.Fatalf("%+v %v", m, err)
	}
}

// clock is a settable time source for the recorder and the store.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func (h *harness) useClock(t0 time.Time) *clock {
	c := &clock{t: t0}
	h.app.Now = c.Now
	h.store.Now = c.Now
	return c
}

func (r *Recorder) isDown(id int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.down[id]
}

// outage runs a recorder on stream 1, cuts AudD off for ten minutes, then
// restores it with recent results starting at recoveredFrom (local time,
// UTC+3). It returns the gaps since an hour before the outage.
func outage(t *testing.T, recoveredFrom string) []streamstore.Gap {
	h := newHarness(t)
	t0 := time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)
	clk := h.useClock(t0)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	h.srv.SetRecent(1, streamstest.RecentBody(30,
		map[string]any{"artist": "A", "title": "Before", "timestamp": "2026-10-08 13:30:00"}))
	r := h.recorder(RecorderOptions{})
	stop := start(t, r)
	waitFor(t, "first poll", func() bool { return h.srv.Count("longpoll") >= 1 })

	// AudD becomes unreachable: polls and recent results fail.
	h.srv.FailNextPolls(1 << 20)
	h.srv.FailNextRecent(1 << 20)
	waitFor(t, "stream down", func() bool { return r.isDown(1) })
	// Ten minutes pass in steps short enough that heartbeats, if any were
	// written, would join into one recorded period.
	for i := 1; i <= 60; i++ {
		clk.Set(t0.Add(time.Duration(i) * 10 * time.Second))
		time.Sleep(10 * time.Millisecond)
	}

	// Plays went on during the outage; AudD now only has the newest ones.
	h.srv.SetRecent(1, streamstest.RecentBody(30,
		map[string]any{"artist": "R", "title": "Recent", "timestamp": recoveredFrom}))
	h.srv.FailNextPolls(0)
	h.srv.FailNextRecent(0)
	waitFor(t, "reconnect backfill", func() bool { return h.count() == 2 })
	waitFor(t, "stream up", func() bool { return !r.isDown(1) })
	time.Sleep(150 * time.Millisecond) // heartbeats resume
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	gaps, err := h.store.Gaps(t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return gaps
}

func TestRecorderOutageLongerThanRecentWindowIsAGap(t *testing.T) {
	// The reconnect backfill reaches back only to 11:08 UTC.
	gaps := outage(t, "2026-10-08 14:08:00")
	t0 := time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)
	var found bool
	for _, g := range gaps {
		if !g.From.After(t0.Add(time.Minute)) && g.To.Equal(t0.Add(8*time.Minute)) {
			found = true
		}
		if g.From.After(t0.Add(8 * time.Minute)) {
			t.Fatalf("after the backfill the store is complete: %+v", gaps)
		}
	}
	if !found {
		t.Fatalf("the outage the backfill could not reach is not a gap: %+v", gaps)
	}
}

func TestRecorderOutageFilledByBackfillIsNoGap(t *testing.T) {
	// The reconnect backfill reaches back past the outage (to 10:40 UTC).
	gaps := outage(t, "2026-10-08 13:40:00")
	if len(gaps) != 1 || !gaps[0].To.Equal(time.Date(2026, 10, 8, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("only the time before the first backfill is a gap: %+v", gaps)
	}
}

func TestRecorderLongpollWorkingAgainEndsOutage(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	r := h.recorder(RecorderOptions{})
	start(t, r)
	waitFor(t, "first poll", func() bool { return h.srv.Count("longpoll") >= 1 })
	// The longpoll fails once; recent results stay unavailable for a while.
	h.srv.FailNextRecent(1 << 20)
	h.srv.FailNextPolls(1)
	waitFor(t, "stream down", func() bool { return r.isDown(1) })
	// A longpoll connection that keeps working brings the stream back up.
	waitFor(t, "stream up", func() bool { return !r.isDown(1) })
	// The reconnect backfill is retried once recent results work again.
	h.srv.SetRecent(1, streamstest.RecentBody(30, map[string]any{"artist": "M", "title": "Missed", "timestamp": "2026-10-08 14:00:00"}))
	h.srv.FailNextRecent(0)
	waitFor(t, "missed play", func() bool { return len(h.livePlays()) == 1 })
}

func TestRecorderReportsPlaysAnotherRecorderStoredFirst(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var bodies []string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	defer recv.Close()
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	start(t, h.recorder(RecorderOptions{ForwardTo: recv.URL}))
	waitFor(t, "first poll", func() bool { return h.srv.Count("longpoll") >= 1 })

	// The background recorder, longpolling the same stream, stores the
	// play before this recorder receives it.
	ts, _ := ParseTimestamp("2026-10-08 14:07:00")
	if ok, err := h.store.AddPlay(streamstore.Play{RadioID: 1, Timestamp: ts, Artist: "C", Title: "Three"}); !ok || err != nil {
		t.Fatalf("second writer: %v %v", ok, err)
	}
	h.srv.PushMatch(1, "2026-10-08 14:07:00", "C", "Three", 150)
	waitFor(t, "reported", func() bool { return len(h.livePlays()) == 1 })
	waitFor(t, "forwarded", func() bool { mu.Lock(); defer mu.Unlock(); return len(bodies) == 1 })
	if p := h.livePlays()[0]; p.Title != "Three" || p.RadioID != 1 {
		t.Fatalf("%+v", p)
	}
	// The same play arriving again is not reported twice.
	h.srv.PushMatch(1, "2026-10-08 14:07:00", "C", "Three", 150)
	h.srv.PushMatch(1, "2026-10-08 14:10:00", "D", "Four", 150)
	waitFor(t, "next play", func() bool { return len(h.livePlays()) == 2 })
	if p := h.livePlays()[1]; p.Title != "Four" {
		t.Fatalf("%+v", h.livePlays())
	}
}

func TestRecorderReconnectBackfillReportsPlaysStoredByAnotherRecorder(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)
	h.useClock(t0)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	h.srv.SetRecent(1, streamstest.RecentBody(30,
		map[string]any{"artist": "A", "title": "Before", "timestamp": "2026-10-08 13:30:00"}))
	r := h.recorder(RecorderOptions{})
	start(t, r)
	waitFor(t, "first poll", func() bool { return h.srv.Count("longpoll") >= 1 })
	// While this recorder is disconnected, another one stores a new play.
	h.srv.FailNextPolls(1)
	missed := map[string]any{"artist": "M", "title": "Missed", "timestamp": "2026-10-08 14:05:00"}
	ts, _ := ParseTimestamp("2026-10-08 14:05:00")
	h.store.AddPlay(streamstore.Play{RadioID: 1, Timestamp: ts, Artist: "M", Title: "Missed"})
	h.srv.SetRecent(1, streamstest.RecentBody(30,
		map[string]any{"artist": "A", "title": "Before", "timestamp": "2026-10-08 13:30:00"}, missed))
	waitFor(t, "missed play", func() bool { return len(h.livePlays()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if live := h.livePlays(); len(live) != 1 || live[0].Title != "Missed" {
		t.Fatalf("only the play after the start is reported: %+v", live)
	}
}

func TestRecorderWithoutCallbackURLLeavesAGap(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	h.srv.SetNoCallbackURL()
	r := h.recorder(RecorderOptions{})
	r.upAfter = 50 * time.Millisecond // longpoll "works": it only times out
	start(t, r)
	waitFor(t, "polls", func() bool { return h.srv.Count("longpoll") >= 2 })
	time.Sleep(200 * time.Millisecond)
	if last, _ := h.store.LastHeartbeat(); !last.IsZero() {
		t.Fatalf("no heartbeat while live results cannot arrive: %v", last)
	}
	if v, _ := h.store.Meta("last_error"); !strings.Contains(v, "no callback URL") {
		t.Fatalf("last_error %q", v)
	}
	// Once a callback URL is set, the next rescan notices and recording
	// counts again.
	if err := h.srv.Client().Streams().SetCallbackUrl("https://example.com/cb", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "heartbeat", func() bool { last, _ := h.store.LastHeartbeat(); return !last.IsZero() })
}

func TestRecorderForSomeStreamsLeavesTheOthersAsGaps(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true}, streamstest.Stream{RadioID: 2, Running: true})
	since := time.Now().Add(-10 * time.Minute)
	stop := start(t, h.recorder(RecorderOptions{RadioIDs: []int{2}}))
	waitFor(t, "heartbeat", func() bool { last, _ := h.store.LastHeartbeat(2); return !last.IsZero() })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if last, _ := h.store.LastHeartbeat(1); !last.IsZero() {
		t.Fatalf("stream 1 was not recorded, yet has a heartbeat at %v", last)
	}
	gaps, _ := h.store.Gaps(since, 1)
	if len(gaps) != 1 || g0(gaps).To.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("stream 1 is a gap up to now: %+v", gaps)
	}
	// The account's streams are known, so the store-wide check sees it too.
	if gaps, _ := h.store.Gaps(since); len(gaps) == 0 {
		t.Fatal("store-wide gaps miss stream 1")
	}
}

func g0(g []streamstore.Gap) streamstore.Gap { return g[0] }

func TestRecorderPicksUpARotatedToken(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true})
	const newToken = "fedcba9876543210fedcba9876543210"
	var mu sync.Mutex
	tok := streamstest.Token
	h.app.APIClient = func() (*audd.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		return h.srv.ClientWithToken(tok), nil
	}
	r := h.recorder(RecorderOptions{})
	start(t, r)
	waitFor(t, "first poll", func() bool { return h.srv.Count("longpoll") >= 1 })

	// The token is rotated: AudD only accepts the new one, and the stored
	// token is not updated yet. Recording pauses: no heartbeats.
	h.srv.SetToken(newToken)
	waitFor(t, "paused", func() bool {
		v, _ := h.store.Meta("last_error")
		return strings.Contains(v, "paused")
	})
	time.Sleep(60 * time.Millisecond) // let a heartbeat already underway land
	paused, _ := h.store.LastHeartbeat(1)
	time.Sleep(300 * time.Millisecond)
	if last, _ := h.store.LastHeartbeat(1); last.After(paused) {
		t.Fatalf("heartbeats went on while AudD refused the token: %v after %v", last, paused)
	}

	// The new token is stored; the next rescan reconnects with it.
	mu.Lock()
	tok = newToken
	mu.Unlock()
	newCat := audd.DeriveLongpollCategory(newToken, 1)
	waitFor(t, "poll with the new category", func() bool {
		for _, c := range h.srv.Calls() {
			if c.Method == "longpoll" && c.Params.Get("category") == newCat {
				return true
			}
		}
		return false
	})
	h.srv.PushMatchFor(newToken, 1, "2026-10-08 15:00:00", "N", "New token", 100)
	waitFor(t, "play under the new token", func() bool { p, _ := h.store.Latest(1); return p != nil && p.Title == "New token" })
	waitFor(t, "heartbeats resume", func() bool { last, _ := h.store.LastHeartbeat(1); return last.After(paused) })
	if v, _ := h.store.Meta("last_error"); v != "" {
		t.Fatalf("last_error is cleared once AudD accepts the token: %q", v)
	}
}

func TestRecorderStopsWatchersWhenTheTokenIsGone(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, Running: true}, streamstest.Stream{RadioID: 2, Running: true})
	var mu sync.Mutex
	gone := false
	h.app.APIClient = func() (*audd.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		if gone {
			return nil, output.Errf(output.ExitAuth, "no_token", "", "no AudD API token is set")
		}
		return h.srv.Client(), nil
	}
	r := h.recorder(RecorderOptions{})
	start(t, r)
	waitFor(t, "both streams polled", func() bool { return h.srv.Count("longpoll") >= 2 })
	running := func() int {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.running)
	}

	// audd logout removed the token: every watcher stops.
	mu.Lock()
	gone = true
	mu.Unlock()
	waitFor(t, "watchers stopped and the problem noted", func() bool {
		v, _ := h.store.Meta("last_error")
		return running() == 0 && strings.Contains(v, "cannot read the API token")
	})
	time.Sleep(100 * time.Millisecond) // let a poll already underway end
	polls := h.srv.Count("longpoll")
	time.Sleep(400 * time.Millisecond)
	if n := h.srv.Count("longpoll"); n != polls || running() != 0 {
		t.Fatalf("streams were polled without a token: %d polls after %d, %d watchers", n, polls, running())
	}

	// A token is back: recording resumes.
	mu.Lock()
	gone = false
	mu.Unlock()
	waitFor(t, "polling again", func() bool { return h.srv.Count("longpoll") > polls && running() == 2 })
}

// When the recorder no longer owns its profile (its PID or lock file was
// removed), it stops at the next heartbeat and logs why.
func TestRecorderStopsWhenItNoLongerOwnsTheProfile(t *testing.T) {
	h := newHarness(t)
	h.srv.SetStreams(streamstest.Stream{RadioID: 1, URL: "https://radio.example/1", Running: true})
	var gone atomic.Bool
	r := h.recorder(RecorderOptions{Owned: func() error {
		if gone.Load() {
			return errors.New("its PID file /data/recorder-default.pid was removed")
		}
		return nil
	}})
	var logs []string
	var mu sync.Mutex
	r.opts.Logf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(120 * time.Millisecond) // a few heartbeats while it owns the profile
	select {
	case err := <-done:
		t.Fatalf("stopped too early: %v", err)
	default:
	}
	gone.Store(true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a clean stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the recorder kept running without its PID file")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(logs, "\n"), "Stopping the recorder: its PID file /data/recorder-default.pid was removed") {
		t.Fatalf("logs: %v", logs)
	}
}
