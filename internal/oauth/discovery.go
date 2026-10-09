package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/output"
)

type metadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	DeviceEndpoint        string   `json:"device_authorization_endpoint"`
	IssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
}

// wellKnown inserts a well-known path between the host and the path of u.
func wellKnown(base, name string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid URL %q", base)
	}
	u.Path = "/.well-known/" + name + strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func (c *Client) discover(ctx context.Context) (*metadata, error) {
	c.metaMu.Lock()
	m := c.meta
	c.metaMu.Unlock()
	if m != nil {
		return m, nil
	}
	prURL, err := wellKnown(c.resource, "oauth-protected-resource")
	if err != nil {
		return nil, err
	}
	var pr struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := c.getJSON(ctx, prURL, &pr); err != nil {
		return nil, err
	}
	if len(pr.AuthorizationServers) == 0 {
		return nil, &output.Error{Code: "unexpected_response", Message: "the AudD account service did not name an authorization server", Exit: output.ExitNetwork}
	}
	issuer := strings.TrimRight(pr.AuthorizationServers[0], "/")
	m = &metadata{}
	asURL, err := wellKnown(issuer, "oauth-authorization-server")
	if err != nil {
		return nil, err
	}
	if err := c.getJSON(ctx, asURL, m); err != nil {
		oidc, _ := wellKnown(issuer, "openid-configuration")
		m = &metadata{}
		if err2 := c.getJSON(ctx, oidc, m); err2 != nil {
			return nil, err
		}
	}
	if strings.TrimRight(m.Issuer, "/") != issuer {
		return nil, authErr("issuer_mismatch", "", "the authorization server metadata names issuer %q, expected %q", m.Issuer, issuer)
	}
	if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
		return nil, &output.Error{Code: "unexpected_response", Message: "the authorization server metadata is incomplete", Exit: output.ExitNetwork}
	}
	if len(m.CodeChallengeMethods) > 0 && !contains(m.CodeChallengeMethods, "S256") {
		return nil, authErr("unsupported_server", "", "the authorization server does not support PKCE with S256")
	}
	c.metaMu.Lock()
	c.meta = m
	c.metaMu.Unlock()
	return m, nil
}
