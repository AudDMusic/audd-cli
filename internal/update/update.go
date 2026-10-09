// Package update finds new audd releases, tells people about them at most
// once a day, and replaces the binary for installs that no package manager
// owns.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases come from.
const Repo = "AudDMusic/audd-cli"

// DefaultAPIBase is the GitHub API.
const DefaultAPIBase = "https://api.github.com"

// APIBase returns the GitHub API base, or AUDD_UPDATE_URL when set (tests).
func APIBase() string {
	if u := strings.TrimSpace(os.Getenv("AUDD_UPDATE_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return DefaultAPIBase
}

// Release is a published audd release.
type Release struct {
	Version string  `json:"version"` // without the leading "v"
	Tag     string  `json:"tag"`
	URL     string  `json:"url"` // the release page
	Assets  []Asset `json:"-"`
}

// Asset is a downloadable file of a release.
type Asset struct {
	Name string
	URL  string
}

// Asset returns the asset with this name.
func (r *Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context, client *http.Client, base string) (*Release, error) {
	if client == nil {
		client = http.DefaultClient
	}
	u := strings.TrimRight(base, "/") + "/repos/" + Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("no audd release has been published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the release check returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("reading the release check: %w", err)
	}
	v := strings.TrimPrefix(strings.TrimSpace(body.TagName), "v")
	if _, ok := parseVersion(v); !ok {
		return nil, fmt.Errorf("the latest release has an unexpected tag %q", body.TagName)
	}
	r := &Release{Version: v, Tag: body.TagName, URL: body.HTMLURL}
	for _, a := range body.Assets {
		r.Assets = append(r.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return r, nil
}

// parseVersion reads "1.2.3" (a leading "v" and a "-pre" suffix are
// allowed; the suffix sorts before the plain version).
func parseVersion(s string) ([4]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var v [4]int
	v[3] = 1 // a release sorts after its pre-releases
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		if s[i] == '-' {
			v[3] = 0
		}
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// Newer reports whether latest is a newer version than current. Versions
// that do not parse are never newer.
func Newer(latest, current string) bool {
	l, ok1 := parseVersion(latest)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// State is the cached result of the last release check.
type State struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
	URL       string    `json:"url,omitempty"`
	// NoticedAt is when the update notice was last shown.
	NoticedAt time.Time `json:"noticed_at,omitzero"`
}

// CheckInterval is how often the release check runs.
const CheckInterval = 24 * time.Hour

const stateFile = "update-check.json"

// LoadState reads the cached check from dir; a missing or broken file
// gives an empty State.
func LoadState(dir string) State {
	var s State
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SaveState writes the cached check to dir.
func SaveState(dir string, s State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, stateFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, stateFile))
}

// Due reports whether the cached check is older than CheckInterval.
func (s State) Due(now time.Time) bool {
	return s.CheckedAt.IsZero() || now.Sub(s.CheckedAt) >= CheckInterval || s.CheckedAt.After(now.Add(time.Hour))
}

// NoticeDue reports whether the update notice may be shown again: it
// appears at most once per CheckInterval.
func (s State) NoticeDue(now time.Time) bool {
	return s.NoticedAt.IsZero() || now.Sub(s.NoticedAt) >= CheckInterval || s.NoticedAt.After(now.Add(time.Hour))
}

// NoticeEnv is what decides whether the update notice may be shown.
type NoticeEnv struct {
	StderrTTY bool
	Getenv    func(string) string
}

// NoticeAllowed reports whether to check for and announce updates: only
// with stderr on a terminal, never in CI, and not with
// AUDD_NO_UPDATE_CHECK set.
func NoticeAllowed(env NoticeEnv) bool {
	if !env.StderrTTY {
		return false
	}
	get := env.Getenv
	if get == nil {
		get = os.Getenv
	}
	if v := get("AUDD_NO_UPDATE_CHECK"); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		return false
	}
	for _, k := range []string{"CI", "BUILD_NUMBER", "RUN_ID", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TF_BUILD", "JENKINS_URL", "TEAMCITY_VERSION"} {
		if v := get(k); v != "" && v != "0" && !strings.EqualFold(v, "false") {
			return false
		}
	}
	return true
}

// Notice is the line shown when a newer release is cached, or "".
func Notice(current string, s State, m Method) string {
	if s.Latest == "" || !Newer(s.Latest, current) {
		return ""
	}
	how := "audd update"
	if !m.SelfUpdate && m.Command != "" {
		how = m.Command
	}
	return fmt.Sprintf("audd %s is available (you have %s). Update with: %s", s.Latest, strings.TrimPrefix(current, "v"), how)
}

// ErrNoAsset means the release has no build for this system.
var ErrNoAsset = errors.New("the release has no build for this system")
