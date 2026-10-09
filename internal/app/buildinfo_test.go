package app

import (
	"runtime/debug"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/update"
)

func withBuildVars(t *testing.T, v, c, d string) {
	t.Helper()
	ov, oc, od := Version, Commit, Date
	Version, Commit, Date = v, c, d
	t.Cleanup(func() { Version, Commit, Date = ov, oc, od })
}

func TestBuildInfoFillsGoInstallVersion(t *testing.T) {
	withBuildVars(t, defaultVersion, defaultCommit, defaultDate)
	applyBuildInfo(&debug.BuildInfo{
		Main: debug.Module{Path: "github.com/AudDMusic/audd-cli", Version: "v1.2.3"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"},
			{Key: "vcs.time", Value: "2026-10-08T10:00:00Z"},
		},
	})
	if Version != "1.2.3" || Commit != "abc123" || Date != "2026-10-08T10:00:00Z" {
		t.Fatalf("got %q %q %q", Version, Commit, Date)
	}
	// A go install of the latest release must not be told to update.
	m := update.DetectMethod("/home/u/go/bin/audd", "linux", func(k string) string {
		if k == "GOBIN" {
			return "/home/u/go/bin"
		}
		return ""
	})
	if m.Name != "go" {
		t.Fatalf("method %q", m.Name)
	}
	if n := update.Notice(Version, update.State{Latest: "v1.2.3"}, m); n != "" {
		t.Fatalf("notice for an up-to-date go install: %q", n)
	}
	if n := update.Notice(Version, update.State{Latest: "v1.2.4"}, m); n == "" {
		t.Fatal("no notice for a newer release")
	}
}

func TestBuildInfoIgnoresDevelAndLdflags(t *testing.T) {
	withBuildVars(t, defaultVersion, defaultCommit, defaultDate)
	applyBuildInfo(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}})
	if Version != defaultVersion || Commit != defaultCommit || Date != defaultDate {
		t.Fatalf("devel build changed values: %q %q %q", Version, Commit, Date)
	}
	applyBuildInfo(nil)
	if Version != defaultVersion {
		t.Fatalf("nil build info changed version: %q", Version)
	}

	withBuildVars(t, "2.0.0", "deadbeef", "2026-01-01")
	applyBuildInfo(&debug.BuildInfo{
		Main:     debug.Module{Version: "v1.2.3"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}},
	})
	if Version != "2.0.0" || Commit != "deadbeef" || Date != "2026-01-01" {
		t.Fatalf("ldflags values overwritten: %q %q %q", Version, Commit, Date)
	}
}
