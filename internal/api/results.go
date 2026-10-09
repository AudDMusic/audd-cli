package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cache"
	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// StandardTimeout bounds one standard recognition request.
const StandardTimeout = 60 * time.Second

// Providers are the metadata sources --return accepts.
var Providers = []string{"apple_music", "spotify", "deezer", "musicbrainz"}

// EnterpriseParams are the enterprise passthrough parameters (wire names)
// accepted in Request.EnterpriseOpts.
var EnterpriseParams = []string{"skip", "every", "skip_first_seconds", "use_timecode", "accurate_offsets"}

// NormalizeReturn validates and canonicalizes a --return list
// ("spotify, apple_music" → "apple_music,spotify").
func NormalizeReturn(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		ok := false
		for _, v := range Providers {
			if p == v {
				ok = true
			}
		}
		if !ok {
			return "", output.Errf(output.ExitUsage, "invalid_argument", "--return accepts "+strings.Join(Providers, ","), "unknown metadata source %q", p)
		}
		seen[p] = true
	}
	var out []string
	for _, v := range Providers {
		if seen[v] {
			out = append(out, v)
		}
	}
	return strings.Join(out, ","), nil
}

// Request describes one recognition of one input.
type Request struct {
	Enterprise     bool
	Return         string            // standard only; normalized provider list
	Limit          *int              // enterprise chunks; nil = no limit
	EnterpriseOpts map[string]string // EnterpriseParams, wire names and values
	// Offset is added to enterprise match positions: the start of a clip
	// (--at) within the original file, in seconds.
	Offset float64
}

// Endpoint is the cache endpoint name.
func (r Request) Endpoint() string {
	if r.Enterprise {
		return cache.EndpointEnterprise
	}
	return cache.EndpointStandard
}

// CacheParams are the parameters that make a cached result reusable.
func (r Request) CacheParams() map[string]string {
	p := map[string]string{}
	if !r.Enterprise {
		if r.Return != "" {
			p["return"] = r.Return
		}
		return p
	}
	if r.Limit != nil {
		p["limit"] = strconv.Itoa(*r.Limit)
	}
	for _, k := range EnterpriseParams {
		if v := r.EnterpriseOpts[k]; v != "" {
			p[k] = v
		}
	}
	if p["accurate_offsets"] == "" {
		p["accurate_offsets"] = "true"
	}
	if r.Offset != 0 {
		p["offset"] = strconv.FormatFloat(r.Offset, 'f', -1, 64)
	}
	return p
}

// EnterpriseOptions builds the SDK options, validating the passthrough values.
func (r Request) EnterpriseOptions() (*audd.EnterpriseOptions, error) {
	o := &audd.EnterpriseOptions{Limit: r.Limit}
	ints := map[string]**int{"skip": &o.Skip, "every": &o.Every, "skip_first_seconds": &o.SkipFirstSeconds}
	bools := map[string]**bool{"use_timecode": &o.UseTimecode, "accurate_offsets": &o.AccurateOffsets}
	for k, v := range r.EnterpriseOpts {
		if v == "" {
			continue
		}
		switch {
		case ints[k] != nil:
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return nil, output.Errf(output.ExitUsage, "invalid_argument", "", "--%s must be a whole number of 0 or more", strings.ReplaceAll(k, "_", "-"))
			}
			*ints[k] = &n
		case bools[k] != nil:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, output.Errf(output.ExitUsage, "invalid_argument", "", "--%s must be true or false", strings.ReplaceAll(k, "_", "-"))
			}
			*bools[k] = &b
		default:
			return nil, output.Errf(output.ExitUsage, "invalid_argument", "", "unknown enterprise parameter %q", k)
		}
	}
	if o.SkipFirstSeconds != nil && o.UseTimecode != nil && *o.UseTimecode {
		return nil, output.Errf(output.ExitUsage, "invalid_argument", "use one of them", "--skip-first-seconds and --use-timecode can't be combined")
	}
	return o, nil
}

// source is what the SDK sends for an input: the URL or the file path.
func source(in media.Input) string {
	if in.URL != "" {
		return in.URL
	}
	return in.Path
}

// Recognize sends one input and returns the result in its cacheable form:
// the standard endpoint's result object ("null" for no match), or the
// enterprise matches as an array with start_seconds/end_seconds added.
func Recognize(ctx context.Context, a *app.App, in media.Input, r Request) (json.RawMessage, error) {
	raw, err, _ := recognize(ctx, a, in, r)
	return raw, err
}

// RecognizeWithCause is Recognize for callers that need the SDK's own error
// as well as the CLI's: on failure it returns errors.Join(mapped, sdkErr),
// so errors.As finds the *output.Error first and the SDK error (which tells
// a connection that never opened from one that dropped after the upload)
// is still reachable.
func RecognizeWithCause(ctx context.Context, a *app.App, in media.Input, r Request) (json.RawMessage, error) {
	raw, err, cause := recognize(ctx, a, in, r)
	if err != nil && cause != nil {
		return nil, errors.Join(err, cause)
	}
	return raw, err
}

// recognize returns the result, the mapped error, and the last error the
// SDK returned (nil when the SDK was never called or succeeded).
func recognize(ctx context.Context, a *app.App, in media.Input, r Request) (raw json.RawMessage, err error, cause error) {
	if r.Enterprise {
		opts, err := r.EnterpriseOptions()
		if err != nil {
			return nil, err, nil
		}
		// Enterprise calls are billed per chunk scanned, so a failure after
		// the upload started (a 5xx, a dropped connection) is never sent
		// again; only a connection that never opened is retried.
		matches, err := DoOnce(ctx, a, func(c *audd.Client) ([]audd.EnterpriseMatch, error) {
			return retryPreUpload(ctx, &cause, func() ([]audd.EnterpriseMatch, error) {
				return c.RecognizeEnterpriseContext(ctx, source(in), opts)
			})
		})
		if err != nil {
			var e *output.Error
			if errors.As(err, &e) && e.APICode != 0 && e.DocsURL == APIDocsURL {
				e.DocsURL = EnterpriseDocsURL
			}
			return nil, err, cause
		}
		if r.Offset != 0 {
			for i := range matches {
				for _, p := range []**float64{&matches[i].StartSeconds, &matches[i].EndSeconds} {
					if *p != nil {
						v := **p + r.Offset
						*p = &v
					}
				}
			}
		}
		return MatchesJSON(matches), nil, nil
	}
	// A standard recognition is billed once AudD has the audio, and a 5xx
	// or a dropped connection only comes back after the upload, so the same
	// rule applies: only a connection that never opened is sent again.
	rec, err := DoOnce(ctx, a, func(c *audd.Client) (*audd.Recognition, error) {
		return retryPreUpload(ctx, &cause, func() (*audd.Recognition, error) {
			return c.RecognizeContext(ctx, source(in), &audd.RecognizeOptions{ReturnMetadata: r.Return, Timeout: StandardTimeout})
		})
	})
	if err != nil {
		return nil, err, cause
	}
	if rec == nil || len(rec.RawResponse) == 0 {
		return json.RawMessage("null"), nil, nil
	}
	return compact(rec.RawResponse), nil, nil
}

// retryPreUpload runs call, and runs it again (up to preUploadAttempts
// times) only when its connection never opened, so nothing reached AudD.
// *cause is set to call's last error.
func retryPreUpload[T any](ctx context.Context, cause *error, call func() (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		v, err := call()
		*cause = err
		var ce *audd.AudDConnectionError
		if err == nil || attempt >= preUploadAttempts || !errors.As(err, &ce) || !PreUpload(ce.Cause) {
			return v, err
		}
		select {
		case <-ctx.Done():
			return v, err
		case <-time.After(time.Duration(attempt) * preUploadBackoff):
		}
	}
}

// Retries of recognition calls whose connection never opened.
var (
	preUploadAttempts = 3
	preUploadBackoff  = 500 * time.Millisecond
)

// PreUpload reports a connection error that happened before any request
// bytes were sent (DNS, TCP or TLS dial, or a dial to the HTTPS_PROXY that
// failed), so nothing reached AudD.
func PreUpload(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}
	switch opErr.Op {
	case "dial":
		return true
	case "proxyconnect":
		// The proxy could not be reached: look at what the dial to it
		// returned.
		var inner *net.OpError
		return errors.As(opErr.Err, &inner) && inner.Op == "dial"
	}
	return false
}

// MatchesJSON renders enterprise matches with every API field plus the
// computed start_seconds and end_seconds (position in the file).
func MatchesJSON(ms []audd.EnterpriseMatch) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		raw := compact(m.RawResponse)
		if len(raw) < 2 || raw[0] != '{' {
			raw, _ = json.Marshal(m)
		}
		body := bytes.TrimSuffix(raw, []byte("}"))
		b.Write(body)
		sep := ","
		if bytes.Equal(body, []byte("{")) {
			sep = ""
		}
		for _, kv := range []struct {
			k string
			v *float64
		}{{"start_seconds", m.StartSeconds}, {"end_seconds", m.EndSeconds}} {
			if kv.v == nil {
				continue
			}
			b.WriteString(sep + `"` + kv.k + `":` + strconv.FormatFloat(round3(*kv.v), 'f', -1, 64))
			sep = ","
		}
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.Bytes()
}

func round3(f float64) float64 {
	return float64(int64(f*1000+0.5)) / 1000
}

func compact(raw []byte) json.RawMessage {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return json.RawMessage(raw)
	}
	return b.Bytes()
}

// Match is an enterprise match read back from its JSON form.
type Match struct {
	Artist       string   `json:"artist"`
	Title        string   `json:"title"`
	Album        string   `json:"album"`
	Label        string   `json:"label"`
	ReleaseDate  string   `json:"release_date"`
	ISRC         string   `json:"isrc"`
	UPC          string   `json:"upc"`
	SongLink     string   `json:"song_link"`
	Timecode     string   `json:"timecode"`
	Score        int      `json:"score"`
	StartOffset  int      `json:"start_offset"`
	EndOffset    int      `json:"end_offset"`
	StartSeconds *float64 `json:"start_seconds"`
	EndSeconds   *float64 `json:"end_seconds"`
}

// ParseMatches reads matches from MatchesJSON output, leniently.
func ParseMatches(raw json.RawMessage) []Match {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]Match, 0, len(items))
	for _, it := range items {
		var em audd.EnterpriseMatch
		if err := json.Unmarshal(it, &em); err != nil {
			continue
		}
		m := Match{
			Artist: em.Artist, Title: em.Title, Album: em.Album, Label: em.Label,
			ReleaseDate: em.ReleaseDate, ISRC: em.ISRC, UPC: em.UPC, SongLink: em.SongLink,
			Timecode: em.Timecode, Score: em.Score, StartOffset: em.StartOffset, EndOffset: em.EndOffset,
		}
		var pos struct {
			Start *float64 `json:"start_seconds"`
			End   *float64 `json:"end_seconds"`
		}
		_ = json.Unmarshal(it, &pos)
		m.StartSeconds, m.EndSeconds = pos.Start, pos.End
		out = append(out, m)
	}
	return out
}

// View flattens a standard result (as returned by Recognize) for display.
// ok is false for no match.
func View(raw json.RawMessage, cached bool) (v app.ResultView, ok bool) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return v, false
	}
	var rec audd.Recognition
	if err := json.Unmarshal(raw, &rec); err != nil {
		return v, false
	}
	v = app.ResultView{
		Artist: rec.Artist, Title: rec.Title, Album: rec.Album, Label: rec.Label,
		ReleaseDate: rec.ReleaseDate, ISRC: rec.ISRC, UPC: rec.UPC, SongLink: rec.SongLink,
		Timecode: rec.Timecode, Cached: cached, Extra: map[string]any{},
	}
	var all map[string]any
	if json.Unmarshal(raw, &all) == nil {
		for k, val := range all {
			switch k {
			case "artist", "title", "album", "label", "release_date", "isrc", "upc", "song_link", "timecode", "score":
			default:
				v.Extra[k] = val
			}
		}
		switch s := all["score"].(type) {
		case float64:
			v.Score = int(s)
		case string:
			if n, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				v.Score = int(n)
			}
		}
	}
	if rec.AudioID != nil && rec.Artist == "" && rec.Title == "" {
		v.Title = "Custom catalog match: audio_id " + strconv.Itoa(*rec.AudioID)
	}
	if rec.AppleMusic != nil {
		var am struct {
			Artwork struct {
				URL     string `json:"url"`
				BGColor string `json:"bgColor"`
			} `json:"artwork"`
		}
		if json.Unmarshal(rec.AppleMusic.RawResponse, &am) == nil {
			v.AppleArtwork, v.AppleBG = am.Artwork.URL, am.Artwork.BGColor
		}
	}
	return v, true
}

// Essential is the one-line form of a result: "Artist — Title".
func Essential(v app.ResultView) string {
	switch {
	case v.Artist != "" && v.Title != "":
		return v.Artist + " — " + v.Title
	case v.Title != "":
		return v.Title
	}
	return v.Artist
}
