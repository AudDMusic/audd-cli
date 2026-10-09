package store

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "test.db")
	m1 := []string{`CREATE TABLE a (id INTEGER PRIMARY KEY, v TEXT)`}
	db, err := Open(path, m1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO a (v) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
	var timeout int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("busy_timeout %d %v", timeout, err)
	}
	db.Close()

	m2 := append(m1, `ALTER TABLE a ADD COLUMN w TEXT`, `CREATE INDEX a_v ON a(v)`)
	db, err = Open(path, m2)
	if err != nil {
		t.Fatalf("second open must only apply new migrations: %v", err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 3 {
		t.Fatalf("user_version %d %v", v, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM a WHERE w IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("data kept: %d %v", n, err)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	_, err := Open(path, []string{`CREATE TABLE ok (id INTEGER)`, `THIS IS NOT SQL`})
	if err == nil {
		t.Fatal("want error")
	}
	db, err := Open(path, []string{`CREATE TABLE ok (id INTEGER)`})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	_ = db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 1 {
		t.Fatalf("user_version %d", v)
	}
}

func TestConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	mig := []string{`CREATE TABLE n (v INTEGER)`}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(path, mig)
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			for j := 0; j < 25; j++ {
				if _, err := db.Exec(`INSERT INTO n VALUES (?)`, j); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	db, _ := Open(path, mig)
	defer db.Close()
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM n`).Scan(&n)
	if n != 100 {
		t.Fatalf("rows %d", n)
	}
}
