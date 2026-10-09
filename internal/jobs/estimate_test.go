package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/safety"
)

// A batch estimates an enterprise file like a single file does, --every
// included.
func TestItemRequestsMatchesTheSingleFileEstimate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mix.mp3")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := Durations
	Durations = func(string) (time.Duration, bool) { return 2 * time.Minute, true } // 10 chunks
	t.Cleanup(func() { Durations = old })
	in := media.Input{Path: path}
	for _, c := range []struct {
		skip, every string
		want        int
	}{
		{"", "", 10},
		{"1", "", 5},
		{"2", "2", 6},
		{"3", "1", 3},
	} {
		opts := map[string]string{}
		if c.skip != "" {
			opts["skip"] = c.skip
		}
		if c.every != "" {
			opts["every"] = c.every
		}
		p := Params{Enterprise: true, Options: opts}
		n, approx, _, unbounded := itemRequests(in, p)
		single := safety.Estimate([]media.Input{in}, true, nil, p.optIntPtr("skip"), p.optIntPtr("every"), nil,
			func(string) (time.Duration, bool) { return 2 * time.Minute, true })
		if n != c.want || n != single.Requests || approx || unbounded {
			t.Errorf("skip=%q every=%q: batch %d (approx %v), single file %d, want %d", c.skip, c.every, n, approx, single.Requests, c.want)
		}
	}
}
