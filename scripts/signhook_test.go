package scripts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runHook runs use-signed.sh the way the GoReleaser post-hook does.
func runHook(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the release hook runs on Linux")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	cmd := exec.Command("sh", append([]string{"use-signed.sh"}, args...)...)
	cmd.Env = append(os.Environ(),
		"AUDD_SIGNED_WINDOWS_DIR=", "AUDD_WINDOWS_AMD64_SHA256=", "AUDD_WINDOWS_ARM64_SHA256=",
		"AUDD_SIGNED_DARWIN_DIR=", "AUDD_DARWIN_AMD64_SHA256=", "AUDD_DARWIN_ARM64_SHA256=")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

type hookFixture struct {
	built, signedDir string
	unsigned, signed []byte
	sum              string
}

func newHookFixture(t *testing.T) hookFixture {
	t.Helper()
	dir := t.TempDir()
	f := hookFixture{
		built:     filepath.Join(dir, "dist", "audd.exe"),
		signedDir: filepath.Join(dir, "signed"),
		unsigned:  []byte("MZ unsigned build"),
		signed:    []byte("MZ unsigned build + signature"),
	}
	sum := sha256.Sum256(f.unsigned)
	f.sum = hex.EncodeToString(sum[:])
	for _, d := range []string{filepath.Dir(f.built), f.signedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.built, f.unsigned, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audd-windows-amd64.exe", "audd-darwin-arm64"} {
		if err := os.WriteFile(filepath.Join(f.signedDir, name), f.signed, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f hookFixture) builtBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(f.built)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUseSignedWindowsReplacesMatchingBuild(t *testing.T) {
	f := newHookFixture(t)
	out, err := runHook(t, []string{
		"AUDD_SIGNED_WINDOWS_DIR=" + f.signedDir,
		"AUDD_WINDOWS_AMD64_SHA256=" + strings.ToUpper(f.sum),
	}, "windows", "amd64", f.built, "1700000000")
	if err != nil {
		t.Fatalf("hook failed: %v\n%s", err, out)
	}
	if !bytes.Equal(f.builtBytes(t), f.signed) {
		t.Fatalf("the build was not replaced with the signed binary\n%s", out)
	}
	if runtime.GOOS == "linux" {
		st, err := os.Stat(f.built)
		if err != nil {
			t.Fatal(err)
		}
		if st.ModTime().Unix() != 1700000000 {
			t.Errorf("mtime = %d, want the commit timestamp", st.ModTime().Unix())
		}
	}
}

func TestUseSignedWindowsRefusesOtherBuild(t *testing.T) {
	f := newHookFixture(t)
	out, err := runHook(t, []string{
		"AUDD_SIGNED_WINDOWS_DIR=" + f.signedDir,
		"AUDD_WINDOWS_AMD64_SHA256=" + strings.Repeat("0", 64),
	}, "windows", "amd64", f.built, "1700000000")
	if err == nil {
		t.Fatalf("hook accepted a build that was not approved\n%s", out)
	}
	if !strings.Contains(out, "refusing") {
		t.Errorf("output does not explain the refusal:\n%s", out)
	}
	if !bytes.Equal(f.builtBytes(t), f.unsigned) {
		t.Error("the build was replaced despite the mismatch")
	}
}

func TestUseSignedWindowsNeedsSignedFileAndSum(t *testing.T) {
	f := newHookFixture(t)
	cases := map[string][]string{
		"no approved sum":    {"AUDD_SIGNED_WINDOWS_DIR=" + f.signedDir},
		"no signed binary":   {"AUDD_SIGNED_WINDOWS_DIR=" + t.TempDir(), "AUDD_WINDOWS_AMD64_SHA256=" + f.sum},
		"other architecture": {"AUDD_SIGNED_WINDOWS_DIR=" + f.signedDir, "AUDD_WINDOWS_ARM64_SHA256=" + f.sum},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			arch := "amd64"
			if name == "other architecture" {
				arch = "arm64"
			}
			if out, err := runHook(t, env, "windows", arch, f.built, "1700000000"); err == nil {
				t.Fatalf("hook succeeded\n%s", out)
			}
			if !bytes.Equal(f.builtBytes(t), f.unsigned) {
				t.Error("the build was replaced")
			}
		})
	}
}

func TestUseSignedWindowsLeavesBuildsAloneWithoutSigning(t *testing.T) {
	f := newHookFixture(t)
	for _, args := range [][]string{
		{"windows", "amd64", f.built, "1700000000"},
		{"darwin", "arm64", f.built, "1700000000"},
	} {
		out, err := runHook(t, nil, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if !bytes.Equal(f.builtBytes(t), f.unsigned) {
			t.Fatalf("%v: the build was replaced", args)
		}
	}
	// Other systems are never touched, even with signing set up.
	out, err := runHook(t, []string{
		"AUDD_SIGNED_WINDOWS_DIR=" + f.signedDir,
		"AUDD_WINDOWS_AMD64_SHA256=" + strings.Repeat("0", 64),
	}, "linux", "amd64", f.built, "1700000000")
	if err != nil || !bytes.Equal(f.builtBytes(t), f.unsigned) {
		t.Fatalf("a linux build was handled: %v\n%s", err, out)
	}
}

func TestUseSignedDarwinReplacesMatchingBuild(t *testing.T) {
	f := newHookFixture(t)
	out, err := runHook(t, []string{
		"AUDD_SIGNED_DARWIN_DIR=" + f.signedDir,
		"AUDD_DARWIN_ARM64_SHA256=" + f.sum,
	}, "darwin", "arm64", f.built, "1700000000")
	if err != nil {
		t.Fatalf("hook failed: %v\n%s", err, out)
	}
	if !bytes.Equal(f.builtBytes(t), f.signed) {
		t.Fatalf("the build was not replaced with the signed binary\n%s", out)
	}
}

func TestUseSignedKeepsEachSystemSeparate(t *testing.T) {
	f := newHookFixture(t)
	// Windows signing set up, macOS not: the macOS build ships unsigned.
	out, err := runHook(t, []string{
		"AUDD_SIGNED_WINDOWS_DIR=" + f.signedDir,
		"AUDD_WINDOWS_AMD64_SHA256=" + f.sum,
	}, "darwin", "arm64", f.built, "1700000000")
	if err != nil || !bytes.Equal(f.builtBytes(t), f.unsigned) {
		t.Fatalf("a darwin build was handled with only Windows signing: %v\n%s", err, out)
	}
	// The Windows sum never approves a macOS binary.
	out, err = runHook(t, []string{
		"AUDD_SIGNED_DARWIN_DIR=" + f.signedDir,
		"AUDD_WINDOWS_ARM64_SHA256=" + f.sum,
	}, "darwin", "arm64", f.built, "1700000000")
	if err == nil || !bytes.Equal(f.builtBytes(t), f.unsigned) {
		t.Fatalf("a darwin build was replaced using a Windows sum\n%s", out)
	}
}
