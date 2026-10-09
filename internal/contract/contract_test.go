//go:build contract

// Live checks; see doc.go.
package contract

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/mcpclient"
	"github.com/AudDMusic/audd-cli/internal/oauth"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
	"github.com/AudDMusic/audd-cli/internal/streams"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func need(t *testing.T, keys ...string) []string {
	t.Helper()
	var vals []string
	for _, k := range keys {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			t.Skipf("%s is not set", k)
		}
		vals = append(vals, v)
	}
	return vals
}

func isolate(t *testing.T) {
	t.Setenv("AUDD_CONFIG_DIR", t.TempDir())
	t.Setenv("AUDD_CACHE_DIR", t.TempDir())
	t.Setenv("AUDD_DATA_DIR", t.TempDir())
	t.Setenv("AUDD_NO_KEYRING", "1")
}

// The standard endpoint recognizes the example file. One request per run.
func TestRecognizeExample(t *testing.T) {
	v := need(t, "AUDD_CONTRACT_API_TOKEN")
	c := audd.NewClient(v[0])
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := c.RecognizeContext(ctx, "https://audd.tech/example.mp3", nil)
	if err != nil {
		t.Fatalf("recognize: %v", err)
	}
	if r == nil || r.Artist == "" || r.Title == "" || r.SongLink == "" {
		t.Fatalf("recognize returned an unexpected result: %+v", r)
	}
}

// The recent-results endpoint (undocumented) still has the shape the parser
// reads, and its timestamps are AudD's UTC+3: a play read from it must not
// land in the future.
func TestRecentResultsShape(t *testing.T) {
	v := need(t, "AUDD_CONTRACT_API_TOKEN", "AUDD_CONTRACT_RADIO_ID")
	id, err := strconv.Atoi(v[1])
	if err != nil {
		t.Fatalf("AUDD_CONTRACT_RADIO_ID: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	plays, err := streams.RecentResults(ctx, id, audd.DeriveLongpollCategory(v[0], id))
	if err != nil {
		t.Fatalf("recent results: %v", err)
	}
	t.Logf("%d recent plays", len(plays))
	now := time.Now()
	for _, p := range plays {
		if p.Artist == "" && p.Title == "" {
			t.Errorf("play without artist and title: %+v", p)
		}
		if p.Timestamp.IsZero() {
			t.Errorf("play without a timestamp: %s", p.Raw)
		} else if p.Timestamp.After(now.Add(5 * time.Minute)) {
			t.Errorf("play at %v is in the future; the time zone of recent results may have changed: %s", p.Timestamp, p.Raw)
		}
	}
}

// Every account tool still publishes the output schema recorded in
// testutil.FakeOutputSchemas (testdata/mcp/tools_list.json), with the same
// required fields and types.
func TestAccountToolSchemas(t *testing.T) {
	v := need(t, "AUDD_CONTRACT_OAUTH_SESSION")
	isolate(t)
	var sess oauth.Tokens
	if err := json.Unmarshal([]byte(v[0]), &sess); err != nil || sess.RefreshToken == "" {
		t.Fatalf("AUDD_CONTRACT_OAUTH_SESSION is not a stored audd session with a refresh token: %v", err)
	}
	sess.Expiry = time.Unix(0, 0) // refresh now
	b, _ := json.Marshal(sess)

	const profile = "contract"
	mem := secrets.NewMemory()
	mem.Set(profile, "oauth", string(b))
	out := output.NewPrinter(io.Discard, os.Stderr, output.PrinterOptions{Format: output.FormatJSON})
	oc := oauth.New(&config.Profile{Name: profile}, mem, out, oauth.WithLockDir(t.TempDir()))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tok, err := oc.Token(ctx)
	if err != nil {
		t.Fatalf("refresh the session: %v", err)
	}
	if path := os.Getenv("AUDD_CONTRACT_SESSION_OUT"); path != "" {
		s, _ := mem.Get(profile, "oauth")
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatalf("save the refreshed session: %v", err)
		}
	}

	mc := mcpclient.New(oc.ResourceURL(), func(ctx context.Context) (string, error) { return tok.AccessToken, nil })
	tools, err := mc.ListTools(ctx)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	live := map[string]mcpclient.Tool{}
	schemas := map[string]map[string]any{}
	for _, tl := range tools {
		live[tl.Name] = tl
		schemas[tl.Name] = tl.OutputSchema
	}
	if path := os.Getenv("AUDD_CONTRACT_SCHEMAS_OUT"); path != "" {
		b, err := json.MarshalIndent(schemas, "", "  ")
		if err == nil {
			err = os.WriteFile(path, append(b, '\n'), 0o644)
		}
		if err != nil {
			t.Fatalf("save the live output schemas: %v", err)
		}
	}
	names := make([]string, 0, len(testutil.FakeOutputSchemas))
	for n := range testutil.FakeOutputSchemas {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		tl, ok := live[name]
		if !ok && name == "rotate_api_token" && !tok.HasScopes("token:write") {
			t.Logf("%s: listed only for sign-ins with token:write; not checked", name)
			continue
		}
		if !ok {
			t.Errorf("%s: the server no longer has this tool", name)
			continue
		}
		if len(tl.OutputSchema) == 0 {
			t.Errorf("%s: the tool publishes no output schema", name)
			continue
		}
		diffs, notes := SchemaDiff(name, testutil.FakeOutputSchemas[name], tl.OutputSchema)
		for _, n := range notes {
			t.Log(n)
		}
		for _, d := range diffs {
			t.Error(d)
		}
	}
}
