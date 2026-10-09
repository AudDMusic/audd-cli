// Package oauth signs the CLI in to the AudD account service: discovery
// (RFC 9728 protected resource metadata, RFC 8414 server metadata), the
// pre-registered public client audd-cli, two ways to approve a sign-in
// (authorization code with PKCE (RFC 7636) through a loopback redirect or a
// pasted redirect URL, and the device authorization grant (RFC 8628)),
// refresh under a cross-process file lock, step-up consent for extra scopes,
// and revocation. Dynamic client registration (RFC 7591) is only a fallback
// for other authorization servers.
//
// Sessions are stored per profile in the secrets store under "oauth"; an
// unfinished login is kept under "oauth_pending" so another invocation can
// complete it with `audd auth login --complete`.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/browser"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
)

// DefaultResourceURL is the AudD MCP server, the resource the CLI signs in to.
const DefaultResourceURL = "https://mcp.audd.io"

// DefaultScopes are requested by `audd login`: everything the CLI uses.
// The user can untick any of them; commands that need an unticked one ask
// for it again (step-up).
var DefaultScopes = []string{"openid", "profile:read", "account:read", "usage:read", "billing:read", "billing:pay", "token:read", "token:write"}

// RegisteredScopes are the scopes the CLI registers its client for (on
// servers other than AudD's): every scope it may ever ask for.
var RegisteredScopes = []string{"openid", "profile:read", "account:read", "usage:read", "billing:read", "billing:pay", "token:read", "token:write"}

// NeverRequested is left out of every sign-in: the CLI calls the API with
// the API token, and the AudD sign-in service refuses the whole request when
// it is asked for.
const NeverRequested = "api:request"

// FirstPartyClientID is the AudD CLI's pre-registered public client.
const FirstPartyClientID = "audd-cli"

// AudDIssuer is AudD's authorization server. Against it the CLI always uses
// FirstPartyClientID and never registers a client.
const AudDIssuer = "https://dashboard-api.audd.io"

// PollUnit is the length of one second of a device sign-in's polling
// interval and lifetime. Tests shorten it.
var PollUnit = time.Second

// LoginTimeout is how long Login waits for the browser.
const LoginTimeout = 10 * time.Minute

// RedirectPath is the loopback callback path registered with the server.
const RedirectPath = "/callback"

// Secret keys.
const (
	keyTokens   = "oauth"
	keyPending  = "oauth_pending"
	keyLoginAPI = "login_api_token"
)

// OpenBrowser opens a URL in the user's browser. Tests replace it.
var OpenBrowser = func(u string) error {
	browser.Stdout, browser.Stderr = io.Discard, io.Discard
	return browser.OpenURL(u)
}

// Tokens is a stored OAuth session.
type Tokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry"`
	Scopes       []string  `json:"scopes,omitempty"`
	// Requested are the scopes the sign-in asked for. The user may untick
	// some, so Scopes can be narrower.
	Requested []string `json:"requested_scopes,omitempty"`
	Issuer    string   `json:"issuer,omitempty"`
	ClientID  string   `json:"client_id,omitempty"`
}

// HasScopes reports whether every scope in needed was granted.
func (t *Tokens) HasScopes(needed ...string) bool {
	return len(missing(t.Scopes, needed)) == 0
}

// Client signs in and keeps the session for one profile.
type Client struct {
	profile  *config.Profile
	sec      secrets.Store
	out      *output.Printer
	resource string
	http     *http.Client
	open     func(string) error
	now      func() time.Time
	save     func() error
	timeout  time.Duration
	lockDir  string
	poll     time.Duration
	env      Environment
	method   Method
	// firstParty reports whether an issuer is AudD's (tests mark a fake).
	firstParty func(issuer string) bool
	// sleep waits between device polls; tests record the intervals.
	sleep func(ctx context.Context, d time.Duration) error

	mu     sync.Mutex // serializes refreshes within the process
	metaMu sync.Mutex
	meta   *metadata

	notesMu sync.Mutex
	// notes are warnings held back while a sign-in's waiting block is on
	// screen; they print after the block (see FlushNotes).
	notes []string
}

// Option configures a Client.
type Option func(*Client)

// WithResourceURL sets the protected resource (default https://mcp.audd.io).
func WithResourceURL(u string) Option {
	return func(c *Client) { c.resource = strings.TrimRight(u, "/") }
}

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithBrowserOpener replaces OpenBrowser for this client.
func WithBrowserOpener(f func(url string) error) Option { return func(c *Client) { c.open = f } }

// WithNow sets the clock.
func WithNow(f func() time.Time) Option { return func(c *Client) { c.now = f } }

// WithSaveConfig is called after the profile changes (client ID, scopes,
// account email) so the caller can persist the config file.
func WithSaveConfig(f func() error) Option { return func(c *Client) { c.save = f } }

// WithLoginTimeout changes how long Login waits (default 10 minutes).
func WithLoginTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithLockDir sets where the refresh lock file lives (default the data dir).
func WithLockDir(dir string) Option { return func(c *Client) { c.lockDir = dir } }

// WithEnvironment sets what method selection looks at (default: this process).
func WithEnvironment(e Environment) Option { return func(c *Client) { c.env = e } }

// WithMethod sets the method for sign-ins this client starts on its own
// (step-up). MethodAuto picks one from the environment.
func WithMethod(m Method) Option { return func(c *Client) { c.method = m } }

// New returns a Client for profile.
func New(profile *config.Profile, sec secrets.Store, out *output.Printer, opts ...Option) *Client {
	c := &Client{
		profile:  profile,
		sec:      sec,
		out:      out,
		resource: DefaultResourceURL,
		http:     &http.Client{Timeout: 30 * time.Second},
		now:      time.Now,
		save:     func() error { return nil },
		timeout:  LoginTimeout,
		lockDir:  config.DataDir(),
		poll:     time.Second,
		firstParty: func(issuer string) bool {
			return strings.TrimRight(issuer, "/") == AudDIssuer
		},
		sleep: sleepCtx,
	}
	if out != nil {
		o := out.Options()
		c.env = DetectEnvironment(o.StdinTTY, o.StdoutTTY)
	}
	for _, o := range opts {
		o(c)
	}
	if c.open == nil {
		c.open = func(u string) error { return OpenBrowser(u) }
	}
	return c
}

// ResourceURL is the protected resource this client signs in to.
func (c *Client) ResourceURL() string { return c.resource }

const docsURL = "https://docs.audd.io/mcp"

func loginRequired(msg string) *output.Error {
	return &output.Error{Code: "login_required", Message: msg, Hint: "audd login", Exit: output.ExitAuth, DocsURL: docsURL}
}

func authErr(code, hint, format string, args ...any) *output.Error {
	e := output.Errf(output.ExitAuth, code, hint, format, args...)
	e.DocsURL = docsURL
	return e
}

func networkErr(err error) *output.Error {
	return &output.Error{Code: "network", Message: "cannot reach the AudD sign-in service: " + err.Error(),
		Hint: "check your connection and try again", Exit: output.ExitNetwork, Retryable: true}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// oauthError is an error response from the authorization server.
type oauthError struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
	// Interval is the new polling interval a device slow_down carries.
	Interval int `json:"interval"`
	// RetryAfter is the Retry-After header in seconds (0 when absent).
	RetryAfter int `json:"-"`
}

func (e *oauthError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

func (c *Client) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return networkErr(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return &output.Error{Code: "server", Message: fmt.Sprintf("the AudD sign-in service returned HTTP %d for %s", resp.StatusCode, u),
			Exit: output.ExitNetwork, Retryable: resp.StatusCode >= 500}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &output.Error{Code: "unexpected_response", Message: "the AudD sign-in service returned an unexpected response from " + u, Exit: output.ExitNetwork}
	}
	return nil
}

func (c *Client) post(ctx context.Context, u string, contentType string, body io.Reader, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return networkErr(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		oe := &oauthError{Status: resp.StatusCode}
		_ = json.Unmarshal(b, oe)
		if ra, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && ra > 0 {
			oe.RetryAfter = ra
		}
		if oe.Code == "" && resp.StatusCode >= 500 {
			return &output.Error{Code: "server", Message: fmt.Sprintf("the AudD sign-in service returned HTTP %d", resp.StatusCode),
				Exit: output.ExitNetwork, Retryable: true}
		}
		return oe
	}
	if v == nil || len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return &output.Error{Code: "unexpected_response", Message: "the AudD sign-in service returned an unexpected response", Exit: output.ExitNetwork}
	}
	return nil
}

func (c *Client) postForm(ctx context.Context, u string, form url.Values, v any) error {
	return c.post(ctx, u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), v)
}

func userAgent() string { return "audd-cli/" + app.Version }
