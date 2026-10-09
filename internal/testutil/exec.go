package testutil

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// PlaceholderToken is the only token value tests and docs use.
const PlaceholderToken = "0123456789abcdef0123456789abcdef"

// Isolate points config, cache, and data dirs at fresh temp dirs, forces the
// file-based secrets store, and clears environment variables that change
// CLI behavior. It returns the config dir.
func Isolate(t testing.TB) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("AUDD_CONFIG_DIR", cfg)
	t.Setenv("AUDD_CACHE_DIR", t.TempDir())
	t.Setenv("AUDD_DATA_DIR", t.TempDir())
	t.Setenv("AUDD_NO_KEYRING", "1")
	t.Setenv("AUDD_NO_UPDATE_CHECK", "1")
	for _, k := range []string{"AUDD_API_TOKEN", "AUDD_FORMAT", "AUDD_PROFILE", "NO_COLOR", "FORCE_COLOR", "AUDD_INSTALL_METHOD", "AUDD_UPDATE_URL", "AUDD_DOCS_URL"} {
		t.Setenv(k, "")
	}
	return cfg
}

// Main is the signature of cli.Main; tests pass it in, which keeps this
// package free of a dependency on internal/cli.
type Main func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

// Result is the outcome of an in-process run.
type Result struct {
	Code           int
	Stdout, Stderr string
}

// Exec runs the CLI in-process with stdin text and args (no TTYs).
func Exec(t testing.TB, main Main, stdin string, args ...string) Result {
	t.Helper()
	var out, errb bytes.Buffer
	code := main(args, strings.NewReader(stdin), &out, &errb)
	return Result{Code: code, Stdout: out.String(), Stderr: errb.String()}
}
