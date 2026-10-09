package cli

import (
	"context"
	"time"
)

// SetMediaHelpers replaces the ffmpeg-backed helpers recognize uses and
// returns a function that restores them.
func SetMediaHelpers(trim func(ctx context.Context, src string, at, dur time.Duration) (string, func(), error), probe func(string) (time.Duration, bool)) (restore func()) {
	oldTrim, oldProbe := trimMedia, probeDuration
	if trim != nil {
		trimMedia = trim
	}
	if probe != nil {
		probeDuration = probe
	}
	return func() { trimMedia, probeDuration = oldTrim, oldProbe }
}

// SetExecutablePath makes audd update treat path as its own binary and
// returns a function that restores the real lookup.
func SetExecutablePath(path string) (restore func()) {
	old := executablePath
	executablePath = func() (string, error) { return path, nil }
	return func() { executablePath = old }
}

// WaitUpdateChecks waits for background release checks started by the
// update notice.
func WaitUpdateChecks() { updateChecks.Wait() }

// RecentSources returns the Source of each entry in the explorer's Recent
// tab, newest first.
func RecentSources(limit int) ([]string, error) {
	items, err := recentResults(limit)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Source
	}
	return out, nil
}
