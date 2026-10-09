package tui

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

type memStore struct {
	plays map[int][]Play
	adds  int
}

func (s *memStore) Plays(id, limit int) ([]Play, error) {
	p := append([]Play(nil), s.plays[id]...)
	sort.Slice(p, func(i, j int) bool { return p[i].At.After(p[j].At) })
	if len(p) > limit {
		p = p[:limit]
	}
	return p, nil
}

func (s *memStore) AddPlay(p Play) error {
	for _, q := range s.plays[p.RadioID] {
		if q.At.Equal(p.At) {
			return nil
		}
	}
	if s.plays == nil {
		s.plays = map[int][]Play{}
	}
	s.plays[p.RadioID] = append(s.plays[p.RadioID], p)
	s.adds++
	return nil
}

func TestStoreFeedPaths(t *testing.T) {
	ctx := context.Background()
	now := testNow
	recent := []Play{play(1, 30*time.Second, "New", "Song", 200), play(1, 5*time.Minute, "Old", "Song", 200)}
	newFeed := func(store *memStore, running bool, recentErr error) (*StoreFeed, *int) {
		calls := 0
		return &StoreFeed{
			Store: store,
			Recent: func(ctx context.Context, id int) ([]Play, error) {
				calls++
				if recentErr != nil {
					return nil, recentErr
				}
				return recent, nil
			},
			RecorderRunning: func(int) bool { return running },
			Now:             func() time.Time { return now },
		}, &calls
	}

	t.Run("store is current while the recorder runs", func(t *testing.T) {
		store := &memStore{plays: map[int][]Play{1: {play(1, time.Hour, "Stored", "Song", 200)}}}
		f, calls := newFeed(store, true, nil)
		got, err := f.Plays(ctx, 1, 30)
		if err != nil || *calls != 0 || len(got) != 1 || got[0].Artist != "Stored" {
			t.Fatalf("got %v %v calls=%d", got, err, *calls)
		}
	})

	t.Run("empty store falls back and saves", func(t *testing.T) {
		store := &memStore{}
		f, calls := newFeed(store, true, nil)
		got, err := f.Plays(ctx, 1, 30)
		if err != nil || *calls != 1 || len(got) != 2 || got[0].Artist != "New" || store.adds != 2 {
			t.Fatalf("got %v %v calls=%d adds=%d", got, err, *calls, store.adds)
		}
	})

	t.Run("stale store without recorder merges recent results", func(t *testing.T) {
		store := &memStore{plays: map[int][]Play{1: {recent[1], play(1, time.Hour, "Stored", "Song", 200)}}}
		f, calls := newFeed(store, false, nil)
		got, err := f.Plays(ctx, 1, 30)
		if err != nil || *calls != 1 || len(got) != 3 || got[0].Artist != "New" || store.adds != 1 {
			t.Fatalf("got %v %v calls=%d adds=%d", got, err, *calls, store.adds)
		}
		// A second read right away does not ask again.
		if _, err := f.Plays(ctx, 1, 30); err != nil || *calls != 1 {
			t.Fatalf("calls=%d", *calls)
		}
	})

	t.Run("recent store without recorder is used as is", func(t *testing.T) {
		store := &memStore{plays: map[int][]Play{1: {play(1, time.Minute, "Stored", "Song", 200)}}}
		f, calls := newFeed(store, false, nil)
		if _, err := f.Plays(ctx, 1, 30); err != nil || *calls != 0 {
			t.Fatalf("calls=%d", *calls)
		}
	})

	t.Run("fallback errors", func(t *testing.T) {
		boom := errors.New("offline")
		store := &memStore{plays: map[int][]Play{1: {play(1, time.Hour, "Stored", "Song", 200)}}}
		f, _ := newFeed(store, false, boom)
		if got, err := f.Plays(ctx, 1, 30); err != nil || len(got) != 1 {
			t.Fatalf("stored plays should be shown: %v %v", got, err)
		}
		f, _ = newFeed(&memStore{}, false, boom)
		if _, err := f.Plays(ctx, 1, 30); !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestPlayFromRaw(t *testing.T) {
	p := PlayFromRaw(4, testNow, 20, []byte(`{"artist":"A","title":"B","apple_music":{"durationInMillis":180000}}`))
	if p.RadioID != 4 || p.Artist != "A" || p.TrackLength != 3*time.Minute || p.PlayLength != 20 {
		t.Fatalf("%+v", p)
	}
}
