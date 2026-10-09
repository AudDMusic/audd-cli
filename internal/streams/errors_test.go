package streams

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams/streamstest"
	"github.com/AudDMusic/audd-cli/internal/streamstore"
)

// refusedClient returns an audd-go client whose requests all go to a port
// nothing listens on.
func refusedClient(t *testing.T) *audd.Client {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host, r.Host = "http", addr, addr
		return http.DefaultTransport.RoundTrip(r)
	})
	return audd.NewClient(streamstest.Token, audd.WithHTTPClient(&http.Client{Transport: rt}),
		audd.WithMaxAttempts(1), audd.WithBackoffFactor(time.Millisecond))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMapErrorRedactsTheTokenFromConnectionErrors(t *testing.T) {
	c := refusedClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var lpErr error
	poll, err := c.Streams().LongpollContext(ctx, "abc", &audd.LongpollOptions{SkipCallbackCheck: true, Timeout: 1})
	if err != nil {
		lpErr = err
	} else {
		defer poll.Close()
		lpErr = <-poll.Errors
	}
	_, listErr := c.Streams().ListContext(ctx)
	for _, err := range []error{lpErr, listErr} {
		if err == nil {
			t.Fatal("expected a connection error")
		}
		mapped := MapError(nil, err)
		if strings.Contains(mapped.Error(), streamstest.Token) {
			t.Fatalf("token in the mapped error: %v", mapped)
		}
	}
	// Errors MapError does not know are redacted too, and still unwrap.
	uerr := &url.Error{Op: "Get", URL: "https://api.audd.io/longpoll/?api_token=" + streamstest.Token, Err: errors.New("boom")}
	m := MapError(nil, uerr)
	if strings.Contains(m.Error(), streamstest.Token) || !errors.Is(m, uerr) {
		t.Fatalf("unknown error: %v", m)
	}
}

func TestRecorderNotesNeverHoldTheToken(t *testing.T) {
	st, err := streamstore.OpenPath(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var logged []string
	r := NewRecorder(app.New(), st, RecorderOptions{Logf: func(f string, args ...any) {
		logged = append(logged, fmtSprintf(f, args...))
	}})
	r.note("stream 1: %v", errors.New(`Get "https://api.audd.io/longpoll/?api_token=`+streamstest.Token+`&category=x"`))
	r.logf("x %s", "token="+streamstest.Token)
	v, _ := st.Meta("last_error")
	if strings.Contains(v, streamstest.Token) || v == "" {
		t.Fatalf("last_error %q", v)
	}
	for _, l := range logged {
		if strings.Contains(l, streamstest.Token) {
			t.Fatalf("log %q", l)
		}
	}
}

func fmtSprintf(f string, args ...any) string { return fmt.Sprintf(f, args...) }

func TestMapErrorStreamsWording(t *testing.T) {
	t.Setenv("AUDD_API_TOKEN", "")
	e := MapError(nil, &audd.AudDAPIError{ErrorCode: 904, Message: "no active subscription for streams"}).(*output.Error)
	if e.Code != "not_enabled" || e.Exit != output.ExitQuota || !strings.Contains(e.Hint, "stream monitoring") || strings.Contains(e.Hint, "audd usage") {
		t.Fatalf("904: %+v", e)
	}
	e = MapError(nil, &audd.AudDAPIError{ErrorCode: 902, Message: "limit reached"}).(*output.Error)
	if e.Code != "quota_exceeded" {
		t.Fatalf("902: %+v", e)
	}
	e = MapError(nil, &audd.AudDAPIError{ErrorCode: 1234, Message: "something new"}).(*output.Error)
	if e.Exit != output.ExitNetwork {
		t.Fatalf("unknown codes are server errors (exit 5): %+v", e)
	}
	a := app.New()
	a.Flags.Token = "x"
	e = MapError(a, &audd.AudDAPIError{ErrorCode: 900, Message: "Wrong API token"}).(*output.Error)
	if e.Code != "token_rejected" || e.Exit != output.ExitAuth || !strings.Contains(e.Hint, "--token") {
		t.Fatalf("--token hint: %+v", e)
	}
}
