package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func TestRenderGoldens(t *testing.T) {
	for _, tg := range AllTargets {
		got, err := Render(tg)
		if err != nil {
			t.Fatal(err)
		}
		testutil.Golden(t, "skill_"+string(tg), []byte(got))
	}
}

func TestRenderedContent(t *testing.T) {
	for _, tg := range AllTargets {
		got, _ := Render(tg)
		for _, want := range []string{"--dry-run", "--limit", "--max-files", "--yes", "audd login", "--reveal", "exit code"} {
			if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
				t.Errorf("%s: missing %q", tg, want)
			}
		}
		if strings.Contains(strings.ToLower(got), "lyric") {
			t.Errorf("%s mentions lyrics", tg)
		}
	}
	claude, _ := Render(TargetClaude)
	if !strings.HasPrefix(claude, "---\nname: audd\ndescription: ") {
		t.Fatalf("claude skill frontmatter: %q", claude[:40])
	}
	cursor, _ := Render(TargetCursor)
	if !strings.HasPrefix(cursor, "---\ndescription: ") || !strings.Contains(cursor, "alwaysApply: false") {
		t.Fatalf("cursor rule frontmatter: %q", cursor[:60])
	}
	agents, _ := Render(TargetAgentsMD)
	if !strings.HasPrefix(agents, BeginMarker+"\n## AudD CLI") || !strings.HasSuffix(agents, EndMarker+"\n") {
		t.Fatalf("AGENTS.md section: %q", agents)
	}
}

func TestMergeAgentsMD(t *testing.T) {
	sec, _ := Render(TargetAgentsMD)
	merge := func(existing string) string {
		t.Helper()
		got, err := MergeAgentsMD(existing, sec)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := merge(""); got != sec {
		t.Fatalf("empty file: %q", got)
	}
	existing := "# Project\n\nUse tabs.\n"
	once := merge(existing)
	if !strings.HasPrefix(once, existing+"\n"+BeginMarker) {
		t.Fatalf("append: %q", once)
	}
	if twice := merge(once); twice != once {
		t.Fatalf("second merge changed the file:\n%s", twice)
	}
	// A stale section in the middle is replaced in place.
	stale := "# Project\n\n<!-- audd:begin -->\nold text\n" + EndMarker + "\n\n## Testing\n\nRun go test.\n"
	got := merge(stale)
	if strings.Contains(got, "old text") || !strings.HasSuffix(got, "\n## Testing\n\nRun go test.\n") || strings.Count(got, EndMarker) != 1 {
		t.Fatalf("replace: %q", got)
	}
}

func TestMergeAgentsMDUnclosedSection(t *testing.T) {
	sec, _ := Render(TargetAgentsMD)
	broken := "# Project\n\n<!-- audd:begin -->\nold text\n\n## Testing\n\nRun go test.\n"
	if _, err := MergeAgentsMD(broken, sec); !errors.Is(err, ErrUnclosedSection) {
		t.Fatalf("err = %v", err)
	}
	// Install leaves the file alone.
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, TargetAgentsMD); !errors.Is(err, ErrUnclosedSection) {
		t.Fatalf("install err = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != broken {
		t.Fatalf("file changed: %q", b)
	}
}

func TestInstallIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tg := range AllTargets {
		r, err := Install(dir, tg)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Changed || r.Created != (tg != TargetAgentsMD) {
			t.Fatalf("%s first install: %+v", tg, r)
		}
		r, err = Install(dir, tg)
		if err != nil {
			t.Fatal(err)
		}
		if r.Changed || r.Created {
			t.Fatalf("%s second install should change nothing: %+v", tg, r)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.HasPrefix(string(b), "# Notes\n\n"+BeginMarker) {
		t.Fatalf("AGENTS.md: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "audd", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	got := Detect(dir)
	if len(got) != 3 {
		t.Fatalf("detect: %v", got)
	}
}

func TestDetectEmpty(t *testing.T) {
	if got := Detect(t.TempDir()); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestTopics(t *testing.T) {
	for _, n := range []string{"api", "API", "streams", "enterprise", "upload", "file-upload", "cli", "mcp", "sdks"} {
		if _, ok := FindTopic(n); !ok {
			t.Errorf("topic %q not found", n)
		}
	}
	if _, ok := FindTopic("nope"); ok {
		t.Fatal("unknown topic found")
	}
	api, _ := FindTopic("api")
	if api.URL("https://docs.audd.io") != "https://docs.audd.io/.md" {
		t.Fatal(api.URL("https://docs.audd.io"))
	}
	up, _ := FindTopic("upload")
	if api.PageURL("https://docs.audd.io") != "https://docs.audd.io/" {
		t.Fatal(api.PageURL("https://docs.audd.io"))
	}
	if up.PageURL("https://docs.audd.io") != "https://docs.audd.io/upload_audio_endpoint" {
		t.Fatal(up.PageURL("https://docs.audd.io"))
	}
	if up.URL("https://docs.audd.io/") != "https://docs.audd.io/upload_audio_endpoint.md" {
		t.Fatal(up.URL("https://docs.audd.io/"))
	}
	if strings.Contains(strings.ToLower(CLIDoc), "lyric") || !strings.HasPrefix(CLIDoc, "# AudD CLI") {
		t.Fatal("embedded CLI doc")
	}
}

func TestFetchDocs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/streams.md":
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Write([]byte("# Streams\n"))
		case "/enterprise.md":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	st, _ := FindTopic("streams")
	md, err := FetchDocs(ctx, srv.Client(), srv.URL, st)
	if err != nil || md != "# Streams\n" {
		t.Fatalf("%q %v", md, err)
	}
	en, _ := FindTopic("enterprise")
	if _, err := FetchDocs(ctx, srv.Client(), srv.URL, en); err == nil {
		t.Fatal("html page accepted")
	}
	up, _ := FindTopic("upload")
	if _, err := FetchDocs(ctx, srv.Client(), srv.URL, up); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404: %v", err)
	}
	cli, _ := FindTopic("cli")
	if md, err := FetchDocs(ctx, nil, "http://127.0.0.1:1", cli); err != nil || md != CLIDoc {
		t.Fatal("cli topic should be embedded")
	}
}

func TestBuildReference(t *testing.T) {
	root := &cobra.Command{Use: "audd"}
	root.PersistentFlags().String("format", "", "output format")
	rec := &cobra.Command{Use: "recognize <file>", Short: "Identify music", Example: "  audd recognize a.mp3\n  audd recognize b.mp3", RunE: func(*cobra.Command, []string) error { return nil }}
	rec.Flags().String("limit", "", "chunks")
	SetRequiredWhen(rec.Flags(), "limit", "with --enterprise")
	rec.Flags().Bool("dry-run", false, "plan only")
	rec.Flags().Int("concurrency", 4, "parallel")
	hiddenFlag := "secret"
	rec.Flags().Bool(hiddenFlag, false, "")
	_ = rec.Flags().MarkHidden(hiddenFlag)
	grp := &cobra.Command{Use: "streams", Short: "Streams"}
	add := &cobra.Command{Use: "add <url>", Short: "Add", RunE: func(*cobra.Command, []string) error { return nil }}
	add.Flags().Int("id", 0, "radio id")
	_ = add.MarkFlagRequired("id")
	watch := &cobra.Command{Use: "watch", Short: "Watch", Annotations: map[string]string{AnnotationStreaming: "true"}, RunE: func(*cobra.Command, []string) error { return nil }}
	grp.AddCommand(watch, add)
	hidden := &cobra.Command{Use: "internal", Hidden: true, RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(rec, grp, hidden)

	ref := BuildReference(root, "1.2.3")
	var paths []string
	for _, c := range ref.Commands {
		paths = append(paths, c.Path)
	}
	if strings.Join(paths, ",") != "audd recognize,audd streams,audd streams add,audd streams watch" {
		t.Fatalf("paths: %v", paths)
	}
	r := ref.Commands[0]
	if r.Output != "recognition" || len(r.Examples) != 2 || len(r.Flags) != 3 || r.Flags[0].Name != "concurrency" || r.Flags[0].Default != "4" || r.Flags[1].Default != "" {
		t.Fatalf("recognize: %+v", r)
	}
	if lim := r.Flags[2]; lim.Name != "limit" || lim.Required || lim.RequiredWhen != "with --enterprise" {
		t.Fatalf("limit: %+v", lim)
	}
	if ref.Commands[1].Runnable || ref.Commands[1].Output != "" {
		t.Fatalf("group: %+v", ref.Commands[1])
	}
	if !ref.Commands[2].Flags[0].Required {
		t.Fatalf("required flag: %+v", ref.Commands[2])
	}
	if !ref.Commands[3].Streaming || ref.Commands[3].Output != "play_stream" {
		t.Fatalf("watch: %+v", ref.Commands[3])
	}
	if len(ref.GlobalFlags) != 1 || len(ref.ExitCodes) != 10 {
		t.Fatalf("globals/exit codes: %+v", ref)
	}
	for _, c := range ref.Commands {
		if c.Output != "" {
			if _, ok := ref.Outputs[c.Output]; !ok {
				t.Errorf("%s: output %q not described", c.Path, c.Output)
			}
		}
	}
	if _, err := json.Marshal(ref); err != nil {
		t.Fatal(err)
	}
}
