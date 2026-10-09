package media

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// Capture records seconds of audio from the default input device (or
// device) into a temporary WAV file, using ffmpeg when installed and sox
// otherwise. Call cleanup when done with outPath.
//
// Device names by platform (ffmpeg):
//
//	macOS    avfoundation audio index or name: "0", ":MacBook Pro Microphone"
//	Linux    PulseAudio source ("default", "alsa_input.usb-…"), or ALSA as
//	         "alsa:hw:1,0" / "hw:1,0"; PulseAudio falls back to ALSA
//	Windows  DirectShow device name: "Microphone (USB Audio)"; the first
//	         audio device by default
//
// With sox, device is passed as AUDIODEV.
func Capture(ctx context.Context, t Tools, seconds int, device string) (outPath string, cleanup func(), err error) {
	if seconds <= 0 {
		return "", nil, output.Errf(output.ExitUsage, "invalid_argument", "", "--seconds must be more than 0")
	}
	if t.FFmpeg == "" && t.Sox == "" {
		e := MissingTool("ffmpeg", "listening to the microphone")
		e.Message += " (sox works too)"
		return "", nil, e
	}
	out, rm, err := tempAudio("audd-listen-", ".wav")
	if err != nil {
		return "", nil, err
	}
	fail := func(err error) (string, func(), error) {
		rm()
		return "", nil, err
	}
	secs := strconv.Itoa(seconds)

	if t.FFmpeg == "" {
		c := command{Name: t.Sox, Args: []string{"-q", "-d", "-c", "1", "-r", "44100", "-b", "16", out, "trim", "0", secs}}
		if device != "" {
			c.Env = []string{"AUDIODEV=" + device}
		}
		if _, stderr, err := run(ctx, c); err != nil {
			return fail(captureError(ctx, "sox", stderr, err))
		}
		return out, rm, nil
	}

	inputs, err := captureInputs(ctx, t, device)
	if err != nil {
		return fail(err)
	}
	var lastErr error
	for _, in := range inputs {
		args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, in...)
		args = append(args, "-t", secs, "-vn", "-ac", "1", "-ar", "44100", "-c:a", "pcm_s16le", out)
		_, stderr, err := run(ctx, command{Name: t.FFmpeg, Args: args})
		if err == nil {
			return out, rm, nil
		}
		lastErr = captureError(ctx, "ffmpeg", stderr, err)
		if ctx.Err() != nil {
			break
		}
	}
	return fail(lastErr)
}

// captureInputs returns the ffmpeg input arguments to try, in order.
func captureInputs(ctx context.Context, t Tools, device string) ([][]string, error) {
	switch currentOS {
	case "darwin":
		if device == "" {
			device = "0"
		}
		if !strings.HasPrefix(device, ":") {
			device = ":" + device
		}
		return [][]string{{"-f", "avfoundation", "-i", device}}, nil
	case "linux":
		switch {
		case device == "":
			return [][]string{{"-f", "pulse", "-i", "default"}, {"-f", "alsa", "-i", "default"}}, nil
		case strings.HasPrefix(device, "alsa:"):
			return [][]string{{"-f", "alsa", "-i", strings.TrimPrefix(device, "alsa:")}}, nil
		case strings.HasPrefix(device, "hw:"), strings.HasPrefix(device, "plughw:"):
			return [][]string{{"-f", "alsa", "-i", device}}, nil
		default:
			return [][]string{{"-f", "pulse", "-i", strings.TrimPrefix(device, "pulse:")}}, nil
		}
	case "windows":
		if device == "" {
			_, stderr, _ := run(ctx, command{Name: t.FFmpeg, Args: []string{"-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy"}})
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			devs := parseDshowAudio(string(stderr))
			if len(devs) == 0 {
				return nil, output.Errf(output.ExitUsage, "no_input_device", "connect a microphone, or name one with --device",
					"no audio input device found")
			}
			device = devs[0]
		}
		if !strings.HasPrefix(device, "audio=") {
			device = "audio=" + device
		}
		return [][]string{{"-f", "dshow", "-i", device}}, nil
	default:
		if device == "" {
			device = "/dev/dsp"
		}
		return [][]string{{"-f", "oss", "-i", device}}, nil
	}
}

var (
	dshowQuoted   = regexp.MustCompile(`"([^"]+)"(?:\s+\((audio|video|audio, video|none)\))?\s*$`)
	dshowAltName  = regexp.MustCompile(`Alternative name`)
	dshowAudioHdr = regexp.MustCompile(`DirectShow audio devices`)
	dshowVideoHdr = regexp.MustCompile(`DirectShow video devices`)
)

// parseDshowAudio extracts audio device names from
// `ffmpeg -list_devices true -f dshow -i dummy` output, in either the
// current format (`"Name" (audio)`) or the older one (devices listed under
// a "DirectShow audio devices" heading).
func parseDshowAudio(listing string) []string {
	var out []string
	section := ""
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case dshowAudioHdr.MatchString(line):
			section = "audio"
			continue
		case dshowVideoHdr.MatchString(line):
			section = "video"
			continue
		case dshowAltName.MatchString(line):
			continue
		}
		m := dshowQuoted.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		kind := m[2]
		if kind == "" {
			kind = section
		}
		if strings.Contains(kind, "audio") {
			out = append(out, m[1])
		}
	}
	return out
}

func captureError(ctx context.Context, tool string, stderr []byte, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	msg := lastLines(stderr, 3)
	if msg == "" {
		msg = err.Error()
	}
	var hint string
	switch currentOS {
	case "darwin":
		hint = `allow microphone access for your terminal in System Settings > Privacy & Security > Microphone, or pick a device with --device (list them: ffmpeg -f avfoundation -list_devices true -i "")`
	case "windows":
		hint = `pick a device with --device "<name>" (list them: ffmpeg -list_devices true -f dshow -i dummy)`
	case "linux":
		hint = "pick a device with --device (PulseAudio sources: pactl list short sources; ALSA: alsa:hw:1,0)"
	default:
		hint = "pick a device with --device"
	}
	return output.Errf(output.ExitUsage, "capture_failed", hint, "%s could not record from the microphone: %s", tool, msg)
}
