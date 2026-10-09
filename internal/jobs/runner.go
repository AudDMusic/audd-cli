package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	app.RunBatch = RunBatch
}

// ExitInterrupted is the exit code after Ctrl-C (128 + SIGINT).
const ExitInterrupted = 130

const (
	defaultConcurrency = 4
	maxStandardBytes   = 10 << 20 // the standard endpoint's upload limit
	rateLimitRetries   = 5
	heartbeatEvery     = 30 * time.Second
)

// chooseResume looks for an unfinished job that ran the same inputs with the
// same settings. In a terminal it asks whether to resume it; otherwise the
// run stops with resume_available (unless BatchOptions.Resume or New
// decides), so a re-run never silently prints only the files that were left.
func chooseResume(a *app.App, st *Store, opts app.BatchOptions, p Params) (*Job, error) {
	if opts.Resume && opts.New {
		return nil, output.Errf(output.ExitUsage, "invalid_argument", "pass one of --resume or --new", "--resume and --new cannot be used together")
	}
	if opts.New {
		return nil, nil
	}
	for _, in := range opts.Inputs {
		if in.IsStdin {
			// Audio read from stdin is not kept, so such a job cannot be
			// matched or resumed by a later run.
			return nil, nil
		}
	}
	prev, ok := st.FindResumable(commandKey(opts.Inputs), p.Map())
	if !ok {
		return nil, nil
	}
	switch {
	case opts.Resume:
		return prev, nil
	case opts.DryRun:
		return nil, nil
	case a.Out.Options().StdinTTY && !opts.Yes && !a.Flags.Yes:
		q := fmt.Sprintf("Job %s ran this command before and has %d of %d files left. Resume it?", prev.ID, prev.Remaining, prev.Total)
		if a.Out.Confirm(q, false) == nil {
			return prev, nil
		}
		return nil, nil
	}
	return nil, output.Errf(output.ExitSafety, "resume_available",
		fmt.Sprintf("run again with --resume (results only for the files left) or --new (recognize everything again), or audd jobs resume %s", prev.ID),
		"job %s ran these files with the same settings and has %d of %d files left", prev.ID, prev.Remaining, prev.Total)
}

// RunBatch recognizes many inputs as a resumable job (spec §5): it enforces
// --max-files, shows the plan and asks to confirm, offers to resume an
// unfinished identical job, runs a worker pool, saves each item as it
// finishes, and streams results. Ctrl-C lets requests in progress finish,
// saves them, and exits 130 with the resume command.
func RunBatch(ctx context.Context, a *app.App, opts app.BatchOptions) (app.BatchSummary, error) {
	if !opts.DryRun {
		// --fields applies to the result lines (CSV rows are flat): a wrong
		// field fails here, before anything is sent.
		if err := CheckFields(a.Out); err != nil {
			return app.BatchSummary{}, err
		}
	}
	st, err := Open()
	if err != nil {
		return app.BatchSummary{}, err
	}
	defer st.Close()

	var job *Job
	var p Params
	if opts.ResumeID != "" {
		if job, err = st.Get(opts.ResumeID); err != nil {
			return app.BatchSummary{}, err
		}
		p = ParamsFromMap(job.Params)
	} else {
		if len(opts.Inputs) == 0 {
			return app.BatchSummary{}, output.Errf(output.ExitUsage, "invalid_argument", "", "nothing to recognize")
		}
		if opts.MaxFiles != nil && len(opts.Inputs) > *opts.MaxFiles {
			return app.BatchSummary{}, output.Errf(output.ExitSafety, "max_files_exceeded",
				fmt.Sprintf("pass --max-files %d (or --max-files none), or narrow the selection; --dry-run shows the plan", len(opts.Inputs)),
				"found %d files, more than --max-files %d", len(opts.Inputs), *opts.MaxFiles)
		}
		p = paramsFromOptions(opts)
		if opts.Enterprise && opts.Return != "" {
			a.Out.Info("--return is not used with --enterprise; the enterprise endpoint returns the core fields.")
		}
		if job, err = chooseResume(a, st, opts, p); err != nil {
			return app.BatchSummary{}, err
		}
	}

	r := &runner{a: a, st: st, p: p, opts: opts, resumed: job != nil && opts.ResumeID == ""}
	if job != nil {
		items, err := st.Items(job.ID)
		if err != nil {
			return app.BatchSummary{}, err
		}
		r.items = items
		r.job = job
	} else {
		for i, in := range opts.Inputs {
			r.items = append(r.items, Item{Index: i, Input: in, State: StatePending})
		}
	}
	return r.run(ctx)
}

type runner struct {
	a    *app.App
	st   *Store
	p    Params
	opts app.BatchOptions
	job  *Job // nil until created (a new batch is stored only once confirmed)
	// resumed is set when a re-run continues an earlier job, so the output
	// covers only the files that were left.
	resumed bool
	items   []Item

	todo   []int                   // item indexes to run
	est    map[int]int             // estimated requests per item
	exact  map[int]bool            // the estimate is a known length, not a guess
	cached map[int]json.RawMessage // cache hits found while planning
	// sized is set when a local file's length was guessed from its size
	// (ffprobe is missing or could not read it).
	sized bool

	mu sync.Mutex // guards the fields below and human output
	// spent is what this run has taken from --max-requests: known costs plus
	// the limits sent for files of unknown length. used, reserved and
	// unknown split it for reporting (see Item.Requests).
	spent    int
	used     int
	reserved int
	unknown  int
	stopErr  error
	stopCh   chan struct{}
	lease    *Lease
	stopOnce sync.Once
	maxReqs  int

	// created is set when this run stored a new job; progressed once any
	// file was sent or finished. A new job that stops before either is
	// removed, so running the command again starts afresh.
	created    bool
	progressed atomic.Bool
}

func (r *runner) run(ctx context.Context) (app.BatchSummary, error) {
	a := r.a
	retry := r.opts.RetryFailed
	for i, it := range r.items {
		switch {
		case it.State == StatePending,
			it.State == StateFailed && (it.SafeRetry || retry):
			r.todo = append(r.todo, i)
		}
	}

	plan := r.plan()
	if r.opts.DryRun {
		err := PrintDryRun(a.Out, "", r.jobID(), plan.Endpoint, plan,
			func(w io.Writer) { fmt.Fprintf(w, "Plan: %s.\nNothing was sent (--dry-run).\n", plan.Line()) })
		return app.BatchSummary{JobID: r.jobID()}, err
	}
	if len(r.todo) == 0 && r.job != nil {
		a.Out.Info("Nothing left to do in job %s.", r.job.ID)
		return r.finish(ctx, StatusDone)
	}
	if plan.Requests > 0 {
		// Without a usable token nothing can be sent: say so before asking
		// to confirm or storing a job.
		if _, err := a.APIClient(); err != nil && output.AsError(err).Code == "no_token" {
			return app.BatchSummary{JobID: r.jobID()}, err
		}
	}
	a.Out.Info("Plan: %s.", plan.Line())
	if plan.Requests > 0 {
		approx := ""
		if plan.Approximate {
			approx = "≈ "
		}
		q := fmt.Sprintf("Recognize %s, using %s%s?", output.Plural(len(r.todo), "file"), approx, output.Plural(plan.Requests, "request"))
		switch {
		case plan.Capped():
			q = fmt.Sprintf("Recognize %s, stopping after %s (%s)?", output.Plural(len(r.todo), "file"), output.Plural(plan.MaxRequests, "request"), plan.MaxRequestsName)
		case plan.Unbounded():
			q = fmt.Sprintf("Recognize %s, with no cap on the requests the URLs of unknown length use?", output.Plural(len(r.todo), "file"))
		}
		if err := a.Out.Confirm(q, r.opts.Yes || a.Flags.Yes); err != nil {
			return app.BatchSummary{JobID: r.jobID()}, err
		}
	}
	if r.job == nil {
		inputs := make([]media.Input, len(r.items))
		for i, it := range r.items {
			inputs[i] = it.Input
		}
		job, err := r.st.Create(commandKey(inputs), inputs, r.p.Map())
		if err != nil {
			return app.BatchSummary{}, err
		}
		r.job, r.created = job, true
	}
	lease, err := r.st.ClaimLease(r.job.ID)
	if err != nil {
		return app.BatchSummary{JobID: r.job.ID}, err
	}
	r.lease = lease
	release := lease.Release
	if !r.created {
		// Another audd process may have run this job while this one was
		// planning or waiting at the prompt: work from the stored state.
		if err := r.refresh(); err != nil {
			release(StatusInterrupted)
			return app.BatchSummary{JobID: r.job.ID}, err
		}
		if len(r.todo) == 0 {
			release(StatusDone)
			a.Out.Info("Nothing left to do in job %s: another audd process finished it.", r.job.ID)
			return r.finish(ctx, StatusDone)
		}
	}
	if r.resumed {
		done := len(r.items) - len(r.todo)
		if !a.Out.IsHuman() {
			a.Out.Event("event", map[string]any{"event": "job_resumed", "job_id": r.job.ID, "total": len(r.items), "done": done, "to_run": len(r.todo)})
		}
		a.Out.Info("Resuming job %s: %d of %d files left. Results from the earlier run are not printed again; audd jobs show %s lists them.",
			r.job.ID, len(r.todo), len(r.items), r.job.ID)
	}
	if !a.Out.IsHuman() {
		if !r.resumed { // a resumed job has its job_resumed event instead
			a.Out.Event("event", map[string]any{"event": "job_started", "job_id": r.job.ID, "total": len(r.items), "to_run": len(r.todo)})
		}
	} else {
		a.Out.Info("Job %s: %s to recognize. Ctrl-C stops safely; resume with audd jobs resume %s.", r.job.ID, output.Plural(len(r.todo), "file"), r.job.ID)
	}

	status := r.work(ctx)
	release(status)
	return r.finish(ctx, status)
}

// refresh reloads the job's items after it is claimed and keeps, of the
// files planned, only those still left to run, then plans them again so
// cache hits stored meanwhile are used. It never adds files that were not
// in the confirmed plan.
func (r *runner) refresh() error {
	items, err := r.st.Items(r.job.ID)
	if err != nil {
		return err
	}
	if len(items) != len(r.items) {
		return fmt.Errorf("job %s changed while starting", r.job.ID)
	}
	retry := r.opts.RetryFailed
	var todo []int
	for _, i := range r.todo {
		it := items[i]
		if it.State == StatePending || it.State == StateFailed && (it.SafeRetry || retry) && it.ErrCode != ErrCodeInFlight {
			todo = append(todo, i)
		} else if it.State == StateFailed && it.ErrCode == ErrCodeInFlight && r.items[i].State == StateFailed && r.items[i].ErrCode == ErrCodeInFlight {
			// Already in flight when this run planned it: --retry-failed
			// asked to send it again.
			todo = append(todo, i)
		}
	}
	r.items = items
	if len(todo) != len(r.todo) {
		r.todo = todo
		r.plan()
	}
	return nil
}

func (r *runner) jobID() string {
	if r.job == nil {
		return ""
	}
	return r.job.ID
}

// plan estimates the requests for the items to run and finds cache hits.
func (r *runner) plan() Plan {
	r.est = map[int]int{}
	r.exact = map[int]bool{}
	r.cached = map[int]json.RawMessage{}
	pl := Plan{Endpoint: r.p.Endpoint(), Files: len(r.todo)}
	for _, i := range r.todo {
		in := r.items[i].Input
		if CacheGet != nil && !r.opts.NoCache {
			if res, ok := CacheGet(r.a, in, r.p); ok {
				r.cached[i] = res
				pl.CachedFiles++
				continue
			}
		}
		if !r.p.Enterprise && in.URL == "" {
			if fi, err := os.Stat(in.Path); err == nil && fi.Size() > maxStandardBytes {
				// Fails the size check in process without being sent.
				pl.TooLargeFiles++
				continue
			}
		}
		n, approx, sized, unbounded := itemRequests(in, r.p)
		if unbounded {
			pl.UnknownLengthFiles++
		}
		r.est[i] = n
		r.exact[i] = !approx
		r.sized = r.sized || sized
		pl.Requests += n
		pl.Approximate = pl.Approximate || approx
	}
	pl.CostUSD = float64(pl.Requests) * pricePerRequest
	if max := r.a.Flags.MaxRequests; max > 0 && (pl.Unbounded() || pl.Requests > max) {
		pl.MaxRequests, pl.MaxRequestsName = max, r.a.Flags.MaxRequestsName()
	}
	if pl.Requests > 0 {
		pl.RemainingAllowance = remainingAllowance(r.a)
	}
	return pl
}

// work runs the worker pool and returns the job status to store.
func (r *runner) work(ctx context.Context) string {
	a := r.a
	r.stopCh = make(chan struct{})
	r.maxReqs = a.Flags.MaxRequests

	// Requests already sent finish even after Ctrl-C; a second Ctrl-C
	// abandons them.
	callCtx, abandon := context.WithCancel(context.WithoutCancel(ctx))
	defer abandon()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-finished:
			return
		case <-ctx.Done():
		}
		a.Out.Warn("Stopping: letting requests in progress finish (Ctrl-C again to abandon them).")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		defer signal.Stop(sig)
		select {
		case <-sig:
			abandon()
		case <-finished:
		}
	}()
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-finished:
				return
			case <-t.C:
				if err := r.lease.Heartbeat(); err != nil && output.AsError(err).Code == ErrCodeTakenOver {
					r.stop(err)
					return
				}
			}
		}
	}()

	// People see a line per file; the progress line is for machine output
	// (JSONL or CSV on stdout) while stderr is a terminal.
	var prog output.Progress = noProgress{}
	if !a.Out.IsHuman() {
		prog = a.Out.Progress()
	}
	prog.Start(len(r.todo), "Recognizing")
	defer prog.Done()

	n := r.opts.Concurrency
	if n <= 0 {
		n = a.Profile.Concurrency
	}
	if n <= 0 {
		n = defaultConcurrency
	}
	work := make(chan int)
	go func() {
		defer close(work)
		for _, i := range r.todo {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case work <- i:
			}
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				if ctx.Err() != nil || r.stopped() {
					continue
				}
				r.process(ctx, callCtx, i)
				prog.Inc(1)
			}
		}()
	}
	wg.Wait()

	switch {
	case r.stopErr != nil:
		return StatusStopped
	case ctx.Err() != nil:
		return StatusInterrupted
	}
	return StatusDone
}

func (r *runner) stopped() bool {
	select {
	case <-r.stopCh:
		return true
	default:
		return false
	}
}

func (r *runner) stop(err error) {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopErr = err
		r.mu.Unlock()
		close(r.stopCh)
	})
}

// reservation is what one call takes from the --max-requests budget.
type reservation struct {
	n     int  // requests taken from the budget
	limit *int // enterprise limit to send with the call
	// exact is set when n is what the call costs: a standard call, or an
	// enterprise file of known length. Otherwise n is only the most the
	// call can cost (the limit), or nothing is known when limit is nil.
	exact bool
}

// reserve takes requests for item i from the --max-requests budget.
//
// The standard endpoint costs one request. An enterprise call is billed
// per 12-second chunk, so with a budget the call carries an explicit limit
// that the reservation covers: the API can never bill more than the budget
// allows, even for a URL or a file whose length is a guess. A known length
// (from ffprobe, without skip options) reserves only what it needs, and
// stops the batch rather than cut the file short when that does not fit.
func (r *runner) reserve(i int) (reservation, bool) {
	est := r.est[i]
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.p.Enterprise {
		if r.maxReqs > 0 && r.spent+est > r.maxReqs {
			return reservation{}, false
		}
		r.spent += est
		return reservation{n: est, exact: true}, true
	}
	skip, _ := r.p.optInt("skip")
	_, every := r.p.Options["every"]
	knownLength := r.exact[i] && skip <= 0 && !every
	if r.maxReqs <= 0 {
		res := reservation{limit: r.p.Limit, exact: knownLength}
		switch {
		case knownLength:
			res.n = est
		case r.p.Limit != nil:
			res.n = *r.p.Limit
		}
		r.spent += res.n
		return res, true
	}
	n := r.maxReqs - r.spent
	if r.p.Limit != nil && *r.p.Limit < n {
		n = *r.p.Limit
	}
	if knownLength {
		if est > n {
			return reservation{}, false // stop rather than recognize part of the file
		}
		n = est
	}
	if n <= 0 {
		return reservation{}, false
	}
	r.spent += n
	return reservation{n: n, limit: &n, exact: knownLength}, true
}

// refund returns a reservation whose call never reached the API.
func (r *runner) refund(res reservation) {
	r.mu.Lock()
	r.spent -= res.n
	r.mu.Unlock()
}

// charge records what a call that reached the API used, on the item and in
// the run's totals.
func (r *runner) charge(it *Item, res reservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case res.exact:
		it.Requests = res.n
		r.used += res.n
	case res.limit != nil:
		it.RequestsReserved = res.n
		r.reserved += res.n
	default:
		it.RequestsUnknown = true
		r.unknown++
	}
}

// ErrCodeInFlight is the error code of an item whose request was sent but
// whose process stopped before the response was saved.
const ErrCodeInFlight = "interrupted_in_flight"

const inFlightMessage = "audd stopped while this file was being sent, so AudD may have received and counted it; resume with --retry-failed to send it again"

// budgetStop is the error that stops the batch at --max-requests.
func (r *runner) budgetStop() error {
	r.mu.Lock()
	reserved := r.reserved
	r.mu.Unlock()
	resume := fmt.Sprintf("audd jobs resume %s --max-requests N", r.job.ID)
	name := r.a.Flags.MaxRequestsName()
	if reserved == 0 {
		hint := resume
		if r.a.Flags.MaxRequestsFromConfig {
			hint += ", or remove the setting with audd config unset max_requests"
		}
		return output.Errf(output.ExitSafety, "max_requests_reached", hint,
			"stopped before going over %s", name)
	}
	fix := "pass --limit N so each file reserves at most N requests"
	if r.sized {
		fix = "install ffprobe so local files' lengths are known, or " + fix
	}
	return output.Errf(output.ExitSafety, "max_requests_reached",
		fix+"; to continue this job: "+resume,
		"stopped before going over %s: each file of unknown length reserves the rest of the budget as its limit, and %s are reserved so far (the exact number used is not known)",
		name, output.Thousands(reserved))
}

// process recognizes one item, saves it, and prints it. runCtx ends on
// Ctrl-C; callCtx lets a request already sent finish.
func (r *runner) process(runCtx, ctx context.Context, i int) {
	it := r.items[i]
	it.Err, it.ErrCode, it.APICode, it.SafeRetry, it.Cached, it.Result = "", "", 0, false, false, nil
	it.Requests, it.RequestsReserved, it.RequestsUnknown, it.BudgetLimit = 0, 0, false, 0

	if res, ok := r.cached[i]; ok {
		it.State, it.Result, it.Cached = StateDone, res, true
		r.save(it)
		return
	}
	// Checks before anything is sent. A missing file is retried by a plain
	// resume (after it is restored, say); a file too large for the standard
	// endpoint, or stdin audio that is gone, fails the same way every time,
	// so only --retry-failed tries those again.
	if in := it.Input; in.URL == "" {
		fi, err := os.Stat(in.Path)
		switch {
		case err != nil && in.IsStdin:
			r.fail(&it, failure{err: output.Errf(output.ExitUsage, "stdin_unavailable",
				"run the command again and pipe the audio in", "the audio read from stdin is no longer available")})
			return
		case err != nil:
			r.fail(&it, failure{safe: true, err: output.Errf(output.ExitUsage, "file_not_found", "", "cannot read %s: %v", in.Name(), err)})
			return
		case !r.p.Enterprise && fi.Size() > maxStandardBytes:
			r.fail(&it, failure{err: output.Errf(output.ExitUsage, "file_too_large",
				"use --enterprise for long recordings, or cut a clip with audd recognize FILE --at T",
				"%s is %.1f MB; the standard endpoint takes files up to 10 MB", in.Name(), float64(fi.Size())/(1<<20))})
			return
		}
	}
	res, ok := r.reserve(i)
	if !ok {
		r.stop(r.budgetStop())
		return
	}
	// p is what is sent. A budget can lower the enterprise limit for a file
	// of unknown length; the result may then cover only the start of the
	// file, so it is marked and cached under the limit that was sent, never
	// under the job's own settings.
	p := r.p
	budgetLimited := false
	if r.p.Enterprise {
		p.Limit = res.limit
		budgetLimited = !res.exact && res.limit != nil && (r.p.Limit == nil || *res.limit < *r.p.Limit)
	}
	r.progressed.Store(true)
	// Saved before the request goes out: if the process dies while it is
	// in flight (a crash, SIGKILL), AudD may have received and billed it,
	// so a plain resume must not send it again; --retry-failed does.
	inFlight := it
	inFlight.State, inFlight.ErrCode, inFlight.Err = StateFailed, ErrCodeInFlight, inFlightMessage
	if err := r.lease.SaveItem(inFlight); err != nil {
		// Fails closed: nothing is sent unless this process still holds
		// the job.
		r.refund(res)
		r.stop(saveErr(err))
		return
	}
	for attempt := 0; ; attempt++ {
		result, err := Recognize(ctx, r.a, it.Input, p)
		if err == nil {
			it.State, it.Result = StateDone, result
			if budgetLimited {
				it.BudgetLimit = *res.limit
			}
			r.charge(&it, res)
			// --no-cache skips the cache read only: fresh results are
			// still recorded (as single recognition does), so they show in
			// the explorer's Recent tab and serve later runs.
			if CachePut != nil {
				key := r.p
				if budgetLimited {
					key = p
				}
				CachePut(r.a, it.Input, key, result)
			}
			r.save(it)
			return
		}
		f := classify(err)
		// Back off and retry a rate limit, but not after Ctrl-C: the item
		// is then left as a failure that resume retries.
		if f.rateLimit && attempt < rateLimitRetries && runCtx.Err() == nil && !r.stopped() {
			if sleep(runCtx, time.Duration(2<<attempt)*time.Second) == nil {
				continue
			}
		}
		switch {
		case f.fatal:
			// Refused before any work: the item goes back to how it was.
			r.refund(res)
			if err := r.lease.SaveItem(r.items[i]); err != nil {
				r.stop(saveErr(err))
			}
			r.stop(f.err)
			return
		case f.rateLimit:
			// Rate-limited requests are refused before any work.
			f.safe = true
		}
		if f.safe {
			r.refund(res)
		} else {
			r.charge(&it, res)
		}
		r.fail(&it, f)
		return
	}
}

// saveErr is the error that stops the batch when progress cannot be saved.
func saveErr(err error) error {
	if e, ok := err.(*output.Error); ok && e.Code == ErrCodeTakenOver {
		return e
	}
	return fmt.Errorf("saving job progress: %w", err)
}

func (r *runner) fail(it *Item, f failure) {
	it.State = StateFailed
	it.Err, it.ErrCode, it.APICode, it.SafeRetry = f.err.Message, f.err.Code, f.err.APICode, f.safe
	r.save(*it)
}

func (r *runner) save(it Item) {
	r.progressed.Store(true)
	r.items[it.Index] = it
	if err := r.lease.SaveItem(it); err != nil {
		r.stop(saveErr(err))
	}
	r.emit(it)
}

// finish prints the summary and returns the run's outcome.
func (r *runner) finish(ctx context.Context, status string) (app.BatchSummary, error) {
	a := r.a
	if r.created && r.stopErr != nil && !r.progressed.Load() {
		// Stopped before any file was sent: nothing to resume.
		if err := r.st.Delete(r.job.ID); err == nil {
			e := *output.AsError(r.stopErr)
			if i := strings.Index(e.Hint, "; to continue this job"); i >= 0 {
				e.Hint = e.Hint[:i]
			} else if strings.HasPrefix(e.Hint, "audd jobs resume") {
				e.Hint = "run the command again with a larger --max-requests"
				if a.Flags.MaxRequestsFromConfig {
					e.Hint += ", or remove the setting with audd config unset max_requests"
				}
			}
			return app.BatchSummary{}, &e
		}
	}
	job, err := r.st.Get(r.job.ID)
	if err != nil {
		return app.BatchSummary{JobID: r.job.ID}, err
	}
	sum := app.BatchSummary{
		JobID:      job.ID,
		Recognized: job.Done - job.NoMatch,
		NoMatch:    job.NoMatch,
		Failed:     job.Failed,
		Cached:     job.Cached,
	}
	r.mu.Lock()
	sum.RequestsSpent, sum.RequestsReserved = r.used, r.reserved
	unknown := r.unknown
	r.mu.Unlock()
	if !a.Out.IsHuman() {
		// Every count is for the whole job, as audd jobs list and jobs show
		// report it; the *_this_run counts are the requests this run used.
		a.Out.Event("summary", map[string]any{
			"job_id": job.ID, "status": job.Status, "total": job.Total,
			"recognized": sum.Recognized, "no_match": sum.NoMatch, "failed": sum.Failed,
			"pending": job.Pending, "cached": sum.Cached, "requests_spent": job.Requests,
			"requests_reserved": job.RequestsReserved, "requests_unknown_files": job.RequestsUnknownFiles,
			"requests_spent_this_run": sum.RequestsSpent, "requests_reserved_this_run": sum.RequestsReserved,
			"requests_unknown_files_this_run": unknown,
			"budget_limited_files":            job.BudgetLimitedFiles,
		})
	}
	if a.Out.IsHuman() || a.Out.Format() == output.FormatCSV {
		used := RequestsUsed(sum.RequestsSpent, sum.RequestsReserved, unknown) + " in this run"
		if job.Requests != sum.RequestsSpent || job.RequestsReserved != sum.RequestsReserved || job.RequestsUnknownFiles != unknown {
			used += " (" + RequestsUsed(job.Requests, job.RequestsReserved, job.RequestsUnknownFiles) + " in the whole job)"
		}
		a.Out.Info("Job %s: %d recognized, %d no match, %d failed, %d cached; %s.",
			job.ID, sum.Recognized, sum.NoMatch, sum.Failed, sum.Cached, used)
	}
	if n := job.BudgetLimitedFiles; n > 0 {
		a.Out.Warn("%s may be only partly recognized: --max-requests lowered the limit sent for them. %s",
			output.Plural(n, "file"), BudgetLimitedHint)
	}

	switch {
	case r.stopErr != nil:
		e := output.AsError(r.stopErr)
		if e.Code != "max_requests_reached" && e.Hint == "" {
			e.Hint = "audd jobs resume " + job.ID
		}
		if job.Pending > 0 {
			e.Message += fmt.Sprintf(" (%d of %d files not done yet; job %s)", job.Pending, job.Total, job.ID)
		}
		return sum, e
	case status == StatusInterrupted || ctx.Err() != nil:
		return sum, &output.Error{Code: "interrupted", Exit: ExitInterrupted,
			Message: fmt.Sprintf("stopped with %d of %d files done; nothing finished is lost", job.Done, job.Total),
			Hint:    "audd jobs resume " + job.ID}
	case job.Failed > 0:
		hint := "see the failures with audd jobs show " + job.ID + ", retry them with audd jobs resume " + job.ID + " --retry-failed"
		tooLarge := 0
		for _, it := range r.items {
			if it.State == StateFailed && it.ErrCode == "file_too_large" {
				tooLarge++
			}
		}
		switch {
		case tooLarge == job.Failed:
			hint = "the standard endpoint takes files up to 10 MB: recognize those files with --enterprise --limit N, or a clip of one with audd recognize FILE --at 1:00; audd jobs show " + job.ID + " lists them"
		case tooLarge > 0:
			hint += "; files over 10 MB fail again the same way, so recognize those with --enterprise --limit N"
		}
		return sum, output.Errf(output.ExitPartial, "partial_failure", hint,
			"%d of %d files failed", job.Failed, job.Total)
	}
	return sum, nil
}

// BudgetLimitedHint says how to recognize budget-limited files in full.
const BudgetLimitedHint = "To recognize them in full, run them again with a larger --max-requests (results cut short are not reused from the cache)."

type noProgress struct{}

func (noProgress) Start(int, string) {}
func (noProgress) Inc(int)           {}
func (noProgress) SetLabel(string)   {}
func (noProgress) Done()             {}
