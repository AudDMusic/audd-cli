package api

import (
	"encoding/json"
	"testing"

	"github.com/AudDMusic/audd-go"
)

// m is a match in the chunk at chunkAt seconds, at [startMS, endMS] within it.
func m(artist, title string, chunkAt float64, startMS, endMS int) Match {
	s := chunkAt + float64(startMS)/1000
	e := chunkAt + float64(endMS)/1000
	return Match{Artist: artist, Title: title, StartOffset: startMS, EndOffset: endMS, StartSeconds: &s, EndSeconds: &e}
}

type span struct {
	title      string
	start, end float64
	matches    int
}

func check(t *testing.T, got []Track, want []span) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d tracks %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Title != w.title || g.StartSeconds != w.start || g.EndSeconds != w.end || g.Matches != w.matches {
			t.Errorf("track %d: got %s %.3f–%.3f (%d), want %s %.3f–%.3f (%d)", i, g.Title, g.StartSeconds, g.EndSeconds, g.Matches, w.title, w.start, w.end, w.matches)
		}
	}
}

func TestTracklistMergesConsecutiveChunks(t *testing.T) {
	got := Tracklist([]Match{
		m("A", "One", 0, 500, 12000),
		m("A", "One", 12, 0, 12000),
		m("a ", "ONE", 24, 0, 7000), // case and spaces don't matter
		m("B", "Two", 36, 1000, 12000),
		m("B", "Two", 48, 0, 9500),
	}, 0)
	check(t, got, []span{{"One", 0.5, 31, 3}, {"Two", 37, 57.5, 2}})
	if got[0].Start != "0:00" || got[0].End != "0:31" || got[1].End != "0:57" {
		t.Fatalf("clock: %+v", got)
	}
}

func TestTracklistGaps(t *testing.T) {
	// One chunk without a match is bridged; two are not.
	got := Tracklist([]Match{
		m("A", "One", 0, 0, 12000),
		m("A", "One", 24, 0, 12000),
		m("A", "One", 60, 0, 5000),
	}, 0)
	check(t, got, []span{{"One", 0, 36, 2}, {"One", 60, 65, 1}})

	// With skip=4, scanned chunks are 60 s apart and are still consecutive.
	got = Tracklist([]Match{
		m("A", "One", 0, 0, 12000),
		m("A", "One", 60, 0, 12000),
		m("A", "One", 120, 0, 12000),
	}, 4)
	check(t, got, []span{{"One", 0, 132, 3}})
}

func TestTracklistAlternatingSongs(t *testing.T) {
	got := Tracklist([]Match{
		m("A", "One", 0, 0, 12000),
		m("B", "Two", 12, 0, 12000),
		m("A", "One", 24, 0, 12000),
	}, 0)
	check(t, got, []span{{"One", 0, 12, 1}, {"Two", 12, 24, 1}, {"One", 24, 36, 1}})
}

func TestTracklistSingleChunkAndSideBySide(t *testing.T) {
	got := Tracklist([]Match{m("A", "One", 0, 100, 8000)}, 0)
	check(t, got, []span{{"One", 0.1, 8, 1}})

	// Two candidates matched in the same chunks are tracked side by side.
	got = Tracklist([]Match{
		m("A", "One", 0, 1, 8680),
		m("B", "Two", 0, 1, 8840),
		m("A", "One", 12, 0, 12000),
		m("B", "Two", 12, 0, 11000),
	}, 0)
	check(t, got, []span{{"One", 0.001, 24, 2}, {"Two", 0.001, 23, 2}})
}

func TestTracklistWithoutPositions(t *testing.T) {
	got := Tracklist([]Match{
		{Artist: "A", Title: "One"},
		{Artist: "A", Title: "One"},
		{Artist: "B", Title: "Two"},
	}, 0)
	if len(got) != 2 || got[0].Matches != 2 || got[1].Title != "Two" {
		t.Fatalf("%+v", got)
	}
}

func TestMatchesJSONRoundTrip(t *testing.T) {
	body := `[{"songs":[{"score":100,"artist":"A","title":"One","start_offset":1,"end_offset":8680,"brand_new":"x"}],"offset":"00:12"}]`
	var chunks []audd.EnterpriseChunkResult
	if err := json.Unmarshal([]byte(body), &chunks); err != nil {
		t.Fatal(err)
	}
	ms := chunks[0].Songs
	s, e := 12.001, 20.68
	ms[0].StartSeconds, ms[0].EndSeconds = &s, &e
	raw := MatchesJSON(ms)
	want := `[{"score":100,"artist":"A","title":"One","start_offset":1,"end_offset":8680,"brand_new":"x","start_seconds":12.001,"end_seconds":20.68}]`
	if string(raw) != want {
		t.Fatalf("got %s", raw)
	}
	back := ParseMatches(raw)
	if len(back) != 1 || *back[0].StartSeconds != 12.001 || back[0].Score != 100 {
		t.Fatalf("%+v", back)
	}
	if string(MatchesJSON(nil)) != "[]" {
		t.Fatal("empty")
	}
}

func TestClock(t *testing.T) {
	for sec, want := range map[float64]string{0: "0:00", 59.9: "0:59", 61: "1:01", 3600: "1:00:00", 3725: "1:02:05"} {
		if got := Clock(sec); got != want {
			t.Errorf("Clock(%v) = %s, want %s", sec, got, want)
		}
	}
}

func TestNormalizeReturn(t *testing.T) {
	got, err := NormalizeReturn(" spotify,Apple_Music,spotify ")
	if err != nil || got != "apple_music,spotify" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := NormalizeReturn("napster"); err == nil {
		t.Fatal("napster is not a metadata source")
	}
	if got, _ := NormalizeReturn(""); got != "" {
		t.Fatal("empty")
	}
}

func TestView(t *testing.T) {
	raw := json.RawMessage(`{"artist":"A","title":"T","score":"87","apple_music":{"artwork":{"url":"https://img/{w}x{h}.jpg","bgColor":"112233"}}}`)
	v, ok := View(raw, true)
	if !ok || v.Artist != "A" || v.Score != 87 || v.AppleArtwork != "https://img/{w}x{h}.jpg" || v.AppleBG != "112233" || !v.Cached {
		t.Fatalf("%+v", v)
	}
	if _, dup := v.Extra["artist"]; dup || v.Extra["apple_music"] == nil {
		t.Fatalf("Extra holds only fields beyond the typed ones: %v", v.Extra)
	}
	if _, ok := View(json.RawMessage("null"), false); ok {
		t.Fatal("null is no match")
	}
	v, _ = View(json.RawMessage(`{"audio_id":42,"timecode":"00:05"}`), false)
	if v.Title != "Custom catalog match: audio_id 42" || Essential(v) != v.Title {
		t.Fatalf("%+v", v)
	}
}
