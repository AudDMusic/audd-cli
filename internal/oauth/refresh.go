package oauth

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// Token returns a usable session, refreshing it (under a file lock shared by
// every audd process for this profile) when it expires within 60 seconds.
func (c *Client) Token(ctx context.Context) (*Tokens, error) {
	t, err := c.Stored()
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, loginRequired("not signed in to AudD")
	}
	if !c.expiring(t) {
		return t, nil
	}
	return c.refresh(ctx, t)
}

// Refresh gets a new access token now, whatever the expiry.
func (c *Client) Refresh(ctx context.Context) (*Tokens, error) {
	t, err := c.Stored()
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, loginRequired("not signed in to AudD")
	}
	return c.refresh(ctx, t)
}

// refresh exchanges the refresh token of seen. If another goroutine or
// process already replaced seen, the newer session is returned instead, so a
// single-use refresh token is only ever spent once.
func (c *Client) refresh(ctx context.Context, seen *Tokens) (*Tokens, error) {
	m, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := lockFile(ctx, filepath.Join(c.lockDir, "oauth-"+config.FileSafe(c.profile.Name)+".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	cur, err := c.Stored()
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, loginRequired("not signed in to AudD")
	}
	if cur.AccessToken != seen.AccessToken {
		return cur, nil
	}
	if cur.RefreshToken == "" {
		return nil, loginRequired("your AudD sign-in has expired")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", cur.RefreshToken)
	clientID := cur.ClientID
	if clientID == "" {
		clientID = c.profile.OAuthClientID
	}
	form.Set("client_id", clientID)
	form.Set("resource", c.resource)
	var tr tokenResponse
	if err := c.postForm(ctx, m.TokenEndpoint, form, &tr); err != nil {
		var oe *oauthError
		if errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "invalid_client" || oe.Code == "unauthorized_client") {
			return nil, loginRequired("your AudD sign-in has expired or was revoked")
		}
		return nil, err
	}
	if tr.AccessToken == "" {
		return nil, &output.Error{Code: "unexpected_response", Message: "the token response has no access token", Exit: output.ExitNetwork}
	}
	t := c.tokensFrom(&tr, cur, nil, cur.Issuer, clientID)
	if err := c.store(t); err != nil {
		return nil, err
	}
	return t, nil
}
