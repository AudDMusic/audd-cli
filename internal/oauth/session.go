package oauth

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/secrets"
)

// Stored returns the saved session without refreshing it (nil when signed out).
func (c *Client) Stored() (*Tokens, error) {
	s, err := c.sec.Get(c.profile.Name, keyTokens)
	if errors.Is(err, secrets.ErrNotFound) || (err == nil && s == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Tokens
	if err := json.Unmarshal([]byte(s), &t); err != nil || t.AccessToken == "" {
		return nil, nil
	}
	return &t, nil
}

func (c *Client) store(t *Tokens) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return c.sec.Set(c.profile.Name, keyTokens, string(b))
}

// LoggedIn reports whether the profile has a saved session.
func (c *Client) LoggedIn() bool {
	t, err := c.Stored()
	return err == nil && t != nil
}

// tokenResponse is the token endpoint's success body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func (c *Client) tokensFrom(r *tokenResponse, prev *Tokens, requested []string, issuer, clientID string) *Tokens {
	t := &Tokens{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		TokenType:    r.TokenType,
		Issuer:       issuer,
		ClientID:     clientID,
	}
	if prev != nil {
		if t.RefreshToken == "" {
			t.RefreshToken = prev.RefreshToken
		}
		t.Requested = prev.Requested
	}
	switch {
	case r.Scope != "":
		t.Scopes = strings.Fields(r.Scope)
	case prev != nil && len(requested) == 0:
		t.Scopes = prev.Scopes
	default:
		t.Scopes = requested
	}
	sort.Strings(t.Scopes)
	if r.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return t
}

func (c *Client) expiring(t *Tokens) bool {
	return !t.Expiry.IsZero() && c.now().Add(60*time.Second).After(t.Expiry)
}
