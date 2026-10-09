// Package scripts holds tests for the release helper scripts.
package scripts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

const fakeVersion = "1.2.3"

// fakeBinary stands in for audd: a script that prints a version line.
var fakeBinary = []byte("#!/bin/sh\necho \"audd " + fakeVersion + " (test build)\"\n")

type release struct {
	name       string
	archive    []byte
	checksums  []byte
	requests   []string
	noRedirect bool // the releases/latest page is unavailable
}

func newRelease(t *testing.T) *release {
	t.Helper()
	name, archive, sums := testutil.ReleaseArchive(t, fakeVersion, fakeBinary)
	return &release{name: name, archive: archive, checksums: sums}
}

func (r *release) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.requests = append(r.requests, req.URL.Path)
		switch req.URL.Path {
		case "/releases/latest":
			if r.noRedirect {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			http.Redirect(w, req, "/releases/tag/v"+fakeVersion, http.StatusFound)
		case "/releases/tag/v" + fakeVersion:
			fmt.Fprint(w, "<html>release page</html>")
		case "/api/releases/latest":
			fmt.Fprintf(w, "{\n  \"url\": \"x\",\n  \"tag_name\": \"v%s\",\n  \"name\": \"v%s\"\n}\n", fakeVersion, fakeVersion)
		case "/download/v" + fakeVersion + "/" + r.name:
			w.Write(r.archive)
		case "/download/v" + fakeVersion + "/checksums.txt":
			w.Write(r.checksums)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type result struct {
	stdout, stderr string
	err            error
	dir            string
}

func runInstall(t *testing.T, srv *httptest.Server, env ...string) result {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is for macOS and Linux")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "bin")
	cmd := exec.Command(sh, "install.sh")
	cmd.Env = append([]string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"AUDD_DOWNLOAD_URL=" + srv.URL + "/download",
		"AUDD_RELEASES_API=" + srv.URL + "/api/releases/latest",
		"AUDD_LATEST_URL=" + srv.URL + "/releases/latest",
	}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return result{stdout: out.String(), stderr: errb.String(), err: err, dir: dir}
}

func TestInstallLatest(t *testing.T) {
	rel := newRelease(t)
	srv := rel.serve(t)
	res := runInstall(t, srv)
	if res.err != nil {
		t.Fatalf("install failed: %v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}
	bin := filepath.Join(res.dir, "audd")
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("audd not installed: %v\n%s%s", err, res.stdout, res.stderr)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("audd is not executable: %v", st.Mode())
	}
	got, err := os.ReadFile(bin)
	if err != nil || !bytes.Equal(got, fakeBinary) {
		t.Fatalf("installed file differs from the archive's binary: %q", got)
	}
	all := res.stdout + res.stderr
	for _, want := range []string{"audd " + fakeVersion, res.dir, "PATH"} {
		if !strings.Contains(all, want) {
			t.Errorf("output lacks %q:\n%s", want, all)
		}
	}
	if !contains(rel.requests, "/releases/latest") {
		t.Errorf("the latest release was not looked up through the releases/latest redirect: %v", rel.requests)
	}
	if contains(rel.requests, "/api/releases/latest") {
		t.Errorf("the rate-limited API was used although the redirect worked: %v", rel.requests)
	}
}

func TestInstallLatestFallsBackToAPI(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl; install.sh uses the API directly")
	}
	rel := newRelease(t)
	rel.noRedirect = true
	srv := rel.serve(t)
	res := runInstall(t, srv)
	if res.err != nil {
		t.Fatalf("install failed: %v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}
	if !contains(rel.requests, "/api/releases/latest") {
		t.Errorf("the API was not used when the redirect failed: %v", rel.requests)
	}
	got, _ := os.ReadFile(filepath.Join(res.dir, "audd"))
	if !bytes.Equal(got, fakeBinary) {
		t.Fatalf("installed file differs from the archive's binary: %q", got)
	}
}

func TestInstallPinnedVersionAndDir(t *testing.T) {
	rel := newRelease(t)
	srv := rel.serve(t)
	dir := filepath.Join(t.TempDir(), "tools")
	// An older binary is replaced.
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "audd"), []byte("old"), 0o755)
	res := runInstall(t, srv, "AUDD_VERSION=v"+fakeVersion, "AUDD_INSTALL_DIR="+dir)
	if res.err != nil {
		t.Fatalf("install failed: %v\n%s%s", res.err, res.stdout, res.stderr)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "audd"))
	if !bytes.Equal(got, fakeBinary) {
		t.Fatalf("audd in AUDD_INSTALL_DIR was not replaced: %q", got)
	}
	if contains(rel.requests, "/api/releases/latest") || contains(rel.requests, "/releases/latest") {
		t.Errorf("AUDD_VERSION should skip the latest-release lookup: %v", rel.requests)
	}
}

func TestInstallChecksumMismatch(t *testing.T) {
	rel := newRelease(t)
	bad := sha256.Sum256([]byte("something else"))
	rel.checksums = []byte(hex.EncodeToString(bad[:]) + "  " + rel.name + "\n")
	srv := rel.serve(t)
	res := runInstall(t, srv)
	if res.err == nil {
		t.Fatalf("install succeeded with a bad checksum:\n%s%s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "checksum") {
		t.Errorf("stderr does not mention the checksum:\n%s", res.stderr)
	}
	if _, err := os.Stat(filepath.Join(res.dir, "audd")); err == nil {
		t.Fatal("audd was installed although its checksum did not match")
	}
}

func TestInstallMissingChecksum(t *testing.T) {
	rel := newRelease(t)
	rel.checksums = []byte(strings.Repeat("0", 64) + "  audd_" + fakeVersion + "_plan9_amd64.tar.gz\n")
	srv := rel.serve(t)
	res := runInstall(t, srv)
	if res.err == nil {
		t.Fatalf("install succeeded without a checksum for the archive:\n%s%s", res.stdout, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(res.dir, "audd")); err == nil {
		t.Fatal("audd was installed without a checksum")
	}
}

func TestInstallMissingRelease(t *testing.T) {
	rel := newRelease(t)
	srv := rel.serve(t)
	res := runInstall(t, srv, "AUDD_VERSION=9.9.9")
	if res.err == nil {
		t.Fatalf("install succeeded for a release that does not exist:\n%s%s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "9.9.9") {
		t.Errorf("stderr does not name the version:\n%s", res.stderr)
	}
}

func TestInstallUnsupportedSystem(t *testing.T) {
	rel := newRelease(t)
	srv := rel.serve(t)
	// A fake uname reports Windows (Git Bash).
	fake := t.TempDir()
	os.WriteFile(filepath.Join(fake, "uname"), []byte("#!/bin/sh\ncase \"$1\" in -m) echo x86_64;; *) echo MINGW64_NT-10.0;; esac\n"), 0o755)
	res := runInstall(t, srv, "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	if res.err == nil {
		t.Fatalf("install succeeded on Windows:\n%s%s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "winget") {
		t.Errorf("stderr does not point Windows users at winget:\n%s", res.stderr)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestInstallSnapshot installs a real GoReleaser build. It runs after
// `goreleaser release --snapshot --clean --skip=sign,sbom` has filled dist/.
func TestInstallSnapshot(t *testing.T) {
	dist := filepath.Join("..", "dist")
	meta, err := os.ReadFile(filepath.Join(dist, "metadata.json"))
	if err != nil {
		t.Skip("no snapshot build in dist/; run goreleaser release --snapshot --clean --skip=sign,sbom")
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(meta, &m); err != nil || m.Version == "" {
		t.Skipf("dist/metadata.json has no version: %v", err)
	}
	archive := "audd_" + m.Version + "_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	for _, f := range []string{archive, "checksums.txt"} {
		if _, err := os.Stat(filepath.Join(dist, f)); err != nil {
			t.Skipf("dist/%s is missing", f)
		}
	}
	prefix := "/download/v" + m.Version + "/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, prefix)
		if name == req.URL.Path || (name != archive && name != "checksums.txt") {
			http.NotFound(w, req)
			return
		}
		http.ServeFile(w, req, filepath.Join(dist, name))
	}))
	t.Cleanup(srv.Close)
	res := runInstall(t, srv, "AUDD_VERSION="+m.Version)
	if res.err != nil {
		t.Fatalf("install failed: %v\nstdout:\n%s\nstderr:\n%s", res.err, res.stdout, res.stderr)
	}
	out, err := exec.Command(filepath.Join(res.dir, "audd"), "version").CombinedOutput()
	if err != nil {
		t.Fatalf("installed audd does not run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), m.Version) {
		t.Errorf("audd version does not report %s:\n%s", m.Version, out)
	}
}
