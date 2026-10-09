package jobs

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/safety"
)

// chunk is the enterprise billing unit: one request per 12 seconds of audio.
const chunk = 12 * time.Second

// pricePerRequest is the pay-as-you-go price: $5 per 1,000 requests.
const pricePerRequest = 5.0 / 1000

// assumedBytesPerSecond turns a file size into a duration when ffprobe is
// not available (128 kbit/s).
const assumedBytesPerSecond = 16000

// Plan is what a batch run is expected to spend.
// Endpoint is printed beside the plan, not inside it, the same as for a
// single input's dry run. UnknownLengthFiles counts enterprise inputs of
// unknown length (URLs) sent without --limit: nothing caps them, so the
// request count and cost are unknown (null in JSON, with "unbounded": true).
type Plan struct {
	Endpoint           string  `json:"-"`
	Files              int     `json:"files"`
	CachedFiles        int     `json:"cached_files"`
	Requests           int     `json:"requests"`
	Approximate        bool    `json:"approximate"`
	CostUSD            float64 `json:"cost_usd"`
	RemainingAllowance *int    `json:"remaining_allowance,omitempty"`
	UnknownLengthFiles int     `json:"unknown_length_files,omitempty"`
	// TooLargeFiles counts local files over the standard endpoint's 10 MB
	// limit. They are not sent and not counted in Requests.
	TooLargeFiles int `json:"too_large_files,omitempty"`
	// MaxRequests is the --max-requests ceiling when it stops the run
	// before the plan is done (the run spends at most this many).
	MaxRequests     int    `json:"max_requests,omitempty"`
	MaxRequestsName string `json:"-"`
}

// Capped reports whether --max-requests stops the run before the plan is
// done.
func (p Plan) Capped() bool {
	return p.MaxRequests > 0 && (p.Unbounded() || p.Requests > p.MaxRequests)
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
		TooLargeFiles      int      `json:"too_large_files,omitempty"`
		MaxRequests        int      `json:"max_requests,omitempty"`
	}{Files: p.Files, CachedFiles: p.CachedFiles, Approximate: p.Approximate,
		RemainingAllowance: p.RemainingAllowance, UnknownLengthFiles: p.UnknownLengthFiles, TooLargeFiles: p.TooLargeFiles,
		MaxRequests: p.MaxRequests}
	if p.Unbounded() {
		doc.Unbounded = true
	} else {
		doc.Requests, doc.CostUSD = &p.Requests, &p.CostUSD
	}
	return json.Marshal(doc)
}

// itemRequests estimates the requests one input costs. approx is true when
// the duration had to be guessed; sized when a local file's duration was
// guessed from its size; unbounded when the length is unknown and no limit
// caps it (n is then 1, a placeholder).
func itemRequests(in media.Input, p Params) (n int, approx, sized, unbounded bool) {
	if !p.Enterprise {
		return 1, false, false, false
	}
	var d time.Duration
	known := false
	if in.URL == "" && in.Path != "" {
		if d, known = Durations(in.Path); !known {
			if fi, err := os.Stat(in.Path); err == nil {
				d = time.Duration(fi.Size()/assumedBytesPerSecond) * time.Second
				known, approx, sized = true, true, true
			}
		}
	}
	if !known {
		// A URL, or a file that cannot be read: count the limit when there
		// is one, else one request.
		if p.Limit != nil && *p.Limit > 0 {
			return *p.Limit, true, false, false
		}
		return 1, true, false, true
	}
	if s, ok := p.optInt("skip_first_seconds"); ok && s > 0 {
		d -= time.Duration(s) * time.Second
	}
	n = int(math.Ceil(float64(d) / float64(chunk)))
	if n < 1 {
		n = 1
	}
	// The same skip/every arithmetic as a single file's estimate.
	n = safety.ScannedChunks(n, p.optIntPtr("skip"), p.optIntPtr("every"))
	if p.Limit != nil && *p.Limit < n {
		n = *p.Limit
	}
	return n, approx, sized, false
}

// remainingAllowance asks the account (when logged in) how many requests
// are left this cycle. It gives up quietly and quickly.
func remainingAllowance(a *app.App) *int {
	if a.Account == nil {
		return nil
	}
	b, err := a.Account()
	if err != nil || b == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	u, err := b.Usage(ctx, 1)
	if err != nil || u == nil || (u.Allowance == 0 && u.Remaining == 0) {
		return nil
	}
	n := u.Remaining
	return &n
}

// Line is the plan as one sentence, worded the same as a single input's
// plan (see safety.PlanText).
func (p Plan) Line() string {
	return safety.PlanText{Files: p.Files, CachedFiles: p.CachedFiles, Requests: p.Requests, CostUSD: p.CostUSD,
		Approximate: p.Approximate, UnknownLengthFiles: p.UnknownLengthFiles, TooLargeFiles: p.TooLargeFiles,
		Enterprise: p.Endpoint == "enterprise", RemainingAllowance: p.RemainingAllowance,
		MaxRequests: p.MaxRequests, MaxRequestsName: p.MaxRequestsName}.String()
}

// RequestsUsed describes the requests a run or job used, for people:
// "12 requests used", "up to 500 requests used (file lengths were not
// known)", or "12 requests used, plus up to 500 for files of unknown length".
// reserved is the limit sent with calls for files of unknown length (the
// most they can have used); unknown counts such files sent without a limit.
func RequestsUsed(spent, reserved, unknown int) string {
	if spent == 0 && reserved > 0 && unknown == 0 {
		return "up to " + output.Plural(reserved, "request") + " used (file lengths were not known)"
	}
	s := output.Plural(spent, "request") + " used"
	if reserved > 0 {
		s += ", plus up to " + output.Thousands(reserved) + " for files of unknown length"
	}
	if unknown > 0 {
		s += ", plus an unknown number for " + output.Plural(unknown, "file") + " of unknown length sent without --limit"
	}
	return s
}
