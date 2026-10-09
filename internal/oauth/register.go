package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// clientFor returns the client ID to sign in with at m's issuer: always
// FirstPartyClientID at AudD's server. Elsewhere (AUDD_MCP_URL), a client
// registered earlier with that server, or FirstPartyClientID, falling back
// to registration when the server does not know it.
func (c *Client) clientFor(m *metadata) string {
	if c.firstParty(m.Issuer) {
		return FirstPartyClientID
	}
	if c.profile.OAuthClientID != "" && sameIssuer(c.profile.OAuthIssuer, m.Issuer) {
		return c.profile.OAuthClientID
	}
	return FirstPartyClientID
}

func sameIssuer(a, b string) bool {
	return a != "" && strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// canRegister reports whether a failure of clientID at m's issuer may be
// answered by registering a client: never at AudD's server, and only once
// (when clientID is not already a registered one).
func (c *Client) canRegister(m *metadata, clientID string) bool {
	return !c.firstParty(m.Issuer) && clientID == FirstPartyClientID && m.RegistrationEndpoint != ""
}

// register registers the CLI with a server other than AudD's, after failed
// (the client ID the server just refused) was not accepted. A server that
// answers with failed again, or refuses the metadata, ends the sign-in.
func (c *Client) register(ctx context.Context, m *metadata, failed string) (string, error) {
	if m.RegistrationEndpoint == "" {
		return "", authErr("unsupported_server", "", "the authorization server does not know the CLI and does not support client registration")
	}
	reg := map[string]any{
		"client_name":                "AudD CLI",
		"redirect_uris":              []string{"http://127.0.0.1" + RedirectPath},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "native",
		"software_id":                "audd-cli",
		"software_version":           app.Version,
		"scope":                      strings.Join(RegisteredScopes, " "),
	}
	var resp struct {
		ClientID string `json:"client_id"`
	}
	b, _ := json.Marshal(reg)
	err := c.post(ctx, m.RegistrationEndpoint, "application/json", strings.NewReader(string(b)), &resp)
	var oe *oauthError
	if errors.As(err, &oe) {
		msg := oe.Description
		if msg == "" {
			msg = oe.Error()
		}
		return "", authErr("registration_refused", "", "the sign-in service refused to register the CLI: %s", msg)
	}
	if err != nil {
		return "", err
	}
	if resp.ClientID == "" {
		return "", &output.Error{Code: "unexpected_response", Message: "client registration returned no client_id", Exit: output.ExitNetwork}
	}
	if resp.ClientID == failed {
		return "", authErr("client_unavailable", "",
			"the sign-in service does not accept the CLI's client (%s), and registering returned the same client", failed)
	}
	c.profile.OAuthClientID = resp.ClientID
	c.profile.OAuthIssuer = m.Issuer
	if err := c.save(); err != nil {
		return "", err
	}
	return resp.ClientID, nil
}

// forgetClient drops a client registered with a server other than AudD's,
// so the next sign-in starts again from FirstPartyClientID.
func (c *Client) forgetClient() error {
	if c.profile.OAuthClientID == "" && c.profile.OAuthIssuer == "" {
		return nil
	}
	c.profile.OAuthClientID, c.profile.OAuthIssuer = "", ""
	return c.save()
}
