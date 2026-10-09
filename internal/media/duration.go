package media

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
)

// Duration returns the length of a local or remote media file using
// ffprobe. ok is false when ffprobe is missing or cannot tell.
func Duration(t Tools, path string) (d time.Duration, ok bool) {
	if t.FFprobe == "" {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, _, err := run(ctx, command{Name: t.FFprobe, Args: []string{
		"-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path,
	}})
	if err != nil {
		return 0, false
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || secs <= 0 || math.IsNaN(secs) || math.IsInf(secs, 0) {
		return 0, false
	}
	return time.Duration(math.Round(secs*1000)) * time.Millisecond, true
}
