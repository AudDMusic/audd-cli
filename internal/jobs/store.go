// Package jobs keeps batch recognitions as resumable jobs and runs them.
//
// Every batch (a folder, glob, list, or several inputs) becomes a job with a
// short ID. Each item is written to DataDir()/jobs.db as soon as it
// finishes, so an interruption (Ctrl-C, a crash, a dropped SSH session)
// never loses or re-bills finished work: `audd jobs resume <id>` picks up
// where the job stopped.
package jobs

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/store"
)

// Job states.
const (
	StatusPending     = "pending"     // created, not started yet
	StatusRunning     = "running"     // a process is working on it now
	StatusInterrupted = "interrupted" // stopped by Ctrl-C or a crash; resumable
	StatusStopped     = "stopped"     // stopped by a limit or an account error; resumable
	StatusDone        = "done"        // every item finished
	StatusPartial     = "partial"     // every item ran, some failed
)

// Item states.
const (
	StatePending = "pending"
	StateDone    = "done" // recognized, or no match (Result is null or [])
	StateFailed  = "failed"
)

// staleAfter is how long a running job may go without a heartbeat before
// it counts as interrupted (its process is gone).
const staleAfter = 2 * time.Minute

// Job is one batch run.
type Job struct {
	ID      string            `json:"id"`
	Command []string          `json:"-"` // "recognize" + every input; can be long
	Inputs  string            `json:"inputs"`
	Params  map[string]string `json:"params"`
	Created time.Time         `json:"created"`
	Updated time.Time         `json:"updated"`
	Total   int               `json:"total"`
	Done    int               `json:"done"` // finished items, matched or not
	NoMatch int               `json:"no_match"`
	Failed  int               `json:"failed"`
	Pending int               `json:"pending"`
	// Remaining is what a plain resume runs: pending items plus failures
	// that are safe to retry.
	Remaining int `json:"remaining"`
	Cached    int `json:"cached"`
	// Requests counts requests whose number is known: standard calls, and
	// enterprise calls for files of known length. RequestsReserved is the
	// most that calls for files of unknown length may have used (the limit
	// sent with them), and RequestsUnknownFiles counts such files sent
	// without any limit.
	Requests             int `json:"requests_spent"`
	RequestsReserved     int `json:"requests_reserved"`
	RequestsUnknownFiles int `json:"requests_unknown_files"`
	// BudgetLimitedFiles counts enterprise files of unknown length whose
	// limit --max-requests lowered; their results may cover only part of
	// the file (see Item.BudgetLimit).
	BudgetLimitedFiles int    `json:"budget_limited_files"`
	Status             string `json:"status"`
}

// Item is one input of a job.
type Item struct {
	Index  int             `json:"index"`
	Input  media.Input     `json:"input"`
	State  string          `json:"state"`
	Result json.RawMessage `json:"result,omitempty"`
	Err    string          `json:"error,omitempty"`
	// ErrCode is the stable error code (see output.Error); APICode the AudD
	// API error code, when there was one.
	ErrCode string `json:"error_code,omitempty"`
	APICode int    `json:"api_code,omitempty"`
	// SafeRetry marks a failure where nothing reached the API (for example
	// a connection that never opened), so resuming retries it automatically.
	// Other failures may have used a request and are retried only with
	// --retry-failed.
	SafeRetry bool `json:"safe_retry,omitempty"`
	Cached    bool `json:"cached,omitempty"`
	// Requests is the number of requests the item used, when it is known.
	// For an enterprise file of unknown length it is 0, and
	// RequestsReserved is the limit sent with the call (the most it can
	// have used), or RequestsUnknown is set when no limit was sent.
	Requests         int  `json:"requests,omitempty"`
	RequestsReserved int  `json:"requests_reserved,omitempty"`
	RequestsUnknown  bool `json:"requests_unknown,omitempty"`
	// BudgetLimit is set when --max-requests lowered the enterprise limit
	// sent for a file of unknown length: the result covers at most the
	// first BudgetLimit 12-second chunks, so it may be partial.
	BudgetLimit int `json:"budget_limit,omitempty"`
}

var migrations = []string{`
CREATE TABLE jobs (
	id          TEXT PRIMARY KEY,
	command     TEXT NOT NULL,
	params      TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	status      TEXT NOT NULL,
	owner       TEXT NOT NULL DEFAULT '',
	heartbeat   INTEGER NOT NULL DEFAULT 0,
	created     INTEGER NOT NULL,
	updated     INTEGER NOT NULL
);
CREATE INDEX jobs_fingerprint ON jobs(fingerprint);
CREATE TABLE items (
	job_id     TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	idx        INTEGER NOT NULL,
	input      TEXT NOT NULL,
	state      TEXT NOT NULL,
	result     TEXT,
	err        TEXT NOT NULL DEFAULT '',
	err_code   TEXT NOT NULL DEFAULT '',
	api_code   INTEGER NOT NULL DEFAULT 0,
	safe_retry INTEGER NOT NULL DEFAULT 0,
	cached     INTEGER NOT NULL DEFAULT 0,
	requests   INTEGER NOT NULL DEFAULT 0,
	requests_reserved INTEGER NOT NULL DEFAULT 0,
	requests_unknown  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (job_id, idx)
);`, `
ALTER TABLE items ADD COLUMN budget_limit INTEGER NOT NULL DEFAULT 0;`}

// Store is the jobs database.
type Store struct {
	db  *sql.DB
	mu  sync.Mutex // serializes writes
	now func() time.Time
}

// Open opens DataDir()/jobs.db.
func Open() (*Store, error) {
	db, err := store.Open(filepath.Join(config.DataDir(), "jobs.db"), migrations)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func ms(t time.Time) int64 { return t.UnixMilli() }

// fingerprint identifies "the same command" for resume offers.
func fingerprint(cmd []string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(cmd)
	for _, k := range keys {
		_ = enc.Encode([2]string{k, params[k]})
	}
	return hex.EncodeToString(h.Sum(nil))
}

const idAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

func newID() (string, error) {
	b := make([]byte, 6)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(idAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = idAlphabet[n.Int64()]
	}
	return string(b), nil
}

// Create stores a new job with every input pending.
func (s *Store) Create(cmd []string, inputs []media.Input, params map[string]string) (*Job, error) {
	if params == nil {
		params = map[string]string{}
	}
	cmdJSON, _ := json.Marshal(cmd)
	paramsJSON, _ := json.Marshal(params)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := ms(s.now())
	var id string
	for {
		if id, err = newID(); err != nil {
			return nil, err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id = ?`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	if _, err := tx.Exec(`INSERT INTO jobs (id, command, params, fingerprint, status, created, updated) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, string(cmdJSON), string(paramsJSON), fingerprint(cmd, params), StatusPending, now, now); err != nil {
		return nil, err
	}
	stmt, err := tx.Prepare(`INSERT INTO items (job_id, idx, input, state) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	for i, in := range inputs {
		b, _ := json.Marshal(in)
		if _, err := stmt.Exec(id, i, string(b), StatePending); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.get(id)
}

// FindResumable returns the newest job for the same command and params that
// still has work left (pending items, or failures that are safe to retry)
// and is not running in another process.
func (s *Store) FindResumable(cmd []string, params map[string]string) (*Job, bool) {
	if params == nil {
		params = map[string]string{}
	}
	rows, err := s.db.Query(`SELECT id FROM jobs WHERE fingerprint = ? ORDER BY created DESC`, fingerprint(cmd, params))
	if err != nil {
		return nil, false
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		j, err := s.get(id)
		if err != nil || j.Status == StatusRunning {
			continue
		}
		if j.Remaining > 0 {
			return j, true
		}
	}
	return nil, false
}

// Get returns one job with its counts.
func (s *Store) Get(id string) (*Job, error) { return s.get(id) }

func (s *Store) get(id string) (*Job, error) {
	jobs, err := s.query(`WHERE j.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, output.Errf(output.ExitUsage, "job_not_found", "audd jobs list", "no job with ID %q", id)
	}
	return &jobs[0], nil
}

// List returns every job, newest first.
func List(s *Store) ([]Job, error) { return s.query(``) }

func (s *Store) query(where string, args ...any) ([]Job, error) {
	rows, err := s.db.Query(`
SELECT j.id, j.command, j.params, j.status, j.heartbeat, j.created, j.updated,
	COUNT(i.idx),
	COALESCE(SUM(i.state = 'done'), 0),
	COALESCE(SUM(i.state = 'done' AND (i.result IS NULL OR i.result = 'null' OR i.result = '[]')), 0),
	COALESCE(SUM(i.state = 'failed'), 0),
	COALESCE(SUM(i.state = 'pending'), 0),
	COALESCE(SUM(i.state = 'failed' AND i.safe_retry = 1), 0),
	COALESCE(SUM(i.cached), 0),
	COALESCE(SUM(i.requests), 0),
	COALESCE(SUM(i.requests_reserved), 0),
	COALESCE(SUM(i.requests_unknown), 0),
	COALESCE(SUM(i.state = 'done' AND i.budget_limit > 0), 0)
FROM jobs j LEFT JOIN items i ON i.job_id = j.id `+where+`
GROUP BY j.id ORDER BY j.created DESC, j.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	now := s.now()
	for rows.Next() {
		var j Job
		var cmd, params string
		var heartbeat, created, updated int64
		var safeFailed int
		if err := rows.Scan(&j.ID, &cmd, &params, &j.Status, &heartbeat, &created, &updated,
			&j.Total, &j.Done, &j.NoMatch, &j.Failed, &j.Pending, &safeFailed, &j.Cached, &j.Requests, &j.RequestsReserved, &j.RequestsUnknownFiles, &j.BudgetLimitedFiles); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cmd), &j.Command)
		j.Inputs = describeInputs(j.Command)
		_ = json.Unmarshal([]byte(params), &j.Params)
		j.Created, j.Updated = time.UnixMilli(created), time.UnixMilli(updated)
		j.Remaining = j.Pending + safeFailed
		j.Status = deriveStatus(j.Status, now.Sub(time.UnixMilli(heartbeat)) > staleAfter, j.Pending, j.Remaining, j.Failed)
		out = append(out, j)
	}
	return out, rows.Err()
}

// deriveStatus turns the stored status and the item counts into what the
// job is now. A run that went through every file is partial when some
// failed, even when those failures are safe to retry (resume runs them).
func deriveStatus(stored string, stale bool, pending, remaining, failed int) string {
	if stored == StatusRunning {
		if !stale {
			return StatusRunning
		}
		stored = StatusInterrupted
	}
	if remaining == 0 || (pending == 0 && (stored == StatusDone || stored == StatusPartial)) {
		if failed > 0 {
			return StatusPartial
		}
		return StatusDone
	}
	switch stored {
	case StatusPending, StatusInterrupted, StatusStopped:
		return stored
	}
	return StatusInterrupted
}

// Items returns a job's items in input order.
func (s *Store) Items(id string) ([]Item, error) {
	rows, err := s.db.Query(`SELECT idx, input, state, result, err, err_code, api_code, safe_retry, cached, requests, requests_reserved, requests_unknown, budget_limit
FROM items WHERE job_id = ? ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var input string
		var result sql.NullString
		if err := rows.Scan(&it.Index, &input, &it.State, &result, &it.Err, &it.ErrCode, &it.APICode, &it.SafeRetry, &it.Cached, &it.Requests, &it.RequestsReserved, &it.RequestsUnknown, &it.BudgetLimit); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(input), &it.Input)
		if result.Valid {
			it.Result = json.RawMessage(result.String)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Results returns a job's items with their results (the explorer's jobs
// tab reads them); it is Items as a package function.
func Results(s *Store, id string) ([]Item, error) { return s.Items(id) }

// RecentResult is a finished job item, for the explorer's Recent tab.
type RecentResult struct {
	Source string
	At     time.Time // when the job last changed
	Result json.RawMessage
}

// RecentResults returns finished items with a result, newest job first.
func (s *Store) RecentResults(limit int) ([]RecentResult, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT i.input, i.result, j.updated FROM items i JOIN jobs j ON j.id = i.job_id
WHERE i.state = ? AND i.result IS NOT NULL ORDER BY j.updated DESC, i.idx DESC LIMIT ?`, StateDone, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentResult
	for rows.Next() {
		var (
			input, result string
			at            int64
			in            media.Input
		)
		if err := rows.Scan(&input, &result, &at); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(input), &in)
		out = append(out, RecentResult{Source: in.Name(), At: time.UnixMilli(at), Result: json.RawMessage(result)})
	}
	return out, rows.Err()
}

// SaveItem writes an item's state and result.
func (s *Store) SaveItem(id string, it Item) error {
	var result any
	if it.Result != nil {
		result = string(it.Result)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE items SET state = ?, result = ?, err = ?, err_code = ?, api_code = ?, safe_retry = ?, cached = ?, requests = ?, requests_reserved = ?, requests_unknown = ?, budget_limit = ?
WHERE job_id = ? AND idx = ?`, it.State, result, it.Err, it.ErrCode, it.APICode, it.SafeRetry, it.Cached, it.Requests, it.RequestsReserved, it.RequestsUnknown, it.BudgetLimit, id, it.Index)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("job item not found")
	}
	_, err = s.db.Exec(`UPDATE jobs SET updated = ? WHERE id = ?`, ms(s.now()), id)
	return err
}

// Claim marks the job as running in this process. It fails with
// job_running while another live process holds it. release stores the
// final status and gives the job up.
func (s *Store) Claim(id string) (release func(status string), err error) {
	l, err := s.ClaimLease(id)
	if err != nil {
		return nil, err
	}
	return l.Release, nil
}

// Lease is a claim on a running job. A process that stops sending
// heartbeats for staleAfter (suspended, asleep) can lose its lease to
// another process; every write through the lease then fails with
// job_taken_over instead of touching the job.
type Lease struct {
	s         *Store
	id, owner string
}

// ClaimLease is Claim, returning the lease the claiming process writes
// through.
func (s *Store) ClaimLease(id string) (*Lease, error) {
	owner, err := newID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, owner = ?, heartbeat = ?, updated = ?
WHERE id = ? AND NOT (status = ? AND heartbeat > ?)`,
		StatusRunning, owner, ms(now), ms(now), id, StatusRunning, ms(now.Add(-staleAfter)))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.get(id); err != nil {
			return nil, err
		}
		return nil, output.Errf(output.ExitSafety, "job_running", "wait for it to finish, or check: audd jobs show "+id,
			"job %s is running in another audd process", id)
	}
	return &Lease{s: s, id: id, owner: owner}, nil
}

// ErrCodeTakenOver is the error code when another process took a job over.
const ErrCodeTakenOver = "job_taken_over"

func (l *Lease) takenOver() error {
	return output.Errf(output.ExitSafety, ErrCodeTakenOver, "check it with audd jobs show "+l.id,
		"job %s was taken over by another audd process while this one was not responding (suspended or asleep), so this one stopped sending files", l.id)
}

// Release stores the final status and gives the job up, unless another
// process has taken it over.
func (l *Lease) Release(status string) {
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	_, _ = l.s.db.Exec(`UPDATE jobs SET status = ?, owner = '', updated = ? WHERE id = ? AND owner = ?`,
		status, ms(l.s.now()), l.id, l.owner)
}

// Heartbeat records that this process is alive and still holds the job.
// It returns job_taken_over when another process holds it now.
func (l *Lease) Heartbeat() error {
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	res, err := l.s.db.Exec(`UPDATE jobs SET heartbeat = ? WHERE id = ? AND status = ? AND owner = ?`,
		ms(l.s.now()), l.id, StatusRunning, l.owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return l.takenOver()
	}
	return nil
}

// SaveItem writes an item's state and result while this process holds the
// job. It returns job_taken_over, and writes nothing, when it does not.
func (l *Lease) SaveItem(it Item) error {
	var result any
	if it.Result != nil {
		result = string(it.Result)
	}
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	res, err := l.s.db.Exec(`UPDATE items SET state = ?, result = ?, err = ?, err_code = ?, api_code = ?, safe_retry = ?, cached = ?, requests = ?, requests_reserved = ?, requests_unknown = ?, budget_limit = ?
WHERE job_id = ? AND idx = ? AND EXISTS (SELECT 1 FROM jobs WHERE id = ? AND owner = ?)`,
		it.State, result, it.Err, it.ErrCode, it.APICode, it.SafeRetry, it.Cached, it.Requests, it.RequestsReserved, it.RequestsUnknown, it.BudgetLimit,
		l.id, it.Index, l.id, l.owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var owner string
		if err := l.s.db.QueryRow(`SELECT owner FROM jobs WHERE id = ?`, l.id).Scan(&owner); err != nil || owner != l.owner {
			return l.takenOver()
		}
		return errors.New("job item not found")
	}
	_, err = l.s.db.Exec(`UPDATE jobs SET updated = ? WHERE id = ?`, ms(l.s.now()), l.id)
	return err
}

// Clean deletes jobs not updated within olderThan, except running ones,
// and returns how many it deleted.
func (s *Store) Clean(olderThan time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	res, err := s.db.Exec(`DELETE FROM jobs WHERE updated <= ? AND NOT (status = ? AND heartbeat > ?)`,
		ms(now.Add(-olderThan)), StatusRunning, ms(now.Add(-staleAfter)))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Delete removes one job and its items.
func (s *Store) Delete(id string) error {
	if _, err := s.get(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	res, err := s.db.Exec(`DELETE FROM jobs WHERE id = ? AND NOT (status = ? AND heartbeat > ?)`, id, StatusRunning, ms(now.Add(-staleAfter)))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return output.Errf(output.ExitSafety, "job_running", "", "job %s is running in another audd process", id)
	}
	return nil
}

// describeInputs summarizes a job's inputs: "song.mp3" or "a.mp3 and 141 more".
func describeInputs(cmd []string) string {
	if len(cmd) < 2 {
		return ""
	}
	first := cmd[1]
	if !strings.Contains(first, "://") {
		first = filepath.Base(first)
	}
	if n := len(cmd) - 2; n > 0 {
		return fmt.Sprintf("%s and %d more", first, n)
	}
	return first
}
