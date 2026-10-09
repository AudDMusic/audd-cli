package cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func fakeDocs(t *testing.T) *int {
	t.Helper()
	hits := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if r.URL.Path == "/.md" {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			io.WriteString(w, "# AudD Music Recognition API Docs\n")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AUDD_DOCS_URL", srv.URL)
	return hits
}

func TestDocs(t *testing.T) {
	testutil.Isolate(t)
	hits := fakeDocs(t)

	r := mustRun(t, "docs")
	var list struct {
		Items []map[string]any `json:"items"`
	}
	json.Unmarshal([]byte(r.Stdout), &list)
	if len(list.Items) != len(agent.Topics) || list.Items[0]["name"] != "api" {
		t.Fatalf("topics: %s", r.Stdout)
	}

	r = mustRun(t, "docs", "api", "--format", "table")
	if r.Stdout != "# AudD Music Recognition API Docs\n" || *hits != 1 {
		t.Fatalf("api: %q", r.Stdout)
	}
	// Piped without --format: the markdown itself, ready to redirect.
	if r = mustRun(t, "docs", "api"); r.Stdout != "# AudD Music Recognition API Docs\n" {
		t.Fatalf("piped api: %q", r.Stdout)
	}
	m := decode(t, mustRun(t, "docs", "API", "--format", "json").Stdout)
	if m["topic"] != "api" || !strings.HasSuffix(m["source"].(string), "/") || strings.HasSuffix(m["source"].(string), ".md") || m["markdown"] != "# AudD Music Recognition API Docs\n" {
		t.Fatalf("api json: %v", m)
	}

	before := *hits
	m = decode(t, mustRun(t, "docs", "cli", "--format", "json").Stdout)
	if m["source"] != "embedded" || m["markdown"] != agent.CLIDoc || *hits != before {
		t.Fatalf("cli docs should be embedded: %v", m["source"])
	}

	// A page the site does not have, not signed in: a network error with a
	// way forward.
	r = run(t, "docs", "streams")
	wantExit(t, r, output.ExitNetwork, "network")
	if !strings.Contains(r.Stderr, "audd docs cli") {
		t.Fatalf("hint: %s", r.Stderr)
	}
	wantExit(t, run(t, "docs", "nope"), output.ExitUsage, "invalid_argument")

	// The MCP docs tool passes the topic after "--".
	m = decode(t, mustRun(t, "docs", "--format", "json", "--", "cli").Stdout)
	if m["topic"] != "cli" {
		t.Fatalf("topic after --: %v", m)
	}
	r = run(t, "docs", "--format", "json", "--", "--debug")
	wantExit(t, r, output.ExitUsage, "invalid_argument")
}

func TestDocsFallsBackToAccount(t *testing.T) {
	_, fm := accountEnv(t)
	login(t)
	t.Setenv("AUDD_DOCS_URL", "http://127.0.0.1:1")
	m := decode(t, mustRun(t, "docs", "upload", "--format", "json").Stdout)
	if m["source"] != "account service" || !strings.HasPrefix(m["markdown"].(string), "# file-upload") {
		t.Fatalf("fallback: %v", m)
	}
	if c := fm.CallsTo("get_api_docs"); len(c) != 1 || c[0].Args["topic"] != "file-upload" {
		t.Fatalf("calls: %v", c)
	}
	// Topics the account service does not have still fail cleanly.
	wantExit(t, run(t, "docs", "sdks"), output.ExitNetwork, "network")
}

func TestAgentSetup(t *testing.T) {
	testutil.Isolate(t)
	dir := t.TempDir()

	// Nothing set up: AGENTS.md.
	r := mustRun(t, "agent-setup", "--dir", dir, "--format", "table")
	if r.Stdout != "Wrote AGENTS.md\n" {
		t.Fatalf("%q", r.Stdout)
	}
	r = mustRun(t, "agent-setup", "--dir", dir, "--format", "table")
	if r.Stdout != "AGENTS.md is up to date\n" {
		t.Fatalf("%q", r.Stdout)
	}

	// A .claude folder is detected next to AGENTS.md.
	os.Mkdir(filepath.Join(dir, ".claude"), 0o755)
	m := decode(t, mustRun(t, "agent-setup", "--dir", dir).Stdout)
	files := m["files"].([]any)
	if len(files) != 2 || files[0].(map[string]any)["target"] != "claude" || files[0].(map[string]any)["created"] != true || files[1].(map[string]any)["changed"] != false {
		t.Fatalf("detect: %v", m)
	}
	want, _ := agent.Render(agent.TargetClaude)
	got, _ := os.ReadFile(filepath.Join(dir, ".claude", "skills", "audd", "SKILL.md"))
	if string(got) != want {
		t.Fatal("skill file content")
	}

	// Explicit flags, --codex is AGENTS.md.
	r = mustRun(t, "agent-setup", "--dir", dir, "--cursor", "--codex", "--format", "table")
	if r.Stdout != "Wrote .cursor/rules/audd.mdc\nAGENTS.md is up to date\n" && r.Stdout != "Wrote "+filepath.Join(".cursor", "rules", "audd.mdc")+"\nAGENTS.md is up to date\n" {
		t.Fatalf("%q", r.Stdout)
	}

	// --print writes nothing.
	empty := t.TempDir()
	r = mustRun(t, "agent-setup", "--dir", empty, "--claude", "--print", "--format", "table")
	if r.Stdout != want {
		t.Fatalf("print: %q", r.Stdout)
	}
	r = mustRun(t, "agent-setup", "--dir", empty, "--claude", "--cursor", "--print", "--format", "table")
	if !strings.HasPrefix(r.Stdout, "==> .claude/skills/audd/SKILL.md <==\n") || !strings.Contains(r.Stdout, "\n==> .cursor/rules/audd.mdc <==\n") {
		t.Fatalf("print two: %q", r.Stdout)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Fatal("--print wrote files")
	}
	wantExit(t, run(t, "agent-setup", "--dir", filepath.Join(empty, "missing")), output.ExitUsage, "invalid_argument")

	// An audd section whose end marker was deleted is left alone.
	broken := t.TempDir()
	text := "# Notes\n\n<!-- audd:begin -->\nold\n\n## Mine\n"
	os.WriteFile(filepath.Join(broken, "AGENTS.md"), []byte(text), 0o644)
	r = run(t, "agent-setup", "--dir", broken)
	wantExit(t, r, output.ExitUnexpected, "write_failed")
	if !strings.Contains(r.Stderr, agent.EndMarker) {
		t.Fatalf("hint: %s", r.Stderr)
	}
	if b, _ := os.ReadFile(filepath.Join(broken, "AGENTS.md")); string(b) != text {
		t.Fatalf("AGENTS.md changed: %q", b)
	}
}

func TestCommandsJSONGolden(t *testing.T) {
	testutil.Isolate(t)
	r := mustRun(t, "commands", "--json")
	got := regexp.MustCompile(`"version":"[^"]+"`).ReplaceAllString(r.Stdout, `"version":"VERSION"`)
	testutil.Golden(t, "commands_json", []byte(got))

	var ref struct {
		SchemaVersion int             `json:"schema_version"`
		Commands      []agent.Command `json:"commands"`
		Outputs       map[string]any  `json:"outputs"`
		GlobalFlags   []agent.Flag    `json:"global_flags"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &ref); err != nil || ref.SchemaVersion != 1 {
		t.Fatalf("%v", err)
	}
	seen := map[string]agent.Command{}
	for _, c := range ref.Commands {
		seen[c.Path] = c
		if c.Runnable && c.Output != "" {
			if _, ok := ref.Outputs[c.Output]; !ok {
				t.Errorf("%s: output %q is not described", c.Path, c.Output)
			}
		}
		if c.Runnable && len(c.ExitCodes) == 0 {
			t.Errorf("%s: no exit codes", c.Path)
		}
	}
	for _, p := range []string{"audd recognize", "audd streams add", "audd now-playing", "audd docs", "audd agent-setup", "audd mcp", "audd commands", "audd update", "audd streams recorder start"} {
		if _, ok := seen[p]; !ok {
			t.Errorf("%s missing", p)
		}
	}
	if _, ok := seen["audd help"]; ok {
		t.Error("help listed")
	}
	for alias, target := range map[string]string{"audd login": "audd auth login", "audd logout": "audd auth logout", "audd whoami": "audd auth status"} {
		if c, ok := seen[alias]; !ok || c.AliasOf != target || !c.Runnable || c.Output != seen[target].Output {
			t.Errorf("%s: listed as a shortcut for %s? %+v", alias, target, c)
		}
	}
	if !seen["audd streams watch"].Streaming {
		t.Error("streams watch should be streaming")
	}
	flag := func(cmd, name string) agent.Flag {
		for _, f := range seen[cmd].Flags {
			if f.Name == name {
				return f
			}
		}
		t.Fatalf("%s --%s missing", cmd, name)
		return agent.Flag{}
	}
	if f := flag("audd recognize", "limit"); f.Type != "string" || f.Required || !strings.Contains(f.RequiredWhen, "--enterprise") {
		t.Errorf("limit flag: %+v", f)
	}
	if f := flag("audd recognize", "max-files"); f.Required || !strings.Contains(f.RequiredWhen, "batch") {
		t.Errorf("max-files flag: %+v", f)
	}
	if f := flag("audd streams add", "id"); !f.Required {
		t.Errorf("streams add --id: %+v", f)
	}
	for _, f := range seen["audd billing subscribe"].Flags {
		if f.Name == "no-consent-prompt" {
			t.Error("hidden flag listed")
		}
	}
	if strings.Contains(strings.ToLower(r.Stdout), "lyric") {
		t.Error("reference mentions lyrics")
	}

	// Piped without --json is JSON too; a terminal gets a list.
	if decode(t, mustRun(t, "commands").Stdout)["commands"] == nil {
		t.Fatal("piped commands")
	}
	h := mustRun(t, "commands", "--format", "table").Stdout
	if !strings.Contains(h, "recognize") || !strings.Contains(h, "streams add") {
		t.Fatalf("human: %s", h)
	}
}
