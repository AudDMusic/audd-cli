// Package config reads and writes ~/.config/audd/config.toml
// (%APPDATA%\audd\config.toml on Windows) and resolves the API token.
//
// Layout:
//
//	active_profile = "default"
//
//	[profiles.default]
//	format = "json"
//	max_requests = 500
//
// Keys the CLI does not know are kept as they are when the file is saved.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"

	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/paths"
	"github.com/AudDMusic/audd-cli/internal/secrets"
)

// DefaultProfile is the profile used when none is selected.
const DefaultProfile = "default"

// Config is the parsed config file.
type Config struct {
	ActiveProfile string
	Profiles      map[string]*Profile
	Path          string

	raw map[string]any // the whole file, for preserving unknown keys

	// loaded is the file's state when it was read (or last saved), so
	// Save writes back only what this process changed.
	loadedActive   string
	loadedProfiles map[string]Profile
}

// Profile holds per-profile settings. API tokens and OAuth sessions are not
// here; they live in the secrets store.
type Profile struct {
	Name               string `toml:"-"`
	Format             string `toml:"format,omitempty"`
	MaxRequests        int    `toml:"max_requests,omitempty"`
	Concurrency        int    `toml:"concurrency,omitempty"`
	BackgroundRecorder *bool  `toml:"background_recorder,omitempty"` // streams.background_recorder
	OAuthClientID      string `toml:"oauth_client_id,omitempty"`
	// OAuthIssuer is the authorization server OAuthClientID belongs to.
	OAuthIssuer string   `toml:"oauth_issuer,omitempty"`
	OAuthScopes []string `toml:"oauth_scopes,omitempty"`
	Account     string   `toml:"account,omitempty"` // email, for display only
}

// RecorderEnabled reports whether the background stream recorder may start
// automatically (on unless streams.background_recorder is false).
func (p *Profile) RecorderEnabled() bool {
	return p.BackgroundRecorder == nil || *p.BackgroundRecorder
}

// Dir is the user config dir for audd.
func Dir() string { return paths.ConfigDir() }

// CacheDir is the user cache dir for audd.
func CacheDir() string { return paths.CacheDir() }

// DataDir is the user data dir for audd.
func DataDir() string { return paths.DataDir() }

// Load reads Dir()/config.toml. A missing file gives defaults with a
// "default" profile.
func Load() (*Config, error) {
	return LoadFrom(filepath.Join(Dir(), "config.toml"))
}

// Defaults returns the configuration used when there is no config file at path.
func Defaults(path string) *Config {
	c := &Config{Path: path, Profiles: map[string]*Profile{}, raw: map[string]any{}, ActiveProfile: DefaultProfile}
	c.Profile(c.ActiveProfile)
	c.snapshot()
	return c
}

// LoadFrom reads the config file at path.
func LoadFrom(path string) (*Config, error) {
	c := &Config{Path: path}
	if err := c.read(); err != nil {
		return nil, err
	}
	if c.ActiveProfile == "" {
		c.ActiveProfile = DefaultProfile
	}
	c.Profile(c.ActiveProfile)
	c.snapshot()
	return c, nil
}

// read parses the file at c.Path into c and records it as the loaded state.
func (c *Config) read() error {
	c.raw, c.Profiles, c.ActiveProfile = map[string]any{}, map[string]*Profile{}, ""
	b, err := os.ReadFile(c.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := toml.Unmarshal(b, &c.raw); err != nil {
			return output.Errf(output.ExitUsage, "invalid_config", "fix or remove "+c.Path,
				"cannot read the config file %s: %v", c.Path, err)
		}
		var known struct {
			ActiveProfile string              `toml:"active_profile"`
			Profiles      map[string]*Profile `toml:"profiles"`
		}
		if err := toml.Unmarshal(b, &known); err != nil {
			return output.Errf(output.ExitUsage, "invalid_config", "fix or remove "+c.Path,
				"cannot read the config file %s: %v", c.Path, err)
		}
		c.ActiveProfile = known.ActiveProfile
		for name, p := range known.Profiles {
			if p == nil {
				p = &Profile{}
			}
			p.Name = name
			c.Profiles[name] = p
		}
	}
	c.snapshot()
	return nil
}

// snapshot records the current settings as the state on disk.
func (c *Config) snapshot() {
	c.loadedActive = c.ActiveProfile
	c.loadedProfiles = map[string]Profile{}
	for name, p := range c.Profiles {
		c.loadedProfiles[name] = p.clone()
	}
}

func (p *Profile) clone() Profile {
	q := *p
	q.OAuthScopes = append([]string(nil), p.OAuthScopes...)
	if p.BackgroundRecorder != nil {
		b := *p.BackgroundRecorder
		q.BackgroundRecorder = &b
	}
	return q
}

// Profile returns the named profile, creating it if missing.
func (c *Config) Profile(name string) *Profile {
	if name == "" {
		name = DefaultProfile
	}
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	p, ok := c.Profiles[name]
	if !ok {
		p = &Profile{Name: name}
		c.Profiles[name] = p
	}
	return p
}

// FileSafe returns name with every character other than letters, digits,
// '-' and '_' replaced by '_', for use in file names. A valid profile name
// is one that FileSafe leaves unchanged.
func FileSafe(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
}

// ProfileNames returns profile names, sorted.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// configLockWait is how long Save waits for another audd process that is
// writing the config file.
const configLockWait = 30 * time.Second

// Save writes the config atomically with mode 0600, keeping unknown keys.
// It re-reads the file under a lock that other audd processes also take
// and writes back only the settings this process changed, so a long-running
// command (audd login, say) does not undo settings changed meanwhile.
func (c *Config) Save() error {
	if c.Path == "" {
		c.Path = filepath.Join(Dir(), "config.toml")
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	fl := flock.New(c.Path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), configLockWait)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("waiting for the lock on %s: %w", c.Path, err)
	}
	if !ok {
		return fmt.Errorf("could not lock %s", c.Path)
	}
	defer func() { _ = fl.Unlock() }()

	disk := &Config{Path: c.Path}
	if err := disk.read(); err != nil {
		return err
	}
	if c.ActiveProfile != c.loadedActive {
		disk.ActiveProfile = c.ActiveProfile
	}
	for name, p := range c.Profiles {
		old, had := c.loadedProfiles[name]
		if !had {
			old = Profile{Name: name}
		}
		mergeChanged(disk.Profile(name), p, &old)
	}
	if disk.ActiveProfile == "" {
		disk.ActiveProfile = DefaultProfile
	}
	if err := disk.write(); err != nil {
		return err
	}
	// Take in what other processes changed, keeping the profile pointers
	// the caller holds.
	c.raw, c.ActiveProfile = disk.raw, disk.ActiveProfile
	for name, p := range disk.Profiles {
		if cur, ok := c.Profiles[name]; ok {
			*cur = p.clone()
			cur.Name = name
		} else {
			q := p.clone()
			c.Profiles[name] = &q
		}
	}
	c.snapshot()
	return nil
}

// mergeChanged copies into dst each setting that p changed from old.
func mergeChanged(dst, p, old *Profile) {
	if p.Format != old.Format {
		dst.Format = p.Format
	}
	if p.MaxRequests != old.MaxRequests {
		dst.MaxRequests = p.MaxRequests
	}
	if p.Concurrency != old.Concurrency {
		dst.Concurrency = p.Concurrency
	}
	if !boolPtrEqual(p.BackgroundRecorder, old.BackgroundRecorder) {
		dst.BackgroundRecorder = p.clone().BackgroundRecorder
	}
	if p.OAuthClientID != old.OAuthClientID {
		dst.OAuthClientID = p.OAuthClientID
	}
	if p.OAuthIssuer != old.OAuthIssuer {
		dst.OAuthIssuer = p.OAuthIssuer
	}
	if !slices.Equal(p.OAuthScopes, old.OAuthScopes) {
		dst.OAuthScopes = append([]string(nil), p.OAuthScopes...)
	}
	if p.Account != old.Account {
		dst.Account = p.Account
	}
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// write saves c to c.Path atomically, keeping unknown keys.
func (c *Config) write() error {
	if c.raw == nil {
		c.raw = map[string]any{}
	}
	c.raw["active_profile"] = c.ActiveProfile
	rawProfiles, _ := c.raw["profiles"].(map[string]any)
	if rawProfiles == nil {
		rawProfiles = map[string]any{}
	}
	for name, p := range c.Profiles {
		rp, _ := rawProfiles[name].(map[string]any)
		if rp == nil {
			rp = map[string]any{}
		}
		mergeProfile(rp, p)
		if len(rp) == 0 {
			delete(rawProfiles, name)
			continue
		}
		rawProfiles[name] = rp
	}
	if len(rawProfiles) == 0 {
		delete(c.raw, "profiles")
	} else {
		c.raw["profiles"] = rawProfiles
	}
	b, err := toml.Marshal(c.raw)
	if err != nil {
		return err
	}
	return secrets.WriteFileAtomic(c.Path, b, 0o600)
}

// mergeProfile writes the known fields of p into rp, removing unset ones.
func mergeProfile(rp map[string]any, p *Profile) {
	set := func(key string, v any, isSet bool) {
		if isSet {
			rp[key] = v
		} else {
			delete(rp, key)
		}
	}
	set("format", p.Format, p.Format != "")
	set("max_requests", p.MaxRequests, p.MaxRequests != 0)
	set("concurrency", p.Concurrency, p.Concurrency != 0)
	if p.BackgroundRecorder != nil {
		rp["background_recorder"] = *p.BackgroundRecorder
	} else {
		delete(rp, "background_recorder")
	}
	set("oauth_client_id", p.OAuthClientID, p.OAuthClientID != "")
	set("oauth_issuer", p.OAuthIssuer, p.OAuthIssuer != "")
	set("oauth_scopes", p.OAuthScopes, len(p.OAuthScopes) > 0)
	set("account", p.Account, p.Account != "")
}

// ProfileName picks the active profile: --profile > AUDD_PROFILE > the
// config's active_profile > "default".
func ProfileName(flag string, c *Config) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("AUDD_PROFILE"); v != "" {
		return v
	}
	if c != nil && c.ActiveProfile != "" {
		return c.ActiveProfile
	}
	return DefaultProfile
}

// TokenSource names where the API token came from.
type TokenSource string

const (
	SourceFlag   TokenSource = "flag"
	SourceEnv    TokenSource = "env"
	SourceConfig TokenSource = "config"
	SourceLogin  TokenSource = "login"
	SourceNone   TokenSource = "none"
)

// Describe returns a human description of the source.
func (s TokenSource) Describe() string {
	switch s {
	case SourceFlag:
		return "--token flag"
	case SourceEnv:
		return "AUDD_API_TOKEN environment variable"
	case SourceConfig:
		return "audd config set token"
	case SourceLogin:
		return "fetched by audd login"
	}
	return "no token"
}

// ResolveToken applies the precedence --token > AUDD_API_TOKEN >
// `audd config set token` > the token fetched by `audd login`. With no
// token it returns "", SourceNone, nil.
func ResolveToken(flag string, profile string, sec secrets.Store) (token string, src TokenSource, err error) {
	if t := strings.TrimSpace(flag); t != "" {
		return t, SourceFlag, nil
	}
	if t := strings.TrimSpace(os.Getenv("AUDD_API_TOKEN")); t != "" {
		return t, SourceEnv, nil
	}
	if sec == nil {
		return "", SourceNone, nil
	}
	for _, k := range []struct {
		key string
		src TokenSource
	}{{"api_token", SourceConfig}, {"login_api_token", SourceLogin}} {
		t, err := sec.Get(profile, k.key)
		if err == nil && t != "" {
			return t, k.src, nil
		}
		if err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return "", SourceNone, fmt.Errorf("reading stored credentials: %w", err)
		}
	}
	return "", SourceNone, nil
}

// MaskToken shows the first and last four characters: "0123…cdef".
func MaskToken(t string) string {
	if len(t) <= 8 {
		return strings.Repeat("*", len(t))
	}
	return t[:4] + "…" + t[len(t)-4:]
}

// Keys are the settings `audd config` accepts. "token" is stored in the
// secrets store, not in config.toml.
var Keys = []string{"token", "format", "max_requests", "concurrency", "streams.background_recorder"}

func badKey(key string) error {
	return output.Errf(output.ExitUsage, "invalid_argument", "keys: "+strings.Join(Keys, ", "),
		"unknown config key %q", key)
}

// GetKey returns a profile setting (not "token") and whether it is set.
func GetKey(p *Profile, key string) (string, bool) {
	switch key {
	case "format":
		return p.Format, p.Format != ""
	case "max_requests":
		return strconv.Itoa(p.MaxRequests), p.MaxRequests != 0
	case "concurrency":
		return strconv.Itoa(p.Concurrency), p.Concurrency != 0
	case "streams.background_recorder":
		if p.BackgroundRecorder == nil {
			return "", false
		}
		return strconv.FormatBool(*p.BackgroundRecorder), true
	}
	return "", false
}

// SetKey validates and stores a profile setting (not "token").
func SetKey(p *Profile, key, value string) error {
	value = strings.TrimSpace(value)
	positive := func(min int) (int, error) {
		n, err := strconv.Atoi(value)
		if err != nil || n < min {
			example := map[string]string{"max_requests": "500", "concurrency": "4"}[key]
			return 0, output.Errf(output.ExitUsage, "invalid_argument", "audd config set "+key+" "+example,
				"%s must be a whole number of at least %d, got %q", key, min, value)
		}
		return n, nil
	}
	switch key {
	case "format":
		f, err := output.ParseFormat(value)
		if err != nil {
			return err
		}
		p.Format = string(f)
	case "max_requests":
		n, err := positive(1)
		if err != nil {
			return err
		}
		p.MaxRequests = n
	case "concurrency":
		n, err := positive(1)
		if err != nil {
			return err
		}
		p.Concurrency = n
	case "streams.background_recorder":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return output.Errf(output.ExitUsage, "invalid_argument", "use true or false",
				"streams.background_recorder must be true or false, got %q", value)
		}
		p.BackgroundRecorder = &b
	default:
		return badKey(key)
	}
	return nil
}

// UnsetKey clears a profile setting (not "token").
func UnsetKey(p *Profile, key string) error {
	switch key {
	case "format":
		p.Format = ""
	case "max_requests":
		p.MaxRequests = 0
	case "concurrency":
		p.Concurrency = 0
	case "streams.background_recorder":
		p.BackgroundRecorder = nil
	default:
		return badKey(key)
	}
	return nil
}
