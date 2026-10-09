package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// callback is an authorization response from the loopback listener or a paste.
type callback struct {
	code      string
	iss       string
	hasIss    bool
	bare      bool // a bare code was pasted
	fromPaste bool
	err       error      // the response itself is an error (state mismatch, denied)
	done      chan error // loopback: receives the outcome for the browser page
}

// parseCallback reads an authorization response: a full redirect URL, its
// query string, or a bare code.
func parseCallback(input string, p *pending) (*callback, error) {
	s := strings.TrimSpace(input)
	s = strings.Trim(s, `"'`)
	if s == "" {
		return nil, errors.New("empty input")
	}
	var q url.Values
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("not a URL: %v", err)
		}
		q = u.Query()
	case strings.HasPrefix(s, "?") || strings.Contains(s, "code=") || strings.Contains(s, "error=") || strings.Contains(s, "state="):
		var err error
		q, err = url.ParseQuery(strings.TrimPrefix(s, "?"))
		if err != nil {
			return nil, fmt.Errorf("not a query string: %v", err)
		}
	default:
		if strings.ContainsAny(s, " \t&=?/") {
			return nil, errors.New("expected the redirect URL, its query string, or the code")
		}
		return &callback{code: s, bare: true}, nil
	}
	return callbackFromQuery(q, p), nil
}

func callbackFromQuery(q url.Values, p *pending) *callback {
	cb := &callback{code: q.Get("code"), iss: q.Get("iss"), hasIss: q.Has("iss")}
	if e := q.Get("error"); e != "" {
		cb.err = authorizeError(e, q.Get("error_description"))
		return cb
	}
	if q.Get("state") != p.State {
		cb.err = authErr("state_mismatch", "audd login", "the sign-in response does not belong to this login (state mismatch); start again")
		return cb
	}
	if cb.code == "" {
		cb.err = authErr("login_failed", "audd login", "the sign-in response has no authorization code")
	}
	return cb
}

func authorizeError(code, desc string) *output.Error {
	msg := code
	if desc != "" {
		msg = desc
	}
	switch code {
	case "access_denied":
		return authErr("login_denied", "audd login", "sign-in was cancelled in the browser")
	case "invalid_scope":
		return authErr("scope_not_allowed", "https://dashboard.audd.io", "the AudD sign-in service did not allow the requested permissions: %s", msg)
	}
	return authErr("login_failed", "audd login", "sign-in failed: %s", msg)
}

// readPastes delivers lines pasted on the terminal as callbacks until ctx
// ends. Input that arrives after that stays with lines for its next reader.
func readPastes(ctx context.Context, lines *output.LineReader, p *pending, results chan<- *callback, echoed func(string)) {
	for {
		raw, err := lines.ReadLine(ctx)
		if err == nil {
			echoed(strings.TrimRight(raw, "\r\n")) // the terminal echoed it
		}
		if line := strings.TrimSpace(raw); line != "" {
			cb, perr := parseCallback(line, p)
			if perr != nil {
				cb = &callback{err: output.Errf(output.ExitAuth, "invalid_input", "", "that is not a sign-in address (%v)", perr)}
			}
			cb.fromPaste = true
			select {
			case results <- cb:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			return
		}
	}
}

const pageOK = `<!doctype html><meta charset="utf-8"><title>AudD CLI</title>
<body style="font-family:system-ui,sans-serif;max-width:32em;margin:4em auto">
<h1>Signed in</h1><p>You can close this tab and return to the terminal.</p>`

const pageFail = `<!doctype html><meta charset="utf-8"><title>AudD CLI</title>
<body style="font-family:system-ui,sans-serif;max-width:32em;margin:4em auto">
<h1>Sign-in did not finish</h1><p>%s</p><p>Return to the terminal for details.</p>`

func callbackHandler(p *pending, results chan<- *callback) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != RedirectPath || (!q.Has("code") && !q.Has("error")) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if q.Get("state") != p.State {
			// Not the response to this login (another local process, or an
			// old browser tab): say so and keep waiting.
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, pageFail, "This sign-in response does not belong to the sign-in in progress.")
			return
		}
		cb := callbackFromQuery(q, p)
		cb.done = make(chan error, 1)
		select {
		case results <- cb:
		case <-r.Context().Done():
			return
		}
		var err error
		select {
		case err = <-cb.done:
		case <-r.Context().Done():
			return
		case <-time.After(time.Minute):
			err = errors.New("timed out")
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, pageFail, htmlEscape(output.AsError(err).Message))
			return
		}
		io.WriteString(w, pageOK)
	})
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
