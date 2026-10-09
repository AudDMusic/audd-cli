// Package streamstore keeps the plays and stream health events the stream
// recorder sees, one SQLite database per profile, and answers history,
// report, and export queries over them.
package streamstore

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/store"
)

// Play is one recognized song on a stream.
type Play struct {
	RadioID     int             `json:"radio_id"`
	Timestamp   time.Time       `json:"timestamp"`
	PlayLength  int             `json:"play_length"` // seconds; 0 when unknown
	Artist      string          `json:"artist"`
	Title       string          `json:"title"`
	Album       string          `json:"album"`
	Label       string          `json:"label"`
	ReleaseDate string          `json:"release_date"`
	ISRC        string          `json:"isrc"`
	UPC         string          `json:"upc"`
	SongLink    string          `json:"song_link"`
	Score       int             `json:"score"`
	Raw         json.RawMessage `json:"-"` // the song object as AudD sent it
}

// MarshalJSON writes the typed fields, then every other field of the song
// object as AudD sent it (provider blocks such as apple_music or spotify,
// and fields this version does not know), under their API names.
func (p Play) MarshalJSON() ([]byte, error) {
	type typed Play
	b, err := json.Marshal(typed(p))
	if err != nil || len(p.Raw) == 0 {
		return b, err
	}
	var extra map[string]json.RawMessage
	if json.Unmarshal(p.Raw, &extra) != nil {
		return b, nil // not an object: leave it out
	}
	var have map[string]json.RawMessage
	if err := json.Unmarshal(b, &have); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if _, ok := have[k]; !ok {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return b, nil
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.Write(b[:len(b)-1])
	for _, k := range keys {
		name, _ := json.Marshal(k)
		buf.WriteByte(',')
		buf.Write(name)
		buf.WriteByte(':')
		if err := json.Compact(&buf, extra[k]); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// HealthEvent is a stream status change (AudD notification or a change in
// the stream_running flag).
type HealthEvent struct {
	RadioID int       `json:"radio_id"`
	At      time.Time `json:"at"`
	Code    int       `json:"code"` // AudD notification code, e.g. 650 can't connect, 651 no music; 0 for running-state changes
	Message string    `json:"message"`
	Running bool      `json:"running"`
}

// ReportRow is one line of a report.
type ReportRow struct {
	Key      string
	Plays    int
	Airtime  time.Duration
	Stations int
}

// Gap is a period the store has no recording for.
type Gap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Recording intervals closer than this merge, and shorter gaps are ignored.
const (
	heartbeatSlack = 90 * time.Second
	minGap         = 2 * time.Minute
)

// Store is a stream store database. It is safe for concurrent use, and
// several processes may write the same file.
type Store struct {
	db   *sql.DB
	path string
	// Now is the clock used for gap detection.
	Now func() time.Time
}

var migrations = []string{
	`CREATE TABLE plays (
		radio_id     INTEGER NOT NULL,
		ts           INTEGER NOT NULL,
		play_length  INTEGER NOT NULL DEFAULT 0,
		artist       TEXT NOT NULL DEFAULT '',
		title        TEXT NOT NULL DEFAULT '',
		album        TEXT NOT NULL DEFAULT '',
		label        TEXT NOT NULL DEFAULT '',
		release_date TEXT NOT NULL DEFAULT '',
		isrc         TEXT NOT NULL DEFAULT '',
		upc          TEXT NOT NULL DEFAULT '',
		song_link    TEXT NOT NULL DEFAULT '',
		score        INTEGER NOT NULL DEFAULT 0,
		raw          TEXT,
		recorded_at  INTEGER NOT NULL,
		PRIMARY KEY (radio_id, ts)
	);
	CREATE INDEX plays_ts ON plays(ts);
	CREATE TABLE health (
		id       INTEGER PRIMARY KEY,
		radio_id INTEGER NOT NULL,
		at       INTEGER NOT NULL,
		code     INTEGER NOT NULL DEFAULT 0,
		message  TEXT NOT NULL DEFAULT '',
		running  INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX health_radio_at ON health(radio_id, at);
	CREATE TABLE recorded (
		id      INTEGER PRIMARY KEY,
		from_ts INTEGER NOT NULL,
		to_ts   INTEGER NOT NULL
	);
	CREATE INDEX recorded_to ON recorded(to_ts);
	CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
	// Coverage is kept per stream; radio_id 0 means every stream.
	`ALTER TABLE recorded ADD COLUMN radio_id INTEGER NOT NULL DEFAULT 0;
	CREATE INDEX recorded_radio_to ON recorded(radio_id, to_ts);
	CREATE TABLE streams (
		radio_id   INTEGER PRIMARY KEY,
		first_seen INTEGER NOT NULL,
		current    INTEGER NOT NULL DEFAULT 1
	);`,
}

// Open opens DataDir()/streams-<profile>.db.
func Open(profile string) (*Store, error) {
	if profile == "" {
		profile = config.DefaultProfile
	}
	return OpenPath(filepath.Join(config.DataDir(), "streams-"+profile+".db"))
}

// OpenPath opens a stream store at an explicit path.
func OpenPath(path string) (*Store, error) {
	db, err := store.Open(path, migrations)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, path: path, Now: time.Now}, nil
}

// Path is the database file.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// AddPlay stores a play unless one with the same stream and timestamp (to
// the second) exists. inserted reports whether it was new.
func (s *Store) AddPlay(p Play) (inserted bool, err error) {
	var raw any
	if len(p.Raw) > 0 {
		raw = string(p.Raw)
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO plays
		(radio_id, ts, play_length, artist, title, album, label, release_date, isrc, upc, song_link, score, raw, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.RadioID, p.Timestamp.Unix(), p.PlayLength, p.Artist, p.Title, p.Album, p.Label, p.ReleaseDate,
		p.ISRC, p.UPC, p.SongLink, p.Score, raw, s.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

const playColumns = `radio_id, ts, play_length, artist, title, album, label, release_date, isrc, upc, song_link, score, raw`

func scanPlays(rows *sql.Rows) ([]Play, error) {
	defer rows.Close()
	out := []Play{}
	for rows.Next() {
		var p Play
		var ts int64
		var raw sql.NullString
		if err := rows.Scan(&p.RadioID, &ts, &p.PlayLength, &p.Artist, &p.Title, &p.Album, &p.Label,
			&p.ReleaseDate, &p.ISRC, &p.UPC, &p.SongLink, &p.Score, &raw); err != nil {
			return nil, err
		}
		p.Timestamp = time.Unix(ts, 0).UTC()
		if raw.Valid && raw.String != "" {
			p.Raw = json.RawMessage(raw.String)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// History returns plays newest first. radioID nil means all streams; a zero
// since means all time; limit 0 means no limit.
func (s *Store) History(radioID *int, since time.Time, limit int) ([]Play, error) {
	q := `SELECT ` + playColumns + ` FROM plays WHERE 1=1`
	var args []any
	if radioID != nil {
		q += ` AND radio_id = ?`
		args = append(args, *radioID)
	}
	if !since.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, since.Unix())
	}
	q += ` ORDER BY ts DESC, radio_id`
	if limit > 0 {
		q += ` LIMIT ` + strconv.Itoa(limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanPlays(rows)
}

// Latest returns the most recent play on a stream, or nil.
func (s *Store) Latest(radioID int) (*Play, error) {
	plays, err := s.History(&radioID, time.Time{}, 1)
	if err != nil || len(plays) == 0 {
		return nil, err
	}
	return &plays[0], nil
}

// CountPlays returns the number of stored plays.
func (s *Store) CountPlays() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM plays`).Scan(&n)
	return n, err
}

// AddHealth stores a health event.
func (s *Store) AddHealth(h HealthEvent) error {
	_, err := s.db.Exec(`INSERT INTO health (radio_id, at, code, message, running) VALUES (?, ?, ?, ?, ?)`,
		h.RadioID, h.At.Unix(), h.Code, h.Message, boolInt(h.Running))
	return err
}

// LatestHealth returns the last health event stored for a stream, or nil.
func (s *Store) LatestHealth(radioID int) (*HealthEvent, error) {
	var h HealthEvent
	var at int64
	var running int
	err := s.db.QueryRow(`SELECT radio_id, at, code, message, running FROM health
		WHERE radio_id = ? ORDER BY id DESC LIMIT 1`, radioID).Scan(&h.RadioID, &at, &h.Code, &h.Message, &running)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.At = time.Unix(at, 0).UTC()
	h.Running = running != 0
	return &h, nil
}

// Report groups plays since a time. by is song, artist, label, or station.
// Rows are ordered by plays, then airtime, then key.
func (s *Store) Report(by string, since time.Time) ([]ReportRow, error) {
	var key, group string
	switch by {
	case "song":
		key, group = `min(artist) || ' — ' || min(title)`, `lower(artist), lower(title)`
	case "artist":
		key, group = `min(artist)`, `lower(artist)`
	case "label":
		key, group = `min(label)`, `lower(label)`
	case "station":
		key, group = `CAST(radio_id AS TEXT)`, `radio_id`
	default:
		return nil, output.Errf(output.ExitUsage, "invalid_argument", "use --by song, artist, label, or station",
			"cannot group a report by %q", by)
	}
	q := fmt.Sprintf(`SELECT %s, count(*), coalesce(sum(play_length), 0), count(DISTINCT radio_id)
		FROM plays WHERE ts >= ? GROUP BY %s`, key, group)
	var sinceTS int64
	if !since.IsZero() {
		sinceTS = since.Unix()
	}
	rows, err := s.db.Query(q+` ORDER BY 2 DESC, 3 DESC, 1`, sinceTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReportRow{}
	for rows.Next() {
		var r ReportRow
		var secs int64
		if err := rows.Scan(&r.Key, &r.Plays, &secs, &r.Stations); err != nil {
			return nil, err
		}
		r.Airtime = time.Duration(secs) * time.Second
		out = append(out, r)
	}
	return out, rows.Err()
}

// Heartbeat records that a recorder was running and connected at a time
// for the given streams. Without IDs it records it for every stream.
func (s *Store) Heartbeat(at time.Time, radioIDs ...int) error {
	return s.record(at, at, radioIDs)
}

// Cover records that the store is complete for a period for the given
// streams (for example after a backfill from the recent-results endpoint
// reached back that far). Without IDs it covers every stream.
func (s *Store) Cover(from, to time.Time, radioIDs ...int) error {
	if to.Before(from) {
		from, to = to, from
	}
	return s.record(from, to, radioIDs)
}

func (s *Store) record(from, to time.Time, radioIDs []int) error {
	if len(radioIDs) == 0 {
		radioIDs = []int{0}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	f, t := from.Unix(), to.Unix()
	slack := int64(heartbeatSlack / time.Second)
	for _, id := range radioIDs {
		var rowID, curFrom, curTo int64
		err = tx.QueryRow(`SELECT id, from_ts, to_ts FROM recorded
			WHERE radio_id = ? AND to_ts >= ? AND from_ts <= ? ORDER BY to_ts DESC LIMIT 1`, id, f-slack, t+slack).Scan(&rowID, &curFrom, &curTo)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(`INSERT INTO recorded (radio_id, from_ts, to_ts) VALUES (?, ?, ?)`, id, f, t)
		case err == nil:
			_, err = tx.Exec(`UPDATE recorded SET from_ts = ?, to_ts = ? WHERE id = ?`, min(f, curFrom), max(t, curTo), rowID)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetAccountStreams records the streams the account has now. A stream seen
// for the first time is remembered with the time it was first seen, so a
// stream added later does not show the time before it existed as a gap.
func (s *Store) SetAccountStreams(radioIDs []int, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE streams SET current = 0`); err != nil {
		return err
	}
	for _, id := range radioIDs {
		if _, err := tx.Exec(`INSERT INTO streams (radio_id, first_seen, current) VALUES (?, ?, 1)
			ON CONFLICT(radio_id) DO UPDATE SET current = 1`, id, at.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AccountStreams returns the streams the account had when it was last
// listed, in radio ID order (empty when it was never listed).
func (s *Store) AccountStreams() ([]int, error) {
	return s.ints(`SELECT radio_id FROM streams WHERE current = 1 ORDER BY radio_id`)
}

// HasStreams reports whether the account has streams or the store has
// plays or coverage for any.
func (s *Store) HasStreams() (bool, error) {
	ids, err := s.knownStreams()
	return len(ids) > 0, err
}

// knownStreams returns every stream the store has plays or coverage for.
func (s *Store) knownStreams() ([]int, error) {
	return s.ints(`SELECT radio_id FROM plays UNION SELECT radio_id FROM recorded WHERE radio_id != 0
		UNION SELECT radio_id FROM streams WHERE current = 1 ORDER BY 1`)
}

func (s *Store) ints(q string, args ...any) ([]int, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// LastHeartbeat returns the end of the newest recorded period (zero if
// none). With IDs it is the oldest of those streams' newest periods: the
// time up to which all of them were recorded (zero when any has none).
func (s *Store) LastHeartbeat(radioIDs ...int) (time.Time, error) {
	if len(radioIDs) == 0 {
		var ts sql.NullInt64
		if err := s.db.QueryRow(`SELECT max(to_ts) FROM recorded`).Scan(&ts); err != nil || !ts.Valid {
			return time.Time{}, err
		}
		return time.Unix(ts.Int64, 0).UTC(), nil
	}
	var oldest int64
	for i, id := range radioIDs {
		var ts sql.NullInt64
		if err := s.db.QueryRow(`SELECT max(to_ts) FROM recorded WHERE radio_id IN (?, 0)`, id).Scan(&ts); err != nil {
			return time.Time{}, err
		}
		if !ts.Valid {
			return time.Time{}, nil
		}
		if i == 0 || ts.Int64 < oldest {
			oldest = ts.Int64
		}
	}
	return time.Unix(oldest, 0).UTC(), nil
}

// Earliest returns the oldest point the store knows about: the start of the
// first recorded period or the first play, whichever is older (zero when
// the store is empty).
func (s *Store) Earliest() (time.Time, error) {
	var rec, play sql.NullInt64
	if err := s.db.QueryRow(`SELECT (SELECT min(from_ts) FROM recorded), (SELECT min(ts) FROM plays)`).Scan(&rec, &play); err != nil {
		return time.Time{}, err
	}
	switch {
	case rec.Valid && play.Valid:
		return time.Unix(min(rec.Int64, play.Int64), 0).UTC(), nil
	case rec.Valid:
		return time.Unix(rec.Int64, 0).UTC(), nil
	case play.Valid:
		return time.Unix(play.Int64, 0).UTC(), nil
	}
	return time.Time{}, nil
}

// Gaps returns the periods since a time (up to now) that no recorder
// heartbeat or backfill covers, for the given streams; a period missing
// for any one of them is a gap. Without IDs it checks the streams the
// account had when last listed (or, if it was never listed, every stream
// the store knows). Gaps shorter than two minutes are left out. A zero
// since means all time: gaps then start at Earliest, and an empty store
// has none. A stream first seen on the account after the store began
// recording has no gap before it was seen.
func (s *Store) Gaps(since time.Time, radioIDs ...int) ([]Gap, error) {
	now := s.Now().UTC()
	if since.IsZero() {
		e, err := s.Earliest()
		if err != nil || e.IsZero() {
			return []Gap{}, err
		}
		since = e
	}
	since = since.UTC()
	ids := radioIDs
	if len(ids) == 0 {
		var err error
		if ids, err = s.AccountStreams(); err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			if ids, err = s.knownStreams(); err != nil {
				return nil, err
			}
		}
	}
	if len(ids) == 0 {
		// Nothing but store-wide coverage (or nothing at all).
		return s.streamGaps(since, now, `1=1`)
	}
	var firstAll sql.NullInt64
	if err := s.db.QueryRow(`SELECT min(first_seen) FROM streams`).Scan(&firstAll); err != nil {
		return nil, err
	}
	var all []Gap
	for _, id := range ids {
		from := since
		var first sql.NullInt64
		err := s.db.QueryRow(`SELECT first_seen FROM streams WHERE radio_id = ?`, id).Scan(&first)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if first.Valid && firstAll.Valid && first.Int64 > firstAll.Int64+int64(heartbeatSlack/time.Second) {
			if t := time.Unix(first.Int64, 0).UTC(); t.After(from) {
				from = t
			}
		}
		g, err := s.streamGaps(from, now, `radio_id IN (`+strconv.Itoa(id)+`, 0)`)
		if err != nil {
			return nil, err
		}
		all = append(all, g...)
	}
	return mergeGaps(all), nil
}

// streamGaps returns the gaps between from and now in the recorded periods
// matching where.
func (s *Store) streamGaps(from, now time.Time, where string) ([]Gap, error) {
	gaps := []Gap{}
	if !now.After(from) {
		return gaps, nil
	}
	rows, err := s.db.Query(`SELECT from_ts, to_ts FROM recorded WHERE `+where+` AND to_ts >= ? ORDER BY from_ts`, from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cursor := from
	add := func(from, to time.Time) {
		if to.Sub(from) >= minGap {
			gaps = append(gaps, Gap{From: from, To: to})
		}
	}
	for rows.Next() {
		var f, t int64
		if err := rows.Scan(&f, &t); err != nil {
			return nil, err
		}
		rf, rt := time.Unix(f, 0).UTC(), time.Unix(t, 0).UTC()
		if rf.After(cursor) {
			add(cursor, rf)
		}
		if rt.After(cursor) {
			cursor = rt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if now.After(cursor) {
		add(cursor, now)
	}
	return gaps, nil
}

// mergeGaps sorts gaps and joins the ones that overlap or touch.
func mergeGaps(gaps []Gap) []Gap {
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].From.Before(gaps[j].From) })
	out := []Gap{}
	for _, g := range gaps {
		if n := len(out); n > 0 && !g.From.After(out[n-1].To) {
			if g.To.After(out[n-1].To) {
				out[n-1].To = g.To
			}
			continue
		}
		out = append(out, g)
	}
	return out
}

// SetMeta stores a small value (recorder status notes).
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Meta reads a value stored with SetMeta ("" when absent).
func (s *Store) Meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
