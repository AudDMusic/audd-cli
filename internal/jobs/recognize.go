package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// Hooks the API layer replaces, so a batch uses the same results cache,
// token self-healing and error wording as a single recognition. The
// defaults call the SDK directly and use no cache.
var (
	// Recognize identifies one input and returns the API's result: an
	// object or null (standard endpoint), or an array of matches
	// (enterprise). Errors may be audd-go errors or *output.Error; a hook
	// that maps SDK errors should return both (errors.Join(mapped, sdkErr))
	// so classify can still tell a connection that never opened, which is
	// safe to retry, from one that dropped after the upload.
	Recognize = RecognizeSDK

	// CacheGet returns a cached result for the input and params. It must
	// use the same key as single recognitions (the request's cache params,
	// with their defaults such as accurate_offsets=true), or batch runs
	// miss results that are already cached.
	CacheGet func(a *app.App, in media.Input, p Params) (json.RawMessage, bool)
	// CachePut stores a fresh result under the same key as CacheGet.
	CachePut func(a *app.App, in media.Input, p Params, result json.RawMessage)

	// Durations gives a local file's length for enterprise estimates
	// (ffprobe by default).
	Durations = defaultDurations

	// sleep waits for rate-limit backoff; tests replace it.
	sleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
)

var (
	toolsOnce sync.Once
	tools     media.Tools
)

func defaultDurations(path string) (time.Duration, bool) {
	toolsOnce.Do(func() { tools = media.Find() })
	return media.Duration(tools, path)
}

// RecognizeSDK is the default Recognize: one call through audd-go.
func RecognizeSDK(ctx context.Context, a *app.App, in media.Input, p Params) (json.RawMessage, error) {
	// Sent once: recognition is billed once AudD has the audio, so a
	// failure after the upload is never sent again on its own.
	c, err := a.APIClientOnce()
	if err != nil {
		return nil, err
	}
	src := in.URL
	if src == "" {
		src = in.Path
	}
	if !p.Enterprise {
		rec, err := c.RecognizeContext(ctx, src, &audd.RecognizeOptions{ReturnMetadata: p.Return})
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return json.RawMessage("null"), nil
		}
		if len(rec.RawResponse) > 0 {
			return append(json.RawMessage(nil), rec.RawResponse...), nil
		}
		return json.Marshal(rec)
	}
	matches, err := c.RecognizeEnterpriseContext(ctx, src, enterpriseOptions(p))
	if err != nil {
		return nil, err
	}
	return EnterpriseJSON(matches), nil
}

func enterpriseOptions(p Params) *audd.EnterpriseOptions {
	o := &audd.EnterpriseOptions{Limit: p.Limit}
	intOpt := func(name string) *int {
		if n, ok := p.optInt(name); ok {
			return &n
		}
		return nil
	}
	boolOpt := func(name string) *bool {
		if b, err := strconv.ParseBool(p.Options[name]); err == nil {
			return &b
		}
		return nil
	}
	o.Skip, o.Every, o.SkipFirstSeconds = intOpt("skip"), intOpt("every"), intOpt("skip_first_seconds")
	o.UseTimecode, o.AccurateOffsets = boolOpt("use_timecode"), boolOpt("accurate_offsets")
	for k, v := range p.Options {
		switch k {
		case "skip", "every", "skip_first_seconds", "use_timecode", "accurate_offsets":
		default:
			if o.ExtraParameters == nil {
				o.ExtraParameters = map[string]string{}
			}
			o.ExtraParameters[k] = v
		}
	}
	return o
}

// EnterpriseJSON renders enterprise matches as the API sent them, plus
// start_seconds and end_seconds (the match's place in the whole file).
func EnterpriseJSON(matches []audd.EnterpriseMatch) json.RawMessage {
	out := make([]map[string]json.RawMessage, 0, len(matches))
	for _, m := range matches {
		obj := map[string]json.RawMessage{}
		if len(m.RawResponse) == 0 || json.Unmarshal(m.RawResponse, &obj) != nil {
			b, _ := json.Marshal(m)
			_ = json.Unmarshal(b, &obj)
		}
		if m.StartSeconds != nil {
			obj["start_seconds"] = num(*m.StartSeconds)
		}
		if m.EndSeconds != nil {
			obj["end_seconds"] = num(*m.EndSeconds)
		}
		out = append(out, obj)
	}
	b, _ := json.Marshal(out)
	return b
}

func num(f float64) json.RawMessage {
	return json.RawMessage(strconv.FormatFloat(f, 'f', -1, 64))
}

// failure is a classified recognition error.
type failure struct {
	err       *output.Error
	safe      bool // nothing reached the API; resume retries it
	fatal     bool // the whole batch cannot continue (auth, quota, plan)
	rateLimit bool
}

// classify decides how a recognition error affects the item and the batch.
// Anything that may have reached the API after the upload started is
// recorded as a failure that is never retried automatically.
//
// It reads the SDK's errors (which tell a connection that never opened from
// one that dropped after the upload) and the CLI's *output.Error codes. A
// Recognize hook that maps SDK errors should return both, for example with
// errors.Join(mapped, sdkErr), so safe retries keep working.
func classify(err error) failure {
	var oe *output.Error
	mapped := errors.As(err, &oe)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (mapped && oe.Code == "interrupted") {
		return failure{err: &output.Error{Code: "interrupted", Exit: output.ExitNetwork,
			Message: "stopped while waiting for the response; the request may have been counted"}}
	}
	var connErr *audd.AudDConnectionError
	if errors.As(err, &connErr) {
		e := &output.Error{Code: "network", Message: err.Error(), Exit: output.ExitNetwork}
		if mapped {
			c := *oe
			e = &c
		}
		if api.PreUpload(connErr.Cause) {
			e.Retryable = true
			return failure{err: e, safe: true}
		}
		e.Retryable = false
		e.Message += " (the upload may have reached AudD, so it is not retried automatically)"
		return failure{err: e}
	}
	var apiErr *audd.AudDAPIError
	if errors.As(err, &apiErr) {
		e := &output.Error{Code: "api_error", APICode: apiErr.ErrorCode, Message: apiErr.Message, Exit: output.ExitUnexpected}
		f := failure{err: e}
		switch {
		case apiErr.HTTPStatus == http.StatusTooManyRequests:
			// Throttled before any work: back off and try again.
			e.Code, e.Exit, e.Retryable, f.rateLimit = "rate_limited", output.ExitQuota, true, true
			if e.Message == "" {
				e.Message = "AudD is receiving too many requests from this token right now"
			}
		case errors.Is(err, audd.ErrRateLimit):
			// Error 611 is a daily limit: waiting seconds does not help, and
			// the next files would hit it too.
			e.Code, e.Exit, f.fatal = "rate_limited", output.ExitQuota, true
		case errors.Is(err, audd.ErrAuthentication):
			e.Code, e.Exit, e.Hint, f.fatal = "token_rejected", output.ExitAuth, "audd login, or set a valid token with audd config set token -", true
		case errors.Is(err, audd.ErrQuota):
			e.Code, e.Exit, e.Hint, f.fatal = "quota_exceeded", output.ExitQuota, "audd usage; get more requests at https://dashboard.audd.io", true
		case errors.Is(err, audd.ErrSubscription):
			e.Code, e.Exit, e.Hint, f.fatal = "feature_unavailable", output.ExitQuota, "check your plan at https://dashboard.audd.io", true
		case errors.Is(err, audd.ErrBlocked), errors.Is(err, audd.ErrNeedsUpdate):
			e.Code, e.Exit, f.fatal = "blocked", output.ExitQuota, true
		case errors.Is(err, audd.ErrInvalidAudio):
			e.Code, e.Exit = "invalid_audio", output.ExitUsage
		case errors.Is(err, audd.ErrInvalidRequest), errors.Is(err, audd.ErrNotReleased):
			e.Code, e.Exit = "invalid_request", output.ExitUsage
		default:
			e.Code, e.Exit = "server", output.ExitNetwork
		}
		if mapped {
			// Keep the mapped error's wording and hint, with the batch's view
			// of whether it is worth retrying.
			c := *oe
			c.Retryable = f.rateLimit
			f.err = &c
		}
		return f
	}
	if mapped {
		f := failure{err: oe}
		switch {
		case oe.Code == "rate_limited" && oe.APICode == 611:
			f.fatal = true
		case oe.Code == "rate_limited":
			f.rateLimit = true
		case oe.Exit == output.ExitAuth || oe.Exit == output.ExitQuota:
			f.fatal = true
		}
		return f
	}
	if errors.Is(err, audd.ErrSerialization) {
		return failure{err: &output.Error{Code: "server", Message: err.Error(), Exit: output.ExitNetwork}}
	}
	return failure{err: output.AsError(err)}
}

// ErrorFor maps a recognition error to the CLI's error (exit code, stable
// code, and hint).
func ErrorFor(err error) *output.Error {
	if err == nil {
		return nil
	}
	return classify(err).err
}
