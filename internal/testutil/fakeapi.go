package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Endpoints of the fake API. Any other path is a raw method name, such as
// "getStreams".
const (
	EndpointRecognize  = "recognize"  // POST https://api.audd.io/
	EndpointEnterprise = "enterprise" // POST https://enterprise.audd.io/
	EndpointUpload     = "upload"     // POST https://api.audd.io/upload/
)

// FakeRequest is one request the fake API received.
type FakeRequest struct {
	Endpoint string
	Token    string
	Form     map[string]string // form fields except api_token and file
	FileName string
	FileSize int // bytes of the uploaded file; -1 when no file was sent
}

// FakeHandler answers a request with an HTTP status and a JSON body.
type FakeHandler func(r FakeRequest) (status int, body any)

// FakeAPI is an httptest server that stands in for api.audd.io and
// enterprise.audd.io. NewFakeAPI points the CLI at it through the
// AUDD_API_BASE_URL and AUDD_ENTERPRISE_BASE_URL environment variables.
type FakeAPI struct {
	*httptest.Server

	mu       sync.Mutex
	requests []FakeRequest
	handlers map[string]FakeHandler
	// ValidTokens, when not empty, is the set of tokens the fake accepts;
	// any other token gets error 900.
	valid map[string]bool
}

// NewFakeAPI starts a fake API for the test. Unhandled endpoints answer
// with error 1000 (unknown method).
func NewFakeAPI(t testing.TB) *FakeAPI {
	t.Helper()
	f := &FakeAPI{handlers: map[string]FakeHandler{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	t.Setenv("AUDD_API_BASE_URL", f.URL)
	t.Setenv("AUDD_ENTERPRISE_BASE_URL", f.URL+"/_enterprise")
	return f
}

// On sets the handler for an endpoint (EndpointRecognize, EndpointEnterprise,
// EndpointUpload, or a raw method name).
func (f *FakeAPI) On(endpoint string, h FakeHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[endpoint] = h
}

// Reply sets a handler that always answers 200 with body.
func (f *FakeAPI) Reply(endpoint string, body any) {
	f.On(endpoint, func(FakeRequest) (int, any) { return http.StatusOK, body })
}

// AcceptTokens limits the tokens the fake accepts; others get error 900.
func (f *FakeAPI) AcceptTokens(tokens ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.valid = map[string]bool{}
	for _, t := range tokens {
		f.valid[t] = true
	}
}

// Requests returns every request received so far.
func (f *FakeAPI) Requests() []FakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeRequest(nil), f.requests...)
}

// Count returns how many requests hit endpoint.
func (f *FakeAPI) Count(endpoint string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Endpoint == endpoint {
			n++
		}
	}
	return n
}

func (f *FakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(r.URL.Path, "/")
	endpoint := p
	switch p {
	case "":
		endpoint = EndpointRecognize
	case "_enterprise":
		endpoint = EndpointEnterprise
	case "upload":
		endpoint = EndpointUpload
	}
	req := FakeRequest{Endpoint: endpoint, Form: map[string]string{}, FileSize: -1}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(64 << 20); err == nil {
			if fh := r.MultipartForm.File["file"]; len(fh) > 0 {
				req.FileName = fh[0].Filename
				if fl, err := fh[0].Open(); err == nil {
					n, _ := io.Copy(io.Discard, fl)
					fl.Close()
					req.FileSize = int(n)
				}
			}
		}
	} else {
		_ = r.ParseForm()
	}
	for k, v := range r.Form {
		if len(v) > 0 {
			req.Form[k] = v[0]
		}
	}
	if r.MultipartForm != nil {
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				req.Form[k] = v[0]
			}
		}
	}
	req.Token = req.Form["api_token"]
	delete(req.Form, "api_token")

	f.mu.Lock()
	f.requests = append(f.requests, req)
	h := f.handlers[endpoint]
	valid := f.valid
	f.mu.Unlock()

	status, body := http.StatusOK, any(nil)
	switch {
	case len(valid) > 0 && !valid[req.Token]:
		body = APIError(900, "Wrong API token")
	case h == nil:
		body = APIError(1000, "Unknown API method")
	default:
		status, body = h(req)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if s, ok := body.(string); ok {
		io.WriteString(w, s)
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// Success wraps a result in {"status":"success","result":...}.
func Success(result any) map[string]any {
	return map[string]any{"status": "success", "result": result}
}

// APIError is an AudD error body.
func APIError(code int, message string) map[string]any {
	return map[string]any{
		"status": "error",
		"error":  map[string]any{"error_code": code, "error_message": message},
	}
}

// MatchResult is a typical standard-endpoint match.
func MatchResult() map[string]any {
	return map[string]any{
		"artist":       "Imagine Dragons",
		"title":        "Warriors",
		"album":        "Warriors",
		"release_date": "2014-09-18",
		"label":        "Universal Music",
		"timecode":     "00:40",
		"song_link":    "https://lis.tn/Warriors",
		"isrc":         "USUM71409990",
		"upc":          "00602547058929",
		"new_field":    "kept as is",
	}
}

// EnterpriseSong is one enterprise match inside a chunk.
func EnterpriseSong(artist, title string, startMS, endMS int) map[string]any {
	return map[string]any{
		"score": 100, "artist": artist, "title": title, "album": title,
		"timecode": "00:00", "song_link": "https://lis.tn/" + strings.ReplaceAll(title, " ", ""),
		"start_offset": startMS, "end_offset": endMS,
	}
}

// EnterpriseChunk is one chunk of an enterprise response.
func EnterpriseChunk(offset string, songs ...map[string]any) map[string]any {
	if songs == nil {
		songs = []map[string]any{}
	}
	return map[string]any{"offset": offset, "songs": songs}
}
