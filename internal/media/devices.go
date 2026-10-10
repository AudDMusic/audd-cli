package media

import (
	"context"
	"regexp"
	"strings"
)

// Device is an audio input that Capture (and audd listen --device) can
// record from.
type Device struct {
	Value string // what --device takes
	Label string // the name to show
}

// InputDevices lists the audio inputs the system reports: DirectShow
// devices on Windows and AVFoundation devices on macOS (through ffmpeg),
// PulseAudio sources on Linux (through pactl). It returns nil when they
// cannot be listed; recording then uses the default input.
func InputDevices(ctx context.Context, t Tools) []Device {
	switch currentOS {
	case "windows":
		if t.FFmpeg == "" {
			return nil
		}
		_, stderr, _ := run(ctx, command{Name: t.FFmpeg, Args: []string{"-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy"}})
		var out []Device
		for _, n := range parseDshowAudio(string(stderr)) {
			out = append(out, Device{Value: n, Label: n})
		}
		return out
	case "darwin":
		if t.FFmpeg == "" {
			return nil
		}
		_, stderr, _ := run(ctx, command{Name: t.FFmpeg, Args: []string{"-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", ""}})
		return parseAVFoundationAudio(string(stderr))
	case "linux":
		pactl, err := lookPath("pactl")
		if err != nil {
			return nil
		}
		stdout, _, err := run(ctx, command{Name: pactl, Args: []string{"list", "short", "sources"}})
		if err != nil {
			return nil
		}
		return parsePactlSources(string(stdout))
	}
	return nil
}

var avfDevice = regexp.MustCompile(`\]\s*\[(\d+)\]\s*(.+?)\s*$`)

// parseAVFoundationAudio reads the audio devices from
// `ffmpeg -f avfoundation -list_devices true -i ""`.
func parseAVFoundationAudio(listing string) []Device {
	var out []Device
	audio := false
	for _, l := range strings.Split(listing, "\n") {
		switch {
		case strings.Contains(l, "AVFoundation audio devices"):
			audio = true
			continue
		case strings.Contains(l, "AVFoundation video devices"):
			audio = false
			continue
		}
		if !audio {
			continue
		}
		if m := avfDevice.FindStringSubmatch(l); m != nil {
			out = append(out, Device{Value: ":" + m[1], Label: m[2]})
		}
	}
	return out
}

// parsePactlSources reads `pactl list short sources`, leaving out the
// monitors of outputs.
func parsePactlSources(listing string) []Device {
	var out []Device
	for _, l := range strings.Split(listing, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 2 {
			continue
		}
		name := strings.TrimSpace(f[1])
		if name == "" || strings.HasSuffix(name, ".monitor") {
			continue
		}
		out = append(out, Device{Value: name, Label: name})
	}
	return out
}
