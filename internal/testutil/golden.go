// Package testutil has helpers shared by tests: isolated config/cache/data
// dirs, in-process command runs, and golden files.
package testutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Golden compares got with testdata/<name>.golden next to the test.
// Run tests with AUDD_UPDATE_GOLDEN=1 to rewrite the files.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("AUDD_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file (run with AUDD_UPDATE_GOLDEN=1 to create it): %v", err)
	}
	// Normalize Windows line endings from checkouts with autocrlf.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from golden file %s\n--- got ---\n%s\n--- want ---\n%s", name, path, got, want)
	}
}
