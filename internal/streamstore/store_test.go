package streamstore

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := OpenPath(filepath.Join(t.TempDir(), "streams.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func play(radio int, at time.Time, artist, title, label string, length int) Play {
	return Play{RadioID: radio, Timestamp: at, PlayLength: length, Artist: artist, Title: title, Label: label, Score: 100}
}

func TestOpenUsesDataDirPerProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUDD_DATA_DIR", dir)
	s, err := Open("work")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Path() != filepath.Join(dir, "streams-work.db") {
		t.Fatalf("path %s", s.Path())
	}
}

func TestAddPlayDedupesOnStreamAndTimestamp(t *testing.T) {
	s := openTemp(t)
	p := play(1, t0, "Artist", "Song", "Label", 180)
	p.Raw = []byte(`{"radio_id":1}`)
	ins, err := s.AddPlay(p)
	if err != nil || !ins {
		t.Fatalf("first insert: %v %v", ins, err)
	}
	ins, err = s.AddPlay(p)
	if err != nil || ins {
		t.Fatalf("duplicate must not insert: %v %v", ins, err)
	}
	// Same time on another stream is a different play.
	ins, _ = s.AddPlay(play(2, t0, "Artist", "Song", "", 0))
	if !ins {
		t.Fatal("other stream, same time")
	}
	// Sub-second differences collapse to the same second.
	ins, _ = s.AddPlay(play(1, t0.Add(300*time.Millisecond), "Artist", "Song", "", 0))
	if ins {
		t.Fatal("timestamps are compared at second precision")
	}
	got, err := s.History(nil, time.Time{}, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("history %v %v", got, err)
	}
	var one *Play
	for i := range got {
		if got[i].RadioID == 1 {
			one = &got[i]
		}
	}
	if one == nil || one.Label != "Label" || one.PlayLength != 180 || string(one.Raw) != `{"radio_id":1}` || !one.Timestamp.Equal(t0) {
		t.Fatalf("round trip %+v", one)
	}
}

func TestHistoryFiltersAndOrdersNewestFirst(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 5; i++ {
		s.AddPlay(play(1, t0.Add(time.Duration(i)*time.Minute), "A", "S", "", 0))
		s.AddPlay(play(2, t0.Add(time.Duration(i)*time.Minute+time.Second), "B", "T", "", 0))
	}
	id := 2
	got, err := s.History(&id, t0.Add(2*time.Minute), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].RadioID != 2 || !got[0].Timestamp.After(got[1].Timestamp) {
		t.Fatalf("filtered %+v", got)
	}
	got, _ = s.History(nil, time.Time{}, 4)
	if len(got) != 4 || got[0].Timestamp != t0.Add(4*time.Minute+time.Second) {
		t.Fatalf("limit %+v", got)
	}
	latest, err := s.Latest(1)
	if err != nil || latest == nil || !latest.Timestamp.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("latest %+v %v", latest, err)
	}
	none, err := s.Latest(99)
	if err != nil || none != nil {
		t.Fatalf("latest of unknown stream: %+v %v", none, err)
	}
}

func TestReportAggregates(t *testing.T) {
	s := openTemp(t)
	s.AddPlay(play(1, t0, "Artist A", "One", "Label X", 200))
	s.AddPlay(play(2, t0.Add(time.Minute), "artist a", "one", "Label X", 100)) // same song, different case
	s.AddPlay(play(1, t0.Add(2*time.Minute), "Artist A", "Two", "Label Y", 0))
	s.AddPlay(play(1, t0.Add(3*time.Minute), "Artist B", "Three", "", 60))
	s.AddPlay(play(1, t0.Add(-time.Hour), "Old", "Song", "", 60)) // before since

	rows, err := s.Report("song", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Key != "Artist A — One" || rows[0].Plays != 2 || rows[0].Airtime != 300*time.Second || rows[0].Stations != 2 {
		t.Fatalf("by song %+v", rows)
	}
	rows, _ = s.Report("artist", t0)
	if len(rows) != 2 || rows[0].Key != "Artist A" || rows[0].Plays != 3 || rows[1].Key != "Artist B" {
		t.Fatalf("by artist %+v", rows)
	}
	rows, _ = s.Report("label", t0)
	if len(rows) != 3 || rows[0].Key != "Label X" || rows[0].Plays != 2 {
		t.Fatalf("by label %+v", rows)
	}
	rows, _ = s.Report("station", t0)
	if len(rows) != 2 || rows[0].Key != "1" || rows[0].Plays != 3 || rows[0].Stations != 1 || rows[1].Key != "2" {
		t.Fatalf("by station %+v", rows)
	}
	if _, err := s.Report("genre", t0); err == nil {
		t.Fatal("unknown grouping must fail")
	}
}

func TestGapsFromHeartbeatsAndCoverage(t *testing.T) {
	s := openTemp(t)
	now := t0.Add(3 * time.Hour)
	s.Now = func() time.Time { return now }

	// Nothing recorded yet: the whole window is a gap.
	gaps, err := s.Gaps(t0)
	if err != nil || len(gaps) != 1 || !gaps[0].From.Equal(t0) || !gaps[0].To.Equal(now) {
		t.Fatalf("empty store: %+v %v", gaps, err)
	}

	// Recorder ran 12:00–13:00 with 30 s heartbeats, then stopped until 14:30.
	for at := t0; !at.After(t0.Add(time.Hour)); at = at.Add(30 * time.Second) {
		if err := s.Heartbeat(at); err != nil {
			t.Fatal(err)
		}
	}
	for at := t0.Add(150 * time.Minute); !at.After(now); at = at.Add(30 * time.Second) {
		s.Heartbeat(at)
	}
	gaps, _ = s.Gaps(t0)
	if len(gaps) != 1 || !gaps[0].From.Equal(t0.Add(time.Hour)) || !gaps[0].To.Equal(t0.Add(150*time.Minute)) {
		t.Fatalf("one gap expected: %+v", gaps)
	}

	// A backfill that reaches back to 13:20 shrinks the gap.
	if err := s.Cover(t0.Add(80*time.Minute), t0.Add(150*time.Minute)); err != nil {
		t.Fatal(err)
	}
	gaps, _ = s.Gaps(t0)
	if len(gaps) != 1 || !gaps[0].To.Equal(t0.Add(80*time.Minute)) {
		t.Fatalf("after cover: %+v", gaps)
	}
	// Covering the rest closes it.
	s.Cover(t0.Add(50*time.Minute), t0.Add(90*time.Minute))
	gaps, _ = s.Gaps(t0)
	if len(gaps) != 0 {
		t.Fatalf("all covered: %+v", gaps)
	}

	// A recorder that stopped 10 minutes ago leaves a gap up to now.
	now = now.Add(10 * time.Minute)
	gaps, _ = s.Gaps(t0)
	if len(gaps) != 1 || !gaps[0].To.Equal(now) {
		t.Fatalf("trailing gap: %+v", gaps)
	}
	// The recorder came back a minute ago: the gap ends there, and the last
	// minute (under 2 minutes) does not count as a gap.
	s.Heartbeat(now.Add(-60 * time.Second))
	gaps, _ = s.Gaps(now.Add(-5 * time.Minute))
	if len(gaps) != 1 || !gaps[0].From.Equal(now.Add(-5*time.Minute)) || !gaps[0].To.Equal(now.Add(-60*time.Second)) {
		t.Fatalf("restarted recorder: %+v", gaps)
	}
	gaps, _ = s.Gaps(now.Add(-60 * time.Second))
	if len(gaps) != 0 {
		t.Fatalf("recent heartbeat: %+v", gaps)
	}
	if last, err := s.LastHeartbeat(); err != nil || !last.Equal(now.Add(-60*time.Second)) {
		t.Fatalf("last heartbeat %v %v", last, err)
	}
}

func TestHealthAndMeta(t *testing.T) {
	s := openTemp(t)
	if err := s.AddHealth(HealthEvent{RadioID: 3, At: t0, Code: 650, Message: "can't connect", Running: false}); err != nil {
		t.Fatal(err)
	}
	s.AddHealth(HealthEvent{RadioID: 3, At: t0.Add(time.Minute), Code: 0, Message: "running", Running: true})
	h, err := s.LatestHealth(3)
	if err != nil || h == nil || !h.Running || !h.At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("latest health %+v %v", h, err)
	}
	if h, _ := s.LatestHealth(4); h != nil {
		t.Fatal("no health for unknown stream")
	}
	if err := s.SetMeta("last_error", "boom"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Meta("last_error"); v != "boom" {
		t.Fatalf("meta %q", v)
	}
	if v, _ := s.Meta("missing"); v != "" {
		t.Fatalf("missing meta %q", v)
	}
	if n, err := s.CountPlays(); err != nil || n != 0 {
		t.Fatalf("count %d %v", n, err)
	}
}

func TestConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "streams.db")
	a, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	inserted := make(chan bool, 200)
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				ins, err := s.AddPlay(play(1, t0.Add(time.Duration(i)*time.Second), "A", "S", "", 0))
				if err != nil {
					t.Error(err)
					return
				}
				inserted <- ins
			}
		}(s)
	}
	wg.Wait()
	close(inserted)
	n := 0
	for ins := range inserted {
		if ins {
			n++
		}
	}
	if n != 100 {
		t.Fatalf("exactly one writer inserts each play, got %d inserts", n)
	}
}

func TestPlayJSONIncludesSongFields(t *testing.T) {
	p := Play{RadioID: 1, Timestamp: time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC), Artist: "A", Title: "T", Score: 90,
		Raw: json.RawMessage(`{"artist":"raw artist","score":"90","spotify":{"id":"x"},"timestamp":"2026-10-08 14:00:00"}`)}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"radio_id":1,"timestamp":"2026-10-08T11:00:00Z","play_length":0,"artist":"A","title":"T","album":"","label":"","release_date":"","isrc":"","upc":"","song_link":"","score":90,"spotify":{"id":"x"}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	p.Raw = json.RawMessage(`[1]`)
	if b, _ := json.Marshal(p); strings.Contains(string(b), "[1]") {
		t.Fatalf("a raw value that is not an object is left out: %s", b)
	}
}

func TestGapsSinceAllStartAtEarliestKnownPoint(t *testing.T) {
	s := openTemp(t)
	now := t0.Add(72 * time.Hour)
	s.Now = func() time.Time { return now }

	// Empty store: nothing known, nothing to report.
	if e, err := s.Earliest(); err != nil || !e.IsZero() {
		t.Fatalf("empty earliest %v %v", e, err)
	}
	if gaps, err := s.Gaps(time.Time{}); err != nil || len(gaps) != 0 {
		t.Fatalf("empty store, all time: %+v %v", gaps, err)
	}

	// Plays only, no recorder ever: everything from the first play is a gap.
	s.AddPlay(Play{RadioID: 1, Timestamp: t0, Artist: "A", Title: "One"})
	if e, _ := s.Earliest(); !e.Equal(t0) {
		t.Fatalf("earliest from plays %v", e)
	}
	gaps, _ := s.Gaps(time.Time{})
	if len(gaps) != 1 || !gaps[0].From.Equal(t0) || !gaps[0].To.Equal(now) {
		t.Fatalf("plays without heartbeats: %+v", gaps)
	}

	// A recorded period before the first play moves the start back, and a
	// hole between two recorded periods is a gap.
	s.Cover(t0.Add(-time.Hour), t0.Add(time.Hour))
	s.Cover(t0.Add(48*time.Hour), now)
	if e, _ := s.Earliest(); !e.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("earliest from recorded %v", e)
	}
	gaps, _ = s.Gaps(time.Time{})
	if len(gaps) != 1 || !gaps[0].From.Equal(t0.Add(time.Hour)) || !gaps[0].To.Equal(t0.Add(48*time.Hour)) {
		t.Fatalf("hole between recorded periods: %+v", gaps)
	}
}

func TestCoverageIsPerStream(t *testing.T) {
	s := openTemp(t)
	now := t0.Add(time.Hour)
	s.Now = func() time.Time { return now }
	if err := s.SetAccountStreams([]int{1, 2}, t0); err != nil {
		t.Fatal(err)
	}
	// Only stream 2 is recorded for the whole hour.
	for at := t0; !at.After(now); at = at.Add(30 * time.Second) {
		if err := s.Heartbeat(at, 2); err != nil {
			t.Fatal(err)
		}
	}
	if gaps, _ := s.Gaps(t0, 2); len(gaps) != 0 {
		t.Fatalf("stream 2 was recorded: %+v", gaps)
	}
	gaps, _ := s.Gaps(t0, 1)
	if len(gaps) != 1 || !gaps[0].From.Equal(t0) || !gaps[0].To.Equal(now) {
		t.Fatalf("stream 1 was never recorded: %+v", gaps)
	}
	// Without IDs, the account's streams count: stream 1's gap shows.
	if gaps, _ := s.Gaps(t0); len(gaps) != 1 {
		t.Fatalf("store-wide gaps: %+v", gaps)
	}
	if last, _ := s.LastHeartbeat(1); !last.IsZero() {
		t.Fatalf("stream 1 has no heartbeat: %v", last)
	}
	if last, _ := s.LastHeartbeat(2); !last.Equal(now) {
		t.Fatalf("stream 2 heartbeat %v", last)
	}
	if last, _ := s.LastHeartbeat(1, 2); !last.IsZero() {
		t.Fatalf("both streams: %v", last)
	}
	// A backfill of stream 1 covers part of the hour.
	s.Cover(t0.Add(40*time.Minute), now, 1)
	gaps, _ = s.Gaps(t0)
	if len(gaps) != 1 || !gaps[0].To.Equal(t0.Add(40*time.Minute)) {
		t.Fatalf("after backfill: %+v", gaps)
	}
	// A stream added later has no gap before it was first seen.
	s.SetAccountStreams([]int{1, 2, 3}, t0.Add(50*time.Minute))
	s.Cover(t0.Add(50*time.Minute), now, 3)
	if gaps, _ := s.Gaps(t0, 3); len(gaps) != 0 {
		t.Fatalf("new stream: %+v", gaps)
	}
	if ids, _ := s.AccountStreams(); len(ids) != 3 {
		t.Fatalf("account streams %v", ids)
	}
	s.SetAccountStreams([]int{2}, now)
	if ids, _ := s.AccountStreams(); len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("removed streams are dropped: %v", ids)
	}
	if gaps, _ := s.Gaps(t0); len(gaps) != 0 {
		t.Fatalf("only stream 2 is on the account now: %+v", gaps)
	}
}
