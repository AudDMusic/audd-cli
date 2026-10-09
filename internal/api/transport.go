package api

import (
	"net/http"
	"net/url"
	"os"
	"path"

	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// Hosts the SDK and the stream endpoints talk to.
const (
	apiHost        = "api.audd.io"
	enterpriseHost = "enterprise.audd.io"
)

// Environment variables that point the CLI at another API server. They are
// intentionally supported but left out of the user docs: tests use them
// (internal/testutil.FakeAPI, internal/e2e), and so can a self-hosted proxy
// in front of api.audd.io and enterprise.audd.io.
const (
	envAPIBase        = "AUDD_API_BASE_URL"
	envEnterpriseBase = "AUDD_ENTERPRISE_BASE_URL"
)

// UserAgent identifies the CLI to AudD; the SDK's own agent follows it.
func UserAgent() string { return "audd-cli/" + app.Version }

// HTTPClient returns the HTTP client every AudD call uses: proxies from
// HTTPS_PROXY/NO_PROXY, the CLI user agent, and with --debug a log of each
// request on stderr (tokens redacted). It has no overall timeout; callers
// bound each call with a context or a per-call timeout.
func HTTPClient(a *app.App) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyFromEnvironment
	t := &transport{base: base}
	t.apiBase = parseBase(os.Getenv(envAPIBase))
	t.entBase = parseBase(os.Getenv(envEnterpriseBase))
	if a != nil && a.Flags.Debug && a.Out != nil {
		t.debug = a.Out.Warn
	}
	return &http.Client{Transport: t}
}

// Transport is the HTTP transport for AudD calls made outside the SDK (the
// stream recent-results endpoint and callback relays): proxies, the CLI
// user agent, and the test base URLs, read when each request is sent.
func Transport() http.RoundTripper {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyFromEnvironment
	return &transport{base: base, fromEnv: true}
}

func parseBase(s string) *url.URL {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil
	}
	return u
}

type transport struct {
	base             http.RoundTripper
	apiBase, entBase *url.URL
	// fromEnv reads the base URLs from the environment on every request.
	fromEnv bool
	debug   func(format string, args ...any)
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	apiBase, entBase := t.apiBase, t.entBase
	if t.fromEnv {
		apiBase, entBase = parseBase(os.Getenv(envAPIBase)), parseBase(os.Getenv(envEnterpriseBase))
	}
	switch {
	case apiBase != nil && r.URL.Host == apiHost:
		rebase(r, apiBase)
	case entBase != nil && r.URL.Host == enterpriseHost:
		rebase(r, entBase)
	}
	ua := UserAgent()
	if orig := req.Header.Get("User-Agent"); orig != "" && !strings.HasPrefix(orig, "audd-cli/") {
		ua += " " + orig
	}
	r.Header.Set("User-Agent", ua)
	if t.debug == nil {
		return t.base.RoundTrip(r)
	}
	start := time.Now()
	t.debug("debug: → %s %s", r.Method, Redact(r.URL.String()))
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		t.debug("debug: ✗ %s %s: %s (%s)", r.Method, Redact(r.URL.String()), Redact(err.Error()), time.Since(start).Round(time.Millisecond))
		return nil, err
	}
	t.debug("debug: ← %d %s %s (%s)", resp.StatusCode, r.Method, Redact(r.URL.String()), time.Since(start).Round(time.Millisecond))
	return resp, nil
}

func rebase(r *http.Request, base *url.URL) {
	r.URL.Scheme = base.Scheme
	r.URL.Host = base.Host
	p := path.Join("/", base.Path, r.URL.Path)
	if strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	r.URL.Path = p
	r.URL.RawPath = ""
	r.Host = base.Host
}

// Redact hides token values in URLs and messages.
func Redact(s string) string { return output.Redact(s) }
