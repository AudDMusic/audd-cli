package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// deviceGrantType is the RFC 8628 grant type.
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// Defaults when the device authorization response leaves them out.
const (
	defaultDeviceInterval = 5   // seconds (RFC 8628 §3.2)
	defaultDeviceExpiry   = 900 // seconds
	// deviceGrace is how long after expires_in an approved code can still
	// be redeemed.
	deviceGrace = 60
)

// deviceAuth is the device authorization response.
type deviceAuth struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// devicePending tells agents where and how to approve a device sign-in.
type devicePending struct {
	SchemaVersion           int    `json:"schema_version"`
	Type                    string `json:"type"`
	Method                  string `json:"method"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	UserCode                string `json:"user_code"`
	ExpiresInSeconds        int    `json:"expires_in_seconds"`
	Interval                int    `json:"interval"`
	Message                 string `json:"message"`
}

// deviceLogin is the device method: ask for a code, show it, and poll the
// token endpoint until the user approves or denies, or the code expires.
func (c *Client) deviceLogin(ctx context.Context, m *metadata, scopes []string, o LoginOptions) (*Tokens, error) {
	clientID := c.clientFor(m)
	da, err := c.deviceAuthorize(ctx, m, clientID, scopes)
	var oe *oauthError
	if errors.As(err, &oe) && oe.Code == "invalid_client" && c.canRegister(m, clientID) {
		id, rerr := c.register(ctx, m, clientID)
		if rerr != nil {
			return nil, rerr
		}
		clientID = id
		da, err = c.deviceAuthorize(ctx, m, clientID, scopes)
	}
	if err != nil {
		return nil, c.deviceAuthorizeError(m, err)
	}
	if da.DeviceCode == "" || da.UserCode == "" || da.VerificationURI == "" {
		return nil, &output.Error{Code: "unexpected_response", Message: "the device sign-in response is incomplete", Exit: output.ExitNetwork}
	}
	if da.Interval <= 0 {
		da.Interval = defaultDeviceInterval
	}
	if da.ExpiresIn <= 0 {
		da.ExpiresIn = defaultDeviceExpiry
	}

	// Never let two processes poll the same code: a second redemption
	// revokes the tokens the first one received.
	sum := sha256.Sum256([]byte(da.DeviceCode))
	unlock, ok, err := tryLockFile(filepath.Join(c.lockDir, "oauth-device-"+hex.EncodeToString(sum[:8])+".lock"))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, authErr("login_in_progress", "audd login", "another audd process is already waiting for this sign-in")
	}
	defer unlock()

	c.out.TransientBlock() // erased once the sign-in succeeds
	c.announceDevice(da)
	open := da.VerificationURIComplete
	if open == "" {
		open = da.VerificationURI
	}
	if !o.NoBrowser && c.env.BrowserAvailable() {
		go func() { _ = c.open(open) }()
	}
	stop := c.out.Spinner("Waiting for you to approve the sign-in")
	tr, err := c.pollDevice(ctx, m, clientID, da)
	stop()
	if err != nil {
		return nil, err
	}
	return c.completeSignIn(ctx, m, tr, scopes, clientID)
}

func (c *Client) deviceAuthorize(ctx context.Context, m *metadata, clientID string, scopes []string) (*deviceAuth, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", strings.Join(scopes, " "))
	form.Set("resource", c.resource)
	var da deviceAuth
	if err := c.postForm(ctx, m.DeviceEndpoint, form, &da); err != nil {
		return nil, err
	}
	return &da, nil
}

func (c *Client) deviceAuthorizeError(m *metadata, err error) error {
	var oe *oauthError
	if !errors.As(err, &oe) {
		return err
	}
	switch {
	case oe.Status == 429:
		wait := "a while"
		if oe.RetryAfter > 0 {
			wait = waitText(oe.RetryAfter)
		}
		return &output.Error{Code: "rate_limited", Exit: output.ExitNetwork, Retryable: true, DocsURL: docsURL,
			Message: "too many sign-ins from this network in the last hour; try again in " + wait,
			Hint:    "wait " + wait + ", then run audd login again"}
	case oe.Code == "invalid_client" && c.firstParty(m.Issuer):
		return authErr("client_unavailable", "audd update", "the AudD sign-in service does not accept this version of the CLI (%s)", oe.Error())
	case oe.Code == "invalid_client" || oe.Code == "unauthorized_client":
		return authErr("device_unsupported", "audd login --browser", "the sign-in service does not allow signing in with a code for this client (%s)", oe.Error())
	case oe.Code == "invalid_scope":
		return authErr("scope_not_allowed", "", "the AudD sign-in service did not allow the requested permissions: %s", oe.Error())
	}
	return authErr("login_failed", "audd login", "the AudD sign-in service did not start the sign-in: %s", oe.Error())
}

// waitText says how long n seconds is, for people.
func waitText(n int) string {
	if n < 90 {
		return output.Plural(n, "second")
	}
	return output.Plural((n+59)/60, "minute")
}

// pollDevice polls the token endpoint at the server's interval. It stops at
// the first success and never polls that code again.
func (c *Client) pollDevice(ctx context.Context, m *metadata, clientID string, da *deviceAuth) (*tokenResponse, error) {
	interval := da.Interval
	limit := time.Duration(da.ExpiresIn+deviceGrace) * PollUnit
	var waited time.Duration
	form := url.Values{}
	form.Set("grant_type", deviceGrantType)
	form.Set("device_code", da.DeviceCode)
	form.Set("client_id", clientID)
	for {
		d := time.Duration(interval) * PollUnit
		if err := c.sleep(ctx, d); err != nil {
			return nil, &output.Error{Code: "interrupted", Message: "sign-in stopped before it finished", Hint: "audd login", Exit: 130}
		}
		waited += d
		var tr tokenResponse
		err := c.postForm(ctx, m.TokenEndpoint, form, &tr)
		if err == nil {
			return &tr, nil
		}
		var oe *oauthError
		if !errors.As(err, &oe) {
			if ctx.Err() != nil {
				return nil, &output.Error{Code: "interrupted", Message: "sign-in stopped before it finished", Hint: "audd login", Exit: 130}
			}
			// The network or the server failed; keep trying until the code
			// expires.
			if waited >= limit {
				return nil, deviceExpired()
			}
			continue
		}
		switch {
		case oe.Code == "slow_down" || oe.Status == 429:
			if oe.Interval > interval {
				interval = oe.Interval
			} else {
				interval += 5
			}
		case oe.Code == "authorization_pending":
		case oe.Code == "access_denied":
			return nil, authErr("login_denied", "run audd login again to start a new sign-in",
				"the sign-in was denied (or every permission was unticked)")
		case oe.Code == "expired_token":
			return nil, deviceExpired()
		case oe.Code == "invalid_grant":
			return nil, authErr("login_failed", "run audd login again",
				"the sign-in code is no longer valid (%s)", oe.Error())
		default:
			return nil, authErr("login_failed", "run audd login again", "the AudD sign-in service did not accept the sign-in: %s", oe.Error())
		}
		if waited >= limit {
			return nil, deviceExpired()
		}
	}
}

func deviceExpired() *output.Error {
	return authErr("login_expired", "run audd login again", "the sign-in code expired before it was approved")
}

// announceDevice shows the page and the code: for people a block on stderr,
// for agents (no terminal on stdin, and output that is not for people) a
// login_pending record.
func (c *Client) announceDevice(da *deviceAuth) {
	if c.out.Options().StdinTTY || c.out.IsHuman() {
		st := c.out.Styles()
		var b strings.Builder
		b.WriteString("\nTo sign in, open this page on any device:\n\n")
		if da.VerificationURIComplete != "" {
			fmt.Fprintf(&b, "  %s\n\nCheck that it shows this code, then approve:\n\n", st.Accent.Render(da.VerificationURIComplete))
		} else {
			fmt.Fprintf(&b, "  %s\n\nEnter this code, then approve:\n\n", st.Accent.Render(da.VerificationURI))
		}
		fmt.Fprintf(&b, "    %s\n\n", st.Bold.Render(spaced(da.UserCode)))
		fmt.Fprintf(&b, "The code expires in %s. Only approve a sign-in you started yourself.\n", waitText(da.ExpiresIn))
		c.out.Warn("%s", b.String())
		return
	}
	c.emitPending(devicePending{
		SchemaVersion:           output.SchemaVersion,
		Type:                    "login_pending",
		Method:                  string(MethodDevice),
		VerificationURI:         da.VerificationURI,
		VerificationURIComplete: da.VerificationURIComplete,
		UserCode:                da.UserCode,
		ExpiresInSeconds:        da.ExpiresIn,
		Interval:                da.Interval,
		Message: "Ask the user to open verification_uri_complete (or verification_uri and enter user_code), check that the page shows user_code, and approve. " +
			"This command keeps waiting and prints the result when they approve.",
	})
}

// spaced puts a space between the characters of a user code, so it reads
// as a larger block in the terminal: "BCDF-GHJK" becomes "B C D F - G H J K".
func spaced(code string) string {
	return strings.Join(strings.Split(code, ""), " ")
}
