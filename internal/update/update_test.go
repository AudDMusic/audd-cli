package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"0.2.0", "0.1.0", true},
		{"v0.1.1", "0.1.0", true},
		{"1.0.0", "0.9.9", true},
		{"0.10.0", "0.9.0", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		{"0.2.0", "0.2.0-rc.1", true},
		{"0.2.0-rc.1", "0.2.0", false},
		{"garbage", "0.1.0", false},
		{"0.2.0", "dev", false},
	} {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.latest, c.current, got)
		}
	}
}

func TestDetectMethod(t *testing.T) {
	none := func(string) string { return "" }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		exe, goos string
		getenv    func(string) string
		want      string
	}{
		{"/opt/homebrew/Cellar/audd/0.1.0/bin/audd", "darwin", none, "homebrew"},
		{"/home/linuxbrew/.linuxbrew/Cellar/audd/0.1.0/bin/audd", "linux", none, "homebrew"},
		{"/usr/local/Caskroom/audd/0.1.0/audd", "darwin", none, "homebrew"},
		{"/opt/homebrew/Caskroom/audd/0.1.0/audd", "darwin", none, "homebrew"},
		{"/usr/lib/node_modules/@audd/cli-linux-x64/bin/audd", "linux", none, "npm"},
		{"/home/u/.npm/_npx/abc/node_modules/@audd/cli-darwin-arm64/bin/audd", "darwin", none, "npx"},
		{`C:\Users\u\AppData\Local\npm-cache\_npx\abc\node_modules\@audd\cli-win32-x64\bin\audd.exe`, "windows", none, "npx"},
		{"/home/u/.cache/uv/archive-v0/xyz/lib/python3.12/site-packages/audd_cli/bin/audd", "linux", none, "uvx"},
		{`C:\Users\u\AppData\Local\uv\cache\archive-v0\xyz\Lib\site-packages\audd_cli\bin\audd.exe`, "windows", none, "uvx"},
		{"/home/u/.local/share/pipx/venvs/audd-cli/lib/python3.12/site-packages/audd_cli/bin/audd", "linux", none, "pipx"},
		{"/home/u/.local/share/uv/tools/audd-cli/lib/python3.12/site-packages/audd_cli/bin/audd", "linux", none, "uv"},
		{"/usr/lib/python3/site-packages/audd_cli/bin/audd", "linux", none, "pip"},
		{`C:\Users\u\scoop\apps\audd\current\audd.exe`, "windows", none, "scoop"},
		{`C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\AudD.CLI\audd.exe`, "windows", none, "winget"},
		{"/home/u/go/bin/audd", "linux", none, "go"},
		{"/work/gopath/bin/audd", "linux", env(map[string]string{"GOPATH": "/work/gopath"}), "go"},
		{"/tools/bin/audd", "linux", env(map[string]string{"GOBIN": "/tools/bin/"}), "go"},
		{"/usr/local/bin/audd", "linux", env(map[string]string{"AUDD_INSTALL_METHOD": "docker"}), "docker"},
		{"/usr/local/bin/audd", "linux", env(map[string]string{"AUDD_INSTALL_METHOD": "unknown"}), "binary"},
		{"/usr/local/bin/audd", "darwin", none, "binary"},
		{"/home/u/.local/bin/audd", "linux", none, "binary"},
		{`C:\Tools\audd.exe`, "windows", none, "binary"},
	} {
		m := DetectMethod(c.exe, c.goos, c.getenv)
		if m.Name != c.want {
			t.Errorf("%s: got %s, want %s", c.exe, m.Name, c.want)
		}
		if m.SelfUpdate != (c.want == "binary") || (m.Command == "") != m.SelfUpdate {
			t.Errorf("%s: %+v", c.exe, m)
		}
	}
}

func TestNoticeAllowed(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		tty  bool
		env  map[string]string
		want bool
	}{
		{true, nil, true},
		{false, nil, false},
		{true, map[string]string{"CI": "true"}, false},
		{true, map[string]string{"GITHUB_ACTIONS": "true"}, false},
		{true, map[string]string{"CI": "false"}, true},
		{true, map[string]string{"AUDD_NO_UPDATE_CHECK": "1"}, false},
		{true, map[string]string{"AUDD_NO_UPDATE_CHECK": "0"}, true},
	} {
		if got := NoticeAllowed(NoticeEnv{StderrTTY: c.tty, Getenv: env(c.env)}); got != c.want {
			t.Errorf("tty=%v env=%v: %v", c.tty, c.env, got)
		}
	}
}

func TestStateAndNotice(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := LoadState(dir)
	if !s.Due(now) {
		t.Fatal("empty state should be due")
	}
	if err := SaveState(dir, State{CheckedAt: now, Latest: "0.3.0"}); err != nil {
		t.Fatal(err)
	}
	s = LoadState(dir)
	if s.Due(now.Add(23*time.Hour)) || !s.Due(now.Add(24*time.Hour)) {
		t.Fatal("due after a day")
	}
	if s.Due(now.Add(-30*time.Minute)) || !s.Due(now.Add(-2*time.Hour)) {
		t.Fatal("a check time in the future is due")
	}
	self := Method{Name: "binary", SelfUpdate: true}
	if got := Notice("0.2.0", s, self); got != "audd 0.3.0 is available (you have 0.2.0). Update with: audd update" {
		t.Fatal(got)
	}
	if got := Notice("0.2.0", s, Method{Name: "homebrew", Command: BrewCommand}); !strings.HasSuffix(got, "Update with: brew upgrade audd") {
		t.Fatal(got)
	}
	if Notice("0.3.0", s, self) != "" || Notice("0.4.0", s, self) != "" || Notice("0.1.0", State{}, self) != "" {
		t.Fatal("no notice when current")
	}
	os.WriteFile(filepath.Join(dir, stateFile), []byte("{broken"), 0o644)
	if LoadState(dir).Latest != "" {
		t.Fatal("broken state")
	}
}

// fakeReleases serves the GitHub latest-release API and its assets.
type fakeReleases struct {
	*httptest.Server
	tag      string
	files    map[string][]byte
	requests int
}

func newFakeReleases(t *testing.T, tag string, files map[string][]byte) *fakeReleases {
	f := &fakeReleases{tag: tag, files: files}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		if r.URL.Path == "/repos/"+Repo+"/releases/latest" {
			var assets []map[string]string
			for name := range f.files {
				assets = append(assets, map[string]string{"name": name, "browser_download_url": f.URL + "/dl/" + name})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "html_url": "https://github.com/" + Repo + "/releases/tag/" + f.tag, "assets": assets})
			return
		}
		if b, ok := f.files[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		n string
		b []byte
	}{{"README.md", []byte("readme")}, {name, body}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.n, Mode: 0o755, Size: int64(len(f.b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(f.b)
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

func zipped(t *testing.T, name string, body []byte) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(body)
	zw.Close()
	return b.Bytes()
}

func sums(files map[string][]byte) []byte {
	var b strings.Builder
	for n, d := range files {
		s := sha256.Sum256(d)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(s[:]), n)
	}
	return []byte(b.String())
}

func TestLatestAndApply(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			newBin := []byte("#!new audd binary")
			name := ArchiveName("0.2.0", goos, "amd64")
			var arch []byte
			if goos == "windows" {
				arch = zipped(t, "audd.exe", newBin)
			} else {
				arch = tarGz(t, "audd", newBin)
			}
			files := map[string][]byte{name: arch}
			files[ChecksumsName] = sums(map[string][]byte{name: arch})
			srv := newFakeReleases(t, "v0.2.0", files)

			rel, err := Latest(context.Background(), srv.Client(), srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			if rel.Version != "0.2.0" || rel.Tag != "v0.2.0" || len(rel.Assets) != 2 {
				t.Fatalf("%+v", rel)
			}
			exe := filepath.Join(t.TempDir(), BinaryName(goos))
			os.WriteFile(exe, []byte("old"), 0o755)
			if err := Apply(context.Background(), srv.Client(), rel, exe, goos, "amd64"); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(exe)
			if !bytes.Equal(got, newBin) {
				t.Fatalf("binary not replaced: %q", got)
			}
			if runtime.GOOS != "windows" {
				if st, _ := os.Stat(exe); st.Mode().Perm()&0o100 == 0 {
					t.Fatal("not executable")
				}
			}
			if err := Apply(context.Background(), srv.Client(), rel, exe, goos, "arm64"); !errors.Is(err, ErrNoAsset) {
				t.Fatalf("missing asset: %v", err)
			}
		})
	}
}

func TestApplyRejectsBadChecksum(t *testing.T) {
	name := ArchiveName("0.2.0", "linux", "amd64")
	arch := tarGz(t, "audd", []byte("evil"))
	files := map[string][]byte{name: arch, ChecksumsName: sums(map[string][]byte{name: []byte("something else")})}
	srv := newFakeReleases(t, "v0.2.0", files)
	rel, err := Latest(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "audd")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Apply(context.Background(), srv.Client(), rel, exe, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("binary changed after a failed check")
	}
	// No checksums file at all.
	delete(srv.files, ChecksumsName)
	rel, _ = Latest(context.Background(), srv.Client(), srv.URL)
	if err := Apply(context.Background(), srv.Client(), rel, exe, "linux", "amd64"); err == nil {
		t.Fatal("applied without checksums")
	}
}

func TestLatestErrors(t *testing.T) {
	srv := newFakeReleases(t, "nightly", nil)
	if _, err := Latest(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("bad tag accepted")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer bad.Close()
	if _, err := Latest(context.Background(), bad.Client(), bad.URL); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("%v", err)
	}
}

func TestLatestNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := Latest(context.Background(), srv.Client(), srv.URL); err == nil || !strings.Contains(err.Error(), "no audd release") {
		t.Fatalf("%v", err)
	}
}

func TestRemoveOld(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "audd.exe")
	if err := os.WriteFile(exe, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".old", []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	RemoveOld(exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatalf("old binary still there: %v", err)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("current binary removed: %v", err)
	}
	RemoveOld(exe) // nothing to remove is fine
}
