package media

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// Trim cuts dur of audio starting at at from in (a file path or URL) into a
// temporary FLAC file. Call cleanup when done with outPath.
func Trim(ctx context.Context, t Tools, in string, at, dur time.Duration) (outPath string, cleanup func(), err error) {
	if at < 0 {
		return "", nil, output.Errf(output.ExitUsage, "invalid_argument", "", "--at must not be negative")
	}
	if dur <= 0 {
		return "", nil, output.Errf(output.ExitUsage, "invalid_argument", "", "--duration must be more than 0")
	}
	if t.FFmpeg == "" {
		return "", nil, MissingTool("ffmpeg", "trimming with --at")
	}
	out, rm, err := tempAudio("audd-clip-", ".flac")
	if err != nil {
		return "", nil, err
	}
	_, stderr, err := run(ctx, command{Name: t.FFmpeg, Args: []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-ss", seconds(at), "-i", in, "-t", seconds(dur),
		"-vn", "-map", "0:a:0", "-c:a", "flac", out,
	}})
	if err != nil {
		rm()
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		msg := lastLines(stderr, 3)
		if msg == "" {
			msg = err.Error()
		}
		return "", nil, output.Errf(output.ExitUsage, "invalid_media", "check that the file plays and has an audio track",
			"ffmpeg could not cut %s: %s", in, msg)
	}
	if !hasAudio(t, out) {
		rm()
		return "", nil, output.Errf(output.ExitUsage, "invalid_argument", "pick an earlier --at",
			"there is no audio at %s in %s", clock(at), in)
	}
	return out, rm, nil
}

// hasAudio reports whether the FLAC clip ffmpeg wrote holds any samples.
// With --at past the end ffmpeg still succeeds and writes a file with
// headers but no audio, so the size alone does not tell. The FLAC
// STREAMINFO block carries the sample count; when it cannot be read,
// ffprobe's duration decides.
func hasAudio(t Tools, path string) bool {
	if n, ok := flacSamples(path); ok {
		return n > 0
	}
	if t.FFprobe != "" {
		_, ok := Duration(t, path)
		return ok
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}

// flacSamples reads the total sample count from a FLAC file's STREAMINFO
// block. ok is false when the file is not FLAC or the count is not stored.
func flacSamples(path string) (n uint64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	var b [42]byte // "fLaC", block header (4), STREAMINFO (34)
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return 0, false
	}
	if string(b[:4]) != "fLaC" || b[4]&0x7f != 0 {
		return 0, false
	}
	// STREAMINFO bytes 13-17 (file offsets 21-25): the low 4 bits of the
	// first byte and the next 4 bytes are the 36-bit sample count.
	n = uint64(b[21]&0x0f)<<32 | uint64(b[22])<<24 | uint64(b[23])<<16 | uint64(b[24])<<8 | uint64(b[25])
	return n, true
}

// seconds formats d as ffmpeg seconds with millisecond precision.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

// clock formats d as m:ss or h:mm:ss.
func clock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
