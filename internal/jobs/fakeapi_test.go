package jobs

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// fakeAPI stands in for api.audd.io and enterprise.audd.io. What it
// answers depends on the uploaded file's name:
//
//	match-*  a match (title = file name)    none-*  no match
//	bad-*    error 300 (invalid audio)      auth-*  error 900 (bad token)
//	rate-*   HTTP 429 twice, then a match   drop-*  connection dropped mid-request
//	daily-*  error 611 (daily rate limit)
//
// The enterprise endpoint returns two matches per file.
type fakeAPI struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests map[string]int    // by file name
	params   map[string]string // last form fields seen, by file name
	hosts    map[string]int
	onReq    func(n int) // called with the running total, before answering
	total    int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{requests: map[string]int{}, params: map[string]string{}, hosts: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		_ = r.ParseForm()
	}
	name := r.FormValue("url")
	if fh, hdr, err := r.FormFile("file"); err == nil {
		fh.Close()
		name = hdr.Filename
	}
	if r.FormValue("api_token") != testutil.PlaceholderToken {
		http.Error(w, "token missing", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests[name]++
	n := f.requests[name]
	f.total++
	total := f.total
	f.hosts[r.Header.Get("X-Original-Host")]++
	var ps []string
	for k := range r.Form {
		if k != "api_token" {
			ps = append(ps, k+"="+r.FormValue(k))
		}
	}
	f.params[name] = strings.Join(sortStrings(ps), "&")
	hook := f.onReq
	f.mu.Unlock()
	if hook != nil {
		hook(total)
	}

	w.Header().Set("Content-Type", "application/json")
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if r.Header.Get("X-Original-Host") == "enterprise.audd.io" {
		fmt.Fprintf(w, `{"status":"success","result":[{"offset":"00:00","songs":[{"artist":"Artist","title":"%s one","score":90,"timecode":"00:05","start_offset":5000,"end_offset":11000,"isrc":"XX0000000001"}]},{"offset":"01:00","songs":[{"artist":"Artist","title":"%s two","score":80,"timecode":"01:03","start_offset":3000,"end_offset":9000}]}]}`, base, base)
		return
	}
	switch {
	case strings.HasPrefix(name, "match-"):
		fmt.Fprintf(w, `{"status":"success","result":{"artist":"Artist","title":"%s","album":"Album","release_date":"2020-01-01","label":"Label","timecode":"00:10","song_link":"https://lis.tn/x","extra_field":7}}`, base)
	case strings.HasPrefix(name, "none-"):
		fmt.Fprint(w, `{"status":"success","result":null}`)
	case strings.HasPrefix(name, "bad-"):
		fmt.Fprint(w, `{"status":"error","error":{"error_code":300,"error_message":"Recognition failed: a problem with fingerprint generation"}}`)
	case strings.HasPrefix(name, "auth-"):
		fmt.Fprint(w, `{"status":"error","error":{"error_code":900,"error_message":"Wrong API token"}}`)
	case strings.HasPrefix(name, "rate-"):
		if n <= 2 {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		fmt.Fprintf(w, `{"status":"success","result":{"artist":"Artist","title":"%s"}}`, base)
	case strings.HasPrefix(name, "daily-"):
		fmt.Fprint(w, `{"status":"error","error":{"error_code":611,"error_message":"Rate limit reached"}}`)
	case strings.HasPrefix(name, "drop-"):
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	default:
		fmt.Fprint(w, `{"status":"success","result":null}`)
	}
}

func sortStrings(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}

func (f *fakeAPI) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[name]
}

func (f *fakeAPI) totalRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

// rewrite sends every request to the fake server, whatever the host.
type rewrite struct{ target *url.URL }

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Original-Host", r.URL.Host)
	r.URL.Scheme, r.URL.Host = rw.target.Scheme, rw.target.Host
	r.Host = rw.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func (f *fakeAPI) client() *audd.Client {
	u, _ := url.Parse(f.srv.URL)
	hc := &http.Client{Transport: rewrite{u}}
	return audd.NewClient(testutil.PlaceholderToken, audd.WithHTTPClient(hc),
		audd.WithBackoffFactor(time.Millisecond), audd.WithOnDeprecation(func(string) {}))
}

// testApp is an App wired to the fake API, writing to buffers.
type testApp struct {
	*app.App
	stdout, stderr *bytes.Buffer
}

func newTestApp(t *testing.T, f *fakeAPI, format output.Format) *testApp {
	t.Helper()
	cfgDir := testutil.Isolate(t)
	a := app.New()
	a.Cfg = config.Defaults(filepath.Join(cfgDir, "config.toml"))
	a.Profile = a.Cfg.Profile(config.DefaultProfile)
	var out, errb bytes.Buffer
	a.Out = output.NewPrinter(&out, &errb, output.PrinterOptions{Format: format, NoColor: true, Stdin: strings.NewReader("")})
	a.Flags.Yes = true
	if f != nil {
		c := f.client()
		a.APIClient = func() (*audd.Client, error) { return c, nil }
	}
	return &testApp{App: a, stdout: &out, stderr: &errb}
}

// mkfiles writes small audio stand-ins into a temp dir and returns their paths.
func mkfiles(t *testing.T, names ...string) []string {
	t.Helper()
	dir := t.TempDir()
	var out []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("audio:"+n), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// noSleep makes rate-limit backoff instant and records the waits.
func noSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	var mu sync.Mutex
	old := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return ctx.Err()
	}
	t.Cleanup(func() { sleep = old })
	return &waits
}
