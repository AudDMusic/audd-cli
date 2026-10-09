package streams

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

// RecorderOptions configure a Recorder.
type RecorderOptions struct {
	// RadioIDs limits recording to these streams; empty means every stream
	// on the account, including ones added later.
	RadioIDs []int
	// ForwardTo, when set, receives each live play and health event as a
	// callback-shaped POST.
	ForwardTo string
	// OnPlay is called once for every play this recorder sees after its
	// start-up backfill: live plays, and plays a reconnect backfill finds.
	// A play is reported even when another recorder writing the same store
	// saved it first. It may be called from several goroutines at once.
	OnPlay func(streamstore.Play)
	// OnHealth is called for every health event.
	OnHealth func(streamstore.HealthEvent)
	// Logf receives connection problems and other notes (default: discard).
	Logf func(format string, args ...any)
	// Owned, when set, is checked with every heartbeat: an error means
	// this recorder no longer owns its profile (see Lock.Check), and Run
	// stops, logging why.
	Owned func() error
}

// Recorder longpolls an account's streams and stores every play and health
// event in the stream store.
type Recorder struct {
	app   *app.App
	store *streamstore.Store
	opts  RecorderOptions
	fwd   *Forwarder

	// Timings; tests shorten them.
	pollTimeout    int // seconds, server side
	rescanEvery    time.Duration
	heartbeatEvery time.Duration
	backoffMin     time.Duration
	backoffMax     time.Duration
	// upAfter is how long a longpoll connection must run without an error
	// before a stream that is down counts as connected again (the SDK
	// hides keepalives, so a full poll cycle without an error is the sign).
	upAfter time.Duration

	mu sync.Mutex
	// client and token are what the recorder polls with. A rescan that
	// finds a different token (after audd token rotate, say) replaces them
	// and reconnects every stream.
	client     *audd.Client
	token      string
	running    map[int]context.CancelFunc
	backfilled map[int]bool // streams the start-up backfill covered
	// rebackfill holds the streams whose first backfill after a token
	// change reports the plays it finds, like a reconnect backfill.
	rebackfill map[int]bool
	// down holds the streams whose longpoll connection is failing. No
	// heartbeat is written for them, so the outage shows as a gap for that
	// stream until a reconnect backfill reaches back past it.
	down map[int]bool
	// blocked is set while AudD refuses the account's stream list (a
	// rejected token, say). Nothing can be recorded then, so no heartbeat
	// is written and the time shows as a gap.
	blocked bool
	// reported holds the plays this recorder already reported or that its
	// start-up backfill saw, so each play is reported once no matter which
	// process stored it first.
	reported map[playKey]bool
	// startedAt is when Run began; a reconnect backfill reports plays from
	// then on even when they were stored already.
	startedAt time.Time
	// noCallback is set while the account has no callback URL. Longpoll then
	// delivers nothing, so no heartbeat is written and the time shows as a
	// gap.
	noCallback bool
	wg         sync.WaitGroup
}

type playKey struct {
	radioID int
	ts      int64
}

// reportedKeep is how long a play stays in the reported set; the recent
// results never reach back this far.
const reportedKeep = 48 * time.Hour

// NewRecorder returns a recorder that writes into store.
func NewRecorder(a *app.App, store *streamstore.Store, opts RecorderOptions) *Recorder {
	r := &Recorder{
		app: a, store: store, opts: opts,
		pollTimeout:    50,
		rescanEvery:    5 * time.Minute,
		heartbeatEvery: 30 * time.Second,
		backoffMin:     time.Second,
		backoffMax:     time.Minute,
		upAfter:        65 * time.Second,
		running:        map[int]context.CancelFunc{},
		backfilled:     map[int]bool{},
		rebackfill:     map[int]bool{},
		down:           map[int]bool{},
		reported:       map[playKey]bool{},
	}
	if opts.ForwardTo != "" {
		r.fwd = &Forwarder{URL: opts.ForwardTo}
	}
	return r
}

func (r *Recorder) now() time.Time {
	if r.app != nil && r.app.Now != nil {
		return r.app.Now()
	}
	return time.Now()
}

// logf passes a note to RecorderOptions.Logf with any token redacted.
func (r *Recorder) logf(format string, args ...any) {
	if r.opts.Logf != nil {
		r.opts.Logf("%s", output.Redact(fmt.Sprintf(format, args...)))
	}
}

// Run records until ctx is cancelled. It backfills each stream from the
// recent-results endpoint, then longpolls every stream, re-reads the
// stream list and the API token every 5 minutes, writes a heartbeat every
// 30 seconds for each connected stream, and reconnects with backoff after
// errors. When AudD refuses the stream list (a rejected token, no stream
// monitoring on the account), recording is paused, the problem is noted in
// the store's last_error, and each rescan tries again. A rejected token
// fetched by audd login is replaced with the account's current one first.
// Run returns nil when ctx ends, or an error when there is no API token or
// AudD rejects a token given with --token or AUDD_API_TOKEN (it cannot
// change while this process runs; the problem is noted all the same).
func (r *Recorder) Run(ctx context.Context) error {
	c, err := r.app.APIClient()
	if err != nil {
		return err
	}
	r.setClient(c)
	r.startedAt = r.now()

	list, err := r.listStreams(ctx)
	switch {
	case err != nil && fixedToken(r.app) && output.ExitCode(err) == output.ExitAuth:
		// A token from --token or AUDD_API_TOKEN is fixed for this
		// process, so waiting cannot help.
		r.note("AudD rejected the API token, so the recorder stopped: %s", withHint(err))
		return err
	case err != nil:
		r.setBlocked(true)
		r.note("AudD refused the stream list, so recording is paused until this is fixed: %s", withHint(err))
	case ctx.Err() == nil:
		r.setClient(c) // healing may have changed its token
		r.saveAccountStreams(list)
		r.checkCallback(ctx)
		r.backfillAll(ctx, list)
		r.sync(ctx, list)
	}
	r.heartbeat()

	hb := time.NewTicker(r.heartbeatEvery)
	defer hb.Stop()
	rescan := time.NewTicker(r.rescanEvery)
	defer rescan.Stop()
	for {
		select {
		case <-ctx.Done():
			r.heartbeat()
			r.stopAll()
			r.wg.Wait()
			return nil
		case <-hb.C:
			if r.opts.Owned != nil {
				if err := r.opts.Owned(); err != nil {
					r.logf("Stopping the recorder: %v, so it could no longer be seen or stopped.", err)
					r.stopAll()
					r.wg.Wait()
					return nil
				}
			}
			r.heartbeat()
		case <-rescan.C:
			r.rescan(ctx)
		}
	}
}

// fixedToken reports whether the API token comes from --token or
// AUDD_API_TOKEN, which this process cannot see change.
func fixedToken(a *app.App) bool {
	src := api.TokenSource(a)
	return src == config.SourceFlag || src == config.SourceEnv
}

// withHint is err's text followed by its fix, when it has one.
func withHint(err error) string {
	var oe *output.Error
	if errors.As(err, &oe) && oe.Hint != "" {
		return oe.Message + " (fix: " + oe.Hint + ")"
	}
	return err.Error()
}

// list reads the stream list with c. When AudD rejects a token fetched by
// audd login, it fetches the account's current token onto c and tries once
// more.
func (r *Recorder) list(ctx context.Context, c *audd.Client) ([]audd.Stream, error) {
	list, err := c.Streams().ListContext(ctx)
	if err == nil || ctx.Err() != nil {
		return list, err
	}
	healed, herr := api.HealLogin(ctx, r.app, c, err)
	if herr != nil {
		return nil, herr
	}
	if !healed {
		return nil, err
	}
	r.logf("AudD rejected the API token from your login; fetched the current one from your AudD account")
	return c.Streams().ListContext(ctx)
}

func (r *Recorder) current() (*audd.Client, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client, r.token
}

func (r *Recorder) setClient(c *audd.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.client, r.token = c, c.APIToken()
}

// rescan resolves the API token again, re-reads the stream list, and
// starts or stops watchers to match. When the token changed, every stream
// reconnects with the new token's longpoll categories. While AudD refuses
// the list (a rejected token, say), recording is paused: no heartbeats, so
// the time shows as a gap.
func (r *Recorder) rescan(ctx context.Context) {
	r.pruneReported()
	c, err := r.app.APIClient()
	if err != nil {
		// No token (after audd logout, say): stop every watcher, so no
		// stream is longpolled with a token this machine no longer holds.
		r.setBlocked(true)
		r.pauseWatchers()
		r.note("cannot read the API token, so recording is paused: %v", err)
		return
	}
	list, err := r.list(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if permanent(err) {
			r.setBlocked(true)
			r.note("AudD refused the stream list, so recording is paused until this is fixed: %s", withHint(MapError(r.app, err)))
			return
		}
		r.note("could not refresh the stream list: %v", MapError(r.app, err))
		return
	}
	if _, tok := r.current(); c.APIToken() != tok {
		r.switchClient(c)
	}
	if r.setBlocked(false) {
		r.logf("AudD accepts the token again; recording resumed")
		_ = r.store.SetMeta("last_error", "")
	}
	r.saveAccountStreams(list)
	r.checkCallback(ctx)
	r.sync(ctx, list)
}

// setBlocked sets whether recording is paused and reports whether that
// changed.
func (r *Recorder) setBlocked(b bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := r.blocked != b
	r.blocked = b
	return changed
}

// switchClient starts using a new token: it stops every watcher so the
// next sync reconnects each stream with the new token's category, and
// backfills it first.
func (r *Recorder) switchClient(c *audd.Client) {
	r.mu.Lock()
	r.client, r.token = c, c.APIToken()
	for id, cancel := range r.running {
		cancel()
		delete(r.running, id)
		r.rebackfill[id] = true
	}
	r.down = map[int]bool{}
	r.backfilled = map[int]bool{}
	r.mu.Unlock()
	r.logf("the API token changed; reconnecting every stream with the new one")
}

// pauseWatchers stops every watcher while the API token cannot be read.
// The next rescan that reads a token reconnects each stream and reports the
// plays it missed meanwhile.
func (r *Recorder) pauseWatchers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.running) == 0 {
		return
	}
	for id, cancel := range r.running {
		cancel()
		delete(r.running, id)
		r.rebackfill[id] = true
	}
	r.down = map[int]bool{}
	r.backfilled = map[int]bool{}
	r.logf("stopped recording every stream until an API token is available")
}

// saveAccountStreams records the account's streams in the store, so
// readers know which streams to check for gaps.
func (r *Recorder) saveAccountStreams(list []audd.Stream) {
	ids := make([]int, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.RadioID)
	}
	if err := r.store.SetAccountStreams(ids, r.now()); err != nil {
		r.logf("could not write to the stream store: %v", err)
	}
}

// listStreams reads the stream list, retrying transient failures forever.
func (r *Recorder) listStreams(ctx context.Context) ([]audd.Stream, error) {
	backoff := r.backoffMin
	c, _ := r.current()
	for {
		list, err := r.list(ctx, c)
		if err == nil {
			return list, nil
		}
		if ctx.Err() != nil {
			return nil, nil
		}
		if permanent(err) {
			return nil, MapError(r.app, err)
		}
		r.note("could not read the stream list, retrying in %s: %v", backoff, MapError(r.app, err))
		if !sleep(ctx, backoff) {
			return nil, nil
		}
		backoff = min(backoff*2, r.backoffMax)
	}
}

// checkCallback re-reads whether the account has a callback URL. Without
// one, longpoll delivers nothing, so the recorder notes the problem and
// writes no heartbeats until one is set.
func (r *Recorder) checkCallback(ctx context.Context) {
	c, _ := r.current()
	missing, err := CallbackMissing(ctx, c)
	if err != nil {
		return // keep the last known state
	}
	r.mu.Lock()
	changed := missing != r.noCallback
	r.noCallback = missing
	r.mu.Unlock()
	switch {
	case missing && changed:
		r.note("no callback URL is set, so live results cannot arrive; run: audd streams callback set %s", EmptyCallbackURL)
	case !missing && changed:
		r.logf("a callback URL is set again; live results can arrive")
		_ = r.store.SetMeta("last_error", "")
	}
}

// markReported records that a play was reported (or seen by the start-up
// backfill). It reports whether the play was new to this recorder.
func (r *Recorder) markReported(p streamstore.Play) bool {
	k := playKey{p.RadioID, p.Timestamp.Unix()}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reported[k] {
		return false
	}
	r.reported[k] = true
	return true
}

// pruneReported forgets plays older than any the recent results return.
func (r *Recorder) pruneReported() {
	cutoff := r.now().Add(-reportedKeep).Unix()
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.reported {
		if k.ts < cutoff {
			delete(r.reported, k)
		}
	}
}

// heartbeat records that the recorder is running, for each stream whose
// longpoll connection works. It writes nothing while the account has no
// callback URL or AudD refuses the stream list.
func (r *Recorder) heartbeat() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.noCallback || r.blocked {
		return
	}
	ids := make([]int, 0, len(r.running))
	for id := range r.running {
		if !r.down[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	sort.Ints(ids)
	if err := r.store.Heartbeat(r.now(), ids...); err != nil {
		r.logf("could not write to the stream store: %v", err)
	}
}

// setDown marks a stream's connection as failing; no heartbeat is written
// for it until it is up again.
func (r *Recorder) setDown(radioID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down[radioID] = true
}

// setUp records that a stream is complete from a time on: after a
// backfill (from its oldest result) or after its longpoll worked again
// (from when it connected). A gap stays only for the part of an outage
// that neither the backfills nor the live feed covered.
func (r *Recorder) setUp(radioID int, completeFrom time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.down, radioID)
	if _, ok := r.running[radioID]; !ok || r.noCallback || r.blocked {
		return
	}
	if err := r.store.Cover(completeFrom, r.now(), radioID); err != nil {
		r.logf("could not write to the stream store: %v", err)
	}
}

func (r *Recorder) note(format string, args ...any) {
	msg := output.Redact(fmt.Sprintf(format, args...))
	r.logf("%s", msg)
	_ = r.store.SetMeta("last_error", r.now().UTC().Format(time.RFC3339)+" "+msg)
}

func (r *Recorder) wanted(id int) bool {
	if len(r.opts.RadioIDs) == 0 {
		return true
	}
	for _, x := range r.opts.RadioIDs {
		if x == id {
			return true
		}
	}
	return false
}

// backfillAll stores each wanted stream's recent results and records, for
// each stream that answered, how far back the store is now complete for it.
func (r *Recorder) backfillAll(ctx context.Context, list []audd.Stream) {
	r.backfillStreams(ctx, list, 0)
}

// backfillStreams is backfillAll, skipping streams recorded within
// freshWithin (when it is above 0).
func (r *Recorder) backfillStreams(ctx context.Context, list []audd.Stream, freshWithin time.Duration) {
	for _, s := range list {
		if !r.wanted(s.RadioID) {
			continue
		}
		if freshWithin > 0 {
			if last, err := r.store.LastHeartbeat(s.RadioID); err == nil && !last.IsZero() && r.now().Sub(last) < freshWithin {
				continue
			}
		}
		oldest, ok := r.backfill(ctx, s.RadioID, false)
		r.backfilled[s.RadioID] = true
		if !ok || oldest.IsZero() {
			continue
		}
		if err := r.store.Cover(oldest, r.now(), s.RadioID); err != nil {
			r.logf("could not write to the stream store: %v", err)
		}
	}
}

// backfill stores a stream's recent results. Without emit it only marks
// them as seen. With emit, plays this recorder has not reported go to
// OnPlay and the forwarder when they are from after the recorder started or
// were not stored yet. It returns the oldest result's time and whether the
// request worked.
func (r *Recorder) backfill(ctx context.Context, radioID int, emit bool) (time.Time, bool) {
	_, tok := r.current()
	plays, err := RecentResults(ctx, radioID, audd.DeriveLongpollCategory(tok, radioID))
	if err != nil {
		if ctx.Err() == nil {
			r.note("%v", err)
		}
		return time.Time{}, false
	}
	var oldest time.Time
	for _, p := range plays {
		p.RadioID = radioID
		if oldest.IsZero() || p.Timestamp.Before(oldest) {
			oldest = p.Timestamp
		}
		inserted, err := r.store.AddPlay(p)
		if err != nil {
			r.note("could not write to the stream store: %v", err)
		}
		if !emit {
			r.markReported(p)
			continue
		}
		if (inserted || !p.Timestamp.Before(r.startedAt)) && r.markReported(p) {
			r.emitPlay(ctx, p, nil)
		}
	}
	return oldest, true
}

// sync starts recording new streams, stops removed ones, and records
// stream_running changes as health events.
func (r *Recorder) sync(ctx context.Context, list []audd.Stream) {
	seen := map[int]bool{}
	ids := []int{}
	for _, s := range list {
		if !r.wanted(s.RadioID) {
			continue
		}
		seen[s.RadioID] = true
		ids = append(ids, s.RadioID)
		r.trackRunning(ctx, s)
	}
	sort.Ints(ids)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	for id, cancel := range r.running {
		if !seen[id] {
			cancel()
			delete(r.running, id)
			delete(r.down, id)
			r.logf("stopped recording stream %d (removed from the account)", id)
		}
	}
	for _, id := range ids {
		if _, ok := r.running[id]; ok {
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		r.running[id] = cancel
		r.wg.Add(1)
		done, emit := r.backfilled[id], r.rebackfill[id]
		delete(r.rebackfill, id)
		go func(id int) {
			defer r.wg.Done()
			r.watch(sctx, id, done, emit)
		}(id)
	}
}

func (r *Recorder) trackRunning(ctx context.Context, s audd.Stream) {
	last, err := r.store.LatestHealth(s.RadioID)
	if err != nil || (last != nil && last.Running == s.StreamRunning) || (last == nil && s.StreamRunning) {
		return
	}
	msg := "stream is running"
	if !s.StreamRunning {
		msg = "stream is not running"
	}
	h := streamstore.HealthEvent{RadioID: s.RadioID, At: r.now().UTC().Truncate(time.Second), Message: msg, Running: s.StreamRunning}
	r.emitHealth(ctx, h, nil)
}

func (r *Recorder) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, cancel := range r.running {
		cancel()
		delete(r.running, id)
	}
}

// watch longpolls one stream until ctx ends, reconnecting with backoff.
// After each reconnect it backfills from the recent-results endpoint and
// reports the plays it missed. A stream that the start-up backfill did not
// cover is backfilled first: quietly for a stream new to this recorder,
// and reporting missed plays (emit) after a token change.
func (r *Recorder) watch(ctx context.Context, radioID int, backfilled, emit bool) {
	_, tok := r.current()
	category := audd.DeriveLongpollCategory(tok, radioID)
	backoff := r.backoffMin
	if !backfilled {
		if oldest, ok := r.backfill(ctx, radioID, emit); ok {
			if oldest.IsZero() {
				oldest = r.now()
			}
			r.setUp(radioID, oldest)
		}
	}
	reconnect := false
	for ctx.Err() == nil {
		pending := false // the reconnect backfill still has to be done
		if reconnect {
			pending = !r.reconnectBackfill(ctx, radioID)
		}
		reconnect = true
		started := time.Now()
		connectedAt := r.now()
		// Tokenless: the category alone selects the stream, so the API
		// token never appears in a longpoll URL. The loop here reconnects
		// with its own backoff, so the consumer does not retry.
		lp := audd.NewLongpollConsumer(category, audd.LongpollConsumerWithHTTPClient(LongpollHTTPClient),
			audd.LongpollConsumerWithMaxAttempts(1))
		poll := lp.IterateContext(ctx, &audd.LongpollConsumerOptions{Timeout: r.pollTimeout})
		err := r.consume(ctx, radioID, poll, connectedAt, pending)
		poll.Close()
		lp.Close()
		if ctx.Err() != nil {
			return
		}
		r.setDown(radioID)
		if time.Since(started) > time.Minute {
			backoff = r.backoffMin
		}
		r.note("stream %d: connection to AudD lost, reconnecting in %s: %v", radioID, backoff, MapError(r.app, err))
		if !sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, r.backoffMax)
	}
}

// reconnectBackfill fetches the plays a stream missed while disconnected,
// reports the new ones, and marks the stream complete from the oldest
// result. It reports whether the request worked.
func (r *Recorder) reconnectBackfill(ctx context.Context, radioID int) bool {
	oldest, ok := r.backfill(ctx, radioID, true)
	if !ok {
		return false
	}
	if oldest.IsZero() {
		oldest = r.now()
	}
	r.setUp(radioID, oldest)
	return true
}

// consume reads one longpoll connection until it fails. A connection that
// runs for upAfter without an error marks the stream connected, complete
// from when the connection was opened. With pendingBackfill, the reconnect
// backfill that failed before is retried at that point and then every
// upAfter until it works.
func (r *Recorder) consume(ctx context.Context, radioID int, poll *audd.LongpollPoll, connectedAt time.Time, pendingBackfill bool) error {
	matches, notifs, errs := poll.Matches, poll.Notifications, poll.Errors
	up := time.NewTimer(r.upAfter)
	defer up.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-up.C:
			r.setUp(radioID, connectedAt)
			if pendingBackfill {
				if r.reconnectBackfill(ctx, radioID) {
					pendingBackfill = false
				} else {
					up.Reset(r.upAfter)
				}
			}
		case m, ok := <-matches:
			if !ok {
				matches = nil
				break
			}
			p := PlayFromMatch(m, r.now())
			if p.RadioID == 0 {
				p.RadioID = radioID
			}
			if _, err := r.store.AddPlay(p); err != nil {
				r.note("could not write to the stream store: %v", err)
			}
			// Each play is reported once, even when another recorder on
			// this store saved it first; one a backfill already reported
			// is not reported again.
			if r.markReported(p) {
				r.emitPlay(ctx, p, m.RawResponse)
			}
		case n, ok := <-notifs:
			if !ok {
				notifs = nil
				break
			}
			r.emitHealth(ctx, HealthFromNotification(n, radioID, r.now()), n.RawResponse)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				break
			}
			return err
		}
		if matches == nil && notifs == nil && errs == nil {
			return fmt.Errorf("the longpoll connection closed")
		}
	}
}

func (r *Recorder) emitPlay(ctx context.Context, p streamstore.Play, raw []byte) {
	if r.opts.OnPlay != nil {
		r.opts.OnPlay(p)
	}
	if r.fwd != nil {
		if len(raw) == 0 {
			raw = PlayCallbackBody(p)
		}
		if err := r.fwd.Post(ctx, raw); err != nil && ctx.Err() == nil {
			r.logf("could not forward a play to %s: %v", r.fwd.URL, err)
		}
	}
}

func (r *Recorder) emitHealth(ctx context.Context, h streamstore.HealthEvent, raw []byte) {
	if err := r.store.AddHealth(h); err != nil {
		r.note("could not write to the stream store: %v", err)
	}
	if r.opts.OnHealth != nil {
		r.opts.OnHealth(h)
	}
	if r.fwd != nil {
		if len(raw) == 0 {
			raw = HealthCallbackBody(h)
		}
		if err := r.fwd.Post(ctx, raw); err != nil && ctx.Err() == nil {
			r.logf("could not forward a stream event to %s: %v", r.fwd.URL, err)
		}
	}
}

// sleep waits d or until ctx ends; it reports whether the full wait passed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Backfill stores the recent results of the account's streams (or only the
// given ones) once, without longpolling, and records the account's stream
// list. Streams a recorder covered within freshWithin are skipped (0: none
// are). Commands that read the store use it when it is not current.
func Backfill(ctx context.Context, a *app.App, store *streamstore.Store, radioIDs []int, freshWithin time.Duration) error {
	var c *audd.Client
	list, err := Do(ctx, a, func(cl *audd.Client) ([]audd.Stream, error) {
		c = cl
		return cl.Streams().ListContext(ctx)
	})
	if err != nil {
		return err
	}
	r := NewRecorder(a, store, RecorderOptions{RadioIDs: radioIDs})
	r.setClient(c)
	r.saveAccountStreams(list)
	r.backfillStreams(ctx, list, freshWithin)
	return nil
}
