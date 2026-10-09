package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// session makes sure the client has an initialized session for token.
func (c *Client) sessionLocked(ctx context.Context, token string) error {
	h := hashToken(token)
	if c.ready && c.tokHash == h {
		return nil
	}
	c.tokHash = h
	if e := c.loadCache(h); e != nil {
		c.sid, c.proto, c.ready = e.SessionID, e.Protocol, true
		if len(e.Tools) > 0 {
			c.tools, c.toolsFor = e.Tools, h
		}
		return nil
	}
	return c.initializeLocked(ctx, token)
}

func (c *Client) initializeLocked(ctx context.Context, token string) error {
	c.ready, c.sid, c.proto = false, "", ""
	c.seq++
	r, hdr, err := c.post(ctx, token, "", "", c.seq, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "audd-cli", "version": strings.TrimPrefix(c.ua, "audd-cli/")},
	})
	if err != nil {
		return err
	}
	if r.Error != nil {
		return unexpected("the AudD account service refused to start a session: %s", r.Error.Message)
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(r.Result, &res)
	c.sid = hdr.Get("Mcp-Session-Id")
	c.proto = res.ProtocolVersion
	if c.proto == "" {
		c.proto = ProtocolVersion
	}
	if _, _, err := c.post(ctx, token, c.sid, c.proto, -1, "notifications/initialized", nil); err != nil {
		return err
	}
	c.ready = true
	c.saveCacheLocked()
	return nil
}

// request sends a request in the current session, starting a new session
// once if the server forgot the old one, and refreshing the access token once
// if the server stopped accepting it.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	sessionRetried, refreshed := false, false
	for {
		var r *rpcResponse
		err := c.sessionLocked(ctx, token)
		if err == nil {
			c.seq++
			r, _, err = c.post(ctx, token, c.sid, c.proto, c.seq, method, params)
		}
		if errors.Is(err, errUnauthorized) {
			c.dropCacheLocked()
			if c.refresh == nil || refreshed {
				return nil, loginRequired()
			}
			refreshed = true
			if token, err = c.refresh(ctx); err != nil {
				return nil, err
			}
			continue
		}
		if errors.Is(err, errSessionGone) || (err == nil && r.Error != nil && isSessionError(r.Error) && c.sid != "") {
			c.dropCacheLocked()
			if !sessionRetried {
				sessionRetried = true
				continue
			}
			return nil, unexpected("the AudD account service keeps losing the session")
		}
		if err != nil {
			return nil, err
		}
		if r.Error != nil {
			return nil, c.rpcErr(method, params, r.Error)
		}
		return r.Result, nil
	}
}

func (c *Client) rpcErr(method string, params any, e *rpcError) error {
	tool := toolName(method, params)
	l := strings.ToLower(e.Message)
	if tool != "" && (strings.Contains(l, "unknown tool") || strings.Contains(l, "not found") || strings.Contains(l, "not allowed") || strings.Contains(l, "scope")) {
		return scopeMissing(tool, ToolScopes[tool])
	}
	return &output.Error{Code: "account_error", Message: "AudD account service: " + e.Message, Exit: output.ExitUnexpected}
}

// ListTools returns the tools available to this sign-in.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	h := hashToken(token)
	c.mu.Lock()
	if c.toolsFor == h && c.tools != nil {
		t := c.tools
		c.mu.Unlock()
		return t, nil
	}
	if e := c.loadCache(h); e != nil && len(e.Tools) > 0 {
		c.tools, c.toolsFor = e.Tools, h
		c.mu.Unlock()
		return e.Tools, nil
	}
	c.mu.Unlock()
	var all []Tool
	cursor := ""
	for page := 0; page < 20; page++ {
		var params map[string]any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, unexpected("cannot read the tool list: %v", err)
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	c.mu.Lock()
	c.tools, c.toolsFor = all, h
	if c.ready {
		c.saveCacheLocked()
	}
	c.mu.Unlock()
	return all, nil
}

// CallTool calls a tool. It returns the structured result (nil when the
// tool returned none) and the text content. A result with isError set is
// returned as an *output.Error.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (structured map[string]any, text string, err error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, "", unexpected("cannot read the result of %s: %v", name, err)
	}
	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	text = strings.Join(parts, "\n")
	if res.IsError {
		return nil, text, ToolError(name, text)
	}
	if len(res.StructuredContent) > 0 && string(res.StructuredContent) != "null" {
		dec := json.NewDecoder(bytes.NewReader(res.StructuredContent))
		dec.UseNumber()
		if err := dec.Decode(&structured); err != nil {
			return nil, text, unexpected("the result of %s is not a JSON object", name)
		}
	}
	return structured, text, nil
}
