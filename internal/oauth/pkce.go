package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/secrets"
)

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// pending is an unfinished login, saved so `--complete` can finish it.
type pending struct {
	State       string    `json:"state"`
	Verifier    string    `json:"verifier"`
	ClientID    string    `json:"client_id"`
	RedirectURI string    `json:"redirect_uri"`
	Issuer      string    `json:"issuer"`
	Scopes      []string  `json:"scopes"`
	Created     time.Time `json:"created"`
}

func (c *Client) loadPending() (*pending, error) {
	s, err := c.sec.Get(c.profile.Name, keyPending)
	if errors.Is(err, secrets.ErrNotFound) || (err == nil && s == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p pending
	if err := json.Unmarshal([]byte(s), &p); err != nil || p.State == "" {
		return nil, nil
	}
	return &p, nil
}

func (c *Client) authURL(m *metadata, p *pending) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("scope", strings.Join(p.Scopes, " "))
	q.Set("state", p.State)
	q.Set("code_challenge", challenge(p.Verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("resource", c.resource)
	sep := "?"
	if strings.Contains(m.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return m.AuthorizationEndpoint + sep + q.Encode()
}
