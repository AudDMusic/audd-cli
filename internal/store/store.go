// Package store opens the CLI's SQLite databases (results cache, jobs,
// stream store) with pure-Go SQLite, WAL journaling, and versioned migrations.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Open opens (creating if needed) the database at path, enables WAL and a
// 5 s busy timeout, and applies migrations[user_version:] in order. Pending
// migrations and the user_version bump run in one transaction, so a failed
// migration leaves the database as it was. Migrations are append-only.
func Open(path string, migrations []string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// Switching a new database to WAL needs an exclusive lock and SQLite
	// does not wait for it, so another process opening the same file at the
	// same moment can make it fail with SQLITE_BUSY. Retry for a while.
	deadline := time.Now().Add(prepareTimeout)
	err = migrate(db, migrations)
	for wait := 10 * time.Millisecond; isBusy(err) && time.Now().Before(deadline); wait = min(2*wait, 200*time.Millisecond) {
		time.Sleep(wait)
		err = migrate(db, migrations)
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("preparing %s: %w", path, err)
	}
	return db, nil
}

// prepareTimeout bounds how long Open retries while the database is busy.
const prepareTimeout = 10 * time.Second

func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	c := se.Code() & 0xff
	return c == sqlite3.SQLITE_BUSY || c == sqlite3.SQLITE_LOCKED
}

func migrate(db *sql.DB, migrations []string) error {
	tx, err := db.Begin() // immediate: serializes concurrent migrators
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version >= len(migrations) {
		return tx.Commit()
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}
