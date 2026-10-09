package streams

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

// Forwarder relays stream events to a local handler as callback-shaped
// POSTs, the same JSON AudD sends to a callback URL.
type Forwarder struct {
	URL    string
	Client *http.Client // defaults to HTTPClient
}

// Post sends one callback body.
func (f *Forwarder) Post(ctx context.Context, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent())
	c := f.Client
	if c == nil {
		c = HTTPClient
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s answered HTTP %d", f.URL, res.StatusCode)
	}
	return nil
}

// PlayCallbackBody builds the callback JSON for a play (used for plays that
// came from the recent-results endpoint rather than a live event).
func PlayCallbackBody(p streamstore.Play) []byte {
	song := p.Raw
	if len(song) == 0 {
		song, _ = json.Marshal(map[string]any{
			"artist": p.Artist, "title": p.Title, "album": p.Album, "release_date": p.ReleaseDate,
			"label": p.Label, "score": p.Score, "song_link": p.SongLink, "isrc": p.ISRC, "upc": p.UPC,
		})
	}
	type result struct {
		RadioID    int               `json:"radio_id"`
		Timestamp  string            `json:"timestamp"`
		PlayLength int               `json:"play_length"`
		Results    []json.RawMessage `json:"results"`
	}
	b, _ := json.Marshal(struct {
		Status string `json:"status"`
		Result result `json:"result"`
	}{"success", result{p.RadioID, FormatTimestamp(p.Timestamp), p.PlayLength, []json.RawMessage{song}}})
	return b
}

// HealthCallbackBody builds the callback JSON for a health event.
func HealthCallbackBody(h streamstore.HealthEvent) []byte {
	type notification struct {
		RadioID int    `json:"radio_id"`
		Running bool   `json:"stream_running"`
		Code    int    `json:"notification_code"`
		Message string `json:"notification_message"`
	}
	b, _ := json.Marshal(struct {
		Status       string       `json:"status"`
		Notification notification `json:"notification"`
		Time         int64        `json:"time"`
	}{"-", notification{h.RadioID, h.Running, h.Code, h.Message}, h.At.Unix()})
	return b
}
