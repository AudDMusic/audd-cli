// Package paths locates the per-user directories the CLI stores files in.
//
// The environment variables AUDD_CONFIG_DIR, AUDD_CACHE_DIR, and AUDD_DATA_DIR
// override the defaults; tests use them to isolate state in temporary dirs.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const appName = "audd"

// ConfigDir is where config.toml and credentials.json live:
// $XDG_CONFIG_HOME/audd or ~/.config/audd on Unix and macOS, %APPDATA%\audd on Windows.
func ConfigDir() string {
	if d := os.Getenv("AUDD_CONFIG_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, appName)
		}
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, appName)
	}
	return filepath.Join(home(), ".config", appName)
}

// CacheDir holds disposable data: the results cache, cover art, session hints.
func CacheDir() string {
	if d := os.Getenv("AUDD_CACHE_DIR"); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, appName)
	}
	return filepath.Join(home(), ".cache", appName)
}

// DataDir holds data worth keeping: jobs, the stream store, recorder state.
// $XDG_DATA_HOME/audd or ~/.local/share/audd on Unix and macOS, %LOCALAPPDATA%\audd on Windows.
func DataDir() string {
	if d := os.Getenv("AUDD_DATA_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, appName)
		}
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, appName)
	}
	return filepath.Join(home(), ".local", "share", appName)
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}
