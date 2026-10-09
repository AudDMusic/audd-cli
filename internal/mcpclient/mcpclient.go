// Package mcpclient is a small Model Context Protocol client for the AudD
// account service: Streamable HTTP transport, JSON-RPC 2.0, tools/list and
// tools/call. The session ID is cached on disk for a few minutes so
// back-to-back commands skip the initialize handshake.
package mcpclient

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// ProtocolVersion is the MCP revision the client speaks.
const ProtocolVersion = "2025-06-18"

// SessionTTL is how long a cached session ID is reused.
const SessionTTL = 10 * time.Minute

// ToolScopes names the OAuth scope each AudD tool needs; used in hints.
var ToolScopes = map[string]string{
	"get_profile":            "profile:read",
	"get_account_status":     "account:read",
	"get_usage_stats":        "usage:read",
	"get_api_token":          "token:read",
	"rotate_api_token":       "token:write",
	"list_plans":             "billing:read",
	"get_billing_history":    "billing:read",
	"get_amount_owed":        "billing:read",
	"subscribe_to_plan":      "billing:pay",
	"create_renewal_payment": "billing:pay",
	"buy_bonus_requests":     "billing:pay",
}

// Tool describes a server tool.
type Tool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"inputSchema,omitempty"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
}

// Client talks to one MCP server.
type Client struct {
	base      string
	token     func(ctx context.Context) (string, error)
	refresh   func(ctx context.Context) (string, error)
	http      *http.Client
	cachePath string
	ua        string
	now       func() time.Time

	mu       sync.Mutex
	sid      string
	proto    string
	tokHash  string
	ready    bool
	tools    []Tool
	toolsFor string
	seq      int
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithSessionCache stores the session ID at path (e.g. CacheDir()/mcp-session-<profile>).
func WithSessionCache(path string) Option { return func(c *Client) { c.cachePath = path } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.ua = ua } }

// WithRefresh sets how to get a new access token, whatever its expiry, when
// the server rejects the current one (revoked early, clock skew). The
// request is then retried once.
func WithRefresh(f func(ctx context.Context) (string, error)) Option {
	return func(c *Client) { c.refresh = f }
}

// WithNow sets the clock.
func WithNow(f func() time.Time) Option { return func(c *Client) { c.now = f } }

// New returns a client for the MCP endpoint baseURL. token returns the
// current OAuth access token.
func New(baseURL string, token func(ctx context.Context) (string, error), opts ...Option) *Client {
	c := &Client{
		base:  baseURL,
		token: token,
		http:  &http.Client{Timeout: 60 * time.Second},
		ua:    "audd-cli",
		now:   time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}
