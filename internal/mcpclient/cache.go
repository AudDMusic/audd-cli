package mcpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/AudDMusic/audd-cli/internal/secrets"
)

type cached struct {
	TokenHash string    `json:"token_hash"`
	BaseURL   string    `json:"base_url"`
	SessionID string    `json:"session_id"`
	Protocol  string    `json:"protocol_version"`
	SavedAt   time.Time `json:"saved_at"`
	Tools     []Tool    `json:"tools,omitempty"`
}

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:8])
}

func (c *Client) loadCache(tokHash string) *cached {
	if c.cachePath == "" {
		return nil
	}
	b, err := os.ReadFile(c.cachePath)
	if err != nil {
		return nil
	}
	var e cached
	if json.Unmarshal(b, &e) != nil || e.TokenHash != tokHash || e.BaseURL != c.base || c.now().Sub(e.SavedAt) > SessionTTL || c.now().Before(e.SavedAt) {
		return nil
	}
	return &e
}

func (c *Client) saveCacheLocked() {
	if c.cachePath == "" {
		return
	}
	e := cached{TokenHash: c.tokHash, BaseURL: c.base, SessionID: c.sid, Protocol: c.proto, SavedAt: c.now()}
	if c.toolsFor == c.tokHash {
		e.Tools = c.tools
	}
	b, _ := json.Marshal(e)
	_ = os.MkdirAll(filepath.Dir(c.cachePath), 0o700)
	_ = secrets.WriteFileAtomic(c.cachePath, b, 0o600)
}

func (c *Client) dropCacheLocked() {
	c.ready, c.sid, c.proto = false, "", ""
	if c.cachePath != "" {
		_ = os.Remove(c.cachePath)
	}
}
