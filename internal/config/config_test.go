package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/secrets"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AUDD_CONFIG_DIR", dir)
	t.Setenv("AUDD_API_TOKEN", "")
	return dir
}

func TestLoadMissingGivesDefaults(t *testing.T) {
	dir := isolate(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ActiveProfile != "default" || c.Profiles["default"] == nil || c.Profiles["default"].Name != "default" {
		t.Fatalf("defaults: %+v", c)
	}
	if c.Path != filepath.Join(dir, "config.toml") || Dir() != dir {
		t.Fatalf("path %q dir %q", c.Path, Dir())
	}
}

func TestSaveLoadRoundTripPreservesUnknownKeys(t *testing.T) {
	dir := isolate(t)
	path := filepath.Join(dir, "config.toml")
	orig := `# hand edited
active_profile = "work"
future_setting = "keep me"

[profiles.work]
format = "json"
max_requests = 500
custom = 42

[profiles.work.nested]
x = "y"

[other_section]
a = 1
`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	w := c.Profiles["work"]
	if c.ActiveProfile != "work" || w == nil || w.Format != "json" || w.MaxRequests != 500 || w.Name != "work" {
		t.Fatalf("loaded: %+v %+v", c, w)
	}
	off := false
	w.BackgroundRecorder = &off
	w.Concurrency = 8
	w.MaxRequests = 0 // unset
	c.Profile("default").Account = "me@example.com"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{`future_setting = 'keep me'`, `custom = 42`, `[other_section]`, `x = 'y'`, `background_recorder = false`, `concurrency = 8`, `account = 'me@example.com'`} {
		if !strings.Contains(strings.ReplaceAll(s, `"`, `'`), want) {
			t.Fatalf("saved file lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "max_requests") {
		t.Fatalf("unset key must be removed:\n%s", s)
	}
	c2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	w2 := c2.Profiles["work"]
	if w2.BackgroundRecorder == nil || *w2.BackgroundRecorder || w2.Concurrency != 8 || c2.Profiles["default"].Account != "me@example.com" {
		t.Fatalf("reloaded: %+v", w2)
	}
}

func TestLoadBadFile(t *testing.T) {
	dir := isolate(t)
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte("this is = = not toml"), 0o600)
	if _, err := Load(); err == nil {
		t.Fatal("want parse error")
	}
}

func TestProfileName(t *testing.T) {
	isolate(t)
	c, _ := Load()
	c.ActiveProfile = "saved"
	t.Setenv("AUDD_PROFILE", "")
	if got := ProfileName("", c); got != "saved" {
		t.Fatal(got)
	}
	t.Setenv("AUDD_PROFILE", "env")
	if got := ProfileName("", c); got != "env" {
		t.Fatal(got)
	}
	if got := ProfileName("flag", c); got != "flag" {
		t.Fatal(got)
	}
	c.ActiveProfile = ""
	t.Setenv("AUDD_PROFILE", "")
	if got := ProfileName("", c); got != "default" {
		t.Fatal(got)
	}
}

func TestResolveTokenPrecedence(t *testing.T) {
	isolate(t)
	sec := secrets.NewMemory()

	tok, src, err := ResolveToken("", "default", sec)
	if err != nil || tok != "" || src != SourceNone {
		t.Fatalf("none: %q %s %v", tok, src, err)
	}
	_ = sec.Set("default", "login_api_token", "login-token")
	if tok, src, _ = ResolveToken("", "default", sec); tok != "login-token" || src != SourceLogin {
		t.Fatalf("login: %q %s", tok, src)
	}
	_ = sec.Set("default", "api_token", "config-token")
	if tok, src, _ = ResolveToken("", "default", sec); tok != "config-token" || src != SourceConfig {
		t.Fatalf("config: %q %s", tok, src)
	}
	t.Setenv("AUDD_API_TOKEN", "env-token")
	if tok, src, _ = ResolveToken("", "default", sec); tok != "env-token" || src != SourceEnv {
		t.Fatalf("env: %q %s", tok, src)
	}
	if tok, src, _ = ResolveToken("flag-token", "default", sec); tok != "flag-token" || src != SourceFlag {
		t.Fatalf("flag: %q %s", tok, src)
	}
	t.Setenv("AUDD_API_TOKEN", "")
	if tok, src, _ = ResolveToken("", "other", sec); tok != "" || src != SourceNone {
		t.Fatalf("other profile must not see default's tokens: %q %s", tok, src)
	}
}

func TestMaskToken(t *testing.T) {
	cases := map[string]string{
		"0123456789abcdef0123456789abcdef": "0123…cdef",
		"short":                            "*****",
		"":                                 "",
		"123456789":                        "1234…6789",
	}
	for in, want := range cases {
		if got := MaskToken(in); got != want {
			t.Errorf("MaskToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKeys(t *testing.T) {
	p := &Profile{Name: "default"}
	for _, tc := range []struct{ key, val, want string }{
		{"format", "JSONL", "jsonl"},
		{"max_requests", "100", "100"},
		{"concurrency", "2", "2"},
		{"streams.background_recorder", "false", "false"},
	} {
		if err := SetKey(p, tc.key, tc.val); err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		if got, ok := GetKey(p, tc.key); !ok || got != tc.want {
			t.Fatalf("%s: got %q %v", tc.key, got, ok)
		}
		if err := UnsetKey(p, tc.key); err != nil {
			t.Fatal(err)
		}
		if _, ok := GetKey(p, tc.key); ok {
			t.Fatalf("%s still set", tc.key)
		}
	}
	for _, bad := range [][2]string{{"format", "yaml"}, {"max_requests", "-1"}, {"max_requests", "x"}, {"concurrency", "0"}, {"streams.background_recorder", "maybe"}, {"nope", "1"}} {
		if err := SetKey(p, bad[0], bad[1]); err == nil {
			t.Fatalf("SetKey(%q, %q) should fail", bad[0], bad[1])
		}
	}
	if !p.RecorderEnabled() {
		t.Fatal("background recorder is on by default")
	}
	off := false
	p.BackgroundRecorder = &off
	if p.RecorderEnabled() {
		t.Fatal("recorder off")
	}
}

func TestFileSafe(t *testing.T) {
	for in, want := range map[string]string{"work": "work", "my-team_2": "my-team_2", "bad/name": "bad_name", "a b.c": "a_b_c"} {
		if got := FileSafe(in); got != want {
			t.Errorf("FileSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveKeepsSettingsAnotherProcessChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	login, err := LoadFrom(path) // a long-running command loads the config
	if err != nil {
		t.Fatal(err)
	}
	other, _ := LoadFrom(path) // meanwhile, audd config set max_requests 50
	other.Profile(DefaultProfile).MaxRequests = 50
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	login.Profile(DefaultProfile).OAuthClientID = "client-1"
	if err := login.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadFrom(path)
	p := got.Profile(DefaultProfile)
	if p.MaxRequests != 50 || p.OAuthClientID != "client-1" {
		t.Fatalf("both changes must be kept: %+v", p)
	}
	if login.Profile(DefaultProfile).MaxRequests != 50 {
		t.Fatalf("the saving process should see the other change: %+v", login.Profile(DefaultProfile))
	}
	// Clearing a setting this process changed still clears it.
	login.Profile(DefaultProfile).OAuthClientID = ""
	if err := login.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ = LoadFrom(path)
	if p := got.Profile(DefaultProfile); p.OAuthClientID != "" || p.MaxRequests != 50 {
		t.Fatalf("%+v", p)
	}
}
