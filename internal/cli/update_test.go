package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/cli"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
	"github.com/AudDMusic/audd-cli/internal/update"
)

// fakeRelease serves GitHub's latest-release API with an archive for this
// system that contains newBin.
func fakeRelease(t *testing.T, tag string, newBin []byte) {
	t.Helper()
	files := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/AudDMusic/audd-cli/releases/latest" {
			var assets []map[string]string
			for n := range files {
				assets = append(assets, map[string]string{"name": n, "browser_download_url": "http://" + r.Host + "/dl/" + n})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "html_url": "https://github.com/AudDMusic/audd-cli/releases/tag/" + tag, "assets": assets})
			return
		}
		if b, ok := files[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AUDD_UPDATE_URL", srv.URL)
	name, archive, sums := testutil.ReleaseArchive(t, strings.TrimPrefix(tag, "v"), newBin)
	files[name] = archive
	files["checksums.txt"] = sums
}

func TestUpdate(t *testing.T) {
	testutil.Isolate(t)
	newBin := []byte("new audd")
	fakeRelease(t, "v"+app.Version, newBin)
	exe := filepath.Join(t.TempDir(), "audd")
	os.WriteFile(exe, []byte("old audd"), 0o755)
	defer cli.SetExecutablePath(exe)()

	m := decode(t, mustRun(t, "update").Stdout)
	if m["up_to_date"] != true || m["updated"] != false {
		t.Fatalf("up to date: %v", m)
	}

	fakeRelease(t, "v99.0.0", newBin)
	r := mustRun(t, "update", "--check", "--format", "table")
	if !strings.Contains(r.Stdout, "audd 99.0.0 is available") {
		t.Fatalf("check: %q", r.Stdout)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old audd" {
		t.Fatal("--check changed the binary")
	}

	m = decode(t, mustRun(t, "update").Stdout)
	if m["updated"] != true || m["latest"] != "99.0.0" || m["method"] != "binary" {
		t.Fatalf("update: %v", m)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new audd" {
		t.Fatalf("binary not replaced: %q", b)
	}
}

func TestUpdatePackageManagerPrintsCommand(t *testing.T) {
	testutil.Isolate(t)
	fakeRelease(t, "v99.0.0", []byte("new"))
	exe := filepath.Join(t.TempDir(), "node_modules", "@audd", "cli-linux-x64", "bin", "audd")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("old"), 0o755)
	defer cli.SetExecutablePath(exe)()
	r := mustRun(t, "update", "--format", "table")
	if !strings.Contains(r.Stdout, "npm install -g @audd/cli@latest") {
		t.Fatalf("%q", r.Stdout)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("package-manager install replaced")
	}
}

func TestUpdateNetworkError(t *testing.T) {
	testutil.Isolate(t)
	t.Setenv("AUDD_UPDATE_URL", "http://127.0.0.1:1")
	wantExit(t, run(t, "update"), output.ExitNetwork, "network")
}

func noticeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AUDD_NO_UPDATE_CHECK", "")
	for _, k := range []string{"CI", "BUILD_NUMBER", "RUN_ID", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TF_BUILD", "JENKINS_URL", "TEAMCITY_VERSION"} {
		t.Setenv(k, "")
	}
}

func runTTY(args ...string) (string, string) {
	var out, errb bytes.Buffer
	cli.Run(context.Background(), args, cli.IO{In: strings.NewReader(""), Out: &out, Err: &errb, StderrTTY: true})
	return out.String(), errb.String()
}

// resetNotice marks the cached notice as last shown more than a day ago.
func resetNotice(t *testing.T) {
	t.Helper()
	dir := config.CacheDir()
	st := update.LoadState(dir)
	st.NoticedAt = time.Now().Add(-25 * time.Hour)
	if err := update.SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateNotice(t *testing.T) {
	testutil.Isolate(t)
	noticeEnv(t)
	fakeRelease(t, "v99.0.0", []byte("new"))
	defer cli.SetExecutablePath(filepath.Join(t.TempDir(), "audd"))()

	_, stderr := runTTY("version")
	if !strings.Contains(stderr, "audd 99.0.0 is available (you have "+app.Version+"). Update with: audd update") {
		t.Fatalf("notice: %q", stderr)
	}
	// Shown at most once a day: the next run stays quiet.
	t.Setenv("AUDD_UPDATE_URL", "http://127.0.0.1:1")
	if _, stderr := runTTY("version"); stderr != "" {
		t.Fatalf("notice shown twice in a day: %q", stderr)
	}
	// A day later it is shown again from the cache, without a new check
	// (the server is gone).
	resetNotice(t)
	if _, stderr := runTTY("version"); !strings.Contains(stderr, "99.0.0") {
		t.Fatalf("cached notice: %q", stderr)
	}
	resetNotice(t)
	// Never for update/mcp, with --quiet, off a terminal, in CI, or when
	// turned off.
	for _, args := range [][]string{{"update", "--help"}, {"version", "--quiet"}} {
		if _, stderr := runTTY(args...); strings.Contains(stderr, "99.0.0") {
			t.Errorf("%v: %q", args, stderr)
		}
	}
	if r := run(t, "version"); strings.Contains(r.Stderr, "99.0.0") {
		t.Error("notice off a terminal")
	}
	t.Setenv("CI", "true")
	if _, stderr := runTTY("version"); strings.Contains(stderr, "99.0.0") {
		t.Error("notice in CI")
	}
	t.Setenv("CI", "")
	t.Setenv("AUDD_NO_UPDATE_CHECK", "1")
	if _, stderr := runTTY("version"); strings.Contains(stderr, "99.0.0") {
		t.Error("notice with AUDD_NO_UPDATE_CHECK")
	}
}

func TestUpdateNoticeWhenCurrent(t *testing.T) {
	testutil.Isolate(t)
	noticeEnv(t)
	fakeRelease(t, "v"+app.Version, []byte("same"))
	if _, stderr := runTTY("version"); stderr != "" {
		t.Fatalf("no notice expected: %q", stderr)
	}
}

func TestUpdateNoticeSlowCheckNotRetriedEveryRun(t *testing.T) {
	testutil.Isolate(t)
	noticeEnv(t)
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AUDD_UPDATE_URL", srv.URL)

	// The release API hangs: the first run waits briefly and moves on.
	if _, stderr := runTTY("version"); stderr != "" {
		t.Fatalf("no notice expected: %q", stderr)
	}
	// The check is recorded as made, so the next run does not wait again.
	start := time.Now()
	runTTY("version")
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("second run waited %v", d)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("release API called %d times, want 1", n)
	}
	close(release)
	cli.WaitUpdateChecks()
}
