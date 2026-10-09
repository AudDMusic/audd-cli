package streams

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

// RecentResultsURL is the recent-results endpoint: the last ~30 results per
// stream, keyed by longpoll category. It needs no API token.
var RecentResultsURL = "https://api.audd.io/lastSong/getChannelById/"

// HTTPClient is used for the recent-results endpoint and for relaying
// plays to --forward-to handlers. It honors HTTPS_PROXY and NO_PROXY.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// LongpollHTTPClient is used for longpoll requests, which carry no API
// token. It has no overall timeout: each poll sets its own deadline.
var LongpollHTTPClient = &http.Client{}

// RecentResults returns a stream's recent results, oldest first. The
// endpoint is undocumented, so the response is read leniently: unknown
// fields are ignored, wrong-typed values degrade to empty, and entries
// without an artist or title are skipped. RadioID is left at 0 unless the
// entry carries one; callers set it. Failures are network or server errors
// (exit 5, retryable) that name the stream.
func RecentResults(ctx context.Context, radioID int, category string) ([]streamstore.Play, error) {
	u, err := url.Parse(RecentResultsURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("ch_id", "-"+category)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	fail := func(code, detail string) error {
		hint := "try again in a minute"
		if code == "network" {
			hint = "check your connection (and HTTPS_PROXY, if you use a proxy) and try again"
		}
		return &output.Error{Code: code, Message: fmt.Sprintf("could not read recent results for stream %d: %s", radioID, detail),
			Hint: hint, Retryable: true, Exit: output.ExitNetwork}
	}
	res, err := HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail("network", err.Error())
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fail("network", err.Error())
	}
	if res.StatusCode >= 300 {
		return nil, fail("server", fmt.Sprintf("HTTP %d", res.StatusCode))
	}
	plays, err := ParseRecentResults(body, time.Now())
	if err != nil {
		return nil, fail("server", strings.TrimPrefix(err.Error(), "recent results: "))
	}
	return plays, nil
}

// ParseRecentResults parses a recent-results response body.
func ParseRecentResults(body []byte, now time.Time) ([]streamstore.Play, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("recent results: response is not JSON: %w", err)
	}
	top, _ := normalize(doc).(map[string]any)
	if top == nil {
		if arr, ok := normalize(doc).([]any); ok {
			return playsFrom(arr, 0, now), nil
		}
		return nil, fmt.Errorf("recent results: unexpected response")
	}
	if s, _ := top["status"].(string); s == "error" {
		msg := "the server returned an error"
		if e, ok := top["error"].(map[string]any); ok {
			if m := str(e, "error_message", "message"); m != "" {
				msg = m
			}
		}
		return nil, fmt.Errorf("recent results: %s", msg)
	}
	if r, ok := top["result"].(map[string]any); ok {
		top = r
	}
	hist, _ := lookup(top, "History", "history").(map[string]any)
	if hist == nil {
		if arr, ok := lookup(top, "Elements", "elements").([]any); ok {
			return playsFrom(arr, num(top, "OldestElement", "oldest_element"), now), nil
		}
		return []streamstore.Play{}, nil
	}
	arr, _ := lookup(hist, "Elements", "elements").([]any)
	return playsFrom(arr, num(hist, "OldestElement", "oldest_element"), now), nil
}

func lookup(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

// playsFrom reads a ring buffer: elements[(oldest+i) % len] runs from the
// oldest entry to the newest; empty slots are null.
func playsFrom(elems []any, oldest int, now time.Time) []streamstore.Play {
	n := len(elems)
	out := []streamstore.Play{}
	if n == 0 {
		return out
	}
	oldest = ((oldest % n) + n) % n
	for i := 0; i < n; i++ {
		m, ok := elems[(oldest+i)%n].(map[string]any)
		if !ok {
			continue
		}
		p, ok := playFromEntry(m, now)
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func playFromEntry(m map[string]any, now time.Time) (streamstore.Play, bool) {
	p := streamstore.Play{
		RadioID:     num(m, "radio_id"),
		PlayLength:  num(m, "play_length"),
		Artist:      str(m, "artist"),
		Title:       str(m, "title"),
		Album:       str(m, "album"),
		Label:       str(m, "label"),
		ReleaseDate: str(m, "release_date"),
		ISRC:        str(m, "isrc"),
		UPC:         str(m, "upc"),
		SongLink:    str(m, "song_link"),
		Score:       num(m, "score"),
	}
	if p.Artist == "" && p.Title == "" {
		return p, false
	}
	// The entry time is in "timestamp" or, in the widget's format, "song_length".
	found := false
	for _, k := range []string{"timestamp", "song_length", "time"} {
		if t, ok := ParseTimestamp(str(m, k)); ok {
			p.Timestamp, found = t, true
			break
		}
	}
	if !found {
		return p, false
	}
	if raw, err := json.Marshal(m); err == nil {
		p.Raw = raw
	}
	return p, true
}

// normalize turns json.Number values into float64 so the lenient readers
// see one numeric type.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
		return t
	case json.Number:
		if f, err := t.Float64(); err == nil {
			// Keep big integer-like strings (ISRC-like ids) as they are.
			if strings.ContainsAny(t.String(), ".eE") || len(t.String()) < 16 {
				return f
			}
		}
		return t.String()
	}
	return v
}
