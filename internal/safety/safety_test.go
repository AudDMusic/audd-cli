package safety

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/cache"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func intp(n int) *int { return &n }

func exitOf(t *testing.T, err error) int {
	t.Helper()
	var oe *output.Error
	if !errors.As(err, &oe) {
		t.Fatalf("want *output.Error, got %v", err)
	}
	return oe.Exit
}

func TestRequireLimit(t *testing.T) {
	if l, err := RequireLimit(false, ""); l != nil || err != nil {
		t.Fatal("standard runs need no limit")
	}
	if _, err := RequireLimit(false, "5"); exitOf(t, err) != output.ExitUsage {
		t.Fatal("--limit without --enterprise is a usage error")
	}
	_, err := RequireLimit(true, "")
	if exitOf(t, err) != output.ExitSafety || !strings.Contains(err.(*output.Error).Hint, "--dry-run") {
		t.Fatalf("missing limit: %v", err)
	}
	if err.(*output.Error).Code != "limit_required" {
		t.Fatal(err.(*output.Error).Code)
	}
	if l, err := RequireLimit(true, "none"); l != nil || err != nil {
		t.Fatal("none means unbounded")
	}
	if l, err := RequireLimit(true, "20"); err != nil || *l != 20 {
		t.Fatal("20")
	}
	for _, bad := range []string{"0", "-3", "lots"} {
		if _, err := RequireLimit(true, bad); exitOf(t, err) != output.ExitUsage {
			t.Fatalf("%s should be a usage error", bad)
		}
	}
}

func TestRequireMaxFiles(t *testing.T) {
	if _, err := RequireMaxFiles(true, ""); exitOf(t, err) != output.ExitSafety {
		t.Fatal("batch needs --max-files")
	}
	if l, err := RequireMaxFiles(true, "None"); l != nil || err != nil {
		t.Fatal("none")
	}
	if l, err := RequireMaxFiles(true, "50"); err != nil || *l != 50 {
		t.Fatal("50")
	}
	if l, err := RequireMaxFiles(false, ""); l != nil || err != nil {
		t.Fatal("single input")
	}
}

func TestBudget(t *testing.T) {
	b := NewBudget(5)
	if err := b.Take(3); err != nil {
		t.Fatal(err)
	}
	if err := b.Take(3); exitOf(t, err) != output.ExitSafety {
		t.Fatal("over the ceiling")
	}
	if b.Spent() != 3 || b.Remaining() != 2 {
		t.Fatalf("spent %d remaining %d", b.Spent(), b.Remaining())
	}
	b.Refund(1)
	if b.Remaining() != 3 {
		t.Fatal("refund")
	}
	// Concurrent takes never exceed the ceiling.
	b = NewBudget(10)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Take(1) == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 10 {
		t.Fatalf("%d takes succeeded", ok)
	}
	if NewBudget(0).Take(1000) != nil || NewBudget(0).Remaining() != -1 {
		t.Fatal("0 is unlimited")
	}
}

func TestScannedChunks(t *testing.T) {
	tests := []struct {
		n, skip, every, want int
	}{
		{10, 0, 0, 10},
		{10, 4, 1, 2},  // scan 1, skip 4: chunks 0 and 5
		{11, 4, 1, 3},  // and 10
		{10, 9, 1, 1},  // one per 120 s
		{10, 1, 2, 7},  // 2 on, 1 off: 0,1,3,4,6,7,9
		{1, 4, 1, 1},   // short file
		{12, 2, 0, 4},  // every defaults to 1
		{12, 0, 3, 12}, // no skip, everything
	}
	for _, tt := range tests {
		var s, e *int
		if tt.skip > 0 {
			s = intp(tt.skip)
		}
		if tt.every > 0 {
			e = intp(tt.every)
		}
		if got := ScannedChunks(tt.n, s, e); got != tt.want {
			t.Errorf("ScannedChunks(%d, skip %d, every %d) = %d, want %d", tt.n, tt.skip, tt.every, got, tt.want)
		}
	}
}

func TestEstimate(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, size int) media.Input {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		return media.Input{Path: p}
	}
	short := mk("short.mp3", 1000)
	long := mk("long.mp3", 2000)
	unknown := mk("unknown.mp3", 16000*60) // 60 s by size
	url := media.Input{URL: "https://example.com/show.mp3"}
	durations := func(p string) (time.Duration, bool) {
		switch filepath.Base(p) {
		case "short.mp3":
			return 10 * time.Second, true
		case "long.mp3":
			return 125 * time.Second, true // 11 chunks
		}
		return 0, false
	}

	p := Estimate([]media.Input{short, long, url}, false, nil, nil, nil, nil, durations)
	if p.Files != 3 || p.Requests != 3 || p.Approximate || p.CostUSD != 0.015 {
		t.Fatalf("standard: %+v", p)
	}

	p = Estimate([]media.Input{short, long}, true, nil, nil, nil, nil, durations)
	if p.Requests != 12 || p.Approximate {
		t.Fatalf("enterprise exact: %+v", p)
	}
	p = Estimate([]media.Input{short, long}, true, intp(5), nil, nil, nil, durations)
	if p.Requests != 6 {
		t.Fatalf("enterprise limit 5: %+v", p)
	}
	p = Estimate([]media.Input{long}, true, nil, intp(4), intp(1), nil, durations)
	if p.Requests != 3 {
		t.Fatalf("enterprise skip 4: %+v", p)
	}
	p = Estimate([]media.Input{unknown}, true, nil, nil, nil, nil, durations)
	if p.Requests != 5 || !p.Approximate {
		t.Fatalf("size-based: %+v", p)
	}
	p = Estimate([]media.Input{url}, true, intp(7), nil, nil, nil, nil)
	if p.Requests != 7 || !p.Approximate {
		t.Fatalf("url with limit: %+v", p)
	}
	if strings.Contains(p.String(), "ffprobe") {
		t.Fatalf("no ffprobe hint for a URL: %s", p.String())
	}
	// A URL of unknown length without a limit is not capped: the request
	// count and cost are unknown, never "1 request".
	p = Estimate([]media.Input{url}, true, nil, nil, nil, nil, nil)
	if !p.Unbounded() || p.UnknownLengthFiles != 1 {
		t.Fatalf("url without limit: %+v", p)
	}
	if s := p.String(); !strings.Contains(s, "requests unknown") || strings.Contains(s, "1 request") || strings.Contains(s, "ffprobe") {
		t.Fatalf("url without limit: %q", s)
	}
	b, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if v, ok := m["requests"]; !ok || v != nil || m["cost_usd"] != nil || m["unbounded"] != true {
		t.Fatalf("url without limit JSON: %s", b)
	}
	b, _ = json.Marshal(Estimate([]media.Input{short}, true, nil, nil, nil, nil, durations))
	if !strings.Contains(string(b), `"requests":1`) || strings.Contains(string(b), "unbounded") {
		t.Fatalf("bounded JSON: %s", b)
	}

	c, err := cache.OpenAt(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	k, _ := cache.KeyForInput(long, cache.EndpointEnterprise, map[string]string{"limit": "5"})
	if err := c.Put(k, false, json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	p = Estimate([]media.Input{short, long}, true, intp(5), nil, nil, c, durations, map[string]string{"limit": "5"})
	if p.CachedFiles != 1 || p.Requests != 1 {
		t.Fatalf("cached: %+v", p)
	}
	if s := p.String(); s != "2 files, 1 request ($0.005 at the pay-as-you-go price of $5 per 1,000 requests) on the enterprise endpoint; 1 already cached (free)" {
		t.Fatalf("render: %q", s)
	}
}

func TestRenderApproximate(t *testing.T) {
	left := 900
	p := Plan{Files: 142, CachedFiles: 30, Requests: 142, CostUSD: 0.71, Approximate: true, SizeEstimated: true, RemainingAllowance: &left}
	var errb strings.Builder
	out := output.NewPrinter(io.Discard, &errb, output.PrinterOptions{})
	p.Render(out)
	// The same wording as a batch's plan (jobs.Plan.Line).
	want := "Plan: 142 files, ≈ 142 requests (≈ $0.71 at the pay-as-you-go price of $5 per 1,000 requests); 30 already cached (free); approximate, since some lengths are estimated from file sizes (install ffmpeg for exact counts); 900 requests left this cycle.\n"
	if errb.String() != want {
		t.Fatalf("got %q", errb.String())
	}
}

func TestPlanOneRequestLeft(t *testing.T) {
	left := 1
	p := Plan{Files: 1, Requests: 1, CostUSD: 0.005, RemainingAllowance: &left}
	if got := p.String(); got != "1 file, 1 request ($0.005 at the pay-as-you-go price of $5 per 1,000 requests); 1 request left this cycle" {
		t.Fatalf("got %q", got)
	}
}

func TestPlanTextWithMaxRequests(t *testing.T) {
	got := PlanText{Files: 3, Requests: 12, CostUSD: 0.06, Enterprise: true, MaxRequests: 5}.String()
	if !strings.HasPrefix(got, "3 files, stops after 5 of 12 requests (--max-requests 5), at most $0.025 at the pay-as-you-go price") {
		t.Fatalf("capped: %s", got)
	}
	got = PlanText{Files: 1, UnknownLengthFiles: 1, MaxRequests: 5, MaxRequestsName: "the max_requests setting (5)"}.String()
	if !strings.HasPrefix(got, "1 file, stops after 5 requests (the max_requests setting (5)), at most $0.025") {
		t.Fatalf("unbounded and capped: %s", got)
	}
	if got := (PlanText{Files: 1, Requests: 4, CostUSD: 0.02, MaxRequests: 5}).String(); strings.Contains(got, "stops") {
		t.Fatalf("under the ceiling: %s", got)
	}
}
