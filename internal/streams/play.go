// Package streams reads AudD stream results (live longpoll events and the
// recent-results endpoint), records them into the local stream store, runs
// the background recorder, and relays plays to a local callback handler.
package streams

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

// audDZone is the time zone of the timestamps in AudD stream results
// ("2026-10-08 14:03:12" is UTC+3). The AudD docs do not state it; the
// widget at widget.audd.tech reads the same getChannelById and longpoll data
// and converts these timestamps from UTC+3 ("the callbacks from AudD API
// are at UTC+3"). The contract test against the live API should confirm it
// on a recorded response. Live and backfilled plays are parsed the same
// way, so deduplication does not depend on it.
var audDZone = time.FixedZone("UTC+3", 3*60*60)

const audDTimeLayout = "2006-01-02 15:04:05"

// ParseTimestamp reads a stream-result timestamp: "YYYY-MM-DD hh:mm:ss" in
// AudD's UTC+3, RFC 3339, or Unix seconds/milliseconds. ok is false when the
// value is not a time.
func ParseTimestamp(s string) (t time.Time, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.ParseInLocation(audDTimeLayout, s, audDZone); err == nil {
		return t.UTC(), true
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, audDZone); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 0 {
		return unixTime(n), true
	}
	return time.Time{}, false
}

func unixTime(n float64) time.Time {
	if n > 1e12 { // milliseconds
		return time.UnixMilli(int64(n)).UTC()
	}
	return time.Unix(int64(n), 0).UTC()
}

// FormatTimestamp writes a time the way AudD stream results do.
func FormatTimestamp(t time.Time) string {
	return t.In(audDZone).Format(audDTimeLayout)
}

// PlayFromMatch converts a longpoll/callback recognition into a Play. now is
// used when the event carries no usable timestamp.
func PlayFromMatch(m audd.StreamCallbackMatch, now time.Time) streamstore.Play {
	p := streamstore.Play{
		RadioID:     int(m.RadioID),
		PlayLength:  m.PlayLength,
		Artist:      m.Song.Artist,
		Title:       m.Song.Title,
		Album:       m.Song.Album,
		Label:       m.Song.Label,
		ReleaseDate: m.Song.ReleaseDate,
		ISRC:        m.Song.ISRC,
		UPC:         m.Song.UPC,
		SongLink:    m.Song.SongLink,
		Score:       m.Song.Score,
	}
	if ts, ok := ParseTimestamp(m.Timestamp); ok {
		p.Timestamp = ts
	} else {
		p.Timestamp = now.UTC().Truncate(time.Second)
	}
	p.Raw = firstResult(m.RawResponse)
	return p
}

// firstResult extracts result.results[0] from a callback body.
func firstResult(body []byte) json.RawMessage {
	var doc struct {
		Result struct {
			Results []json.RawMessage `json:"results"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &doc) != nil || len(doc.Result.Results) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), doc.Result.Results[0]...)
}

// HealthFromNotification converts a stream notification into a HealthEvent.
func HealthFromNotification(n audd.StreamCallbackNotification, radioID int, now time.Time) streamstore.HealthEvent {
	h := streamstore.HealthEvent{RadioID: n.RadioID, Code: n.NotificationCode, Message: n.NotificationMessage}
	if h.RadioID == 0 {
		h.RadioID = radioID
	}
	if n.Time > 0 {
		h.At = unixTime(float64(n.Time))
	} else {
		h.At = now.UTC().Truncate(time.Second)
	}
	if n.StreamRunning != nil {
		h.Running = *n.StreamRunning
	} else {
		h.Running = n.NotificationCode != 650
	}
	return h
}

// Lenient readers for loosely typed JSON objects.

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case json.Number:
			return v.String()
		}
	}
	return ""
}

func num(m map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				return int(v)
			}
		case string:
			if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return int(n)
			}
		}
	}
	return 0
}
