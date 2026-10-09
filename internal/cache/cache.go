// Package cache keeps recognition results (never audio) so the same file or
// URL is not paid for twice. Files are keyed by a SHA-256 of their bytes, so
// a renamed or copied file still hits; URLs are keyed by the URL and expire
// after 24 hours.
package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/store"
)

// Endpoints used in cache keys.
const (
	EndpointStandard   = "recognize"
	EndpointEnterprise = "enterprise"
)

// URLTTL is how long a URL result stays valid: the audio behind a URL can change.
const URLTTL = 24 * time.Hour

var migrations = []string{
	`CREATE TABLE results (
		key      TEXT PRIMARY KEY,
		source   TEXT NOT NULL DEFAULT '',
		endpoint TEXT NOT NULL DEFAULT '',
		is_url   INTEGER NOT NULL DEFAULT 0,
		at       INTEGER NOT NULL,
		result   TEXT NOT NULL
	);
	CREATE INDEX results_at ON results(at);`,
}

// Cache is the results cache. It is safe for concurrent use.
type Cache struct {
	db  *sql.DB
	now func() time.Time
}

// Entry is one cached result. Result is the API's result object for the
// standard endpoint ("null" for no match) or the array of enterprise matches.
type Entry struct {
	Key, Source string
	Endpoint    string
	At          time.Time
	Result      json.RawMessage
}

// Open opens the cache at CacheDir()/cache.db.
func Open() (*Cache, error) {
	return OpenAt(filepath.Join(config.CacheDir(), "cache.db"))
}

// OpenAt opens a cache database at path.
func OpenAt(path string) (*Cache, error) {
	db, err := store.Open(path, migrations)
	if err != nil {
		return nil, err
	}
	return &Cache{db: db, now: time.Now}, nil
}

// SetClock replaces the clock (tests).
func (c *Cache) SetClock(now func() time.Time) { c.now = now }

// Close closes the database.
func (c *Cache) Close() error { return c.db.Close() }

// KeyForFile hashes the file's bytes with the endpoint and parameters.
func KeyForFile(path string, endpoint string, params map[string]string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return key("file", hex.EncodeToString(h.Sum(nil)), endpoint, params), nil
}

// KeyForURL keys a URL with the endpoint and parameters.
func KeyForURL(u string, endpoint string, params map[string]string) string {
	return key("url", strings.TrimSpace(u), endpoint, params)
}

// KeyForInput keys a media input (file, stdin audio, or URL).
func KeyForInput(in media.Input, endpoint string, params map[string]string) (string, error) {
	if in.URL != "" {
		return KeyForURL(in.URL, endpoint, params), nil
	}
	return KeyForFile(in.Path, endpoint, params)
}

func key(kind, id, endpoint string, params map[string]string) string {
	names := make([]string, 0, len(params))
	for k := range params {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(kind + "\x00" + id + "\x00" + endpoint)
	for _, k := range names {
		if params[k] == "" {
			continue
		}
		b.WriteString("\x00" + k + "=" + params[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Get returns a cached result. URL entries older than URLTTL are misses.
func (c *Cache) Get(key string) (json.RawMessage, bool) {
	var (
		res   string
		isURL bool
		at    int64
	)
	err := c.db.QueryRow(`SELECT result, is_url, at FROM results WHERE key = ?`, key).Scan(&res, &isURL, &at)
	if err != nil {
		return nil, false
	}
	if isURL && c.now().Sub(time.UnixMilli(at)) > URLTTL {
		return nil, false
	}
	return json.RawMessage(res), true
}

// Put stores a result.
func (c *Cache) Put(key string, isURL bool, result json.RawMessage) error {
	return c.PutEntry(Entry{Key: key, Result: result}, isURL)
}

// PutEntry stores a result with its source (path or URL, for display) and
// endpoint. At defaults to now.
func (c *Cache) PutEntry(e Entry, isURL bool) error {
	if e.Key == "" {
		return errors.New("cache: empty key")
	}
	if len(e.Result) == 0 {
		e.Result = json.RawMessage("null")
	}
	at := e.At
	if at.IsZero() {
		at = c.now()
	}
	_, err := c.db.Exec(`INSERT INTO results (key, source, endpoint, is_url, at, result) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET source = excluded.source, endpoint = excluded.endpoint,
		is_url = excluded.is_url, at = excluded.at, result = excluded.result`,
		e.Key, e.Source, e.Endpoint, isURL, at.UnixMilli(), string(e.Result))
	return err
}

// Recent returns the newest entries first (for the explorer's Recent tab).
func (c *Cache) Recent(limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := c.db.Query(`SELECT key, source, endpoint, at, result FROM results ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var (
			e   Entry
			at  int64
			res string
		)
		if err := rows.Scan(&e.Key, &e.Source, &e.Endpoint, &at, &res); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at)
		e.Result = json.RawMessage(res)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Count returns the number of cached results.
func (c *Cache) Count() (int, error) {
	var n int
	err := c.db.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&n)
	return n, err
}

// Clear deletes every cached result.
func (c *Cache) Clear() error {
	_, err := c.db.Exec(`DELETE FROM results`)
	return err
}
