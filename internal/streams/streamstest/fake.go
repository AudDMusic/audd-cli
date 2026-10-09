// Package streamstest is a fake AudD stream API for tests: stream
// management methods, longpoll, and the recent-results endpoint.
package streamstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-go"
)

// Token is the placeholder API token the fake accepts.
const Token = "0123456789abcdef0123456789abcdef"

// Stream is a stream the fake account has.
type Stream struct {
	RadioID int    `json:"radio_id"`
	URL     string `json:"url"`
	Running bool   `json:"stream_running"`
}

// Server is a fake api.audd.io.
type Server struct {
	*httptest.Server
	t testing.TB

	mu          sync.Mutex
	streams     []Stream
	callbackURL string
	noCallback  bool
	events      map[string][]json.RawMessage // category → queued longpoll bodies
	wake        chan struct{}
	recent      map[string]json.RawMessage // category → getChannelById body
	failPolls   int                        // next N longpoll requests fail with HTTP 500
	failRecent  int
	calls       []Call
	lpTimeout   time.Duration
	token       string // the API token the fake accepts
}

// Call is one request the fake received.
type Call struct {
	Method string // AudD method ("getStreams", "longpoll", "getChannelById", ...)
	Params url.Values
}

// New starts a fake server. Longpoll requests wait at most lpTimeout
// (instead of the requested timeout) before answering with a keepalive.
func New(t testing.TB) *Server {
	s := &Server{t: t, events: map[string][]json.RawMessage{}, recent: map[string]json.RawMessage{},
		wake: make(chan struct{}), lpTimeout: 200 * time.Millisecond, callbackURL: "https://example.com/callback", token: Token}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// HTTPClient returns a client that sends every request (whatever its host)
// to the fake.
func (s *Server) HTTPClient() *http.Client {
	target, _ := url.Parse(s.URL)
	return &http.Client{Transport: rewrite{target: target, base: http.DefaultTransport}}
}

// Client returns an audd-go client wired to the fake, without retries.
func (s *Server) Client() *audd.Client { return s.ClientWithToken(Token) }

// ClientWithToken is Client with another API token.
func (s *Server) ClientWithToken(tok string) *audd.Client {
	return audd.NewClient(tok, audd.WithHTTPClient(s.HTTPClient()),
		audd.WithMaxAttempts(1), audd.WithBackoffFactor(time.Millisecond))
}

// SetToken changes the API token the fake accepts (as after a rotation).
func (s *Server) SetToken(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = tok
}

type rewrite struct {
	target *url.URL
	base   http.RoundTripper
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = r.target.Scheme
	req.URL.Host = r.target.Host
	req.Host = r.target.Host
	return r.base.RoundTrip(req)
}

// SetStreams replaces the account's streams.
func (s *Server) SetStreams(streams ...Stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams = append([]Stream(nil), streams...)
}

// Streams returns the account's streams.
func (s *Server) Streams() []Stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Stream(nil), s.streams...)
}

// SetNoCallbackURL makes getCallbackUrl answer with error 19.
func (s *Server) SetNoCallbackURL() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noCallback = true
	s.callbackURL = ""
}

// CallbackURL returns the account's callback URL.
func (s *Server) CallbackURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callbackURL
}

// Category is the longpoll category for a radio ID under Token.
func Category(radioID int) string { return audd.DeriveLongpollCategory(Token, radioID) }

// PushMatch queues a recognition for a stream's longpoll.
func (s *Server) PushMatch(radioID int, timestamp, artist, title string, playLength int) {
	s.Push(radioID, matchBody(radioID, timestamp, artist, title, playLength))
}

// PushMatchFor queues a recognition for a stream's longpoll category under
// another API token.
func (s *Server) PushMatchFor(tok string, radioID int, timestamp, artist, title string, playLength int) {
	s.pushCategory(audd.DeriveLongpollCategory(tok, radioID), matchBody(radioID, timestamp, artist, title, playLength))
}

func matchBody(radioID int, timestamp, artist, title string, playLength int) json.RawMessage {
	body := fmt.Sprintf(`{"status":"success","result":{"radio_id":%d,"timestamp":%q,"play_length":%d,"results":[{"artist":%q,"title":%q,"album":"Album","release_date":"2020-01-01","label":"Label","score":100,"song_link":"https://lis.tn/test","isrc":"USXXX0000001"}]}}`,
		radioID, timestamp, playLength, artist, title)
	return json.RawMessage(body)
}

// PushNotification queues a stream notification for a stream's longpoll.
func (s *Server) PushNotification(radioID, code int, message string, running bool, at int64) {
	body := fmt.Sprintf(`{"status":"-","notification":{"radio_id":%d,"stream_running":%t,"notification_code":%d,"notification_message":%q},"time":%d}`,
		radioID, running, code, message, at)
	s.Push(radioID, json.RawMessage(body))
}

// Push queues a raw longpoll body for a stream.
func (s *Server) Push(radioID int, body json.RawMessage) {
	s.pushCategory(Category(radioID), body)
}

func (s *Server) pushCategory(cat string, body json.RawMessage) {
	s.mu.Lock()
	s.events[cat] = append(s.events[cat], body)
	close(s.wake)
	s.wake = make(chan struct{})
	s.mu.Unlock()
}

// SetRecent sets the getChannelById response for a stream.
func (s *Server) SetRecent(radioID int, body json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent[Category(radioID)] = body
}

// FailNextPolls makes the next n longpoll requests return HTTP 500.
func (s *Server) FailNextPolls(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPolls = n
}

// FailNextRecent makes the next n recent-results requests return HTTP 500.
func (s *Server) FailNextRecent(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failRecent = n
}

// Calls returns the requests received so far.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Count returns how many requests used an AudD method.
func (s *Server) Count(method string) int {
	n := 0
	for _, c := range s.Calls() {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.ParseMultipartForm(1 << 20)
	} else {
		r.ParseForm()
	}
	params := url.Values{}
	for k, v := range r.Form {
		params[k] = v
	}
	if r.MultipartForm != nil {
		for k, v := range r.MultipartForm.Value {
			params[k] = v
		}
	}
	method := strings.Trim(r.URL.Path, "/")
	if i := strings.LastIndex(method, "/"); i >= 0 {
		method = method[i+1:]
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	s.mu.Lock()
	tok := s.token
	s.mu.Unlock()
	if method == "longpoll" && params.Has("api_token") {
		s.t.Errorf("longpoll request carries the API token: %s", r.URL.RawQuery)
	}
	if method != "getChannelById" && method != "longpoll" && params.Get("api_token") != tok {
		writeJSON(w, map[string]any{"status": "error", "error": map[string]any{"error_code": 900, "error_message": "Wrong API token"}})
		return
	}
	switch method {
	case "getStreams":
		writeJSON(w, map[string]any{"status": "success", "result": s.Streams()})
	case "addStream":
		id, _ := strconv.Atoi(params.Get("radio_id"))
		s.mu.Lock()
		s.streams = append(s.streams, Stream{RadioID: id, URL: params.Get("url"), Running: true})
		s.mu.Unlock()
		writeJSON(w, map[string]any{"status": "success"})
	case "deleteStream":
		id, _ := strconv.Atoi(params.Get("radio_id"))
		s.mu.Lock()
		kept := s.streams[:0]
		found := false
		for _, st := range s.streams {
			if st.RadioID == id {
				found = true
				continue
			}
			kept = append(kept, st)
		}
		s.streams = kept
		s.mu.Unlock()
		if !found {
			writeJSON(w, map[string]any{"status": "error", "error": map[string]any{"error_code": 700, "error_message": "radio_id not found"}})
			return
		}
		writeJSON(w, map[string]any{"status": "success"})
	case "setStreamUrl":
		id, _ := strconv.Atoi(params.Get("radio_id"))
		s.mu.Lock()
		for i := range s.streams {
			if s.streams[i].RadioID == id {
				s.streams[i].URL = params.Get("url")
			}
		}
		s.mu.Unlock()
		writeJSON(w, map[string]any{"status": "success"})
	case "setCallbackUrl":
		s.mu.Lock()
		s.callbackURL = params.Get("url")
		s.noCallback = false
		s.mu.Unlock()
		writeJSON(w, map[string]any{"status": "success"})
	case "getCallbackUrl":
		s.mu.Lock()
		no, u := s.noCallback, s.callbackURL
		s.mu.Unlock()
		if no {
			writeJSON(w, map[string]any{"status": "error", "error": map[string]any{"error_code": 19, "error_message": "Internal error"}})
			return
		}
		writeJSON(w, map[string]any{"status": "success", "result": u})
	case "getChannelById":
		s.mu.Lock()
		fail := s.failRecent > 0
		if fail {
			s.failRecent--
		}
		body, ok := s.recent[strings.TrimPrefix(params.Get("ch_id"), "-")]
		s.mu.Unlock()
		if fail {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if !ok {
			body = json.RawMessage(`{"NumericalId":0,"StringId":"","History":{"Elements":[],"OldestElement":0}}`)
		}
		w.Write(body)
	case "longpoll":
		s.longpoll(w, r, params.Get("category"))
	default:
		writeJSON(w, map[string]any{"status": "error", "error": map[string]any{"error_code": 51, "error_message": "unknown method " + method}})
	}
}

func (s *Server) longpoll(w http.ResponseWriter, r *http.Request, cat string) {
	s.mu.Lock()
	if s.failPolls > 0 {
		s.failPolls--
		s.mu.Unlock()
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.mu.Unlock()
	deadline := time.After(s.lpTimeout)
	for {
		s.mu.Lock()
		if q := s.events[cat]; len(q) > 0 {
			body := q[0]
			s.events[cat] = q[1:]
			s.mu.Unlock()
			w.Write(body)
			return
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-deadline:
			writeJSON(w, map[string]any{"timeout": "no events before timeout", "timestamp": time.Now().UnixMilli()})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	json.NewEncoder(w).Encode(v)
}

// RecentBody builds a getChannelById response holding songs as a ring
// buffer of the given capacity, the way AudD returns it: Elements[(oldest+i)
// % capacity] runs from the oldest to the newest, unused slots are null.
func RecentBody(capacity int, songs ...map[string]any) json.RawMessage {
	elems := make([]any, capacity)
	oldest := 0
	if len(songs) >= capacity {
		songs = songs[len(songs)-capacity:]
	}
	// Place them so that the ring wraps around, as in a live buffer.
	start := capacity - len(songs)/2 - 1
	if start < 0 {
		start = 0
	}
	for i, s := range songs {
		elems[(start+i)%capacity] = s
	}
	if len(songs) == capacity {
		oldest = start % capacity
	} else {
		oldest = (start + len(songs)) % capacity
	}
	b, _ := json.Marshal(map[string]any{
		"NumericalId": 42,
		"StringId":    "x",
		"History":     map[string]any{"Elements": elems, "OldestElement": oldest},
	})
	return b
}
