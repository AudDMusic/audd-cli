package oauth

import (
	"context"
	"sort"
	"strings"
)

// ScopeDescriptions explain scopes in prompts.
var ScopeDescriptions = map[string]string{
	"openid":       "confirm who you are",
	"email":        "read your email address",
	"profile:read": "read your account email and sign-in methods",
	"account:read": "read your account details",
	"usage:read":   "read your usage",
	"billing:read": "read your plan and billing history",
	"billing:pay":  "create payment links",
	"token:read":   "read your API token",
	"token:write":  "rotate your API token",
}

// describeScopes is "create payment links (billing:pay), …".
func describeScopes(scopes []string) string {
	var what []string
	for _, s := range scopes {
		if d := ScopeDescriptions[s]; d != "" {
			what = append(what, d+" ("+s+")")
		} else {
			what = append(what, s)
		}
	}
	return strings.Join(what, ", ")
}

// EnsureScopes returns a session that has every scope in needed. When some
// are missing it signs in again (step-up), asking for the scopes granted
// now plus the missing ones, with the method chosen for this client.
func (c *Client) EnsureScopes(ctx context.Context, needed ...string) (*Tokens, error) {
	t, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	needed = c.requestable(needed, true)
	miss := missing(t.Scopes, needed)
	if len(miss) == 0 {
		return t, nil
	}
	var unticked []string
	for _, s := range miss {
		if contains(t.Requested, s) {
			unticked = append(unticked, s)
		}
	}
	if len(unticked) > 0 {
		c.out.Info("This needs permission to %s. It was not approved when you signed in (it was unticked). Approve it this time to continue.", describeScopes(miss))
	} else {
		c.out.Info("This needs permission to %s. Approve it to continue.", describeScopes(miss))
	}
	nt, err := c.Login(ctx, LoginOptions{Scopes: needed, KeepGranted: true, Method: c.method, In: c.out.Options().Stdin})
	if err != nil {
		return nil, err
	}
	if miss := missing(nt.Scopes, needed); len(miss) > 0 {
		c.keepBlock()
		return nil, authErr("scope_not_granted", "audd auth refresh --scopes "+strings.Join(miss, ","),
			"the sign-in did not include permission to %s, which this command needs; leave it ticked when you approve", describeScopes(miss))
	}
	c.signedIn()
	return nt, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func missing(have, needed []string) []string {
	var out []string
	for _, n := range needed {
		if !contains(have, n) {
			out = append(out, n)
		}
	}
	return out
}

// union returns the sorted union of a and b without duplicates.
func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Union is the sorted union of scope lists.
func Union(a, b []string) []string { return union(a, b) }
