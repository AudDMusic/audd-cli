package app

import (
	"runtime/debug"
	"strings"
)

// The values Version, Commit and Date hold when no -ldflags set them.
const (
	defaultVersion = "0.1.0"
	defaultCommit  = "none"
	defaultDate    = "unknown"
)

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		applyBuildInfo(info)
	}
}

// applyBuildInfo fills in build information that -ldflags left at its
// defaults, so `go install …@vX.Y.Z` builds report vX.Y.Z (and the commit
// and time, when the build recorded them).
func applyBuildInfo(info *debug.BuildInfo) {
	if info == nil {
		return
	}
	if Version == defaultVersion {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			Version = strings.TrimPrefix(v, "v")
		}
	}
	for _, s := range info.Settings {
		switch {
		case s.Key == "vcs.revision" && Commit == defaultCommit && s.Value != "":
			Commit = s.Value
		case s.Key == "vcs.time" && Date == defaultDate && s.Value != "":
			Date = s.Value
		}
	}
}
