package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// LoginOptions configure a sign-in.
type LoginOptions struct {
	// Scopes to ask for (DefaultScopes when empty). api:request is left out.
	Scopes []string
	// KeepGranted also asks for the scopes of the saved sign-in when it was
	// made with the same client, and for DefaultScopes otherwise.
	KeepGranted bool
	// Method forces browser or device; MethodAuto picks from the environment.
	Method Method
	// NoBrowser stops the CLI from opening a browser (the URL is printed
	// either way).
	NoBrowser bool
	// In is where a pasted redirect URL is read from (browser method, with
	// a terminal on stdin).
	In io.Reader
}

// Login signs in. The browser method always prints the URL, opens the
// browser unless NoBrowser is set, listens on a loopback port, and, when
// stdin is a terminal, also accepts a pasted redirect URL, query string, or
// code. The device method prints a page and a code to approve on any
// device and polls until the user approves or denies, or the code expires.
// Without a terminal (agents), either method prints a login_pending record.
//
// Against AudD's server the CLI signs in as FirstPartyClientID. When the
// saved session belongs to another client (a client an earlier version
// registered), it is revoked after the new sign-in succeeds.
func (c *Client) Login(ctx context.Context, o LoginOptions) (*Tokens, error) {
	scopes := c.requestable(o.Scopes, true)
	if len(scopes) == 0 {
		scopes = union(DefaultScopes, nil)
	}
	m, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	if o.KeepGranted {
		if prev, _ := c.Stored(); prev != nil && prev.ClientID == c.clientFor(m) {
			scopes = union(scopes, c.requestable(prev.Scopes, false))
		} else {
			// The saved sign-in, if any, was made with another client (an
			// earlier version's registration): start from the defaults
			// rather than what that client was granted.
			scopes = union(scopes, DefaultScopes)
		}
	}
	method := o.Method
	if method == MethodAuto {
		method = c.env.Method()
	}
	if method == MethodDevice && m.DeviceEndpoint == "" {
		if o.Method == MethodDevice {
			return nil, authErr("device_unsupported", "audd login --browser", "the sign-in service does not support signing in with a code")
		}
		method = MethodBrowser
	}
	var t *Tokens
	if method == MethodDevice {
		t, err = c.deviceLogin(ctx, m, scopes, o)
	} else {
		t, err = c.login(ctx, m, scopes, !o.NoBrowser, o.In)
	}
	if err != nil {
		c.keepBlock()
	}
	if isCode(err, "scope_not_allowed") {
		e := output.AsError(err)
		e.Hint = "the AudD sign-in service does not grant these permissions to the CLI; do this at https://dashboard.audd.io instead"
		return nil, e
	}
	return t, err
}

// requestable drops api:request from scopes, saying so when note is set.
func (c *Client) requestable(scopes []string, note bool) []string {
	var out []string
	dropped := false
	for _, s := range scopes {
		if strings.TrimSpace(s) == NeverRequested {
			dropped = true
			continue
		}
		out = append(out, s)
	}
	if dropped && note {
		c.out.Info("Left out api:request: the CLI calls the API with your API token, so the sign-in does not need it.")
	}
	return union(out, nil)
}

func isCode(err error, code string) bool {
	var oe *output.Error
	return errors.As(err, &oe) && oe.Code == code
}

// login is the browser method.
func (c *Client) login(ctx context.Context, m *metadata, scopes []string, openBrowser bool, in io.Reader) (*Tokens, error) {
	clientID := c.clientFor(m)
	if c.canRegister(m, clientID) && m.DeviceEndpoint == "" {
		// A server without device sign-in is a plain MCP authorization
		// server, which only knows clients that registered with it.
		id, err := c.register(ctx, m, "")
		if err != nil {
			return nil, err
		}
		clientID = id
	}
	ln, err := listenLoopback()
	if err != nil {
		return nil, &output.Error{Code: "unexpected", Message: "cannot listen on a local port for the sign-in redirect: " + err.Error(),
			Hint: "use the printed URL in any browser and run audd auth login --complete '<redirect URL>'", Exit: output.ExitUnexpected}
	}
	defer ln.Close()
	p := &pending{
		State:       randomString(24),
		Verifier:    randomString(48),
		ClientID:    clientID,
		RedirectURI: fmt.Sprintf("http://127.0.0.1:%d%s", portOf(ln), RedirectPath),
		Issuer:      m.Issuer,
		Scopes:      scopes,
		Created:     c.now(),
	}
	pb, _ := json.Marshal(p)
	if err := c.sec.Set(c.profile.Name, keyPending, string(pb)); err != nil {
		return nil, err
	}
	before, _ := c.Stored()

	results := make(chan *callback, 8)
	srv := &http.Server{Handler: callbackHandler(p, results), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		// Let the browser page finish rendering before closing the listener.
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			srv.Close()
		}
	}()

	authURL := c.authURL(m, p)
	interactive := c.out.Options().StdinTTY && in != nil
	// On a terminal the instructions are erased once the sign-in succeeds
	// (see Client.signedIn and the login command).
	blk := c.out.TransientBlock()
	c.announce(authURL, interactive, openBrowser)
	if openBrowser {
		go func() {
			if err := c.open(authURL); err != nil {
				c.out.Info("Could not open a browser; open the URL above.")
			}
		}()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if interactive {
		go readPastes(ctx, c.lineReader(in), p, results, blk.CountInput)
	}
	tick := time.NewTicker(c.poll)
	defer tick.Stop()
	for {
		select {
		case cb := <-results:
			t, err := c.finish(ctx, m, p, cb)
			if cb.done != nil {
				cb.done <- err
			}
			if err != nil && cb.fromPaste && isInputError(err) {
				c.out.Warn("%s. Paste the full address from the browser.", output.AsError(err).Message)
				continue
			}
			return t, err
		case <-tick.C:
			// Finished by `audd auth login --complete` in another process?
			if cur, _ := c.loadPending(); cur == nil || cur.State != p.State {
				if t, _ := c.Stored(); t != nil && (before == nil || t.AccessToken != before.AccessToken) {
					c.profile.OAuthScopes = t.Scopes
					c.profile.OAuthClientID, c.profile.OAuthIssuer = t.ClientID, t.Issuer
					return t, c.save()
				}
				if cur != nil {
					return nil, authErr("login_superseded", "audd login", "another sign-in started for this profile")
				}
			}
		case <-ctx.Done():
			// On a server other than AudD's, the browser may have stopped
			// at an error page because the server no longer knows a client
			// registered with it; the next sign-in starts over. The pending
			// login keeps its own client ID, so --complete still works.
			if !c.firstParty(m.Issuer) {
				_ = c.forgetClient()
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, authErr("login_timeout", "run audd login again, or finish this one with audd auth login --complete '<redirect URL>'",
					"timed out waiting for the browser sign-in")
			}
			return nil, &output.Error{Code: "interrupted", Message: "sign-in stopped before it finished",
				Hint: "audd login", Exit: 130}
		}
	}
}

// lineReader returns the printer's shared stdin reader when in is the
// printer's stdin, so a line typed after Login returns is not lost.
func (c *Client) lineReader(in io.Reader) *output.LineReader {
	std := c.out.Options().Stdin
	if std != nil && reflect.TypeOf(in).Comparable() && reflect.TypeOf(std).Comparable() && in == std {
		return c.out.StdinLines()
	}
	return output.NewLineReader(in)
}

func isInputError(err error) bool { return isCode(err, "invalid_input") }

// announce prints the sign-in URL: for people on stderr, for agents (no
// terminal on stdin) as a login_pending record. The record goes to stdout as
// a JSONL line, switching an automatic JSON format to JSONL; when the format
// cannot carry it (explicit json or csv) it goes to stderr, so the URL is
// always printed.
func (c *Client) announce(authURL string, interactive, openBrowser bool) {
	complete := "audd auth login --complete '<redirect URL>'"
	if c.profile.Name != config.DefaultProfile {
		complete = "audd auth login --profile " + c.profile.Name + " --complete '<redirect URL>'"
	}
	if interactive || c.out.IsHuman() {
		if openBrowser {
			c.out.Warn("Sign in to AudD in your browser. If it did not open, visit:\n\n  %s\n", authURL)
		} else {
			c.out.Warn("Open this address in a browser to sign in:\n\n  %s\n", authURL)
		}
		if interactive {
			c.out.Warn("Waiting for the browser. If the browser is on another machine, approve there, copy the address it was sent to (http://127.0.0.1…), paste it here, and press Enter.")
		} else {
			c.out.Warn("Waiting up to %s for the browser. If the browser is on another machine, approve there, copy the address it was sent to (http://127.0.0.1…), and run:\n  %s", c.timeout.Round(time.Minute), complete)
		}
		return
	}
	rec := loginPending{
		SchemaVersion:    output.SchemaVersion,
		Type:             "login_pending",
		Method:           string(MethodBrowser),
		URL:              authURL,
		CompleteWith:     complete,
		ExpiresInSeconds: int(c.timeout / time.Second),
		Message:          "Open the URL in a browser and approve. On this machine the login finishes by itself; from another machine, copy the address the browser was sent to and run complete_with.",
	}
	c.emitPending(rec)
}

// emitPending prints a login_pending record. It is a streaming line, so the
// command's own output (the sign-in result, or the result of the command
// that needed more permissions) follows as a typed "result" line. An
// explicit --format json or csv keeps stdout to one document or to rows,
// which cannot carry the record: then it goes to stderr instead.
func (c *Client) emitPending(rec any) {
	c.out.SetStreaming()
	if c.out.Format() == output.FormatJSONL {
		_ = c.out.Event("login_pending", rec)
		return
	}
	b, _ := json.Marshal(rec)
	c.out.Warn("%s", b)
}

// loginPending tells agents where to sign in.
type loginPending struct {
	SchemaVersion    int    `json:"schema_version"`
	Type             string `json:"type"`
	Method           string `json:"method"`
	URL              string `json:"url"`
	CompleteWith     string `json:"complete_with"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	Message          string `json:"message"`
}

// finish validates an authorization response and exchanges the code.
func (c *Client) finish(ctx context.Context, m *metadata, p *pending, cb *callback) (*Tokens, error) {
	if cb.err != nil {
		return nil, cb.err
	}
	if cb.hasIss && strings.TrimRight(cb.iss, "/") != strings.TrimRight(m.Issuer, "/") {
		return nil, authErr("issuer_mismatch", "audd login", "the sign-in response came from %q, not the AudD sign-in service; start again", cb.iss)
	}
	if !cb.hasIss && !cb.bare && m.IssParameterSupported {
		return nil, authErr("issuer_mismatch", "audd login", "the sign-in response does not name its issuer; start again")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", cb.code)
	form.Set("redirect_uri", p.RedirectURI)
	form.Set("client_id", p.ClientID)
	form.Set("code_verifier", p.Verifier)
	form.Set("resource", c.resource)
	var tr tokenResponse
	if err := c.postForm(ctx, m.TokenEndpoint, form, &tr); err != nil {
		var oe *oauthError
		if errors.As(err, &oe) {
			if oe.Code == "invalid_client" && !c.firstParty(m.Issuer) {
				_ = c.forgetClient()
			}
			if cb.bare && oe.Code == "invalid_grant" {
				return nil, output.Errf(output.ExitAuth, "invalid_input", "", "the code was not accepted (%s)", oe.Error())
			}
			return nil, authErr("login_failed", "audd login", "the AudD sign-in service did not accept the sign-in: %s", oe.Error())
		}
		return nil, err
	}
	_ = c.sec.Delete(c.profile.Name, keyPending)
	return c.completeSignIn(ctx, m, &tr, p.Scopes, p.ClientID)
}

// completeSignIn stores the session a sign-in produced and records its
// client with the profile. When it replaces a session made with another
// client (one an earlier version registered), that session's refresh token
// is revoked with its own client ID; a failure is only noted.
func (c *Client) completeSignIn(ctx context.Context, m *metadata, tr *tokenResponse, requested []string, clientID string) (*Tokens, error) {
	if tr.AccessToken == "" {
		return nil, &output.Error{Code: "unexpected_response", Message: "the token response has no access token", Exit: output.ExitNetwork}
	}
	prev, _ := c.Stored()
	oldClient := ""
	if prev != nil {
		oldClient = prev.ClientID
		if oldClient == "" {
			oldClient = c.profile.OAuthClientID
		}
	}
	t := c.tokensFrom(tr, nil, requested, m.Issuer, clientID)
	t.Requested = requested
	if t.RefreshToken == "" && prev != nil && oldClient == clientID {
		t.RefreshToken = prev.RefreshToken
	}
	if err := c.store(t); err != nil {
		return nil, err
	}
	c.profile.OAuthScopes = t.Scopes
	c.profile.OAuthClientID, c.profile.OAuthIssuer = clientID, m.Issuer
	if err := c.save(); err != nil {
		return nil, err
	}
	if prev != nil && oldClient != "" && oldClient != clientID && prev.RefreshToken != "" {
		old := *prev
		old.ClientID = oldClient
		old.AccessToken = ""
		if err := c.revoke(ctx, &old); err != nil {
			c.noteAfter("Signed in, but could not end the previous sign-in on the server (%s). You can remove it at https://dashboard.audd.io under MCP and applications.",
				output.AsError(err).Message)
		}
	}
	return t, nil
}

// CompletePending finishes a login started elsewhere (usually a login that
// printed login_pending) from the redirect URL the browser was sent to.
func (c *Client) CompletePending(ctx context.Context, redirectURL string) (*Tokens, error) {
	p, err := c.loadPending()
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, authErr("no_pending_login", "audd login", "there is no sign-in waiting to be completed for profile %s", c.profile.Name)
	}
	if c.now().Sub(p.Created) > 30*time.Minute {
		_ = c.sec.Delete(c.profile.Name, keyPending)
		return nil, authErr("login_timeout", "audd login", "the sign-in waiting to be completed has expired; start again")
	}
	cb, perr := parseCallback(redirectURL, p)
	if perr != nil {
		return nil, output.Errf(output.ExitUsage, "invalid_argument", "pass the full address the browser was sent to, in quotes",
			"cannot read the redirect URL: %v", perr)
	}
	cb.fromPaste = true
	m, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	if p.Issuer != "" && strings.TrimRight(p.Issuer, "/") != strings.TrimRight(m.Issuer, "/") {
		return nil, authErr("issuer_mismatch", "audd login", "the pending sign-in was started with a different sign-in service; start again")
	}
	t, err := c.finish(ctx, m, p, cb)
	if err != nil && isInputError(err) {
		e := output.AsError(err)
		e.Exit, e.Code = output.ExitAuth, "login_failed"
	}
	return t, err
}
