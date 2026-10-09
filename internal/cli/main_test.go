package cli_test

import (
	"os"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/oauth"
)

func TestMain(m *testing.M) {
	// Device sign-ins poll in milliseconds, and every test runs on the
	// same kind of machine whatever the host: Linux without a display.
	oauth.PollUnit = time.Millisecond
	oauth.DetectEnvironment = func(stdinTTY, stdoutTTY bool) oauth.Environment {
		return oauth.Environment{GOOS: "linux", Getenv: func(string) string { return "" }, StdinTTY: stdinTTY, StdoutTTY: stdoutTTY}
	}
	os.Exit(m.Run())
}
