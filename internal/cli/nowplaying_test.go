package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
	"github.com/AudDMusic/audd-cli/internal/tui"
)

func useStreamsFeed(t *testing.T) {
	t.Helper()
	// A result as AudD sends it by default: when the song ends, with
	// play_length, without provider metadata.
	at := time.Now().Add(-4 * time.Minute)
	feed := tui.FeedFuncs{
		StationsFunc: func(ctx context.Context) ([]tui.Station, error) {
			return []tui.Station{{RadioID: 3, URL: "https://radio.example/a.mp3", Running: true}}, nil
		},
		PlaysFunc: func(ctx context.Context, id, limit int) ([]tui.Play, error) {
			return []tui.Play{tui.PlayFromRaw(id, at, 200, json.RawMessage(`{"artist":"Imagine Dragons","title":"Warriors"}`))}, nil
		},
	}
	old := tui.NewFeed
	tui.NewFeed = func(a *app.App) (tui.Feed, error) { return feed, nil }
	t.Cleanup(func() { tui.NewFeed = old })
}

func TestNowPlayingCommand(t *testing.T) {
	testutil.Isolate(t)
	useStreamsFeed(t)

	r := run(t, "now-playing", "--once")
	var doc map[string]any
	if r.Code != 0 || json.Unmarshal([]byte(r.Stdout), &doc) != nil || doc["schema_version"] != float64(1) {
		t.Fatalf("exit %d: %q %q", r.Code, r.Stdout, r.Stderr)
	}

	r = run(t, "now-playing", "3", "--once", "--format", "{{.Artist}} - {{.Title}}")
	if r.Code != 0 || r.Stdout != "Imagine Dragons - Warriors\n" {
		t.Fatalf("template: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}

	r = run(t, "now-playing", "--once", "--format", "csv")
	if r.Code != 0 || !strings.HasPrefix(r.Stdout, "stations.0.") && !strings.Contains(r.Stdout, "radio_id") {
		t.Fatalf("csv: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}

	r = run(t, "now-playing", "--timeout", "50ms")
	if r.Code != 0 || !strings.Contains(r.Stdout, `"type":"result"`) || !strings.Contains(r.Stdout, `"state":"just_played"`) || !strings.Contains(r.Stdout, `"played_seconds":200`) {
		t.Fatalf("stream: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}

	r = run(t, "now-playing", "abc")
	if r.Code != 2 || !strings.Contains(r.Stderr, "not a radio ID") {
		t.Fatalf("bad id: exit %d %q", r.Code, r.Stderr)
	}
}

func TestBrowseCommand(t *testing.T) {
	testutil.Isolate(t)
	useStreamsFeed(t)
	r := run(t, "browse", "--tab", "streams")
	var doc map[string]any
	if r.Code != 0 || json.Unmarshal([]byte(r.Stdout), &doc) != nil || doc["stations"] == nil {
		t.Fatalf("exit %d: %q %q", r.Code, r.Stdout, r.Stderr)
	}
	r = run(t, "browse", "--tab", "nope")
	if r.Code != 2 {
		t.Fatalf("bad tab: exit %d %q", r.Code, r.Stderr)
	}
}

// One station that cannot be read is shown with its error; the command
// fails only when no station can be read.
func TestNowPlayingOnceShowsEveryStation(t *testing.T) {
	testutil.Isolate(t)
	// A result as AudD sends it by default: when the song ends, with
	// play_length, without provider metadata.
	at := time.Now().Add(-4 * time.Minute)
	broken := map[int]bool{4: true}
	feed := tui.FeedFuncs{
		StationsFunc: func(ctx context.Context) ([]tui.Station, error) {
			return []tui.Station{{RadioID: 3, Running: true}, {RadioID: 4, Running: true}}, nil
		},
		PlaysFunc: func(ctx context.Context, id, limit int) ([]tui.Play, error) {
			if broken[id] {
				return nil, &output.Error{Code: "server", Message: fmt.Sprintf("could not read recent results for stream %d: HTTP 500", id), Retryable: true, Exit: output.ExitNetwork}
			}
			return []tui.Play{tui.PlayFromRaw(id, at, 200, json.RawMessage(`{"artist":"Imagine Dragons","title":"Warriors"}`))}, nil
		},
	}
	old := tui.NewFeed
	tui.NewFeed = func(a *app.App) (tui.Feed, error) { return feed, nil }
	t.Cleanup(func() { tui.NewFeed = old })

	r := run(t, "now-playing", "--once")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	var doc struct {
		Stations []struct {
			RadioID    int            `json:"radio_id"`
			NowPlaying map[string]any `json:"now_playing"`
			Error      *struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		} `json:"stations"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil || len(doc.Stations) != 2 {
		t.Fatalf("%v %s", err, r.Stdout)
	}
	if s := doc.Stations[0]; s.NowPlaying["result"].(map[string]any)["title"] != "Warriors" || s.Error != nil {
		t.Fatalf("healthy station: %s", r.Stdout)
	}
	if s := doc.Stations[1]; s.Error == nil || s.Error.Code != "server" || !s.Error.Retryable || !strings.Contains(s.Error.Message, "stream 4") {
		t.Fatalf("failed station: %s", r.Stdout)
	}

	r = run(t, "now-playing", "--once", "--format", "table")
	if r.Code != 0 || !strings.Contains(r.Stdout, "Warriors") || !strings.Contains(r.Stdout, "stream 4: HTTP 500") {
		t.Fatalf("table: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	r = run(t, "now-playing", "--once", "--format", "{{.Title}}")
	if r.Code != 0 || r.Stdout != "Warriors\n" || !strings.Contains(r.Stderr, "stream 4") {
		t.Fatalf("template: exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}

	// Every station failing is a retryable server error (exit 5).
	broken[3] = true
	r = run(t, "now-playing", "--once")
	if r.Code != output.ExitNetwork || errCode(t, r) != "server" {
		t.Fatalf("all failed: exit %d %s", r.Code, r.Stderr)
	}
}
