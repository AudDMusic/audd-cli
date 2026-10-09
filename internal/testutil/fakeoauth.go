package testutil

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// FakeOAuth is an OAuth 2.0 authorization server for tests, shaped like
// AudD's: RFC 8414 metadata, the pre-registered public client audd-cli,
// dynamic client registration, an /authorize endpoint that approves
// immediately (a 302 to the client's redirect URI), device authorization
// (RFC 8628) whose polls follow DeviceScript, a token endpoint with PKCE
// checks and single-use rotating refresh tokens, and a revocation endpoint
// that only revokes for the issuing client.
type FakeOAuth struct {
	Server *httptest.Server

	mu sync.Mutex
	// Behavior knobs; set before the flow starts.
	IssOverride   string   // iss sent on the redirect ("" = the real issuer)
	OmitIss       bool     // leave iss off the redirect
	StateOverride string   // state sent on the redirect ("" = echo)
	DenyScopes    []string // scopes the server refuses with invalid_scope
	// RejectRegistrationScope refuses registrations that name a scope list
	// (invalid_client_metadata); clients registered without one get
	// DefaultGrantScopes.
	RejectRegistrationScope bool
	AccessTTL               time.Duration // access token lifetime (default 1h)
	RefreshDelay            time.Duration // slow down refresh responses

	// NoFirstPartyClient models a server that does not know audd-cli.
	NoFirstPartyClient bool
	// NoDeviceEndpoint leaves device authorization out of the metadata.
	NoDeviceEndpoint bool
	// Untick lists scopes the user unticks on the consent or approval
	// screen: they are left out of the grant.
	Untick []string
	// DeviceScript is the answer to each device poll in turn: "pending",
	// "slow_down", "slow_down_429" (429 with no interval), "deny",
	// "expire", "invalid_grant", or "approve". After the script, polls
	// are approved.
	DeviceScript []string
	// DeviceInterval and DeviceExpiresIn are sent with each device code
	// (default 5 and 900, like AudD's server).
	DeviceInterval, DeviceExpiresIn int
	// DeviceRetryAfter, when set, answers device authorization with 429
	// and this Retry-After.
	DeviceRetryAfter int
	// RegisterReturns makes registration answer with this client_id
	// without creating a client (AudD's answer to an "AudD CLI"
	// registration is audd-cli itself).
	RegisterReturns string
	// RegisterError refuses registrations with 400 and this error code.
	RegisterError, RegisterErrorDescription string

	clients  map[string]fakeClient
	codes    map[string]fakeCode
	access   map[string][]string // access token → scopes
	refresh  map[string]fakeGrant
	devices  map[string]*fakeDevice
	seq      int
	Counters FakeOAuthCounters
	Revoked  []string
	// Revocations lists each revocation request.
	Revocations []Revocation
	// Scopes lists the scope parameter of each authorization and device
	// authorization request, in order.
	Scopes []string
	// ClientIDs lists the client_id of each authorization and device
	// authorization request, in order.
	ClientIDs []string
}

// Revocation is one request to the revocation endpoint.
type Revocation struct{ Token, ClientID string }

type fakeDevice struct {
	ClientID string
	Scopes   []string
	UserCode string
	polls    int
	redeemed bool
	access   string
	refresh  string
}

// FakeOAuthCounters counts requests per endpoint.
type FakeOAuthCounters struct {
	Register, Authorize, CodeExchange, Refresh, RefreshRejected, Revoke int
	// DeviceAuthorize counts device authorization requests, DevicePoll
	// device-code token requests, DeviceSuccess polls answered 200, and
	// DevicePollAfterSuccess polls for a code that was already redeemed.
	DeviceAuthorize, DevicePoll, DeviceSuccess, DevicePollAfterSuccess int
}

// FirstPartyClientID is AudD's pre-registered CLI client.
const FirstPartyClientID = "audd-cli"

// FirstPartyScopes are the scopes audd-cli may ask for.
var FirstPartyScopes = []string{"openid", "email", "profile:read", "account:read", "usage:read", "billing:read", "billing:pay", "token:read", "token:write"}

type fakeClient struct {
	Redirects  []string
	Scopes     []string // scopes the client may ask for
	FirstParty bool     // may use device authorization
}

// DefaultGrantScopes are granted to a client registered without a scope
// list: like the AudD service, they leave out billing:pay and token:write.
var DefaultGrantScopes = []string{"openid", "profile:read", "account:read", "usage:read", "billing:read", "token:read"}

type fakeCode struct {
	ClientID, Redirect, Challenge string
	Scopes                        []string
}

type fakeGrant struct {
	ClientID string
	Scopes   []string
}

// NewFakeOAuth starts a fake authorization server; it stops when the test ends.
func NewFakeOAuth(t testing.TB) *FakeOAuth {
	f := &FakeOAuth{
		clients: map[string]fakeClient{},
		codes:   map[string]fakeCode{},
		access:  map[string][]string{},
		refresh: map[string]fakeGrant{},
		devices: map[string]*fakeDevice{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/device_authorization", f.deviceAuthorization)
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<!doctype html><title>Connect a device</title>")
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", f.metadata)
	mux.HandleFunc("/register", f.register)
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/revoke", f.revoke)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the issuer.
func (f *FakeOAuth) URL() string { return f.Server.URL }

// Snapshot returns a copy of the counters.
func (f *FakeOAuth) Snapshot() FakeOAuthCounters {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Counters
}

// RevokedTokens returns tokens sent to the revocation endpoint.
func (f *FakeOAuth) RevokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Revoked...)
}

// Set changes behavior knobs under the server's lock.
func (f *FakeOAuth) Set(fn func(f *FakeOAuth)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// client returns a registered client, or audd-cli unless the server does
// not know it. Call with f.mu held.
func (f *FakeOAuth) clientLocked(id string) (fakeClient, bool) {
	if id == FirstPartyClientID && !f.NoFirstPartyClient {
		return fakeClient{
			Redirects:  []string{"http://127.0.0.1/callback", "http://localhost/callback", "http://[::1]/callback"},
			Scopes:     FirstPartyScopes,
			FirstParty: true,
		}, true
	}
	c, ok := f.clients[id]
	return c, ok
}

// grantLocked is what the user approves: the requested scopes minus Untick.
func (f *FakeOAuth) grantLocked(scopes []string) []string {
	var out []string
	for _, s := range scopes {
		if !slices.Contains(f.Untick, s) {
			out = append(out, s)
		}
	}
	return out
}

// RequestedScopes returns the scope parameter of each authorization and
// device authorization request.
func (f *FakeOAuth) RequestedScopes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Scopes...)
}

// RequestedClientIDs returns the client_id of each authorization and device
// authorization request.
func (f *FakeOAuth) RequestedClientIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ClientIDs...)
}

// RevocationRequests returns each revocation request.
func (f *FakeOAuth) RevocationRequests() []Revocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Revocation(nil), f.Revocations...)
}

// HasRefresh reports whether a refresh token is still valid.
func (f *FakeOAuth) HasRefresh(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.refresh[token]
	return ok
}

// IssueSession mints a session for clientID directly, as a sign-in by an
// earlier version would have, and returns its access and refresh tokens.
func (f *FakeOAuth) IssueSession(clientID string, scopes ...string) (access, refresh string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	access, refresh = fmt.Sprintf("access-%d", f.seq), fmt.Sprintf("refresh-%d", f.seq)
	f.access[access] = scopes
	f.refresh[refresh] = fakeGrant{ClientID: clientID, Scopes: scopes}
	return access, refresh
}

// RegisterClient adds a registered client, as an earlier sign-in would have.
func (f *FakeOAuth) RegisterClient(scopes ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("client-%d", f.seq)
	f.clients[id] = fakeClient{Redirects: []string{"http://127.0.0.1/callback"}, Scopes: scopes}
	return id
}

// ForgetClients drops every registered client, as when the server no longer
// knows a client ID the CLI cached.
func (f *FakeOAuth) ForgetClients() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients = map[string]fakeClient{}
}

// RestrictClient limits the scopes a registered client may ask for.
func (f *FakeOAuth) RestrictClient(id string, scopes ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.clients[id]
	c.Scopes = scopes
	f.clients[id] = c
}

// AccessScopes returns the scopes of a valid access token.
func (f *FakeOAuth) AccessScopes(token string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.access[token]
	return s, ok
}

// IssueAccess mints an access token directly (for MCP tests that skip login).
func (f *FakeOAuth) IssueAccess(scopes ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	tok := fmt.Sprintf("access-%d", f.seq)
	f.access[tok] = scopes
	return tok
}

// DropAccess invalidates every access token but keeps refresh tokens, as
// when the server revokes access tokens early.
func (f *FakeOAuth) DropAccess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access = map[string][]string{}
}

// RevokeAll invalidates every access and refresh token (an ended session).
func (f *FakeOAuth) RevokeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access = map[string][]string{}
	f.refresh = map[string]fakeGrant{}
}

func (f *FakeOAuth) metadata(w http.ResponseWriter, r *http.Request) {
	u := f.Server.URL
	f.mu.Lock()
	noDevice := f.NoDeviceEndpoint
	f.mu.Unlock()
	grants := []string{"authorization_code", "refresh_token"}
	md := map[string]any{}
	if !noDevice {
		md["device_authorization_endpoint"] = u + "/device_authorization"
		grants = append(grants, "urn:ietf:params:oauth:grant-type:device_code")
	}
	md["grant_types_supported"] = grants
	writeJSON(w, http.StatusOK, mergeMaps(md, map[string]any{
		"issuer":                                         u,
		"authorization_endpoint":                         u + "/authorize",
		"token_endpoint":                                 u + "/token",
		"registration_endpoint":                          u + "/register",
		"revocation_endpoint":                            u + "/revoke",
		"code_challenge_methods_supported":               []string{"S256"},
		"response_types_supported":                       []string{"code"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"authorization_response_iss_parameter_supported": true,
		"scopes_supported":                               []string{"openid", "profile:read", "account:read", "usage:read", "billing:read", "billing:pay", "token:read", "token:write", "api:request"},
	}))
}

func mergeMaps(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

func (f *FakeOAuth) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		ClientName              string   `json:"client_name"`
		Scope                   *string  `json:"scope"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || len(req.RedirectURIs) == 0 || req.TokenEndpointAuthMethod != "none" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_client_metadata"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Counters.Register++
	if f.RegisterError != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": f.RegisterError, "error_description": f.RegisterErrorDescription})
		return
	}
	if f.RegisterReturns != "" {
		writeJSON(w, http.StatusCreated, map[string]any{"client_id": f.RegisterReturns, "redirect_uris": req.RedirectURIs, "token_endpoint_auth_method": "none"})
		return
	}
	if req.Scope != nil && f.RejectRegistrationScope {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_client_metadata", "error_description": "scope is not accepted"})
		return
	}
	scopes := DefaultGrantScopes
	if req.Scope != nil {
		scopes = strings.Fields(*req.Scope)
	}
	f.seq++
	id := fmt.Sprintf("client-%d", f.seq)
	f.clients[id] = fakeClient{Redirects: req.RedirectURIs, Scopes: scopes}
	writeJSON(w, http.StatusCreated, map[string]any{"client_id": id, "redirect_uris": req.RedirectURIs, "token_endpoint_auth_method": "none"})
}

// loopbackMatches accepts any port on a registered loopback redirect (RFC 8252 §7.3).
func loopbackMatches(registered, got string) bool {
	ru, err1 := url.Parse(registered)
	gu, err2 := url.Parse(got)
	if err1 != nil || err2 != nil {
		return false
	}
	return ru.Scheme == gu.Scheme && ru.Hostname() == gu.Hostname() && ru.Path == gu.Path && gu.RawQuery == ""
}

func (f *FakeOAuth) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Counters.Authorize++
	f.Scopes = append(f.Scopes, q.Get("scope"))
	f.ClientIDs = append(f.ClientIDs, q.Get("client_id"))
	c, ok := f.clientLocked(q.Get("client_id"))
	redirect := q.Get("redirect_uri")
	okRedirect := false
	for _, reg := range c.Redirects {
		if loopbackMatches(reg, redirect) {
			okRedirect = true
		}
	}
	if !ok || !okRedirect || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	if f.StateOverride != "" {
		state = f.StateOverride
	}
	iss := f.Server.URL
	if f.IssOverride != "" {
		iss = f.IssOverride
	}
	v := url.Values{}
	scopes := strings.Fields(q.Get("scope"))
	for _, s := range scopes {
		allowed := false
		for _, a := range c.Scopes {
			allowed = allowed || a == s
		}
		if !allowed {
			v.Set("error", "invalid_scope")
			v.Set("error_description", "scope "+s+" is beyond this client's registration")
		}
		for _, d := range f.DenyScopes {
			if s == d {
				v.Set("error", "invalid_scope")
				v.Set("error_description", "scope "+s+" is not allowed for this client")
			}
		}
	}
	if v.Get("error") == "" {
		f.seq++
		code := fmt.Sprintf("code-%d", f.seq)
		f.codes[code] = fakeCode{ClientID: q.Get("client_id"), Redirect: redirect, Challenge: q.Get("code_challenge"), Scopes: f.grantLocked(scopes)}
		v.Set("code", code)
	}
	v.Set("state", state)
	if !f.OmitIss {
		v.Set("iss", iss)
	}
	http.Redirect(w, r, redirect+"?"+v.Encode(), http.StatusFound)
}

func (f *FakeOAuth) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	f.mu.Lock()
	delay := f.RefreshDelay
	f.mu.Unlock()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		f.mu.Lock()
		defer f.mu.Unlock()
		f.Counters.CodeExchange++
		code, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		if !ok || code.ClientID != r.PostForm.Get("client_id") || code.Redirect != r.PostForm.Get("redirect_uri") || code.Challenge != challenge {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "bad code or verifier"})
			return
		}
		f.issueLocked(w, code.ClientID, code.Scopes)
	case "refresh_token":
		if delay > 0 {
			time.Sleep(delay)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.Counters.Refresh++
		rt := r.PostForm.Get("refresh_token")
		g, ok := f.refresh[rt]
		delete(f.refresh, rt) // single use
		if !ok || g.ClientID != r.PostForm.Get("client_id") {
			f.Counters.RefreshRejected++
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "refresh token is invalid or was already used"})
			return
		}
		f.issueLocked(w, g.ClientID, g.Scopes)
	case "urn:ietf:params:oauth:grant-type:device_code":
		f.mu.Lock()
		defer f.mu.Unlock()
		f.devicePollLocked(w, r.PostForm.Get("device_code"), r.PostForm.Get("client_id"))
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
	}
}

func (f *FakeOAuth) deviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Counters.DeviceAuthorize++
	id := r.PostForm.Get("client_id")
	f.Scopes = append(f.Scopes, r.PostForm.Get("scope"))
	f.ClientIDs = append(f.ClientIDs, id)
	if f.DeviceRetryAfter > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(f.DeviceRetryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "invalid_request", "error_description": "too many device sign-ins"})
		return
	}
	c, ok := f.clientLocked(id)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
		return
	}
	if !c.FirstParty {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unauthorized_client"})
		return
	}
	scopes := strings.Fields(r.PostForm.Get("scope"))
	for _, s := range scopes {
		if !slices.Contains(c.Scopes, s) || slices.Contains(f.DenyScopes, s) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_scope", "error_description": "scope " + s + " is not allowed"})
			return
		}
	}
	f.seq++
	code := fmt.Sprintf("device-%d", f.seq)
	user := fakeUserCode(f.seq)
	f.devices[code] = &fakeDevice{ClientID: id, Scopes: f.grantLocked(scopes), UserCode: user}
	interval, expires := f.DeviceInterval, f.DeviceExpiresIn
	if interval == 0 {
		interval = 5
	}
	if expires == 0 {
		expires = 900
	}
	u := f.Server.URL
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               code,
		"user_code":                 user,
		"verification_uri":          u + "/device",
		"verification_uri_complete": u + "/device?user_code=" + user,
		"expires_in":                expires,
		"interval":                  interval,
	})
}

// fakeUserCode is a deterministic XXXX-XXXX code from AudD's alphabet.
func fakeUserCode(n int) string {
	const alphabet = "BCDFGHJKLMNPQRSTVWXZ"
	b := make([]byte, 8)
	for i := range b {
		b[i] = alphabet[(n*7+i*3)%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func (f *FakeOAuth) devicePollLocked(w http.ResponseWriter, code, clientID string) {
	f.Counters.DevicePoll++
	d, ok := f.devices[code]
	if !ok || d.ClientID != clientID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
		return
	}
	if d.redeemed {
		// A replay revokes what the code produced.
		f.Counters.DevicePollAfterSuccess++
		delete(f.access, d.access)
		delete(f.refresh, d.refresh)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "the device code was already used"})
		return
	}
	action := "approve"
	if d.polls < len(f.DeviceScript) {
		action = f.DeviceScript[d.polls]
	}
	d.polls++
	switch action {
	case "pending":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
	case "slow_down":
		interval := f.DeviceInterval
		if interval == 0 {
			interval = 5
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "slow_down", "interval": interval + 5})
	case "slow_down_429":
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "slow_down"})
	case "deny":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "access_denied"})
	case "expire":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expired_token"})
	case "invalid_grant":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
	default:
		d.redeemed = true
		f.Counters.DeviceSuccess++
		rec := httptest.NewRecorder()
		f.issueLocked(rec, d.ClientID, d.Scopes)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		d.access, _ = body["access_token"].(string)
		d.refresh, _ = body["refresh_token"].(string)
		writeJSON(w, http.StatusOK, body)
	}
}

func (f *FakeOAuth) issueLocked(w http.ResponseWriter, clientID string, scopes []string) {
	f.seq++
	at := fmt.Sprintf("access-%d", f.seq)
	rt := fmt.Sprintf("refresh-%d", f.seq)
	f.access[at] = scopes
	f.refresh[rt] = fakeGrant{ClientID: clientID, Scopes: scopes}
	ttl := f.AccessTTL
	if ttl == 0 {
		ttl = time.Hour
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  at,
		"token_type":    "Bearer",
		"expires_in":    int(ttl / time.Second),
		"refresh_token": rt,
		"scope":         strings.Join(scopes, " "),
	})
}

func (f *FakeOAuth) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Counters.Revoke++
	tok := r.PostForm.Get("token")
	f.Revoked = append(f.Revoked, tok)
	f.Revocations = append(f.Revocations, Revocation{Token: tok, ClientID: r.PostForm.Get("client_id")})
	// Like AudD's server: a request from another client answers 200 but
	// revokes nothing.
	if g, ok := f.refresh[tok]; !ok || g.ClientID == r.PostForm.Get("client_id") {
		delete(f.refresh, tok)
		delete(f.access, tok)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// FollowAuthorize plays the user's browser: it requests the authorization
// URL and follows the redirect to the CLI's loopback listener. Use it as the
// browser opener in tests.
func FollowAuthorize(authURL string) error {
	resp, err := http.Get(authURL)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("authorize: HTTP %d", resp.StatusCode)
	}
	return nil
}

// AuthorizeRedirect requests the authorization URL without following the
// redirect and returns the redirect URL (what a user would paste).
func AuthorizeRedirect(authURL string) (string, error) {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return "", fmt.Errorf("authorize: HTTP %d", resp.StatusCode)
	}
	return resp.Header.Get("Location"), nil
}
