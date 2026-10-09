package jobs

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	testutil.Isolate(t)
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func files(names ...string) []media.Input {
	out := make([]media.Input, len(names))
	for i, n := range names {
		out[i] = media.Input{Path: n}
	}
	return out
}

func TestStoreCreateItemsAndCounts(t *testing.T) {
	s := openStore(t)
	params := map[string]string{"endpoint": "standard"}
	j, err := s.Create([]string{"recognize", "a.mp3", "b.mp3", "c.mp3"}, files("a.mp3", "b.mp3", "c.mp3"), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.ID) != 6 || j.Total != 3 || j.Status != StatusPending {
		t.Fatalf("job %+v", j)
	}
	items, err := s.Items(j.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("items %v %v", items, err)
	}
	if items[1].Index != 1 || items[1].Input.Path != "b.mp3" || items[1].State != StatePending {
		t.Fatalf("item %+v", items[1])
	}

	items[0].State, items[0].Result, items[0].Requests = StateDone, json.RawMessage(`{"artist":"A","title":"T"}`), 1
	items[1].State, items[1].Result = StateDone, json.RawMessage(`null`)
	items[2].State, items[2].Err, items[2].ErrCode = StateFailed, "boom", "server"
	for _, it := range items {
		if err := s.SaveItem(j.ID, it); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Done != 2 || got.Failed != 1 || got.NoMatch != 1 || got.Pending != 0 || got.Requests != 1 {
		t.Fatalf("counts %+v", got)
	}
	if got.Params["endpoint"] != "standard" || len(got.Command) != 4 {
		t.Fatalf("params/command %+v", got)
	}
	items, _ = s.Items(j.ID)
	if string(items[0].Result) != `{"artist":"A","title":"T"}` || items[2].Err != "boom" {
		t.Fatalf("round trip %+v", items)
	}

	list, err := List(s)
	if err != nil || len(list) != 1 || list[0].ID != j.ID {
		t.Fatalf("list %v %v", list, err)
	}
}

func TestStoreGetUnknown(t *testing.T) {
	s := openStore(t)
	_, err := s.Get("nope00")
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "job_not_found" || oe.Exit != output.ExitUsage {
		t.Fatalf("got %#v", err)
	}
}

func TestFindResumable(t *testing.T) {
	s := openStore(t)
	cmd := []string{"recognize", "a.mp3", "b.mp3"}
	p := map[string]string{"endpoint": "standard"}
	if _, ok := s.FindResumable(cmd, p); ok {
		t.Fatal("empty store")
	}
	j, _ := s.Create(cmd, files("a.mp3", "b.mp3"), p)
	if got, ok := s.FindResumable(cmd, p); !ok || got.ID != j.ID {
		t.Fatalf("should find %s: %+v %v", j.ID, got, ok)
	}
	if _, ok := s.FindResumable(cmd, map[string]string{"endpoint": "enterprise"}); ok {
		t.Fatal("different params must not match")
	}
	if _, ok := s.FindResumable([]string{"recognize", "a.mp3"}, p); ok {
		t.Fatal("different inputs must not match")
	}
	// Finished jobs (nothing left to do) are not offered.
	items, _ := s.Items(j.ID)
	for _, it := range items {
		it.State, it.Result = StateDone, json.RawMessage("null")
		s.SaveItem(j.ID, it)
	}
	if _, ok := s.FindResumable(cmd, p); ok {
		t.Fatal("a finished job should not be offered")
	}
	// A failure that is safe to retry (nothing was sent) makes it resumable.
	items[0].State, items[0].SafeRetry = StateFailed, true
	s.SaveItem(j.ID, items[0])
	if _, ok := s.FindResumable(cmd, p); !ok {
		t.Fatal("safe-to-retry failures should be resumable")
	}
	// A failure that may have been billed does not.
	items[0].SafeRetry = false
	s.SaveItem(j.ID, items[0])
	if _, ok := s.FindResumable(cmd, p); ok {
		t.Fatal("possibly-billed failures should not trigger a resume offer")
	}
}

func TestClaimPreventsTwoRunners(t *testing.T) {
	s := openStore(t)
	j, _ := s.Create([]string{"recognize", "a"}, files("a"), nil)
	release, err := s.Claim(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(j.ID); err == nil {
		t.Fatal("second claim should fail while the first runner is alive")
	} else {
		var oe *output.Error
		if !errors.As(err, &oe) || oe.Code != "job_running" {
			t.Fatalf("got %#v", err)
		}
	}
	got, _ := s.Get(j.ID)
	if got.Status != StatusRunning {
		t.Fatalf("status %q", got.Status)
	}
	release(StatusInterrupted)
	got, _ = s.Get(j.ID)
	if got.Status != StatusInterrupted {
		t.Fatalf("status %q", got.Status)
	}
	if _, err := s.Claim(j.ID); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
}

func TestStaleRunningJobShowsInterrupted(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	j, _ := s.Create([]string{"recognize", "a"}, files("a"), nil)
	if _, err := s.Claim(j.ID); err != nil {
		t.Fatal(err)
	}
	// The process died: no heartbeat for longer than the stale window.
	s.now = func() time.Time { return now.Add(staleAfter + time.Minute) }
	got, _ := s.Get(j.ID)
	if got.Status != StatusInterrupted {
		t.Fatalf("status %q", got.Status)
	}
	if _, err := s.Claim(j.ID); err != nil {
		t.Fatalf("a stale claim should be taken over: %v", err)
	}
}

func TestTakenOverLeaseWritesNothing(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	j, _ := s.Create([]string{"recognize", "a", "b"}, files("a", "b"), nil)
	a, err := s.ClaimLease(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := s.Items(j.ID)
	if err := a.SaveItem(items[0]); err != nil || a.Heartbeat() != nil {
		t.Fatalf("the holder writes: %v", err)
	}
	// A was suspended past the stale window and B took the job over.
	s.now = func() time.Time { return now.Add(staleAfter + time.Minute) }
	b, err := s.ClaimLease(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantTakenOver := func(err error) {
		t.Helper()
		var oe *output.Error
		if !errors.As(err, &oe) || oe.Code != ErrCodeTakenOver {
			t.Fatalf("got %#v", err)
		}
	}
	wantTakenOver(a.Heartbeat())
	inFlight := items[1]
	inFlight.State, inFlight.ErrCode = StateFailed, ErrCodeInFlight
	wantTakenOver(a.SaveItem(inFlight))
	if got, _ := s.Items(j.ID); got[1].State != StatePending {
		t.Fatalf("A's save went through: %+v", got[1])
	}
	a.Release(StatusInterrupted)
	if got, _ := s.Get(j.ID); got.Status != StatusRunning {
		t.Fatalf("A's release changed B's job: %q", got.Status)
	}
	if err := b.SaveItem(inFlight); err != nil || b.Heartbeat() != nil {
		t.Fatalf("B writes: %v", err)
	}
}

func TestDerivedStatus(t *testing.T) {
	s := openStore(t)
	j, _ := s.Create([]string{"recognize", "a", "b"}, files("a", "b"), nil)
	items, _ := s.Items(j.ID)
	items[0].State, items[0].Result = StateDone, json.RawMessage("null")
	s.SaveItem(j.ID, items[0])
	release, _ := s.Claim(j.ID)
	release(StatusStopped)
	if got, _ := s.Get(j.ID); got.Status != StatusStopped {
		t.Fatalf("stopped: %q", got.Status)
	}
	items[1].State = StateFailed
	s.SaveItem(j.ID, items[1])
	if got, _ := s.Get(j.ID); got.Status != StatusPartial {
		t.Fatalf("all items settled with a failure: %q", got.Status)
	}
	items[1].State, items[1].Result = StateDone, json.RawMessage("null")
	s.SaveItem(j.ID, items[1])
	if got, _ := s.Get(j.ID); got.Status != StatusDone {
		t.Fatalf("all done: %q", got.Status)
	}
}

func TestFinishedRunWithSafeFailuresIsPartial(t *testing.T) {
	s := openStore(t)
	j, _ := s.Create([]string{"recognize", "a", "b"}, files("a", "b"), nil)
	items, _ := s.Items(j.ID)
	for _, it := range items {
		it.State, it.SafeRetry, it.Err = StateFailed, true, "connection refused"
		s.SaveItem(j.ID, it)
	}
	release, _ := s.Claim(j.ID)
	release(StatusDone)
	got, _ := s.Get(j.ID)
	if got.Status != StatusPartial || got.Remaining != 2 {
		t.Fatalf("a run that went through every file is not interrupted: %q, %d remaining", got.Status, got.Remaining)
	}
}

func TestCleanAndDelete(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	s.now = func() time.Time { return now.Add(-48 * time.Hour) }
	old, _ := s.Create([]string{"recognize", "old"}, files("old"), nil)
	s.now = func() time.Time { return now }
	fresh, _ := s.Create([]string{"recognize", "new"}, files("new"), nil)
	n, err := s.Clean(24 * time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("clean %d %v", n, err)
	}
	if _, err := s.Get(old.ID); err == nil {
		t.Fatal("old job should be gone")
	}
	if items, _ := s.Items(old.ID); len(items) != 0 {
		t.Fatal("old items should be gone")
	}
	if err := s.Delete(fresh.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(fresh.ID); err == nil {
		t.Fatal("deleting twice should fail")
	}
}

func TestCleanKeepsRunningJobs(t *testing.T) {
	s := openStore(t)
	j, _ := s.Create([]string{"recognize", "a"}, files("a"), nil)
	if _, err := s.Claim(j.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Clean(0); n != 0 {
		t.Fatalf("a running job must not be cleaned, removed %d", n)
	}
}
