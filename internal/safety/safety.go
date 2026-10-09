// Package safety keeps billable work bounded: it requires explicit limits,
// estimates what a run will cost before it starts, and enforces the
// --max-requests ceiling.
package safety

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cache"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// ChunkSeconds is the length of one enterprise chunk: the enterprise
// endpoint bills one request per 12 seconds of audio.
const ChunkSeconds = 12

// PricePerRequestUSD is the pay-as-you-go price used for estimates ($5 per
// 1,000 requests).
const PricePerRequestUSD = 0.005

// bytesPerSecond guesses audio length from file size when ffprobe is not
// available: 128 kbit/s, a common bitrate for compressed music.
const bytesPerSecond = 16000

// Plan is what a run is expected to spend.
//
// UnknownLengthFiles counts enterprise inputs of unknown length (URLs) sent
// without --limit: nothing caps what they can use, so the plan's request
// count and cost are unknown (null in JSON, with "unbounded": true).
type Plan struct {
	Files              int     `json:"files"`
	CachedFiles        int     `json:"cached_files"`
	Requests           int     `json:"requests"`
	Approximate        bool    `json:"approximate"`
	CostUSD            float64 `json:"cost_usd"`
	RemainingAllowance *int    `json:"remaining_allowance,omitempty"`
	UnknownLengthFiles int     `json:"unknown_length_files,omitempty"`
	// SizeEstimated is set when a local file's length was guessed from its
	// size (ffprobe would give the exact length).
	SizeEstimated bool `json:"-"`
	// Enterprise is set for a plan on the enterprise endpoint.
	Enterprise bool `json:"-"`
	// MaxRequests is the --max-requests ceiling when it stops the run
	// before the plan is done (see Cap).
	MaxRequests     int    `json:"-"`
	MaxRequestsName string `json:"-"`
}

// Cap applies a --max-requests ceiling (max > 0) named name: when it stops
// the run before the plan is done, the plan says so.
func (p *Plan) Cap(max int, name string) {
	if max > 0 && (p.Unbounded() || p.Requests > max) {
		p.MaxRequests, p.MaxRequestsName = max, name
	}
}

// Unbounded reports whether nothing caps what the plan can use.
func (p Plan) Unbounded() bool { return p.UnknownLengthFiles > 0 }

// MarshalJSON writes requests and cost_usd as null, with "unbounded": true,
// when the plan is unbounded.
func (p Plan) MarshalJSON() ([]byte, error) {
	doc := struct {
		Files              int      `json:"files"`
		CachedFiles        int      `json:"cached_files"`
		Requests           *int     `json:"requests"`
		Approximate        bool     `json:"approximate"`
		CostUSD            *float64 `json:"cost_usd"`
		RemainingAllowance *int     `json:"remaining_allowance,omitempty"`
		Unbounded          bool     `json:"unbounded,omitempty"`
		UnknownLengthFiles int      `json:"unknown_length_files,omitempty"`
		MaxRequests        int      `json:"max_requests,omitempty"`
	}{Files: p.Files, CachedFiles: p.CachedFiles, Approximate: p.Approximate,
		RemainingAllowance: p.RemainingAllowance, UnknownLengthFiles: p.UnknownLengthFiles, MaxRequests: p.MaxRequests}
	if p.Unbounded() {
		doc.Unbounded = true
	} else {
		doc.Requests, doc.CostUSD = &p.Requests, &p.CostUSD
	}
	return json.Marshal(doc)
}

// Estimate counts files and requests. Cached files cost nothing. Standard
// recognition is one request per file. Enterprise is one request per 12 s
// chunk (after skip/every), capped by limit; durations come from durations
// (ffprobe) when it knows the file, else from the file size, which makes the
// plan approximate. params are the request parameters used in cache keys
// (see cache.KeyForInput); leave them out when there are none.
func Estimate(files []media.Input, enterprise bool, limit *int, skip, every *int, c *cache.Cache, durations func(path string) (time.Duration, bool), params ...map[string]string) Plan {
	p := Plan{Enterprise: enterprise}
	var kp map[string]string
	if len(params) > 0 {
		kp = params[0]
	}
	endpoint := cache.EndpointStandard
	if enterprise {
		endpoint = cache.EndpointEnterprise
	}
	for _, in := range files {
		p.Files++
		if c != nil {
			if k, err := cache.KeyForInput(in, endpoint, kp); err == nil {
				if _, ok := c.Get(k); ok {
					p.CachedFiles++
					continue
				}
			}
		}
		if !enterprise {
			p.Requests++
			continue
		}
		n, approx := chunksFor(in, durations)
		if approx {
			p.Approximate = true
		}
		switch {
		case n < 0 && limit == nil:
			// Unknown length and no limit: nothing caps it.
			p.UnknownLengthFiles++
			continue
		case n < 0: // unknown length (a URL): count the limit
			n = *limit
		case approx:
			p.SizeEstimated = true
		}
		n = ScannedChunks(n, skip, every)
		if limit != nil && n > *limit {
			n = *limit
		}
		p.Requests += n
	}
	p.CostUSD = math.Round(float64(p.Requests)*PricePerRequestUSD*1000) / 1000
	return p
}

// chunksFor returns the number of 12 s chunks in an input, or -1 when it
// cannot tell. approx is true when the count is a guess.
func chunksFor(in media.Input, durations func(string) (time.Duration, bool)) (n int, approx bool) {
	if in.URL != "" {
		return -1, true
	}
	if durations != nil {
		if d, ok := durations(in.Path); ok {
			return Chunks(d), false
		}
	}
	fi, err := os.Stat(in.Path)
	if err != nil {
		return 1, true
	}
	return Chunks(time.Duration(fi.Size()/bytesPerSecond) * time.Second), true
}

// Chunks is the number of 12-second chunks in d (at least 1).
func Chunks(d time.Duration) int {
	n := int(math.Ceil(d.Seconds() / ChunkSeconds))
	if n < 1 {
		n = 1
	}
	return n
}

// ScannedChunks applies the enterprise skip/every parameters to n chunks:
// scan `every` chunks in a row, then skip `skip` chunks.
func ScannedChunks(n int, skip, every *int) int {
	s, e := 0, 1
	if skip != nil && *skip > 0 {
		s = *skip
	}
	if every != nil && *every > 0 {
		e = *every
	}
	if s == 0 {
		return n
	}
	cycle := s + e
	out := (n / cycle) * e
	if r := n % cycle; r < e {
		out += r
	} else {
		out += e
	}
	return out
}

// Render prints the plan on stderr, for example "Plan: 142 files, ≈ 142
// requests (≈ $0.71 at the pay-as-you-go price of $5 per 1,000 requests);
// 30 already cached (free)."
func (p Plan) Render(out *output.Printer) {
	out.Info("Plan: %s.", p.String())
}

// String is the one-line plan summary (see PlanText).
func (p Plan) String() string {
	return PlanText{Files: p.Files, CachedFiles: p.CachedFiles, Requests: p.Requests, CostUSD: p.CostUSD,
		Approximate: p.Approximate, SizeEstimated: p.SizeEstimated, UnknownLengthFiles: p.UnknownLengthFiles,
		Enterprise: p.Enterprise, RemainingAllowance: p.RemainingAllowance,
		MaxRequests: p.MaxRequests, MaxRequestsName: p.MaxRequestsName}.String()
}

// PlanText is what a plan says, for one input and for a batch alike.
type PlanText struct {
	Files, CachedFiles, Requests int
	CostUSD                      float64
	Approximate                  bool
	// SizeEstimated: some lengths were guessed from file sizes.
	SizeEstimated      bool
	UnknownLengthFiles int
	TooLargeFiles      int
	Enterprise         bool
	RemainingAllowance *int
	// MaxRequests is the --max-requests ceiling (0: none), and
	// MaxRequestsName how to name it ("--max-requests 5").
	MaxRequests     int
	MaxRequestsName string
}

// Capped reports whether the ceiling stops the run before the plan is done.
func (p PlanText) Capped() bool {
	return p.MaxRequests > 0 && (p.UnknownLengthFiles > 0 || p.Requests > p.MaxRequests)
}

// String is the plan as one sentence without the "Plan: " lead or the
// final period: "142 files, 142 requests ($0.71 at the pay-as-you-go price
// of $5 per 1,000 requests); 30 already cached (free)". "≈" marks an
// approximate plan.
func (p PlanText) String() string {
	var b strings.Builder
	approx := ""
	if p.Approximate {
		approx = "≈ "
	}
	name := p.MaxRequestsName
	if name == "" {
		name = fmt.Sprintf("--max-requests %d", p.MaxRequests)
	}
	switch {
	case p.Capped() && p.UnknownLengthFiles > 0:
		fmt.Fprintf(&b, "%s, stops after %s (%s), at most $%s at the pay-as-you-go price of $5 per 1,000 requests; without it nothing would cap the scan, since %s no known length and no --limit",
			plural(p.Files, "file", "files"), plural(p.MaxRequests, "request", "requests"), name,
			Dollars(cost(p.MaxRequests)), plural(p.UnknownLengthFiles, "URL has", "URLs have"))
	case p.Capped():
		fmt.Fprintf(&b, "%s, stops after %d of %s%s (%s), at most $%s at the pay-as-you-go price of $5 per 1,000 requests",
			plural(p.Files, "file", "files"), p.MaxRequests, approx, plural(p.Requests, "request", "requests"), name, Dollars(cost(p.MaxRequests)))
	case p.UnknownLengthFiles > 0:
		fmt.Fprintf(&b, "%s, requests unknown: %s no known length and no --limit, so the scan is not capped (each 12-second chunk is one request, 300 per hour of audio)",
			plural(p.Files, "file", "files"), plural(p.UnknownLengthFiles, "URL has", "URLs have"))
	default:
		fmt.Fprintf(&b, "%s, %s%s (%s$%s at the pay-as-you-go price of $5 per 1,000 requests)",
			plural(p.Files, "file", "files"), approx, plural(p.Requests, "request", "requests"), approx, Dollars(p.CostUSD))
	}
	if p.Enterprise {
		b.WriteString(" on the enterprise endpoint")
	}
	if p.CachedFiles > 0 {
		fmt.Fprintf(&b, "; %d already cached (free)", p.CachedFiles)
	}
	if p.TooLargeFiles > 0 {
		fmt.Fprintf(&b, "; %s over 10 MB will not be sent, since the standard endpoint does not take them (use --enterprise for those)",
			output.Plural(p.TooLargeFiles, "file"))
	}
	switch {
	case p.SizeEstimated:
		b.WriteString("; approximate, since some lengths are estimated from file sizes (install ffmpeg for exact counts)")
	case p.Approximate:
		b.WriteString("; approximate, since some lengths are unknown or estimated from file sizes")
	}
	if p.RemainingAllowance != nil {
		fmt.Fprintf(&b, "; %s left this cycle", output.Plural(*p.RemainingAllowance, "request"))
	}
	return b.String()
}

func cost(requests int) float64 {
	return math.Round(float64(requests)*PricePerRequestUSD*1000) / 1000
}

// Dollars shows cents, or tenths of a cent when that is all there is.
func Dollars(v float64) string {
	if math.Abs(v*100-math.Round(v*100)) < 1e-9 {
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.3f", v)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// FillAllowance adds the account's remaining requests to the plan when the
// person is logged in. It never fails: without a login it does nothing.
func FillAllowance(ctx context.Context, a *app.App, p *Plan) {
	if a == nil || a.Account == nil {
		return
	}
	acct, err := a.Account()
	if err != nil || acct == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u, err := acct.Usage(ctx, 1)
	// Both zero means the account reported no allowance at all.
	if err != nil || u == nil || (u.Allowance == 0 && u.Remaining == 0) {
		return
	}
	r := u.Remaining
	p.RemainingAllowance = &r
}

// RequireLimit reads --limit for enterprise runs: a positive number of
// chunks per file, or "none". Missing → limit_required (exit 6). For
// standard runs the flag is not used and nil is returned.
func RequireLimit(enterprise bool, limitFlag string) (*int, error) {
	if !enterprise {
		if strings.TrimSpace(limitFlag) != "" {
			return nil, output.Errf(output.ExitUsage, "invalid_argument", "add --enterprise, or drop --limit", "--limit applies to --enterprise recognition only")
		}
		return nil, nil
	}
	return parseBound(limitFlag, "--limit", &output.Error{
		Code:    "limit_required",
		Message: "--enterprise needs --limit: the enterprise endpoint bills one request per 12 seconds of audio",
		Hint:    "add --limit N (at most N 12-second chunks per file) or --limit none; preview the cost with --dry-run",
		Exit:    output.ExitSafety,
		DocsURL: "https://docs.audd.io/enterprise",
	})
}

// RequireMaxFiles reads --max-files for batch runs: a positive number or
// "none". Missing → limit_required (exit 6). Single inputs return nil.
func RequireMaxFiles(batch bool, flag string) (*int, error) {
	if !batch {
		if strings.TrimSpace(flag) != "" {
			if _, err := parseBound(flag, "--max-files", nil); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	return parseBound(flag, "--max-files", &output.Error{
		Code:    "limit_required",
		Message: "recognizing many files needs --max-files, so a large folder is never sent by accident",
		Hint:    "add --max-files N or --max-files none; preview the plan with --dry-run",
		Exit:    output.ExitSafety,
	})
}

func parseBound(flag, name string, missing *output.Error) (*int, error) {
	v := strings.ToLower(strings.TrimSpace(flag))
	switch v {
	case "":
		if missing == nil {
			return nil, nil
		}
		return nil, missing
	case "none":
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return nil, output.Errf(output.ExitUsage, "invalid_argument", name+" N (a positive number) or "+name+" none", "invalid %s value %q", name, flag)
	}
	return &n, nil
}

// Budget enforces --max-requests across a run. It is safe for concurrent use.
type Budget struct {
	mu    sync.Mutex
	max   int
	spent int
	name  string // where the ceiling came from
	hint  string
}

// NewBudget returns a budget of max requests; 0 means no ceiling.
func NewBudget(max int) *Budget {
	return &Budget{max: max, name: fmt.Sprintf("--max-requests %d", max),
		hint: "raise --max-requests (or audd config set max_requests N) and run again"}
}

// BudgetFor returns the budget of the run's --max-requests or
// max_requests setting, whose messages name the one in use.
func BudgetFor(a *app.App) *Budget {
	b := NewBudget(a.Flags.MaxRequests)
	if a.Flags.MaxRequestsFromConfig {
		b.name, b.hint = a.Flags.MaxRequestsName(), a.Flags.MaxRequestsHint()
	}
	return b
}

// Take reserves n requests, or returns max_requests_reached (exit 6) when
// that would go over the ceiling (and reserves nothing).
func (b *Budget) Take(n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max > 0 && b.spent+n > b.max {
		return &output.Error{
			Code:    "max_requests_reached",
			Message: fmt.Sprintf("stopped at the ceiling set by %s (%d used)", b.name, b.spent),
			Hint:    b.hint,
			Exit:    output.ExitSafety,
		}
	}
	b.spent += n
	return nil
}

// Refund returns n reserved requests that were not spent (for example a
// cache hit discovered after reserving).
func (b *Budget) Refund(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent -= n
	if b.spent < 0 {
		b.spent = 0
	}
}

// Spent is the number of requests taken so far.
func (b *Budget) Spent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// Remaining is how many requests are left, or -1 without a ceiling.
func (b *Budget) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 {
		return -1
	}
	return b.max - b.spent
}
