package streams

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams/streamstest"
)

func TestParseTimestamp(t *testing.T) {
	want := time.Date(2026, 10, 8, 11, 2, 0, 0, time.UTC)
	for _, s := range []string{"2026-10-08 14:02:00", "2026-10-08T11:02:00Z", "1791457320", "1791457320000"} {
		got, ok := ParseTimestamp(s)
		if !ok || !got.Equal(want) {
			t.Errorf("%q: %v %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "soon", "-5"} {
		if _, ok := ParseTimestamp(s); ok {
			t.Errorf("%q should not parse", s)
		}
	}
	if FormatTimestamp(want) != "2026-10-08 14:02:00" {
		t.Fatal(FormatTimestamp(want))
	}
}

// testdata/getchannelbyid.json is built by hand in the shape widget.audd.tech
// reads (NumericalId, StringId, a History ring buffer whose newest entry is
// Elements[(OldestElement-1) % len], times in "song_length" or "timestamp"
// at UTC+3), with wrong-typed and unknown values mixed in. It is not a
// recording: recording one needs a live category, which tests never use.
// The contract test should replace it with a recorded response (placeholder
// token and category) and confirm the UTC+3 timestamps.
func TestParseRecentResultsFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/getchannelbyid.json")
	if err != nil {
		t.Fatal(err)
	}
	plays, err := ParseRecentResults(body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, p := range plays {
		titles = append(titles, p.Title)
	}
	if strings.Join(titles, ",") != "First Song,Second Song,Third Song,Fourth Song" {
		t.Fatalf("oldest-first order from the ring buffer: %v", titles)
	}
	third := plays[2]
	if third.Score != 97 || third.PlayLength != 201 || third.Label != "Label C" || third.ISRC != "USAAA2400003" || third.RadioID != 7 ||
		!third.Timestamp.Equal(time.Date(2026, 10, 8, 10, 58, 10, 0, time.UTC)) {
		t.Fatalf("third %+v", third)
	}
	if plays[1].Score != 0 || plays[3].PlayLength != 180 {
		t.Fatalf("lenient numbers: %+v %+v", plays[1], plays[3])
	}
	if !plays[0].Timestamp.Equal(time.Date(2026, 10, 8, 10, 50, 0, 0, time.UTC)) {
		t.Fatalf("song_length holds the time in the widget format: %v", plays[0].Timestamp)
	}
	var raw map[string]any
	if err := json.Unmarshal(third.Raw, &raw); err != nil || raw["apple_music"] == nil {
		t.Fatalf("raw keeps provider metadata: %s", third.Raw)
	}
}

func TestParseRecentResultsShapes(t *testing.T) {
	for name, body := range map[string]string{
		"wrapped": `{"status":"success","result":{"History":{"Elements":[{"artist":"A","title":"B","timestamp":"2026-10-08 14:00:00"}],"OldestElement":0}}}`,
		"flat":    `{"Elements":[{"artist":"A","title":"B","timestamp":"2026-10-08 14:00:00"}],"OldestElement":"0"}`,
		"array":   `[{"artist":"A","title":"B","timestamp":"2026-10-08 14:00:00"}]`,
	} {
		plays, err := ParseRecentResults([]byte(body), time.Now())
		if err != nil || len(plays) != 1 || plays[0].Artist != "A" {
			t.Errorf("%s: %+v %v", name, plays, err)
		}
	}
	plays, err := ParseRecentResults([]byte(`{"NumericalId":1}`), time.Now())
	if err != nil || len(plays) != 0 {
		t.Fatalf("no history: %v %v", plays, err)
	}
	if _, err := ParseRecentResults([]byte(`{"status":"error","error":{"error_code":19,"error_message":"nope"}}`), time.Now()); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error status: %v", err)
	}
	if _, err := ParseRecentResults([]byte(`<html>`), time.Now()); err == nil {
		t.Fatal("not JSON")
	}
}

func useFake(t *testing.T, srv *streamstest.Server) {
	t.Helper()
	old := RecentResultsURL
	RecentResultsURL = srv.URL + "/lastSong/getChannelById/"
	oldLP := LongpollHTTPClient
	LongpollHTTPClient = srv.HTTPClient()
	t.Cleanup(func() { RecentResultsURL, LongpollHTTPClient = old, oldLP })
}

func TestRecentResultsRequest(t *testing.T) {
	srv := streamstest.New(t)
	useFake(t, srv)
	srv.SetRecent(5, streamstest.RecentBody(30,
		map[string]any{"artist": "A", "title": "One", "timestamp": "2026-10-08 14:00:00"},
		map[string]any{"artist": "B", "title": "Two", "timestamp": "2026-10-08 14:04:00"},
		map[string]any{"artist": "C", "title": "Three", "timestamp": "2026-10-08 14:08:00"},
	))
	plays, err := RecentResults(context.Background(), 5, streamstest.Category(5))
	if err != nil || len(plays) != 3 || plays[0].Title != "One" || plays[2].Title != "Three" {
		t.Fatalf("%+v %v", plays, err)
	}
	calls := srv.Calls()
	if len(calls) != 1 || calls[0].Params.Get("ch_id") != "-"+streamstest.Category(5) || calls[0].Params.Get("api_token") != "" {
		t.Fatalf("request: %+v", calls)
	}
	srv.FailNextRecent(1)
	_, err = RecentResults(context.Background(), 5, streamstest.Category(5))
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "server" || oe.Exit != output.ExitNetwork || !oe.Retryable || !strings.Contains(oe.Message, "stream 5") {
		t.Fatalf("HTTP 500 is a retryable server error naming the stream: %#v", err)
	}
}

func TestPlayFromMatch(t *testing.T) {
	body := []byte(`{"status":"success","result":{"radio_id":3,"timestamp":"2026-10-08 14:02:00","play_length":111,"results":[{"artist":"A","title":"T","score":"90","isrc":"X","apple_music":{"url":"u"}}]}}`)
	m, _, err := audd.ParseCallback(body)
	if err != nil {
		t.Fatal(err)
	}
	p := PlayFromMatch(*m, time.Now())
	if p.RadioID != 3 || p.PlayLength != 111 || p.Artist != "A" || p.ISRC != "X" || !p.Timestamp.Equal(time.Date(2026, 10, 8, 11, 2, 0, 0, time.UTC)) {
		t.Fatalf("%+v", p)
	}
	if !strings.Contains(string(p.Raw), `"apple_music"`) {
		t.Fatalf("raw song: %s", p.Raw)
	}
}
