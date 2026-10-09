package oauth

import (
	"context"
	"errors"
	"net/url"

	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
)

// Logout revokes the refresh token and deletes the session, the pending
// login, and the API token fetched at login. Tokens the user set
// (config, environment, flag) are not touched. A failed revocation is
// reported as a warning; the local session is deleted regardless.
func (c *Client) Logout(ctx context.Context) error {
	t, err := c.Stored()
	if err != nil {
		return err
	}
	if t != nil && (t.RefreshToken != "" || t.AccessToken != "") {
		if err := c.revoke(ctx, t); err != nil {
			c.out.Warn("Could not revoke the sign-in on the server (%s); it was removed from this machine.", output.AsError(err).Message)
		}
	}
	var stuck error
	for _, k := range []string{keyTokens, keyPending, keyLoginAPI} {
		if err := c.sec.Delete(c.profile.Name, k); err != nil {
			if !errors.Is(err, secrets.ErrKeyringDelete) {
				return err
			}
			stuck = err
		}
	}
	if stuck != nil {
		// The sign-in is still in the system credential store, so the next
		// audd run would read it back: this is not a sign-out.
		return &output.Error{Code: "logout_incomplete", Exit: output.ExitUnexpected,
			Message: "the sign-in could not be removed from the system credential store (" + stuck.Error() + "), so this machine is still signed in",
			Hint:    "unlock the keychain or credential store and run audd logout again"}
	}
	c.profile.OAuthScopes = nil
	c.profile.Account = ""
	return c.save()
}

func (c *Client) revoke(ctx context.Context, t *Tokens) error {
	m, err := c.discover(ctx)
	if err != nil {
		return err
	}
	if m.RevocationEndpoint == "" {
		return errors.New("the server has no revocation endpoint")
	}
	tok, hint := t.RefreshToken, "refresh_token"
	if tok == "" {
		tok, hint = t.AccessToken, "access_token"
	}
	form := url.Values{}
	form.Set("token", tok)
	form.Set("token_type_hint", hint)
	clientID := t.ClientID
	if clientID == "" {
		clientID = c.profile.OAuthClientID
	}
	form.Set("client_id", clientID)
	return c.postForm(ctx, m.RevocationEndpoint, form, nil)
}
