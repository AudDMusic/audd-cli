package media

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// fakeRunner records commands and answers them with canned output. When a
// command's last argument looks like an output file, it writes a few bytes
// there, as ffmpeg and sox would.
type fakeRunner struct {
	calls  []command
	stdout map[string]string // by tool base name
	stderr map[string]string
	fail   map[string]error
	failN  int // fail only the first failN calls of a failing tool (0: all)
}

func (f *fakeRunner) run(ctx context.Context, c command) ([]byte, []byte, error) {
	f.calls = append(f.calls, c)
	base := filepath.Base(c.Name)
	if err := f.fail[base]; err != nil {
		n := 0
		for _, cc := range f.calls {
			if filepath.Base(cc.Name) == base {
				n++
			}
		}
		if f.failN == 0 || n <= f.failN {
			return nil, []byte(f.stderr[base]), err
		}
	}
	if base != "ffprobe" && len(c.Args) > 0 {
		out := c.Args[len(c.Args)-1]
		if base == "sox" {
			for i, a := range c.Args {
				if a == "trim" && i > 0 {
					out = c.Args[i-1]
				}
			}
		}
		if strings.Contains(filepath.Base(out), "audd-") {
			_ = os.WriteFile(out, []byte("audio"), 0o600)
		}
	}
	return []byte(f.stdout[base]), []byte(f.stderr[base]), nil
}

func withRunner(t *testing.T, f *fakeRunner) {
	t.Helper()
	old := run
	run = f.run
	t.Cleanup(func() { run = old })
}

func withOS(t *testing.T, goos string) {
	t.Helper()
	old := currentOS
	currentOS = goos
	t.Cleanup(func() { currentOS = old })
}

func withLookPath(t *testing.T, found ...string) {
	t.Helper()
	old := lookPath
	lookPath = func(name string) (string, error) {
		for _, f := range found {
			if f == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = old })
}

func TestFind(t *testing.T) {
	withLookPath(t, "ffmpeg", "sox")
	got := Find()
	if got.FFmpeg != "/usr/bin/ffmpeg" || got.FFprobe != "" || got.Sox != "/usr/bin/sox" {
		t.Fatalf("Find() = %+v", got)
	}
}

func TestInstallHint(t *testing.T) {
	cases := []struct {
		os, tool string
		have     []string
		want     string
	}{
		{"darwin", "ffmpeg", nil, "brew install ffmpeg"},
		{"darwin", "ffprobe", nil, "brew install ffmpeg"},
		{"darwin", "sox", nil, "brew install sox"},
		{"linux", "ffmpeg", []string{"apt-get"}, "sudo apt install ffmpeg"},
		{"linux", "ffprobe", []string{"apt-get"}, "sudo apt install ffmpeg"},
		{"linux", "sox", []string{"apt-get"}, "sudo apt install sox"},
		{"linux", "ffmpeg", []string{"dnf"}, "sudo dnf install ffmpeg-free"},
		{"linux", "sox", []string{"dnf"}, "sudo dnf install sox"},
		{"linux", "ffmpeg", []string{"pacman"}, "sudo pacman -S ffmpeg"},
		{"linux", "ffmpeg", []string{"apk"}, "sudo apk add ffmpeg"},
		{"linux", "ffmpeg", []string{"zypper"}, "sudo zypper install ffmpeg"},
		{"linux", "ffmpeg", nil, "install ffmpeg with your package manager (https://ffmpeg.org/download.html)"},
		{"windows", "ffmpeg", nil, "winget install --id Gyan.FFmpeg (or: choco install ffmpeg)"},
		{"windows", "sox", nil, "winget install --id ChrisBagwell.SoX (or: choco install sox.portable)"},
		{"freebsd", "sox", nil, "install sox with your package manager (https://sourceforge.net/projects/sox/)"},
	}
	for _, c := range cases {
		withOS(t, c.os)
		withLookPath(t, c.have...)
		if got := InstallHint(c.tool); got != c.want {
			t.Errorf("%s/%s %v: got %q, want %q", c.os, c.tool, c.have, got, c.want)
		}
	}
}

func TestMissingToolIsUsageError(t *testing.T) {
	withOS(t, "darwin")
	err := MissingTool("ffmpeg", "trimming with --at")
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Exit != output.ExitUsage || oe.Code != "missing_tool" || oe.Hint != "brew install ffmpeg" {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(oe.Message, "ffmpeg") || !strings.Contains(oe.Message, "trimming with --at") {
		t.Fatalf("message %q", oe.Message)
	}
}

func TestDuration(t *testing.T) {
	f := &fakeRunner{stdout: map[string]string{"ffprobe": "183.456000\n"}}
	withRunner(t, f)
	d, ok := Duration(Tools{FFprobe: "/usr/bin/ffprobe"}, "song.mp3")
	if !ok || d != 183456*time.Millisecond {
		t.Fatalf("got %v %v", d, ok)
	}
	want := []string{"-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", "song.mp3"}
	if got := f.calls[0].Args; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args %q", got)
	}

	if _, ok := Duration(Tools{}, "song.mp3"); ok {
		t.Fatal("no ffprobe should report unknown")
	}
	for _, out := range []string{"N/A\n", "", "-1"} {
		f.stdout["ffprobe"] = out
		if _, ok := Duration(Tools{FFprobe: "ffprobe"}, "x.mp3"); ok {
			t.Fatalf("%q should be unknown", out)
		}
	}
	f.fail = map[string]error{"ffprobe": errors.New("exit status 1")}
	if _, ok := Duration(Tools{FFprobe: "ffprobe"}, "x.mp3"); ok {
		t.Fatal("failed ffprobe should be unknown")
	}
}

func TestTrimArgsAndCleanup(t *testing.T) {
	f := &fakeRunner{}
	withRunner(t, f)
	out, cleanup, err := Trim(context.Background(), Tools{FFmpeg: "/usr/bin/ffmpeg"}, "in.mp3", 90*time.Second+500*time.Millisecond, 12*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(out) != ".flac" {
		t.Fatalf("out %q", out)
	}
	want := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-ss", "90.500", "-i", "in.mp3", "-t", "12.000", "-vn", "-map", "0:a:0", "-c:a", "flac", out}
	if got := f.calls[0].Args; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("clip should exist: %v", err)
	}
	cleanup()
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove the clip: %v", err)
	}
}

func TestTrimErrors(t *testing.T) {
	withOS(t, "linux")
	withLookPath(t, "apt-get")
	_, _, err := Trim(context.Background(), Tools{}, "in.mp3", 0, time.Second)
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "missing_tool" || oe.Hint != "sudo apt install ffmpeg" {
		t.Fatalf("got %#v", err)
	}

	f := &fakeRunner{fail: map[string]error{"ffmpeg": errors.New("exit status 1")}, stderr: map[string]string{"ffmpeg": "in.mp3: Invalid data found when processing input\n"}}
	withRunner(t, f)
	_, _, err = Trim(context.Background(), Tools{FFmpeg: "ffmpeg"}, "in.mp3", 0, time.Second)
	if !errors.As(err, &oe) || oe.Exit != output.ExitUsage || !strings.Contains(oe.Message, "Invalid data") {
		t.Fatalf("got %#v", err)
	}
	if _, _, err := Trim(context.Background(), Tools{FFmpeg: "ffmpeg"}, "in.mp3", -time.Second, time.Second); err == nil {
		t.Fatal("negative --at should fail")
	}
	if _, _, err := Trim(context.Background(), Tools{FFmpeg: "ffmpeg"}, "in.mp3", 0, 0); err == nil {
		t.Fatal("zero duration should fail")
	}
}

// flacHeader is the start of the FLAC file ffmpeg 6 writes when --at is
// past the end: a STREAMINFO block with a sample count of 0, then padding.
func flacHeader(samples uint64) []byte {
	b := []byte{0x66, 0x4c, 0x61, 0x43, 0x00, 0x00, 0x00, 0x22,
		0x12, 0x00, 0x12, 0x00, 0x00, 0x36, 0x16, 0x00, 0x00, 0x00, 0x0a, 0xc4, 0x41, 0x70,
		0x00, 0x00, 0x00, 0x00, // sample count, set below
		0xd4, 0x1d, 0x8c, 0xd9, 0x8f, 0x00, 0xb2, 0x04, 0xe9, 0x80, 0x09, 0x98, 0xec, 0xf8, 0x42, 0x7e}
	b[21] |= byte(samples>>32) & 0x0f
	b[22], b[23], b[24], b[25] = byte(samples>>24), byte(samples>>16), byte(samples>>8), byte(samples)
	return append(b, make([]byte, 8288-len(b))...) // padding, as ffmpeg writes
}

func TestTrimPastTheEnd(t *testing.T) {
	// ffmpeg succeeds and writes a FLAC file with headers but no audio when
	// --at is past the end.
	old := run
	t.Cleanup(func() { run = old })
	for _, samples := range []uint64{0, 88200} {
		run = func(ctx context.Context, c command) ([]byte, []byte, error) {
			if filepath.Base(c.Name) == "ffprobe" {
				return nil, nil, errors.New("ffprobe should not be needed for FLAC")
			}
			return nil, nil, os.WriteFile(c.Args[len(c.Args)-1], flacHeader(samples), 0o600)
		}
		out, cleanup, err := Trim(context.Background(), Tools{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}, "in.mp3", time.Hour, time.Second)
		if samples > 0 {
			if err != nil {
				t.Fatalf("clip with audio: %v", err)
			}
			cleanup()
			continue
		}
		var oe *output.Error
		if !errors.As(err, &oe) || oe.Code != "invalid_argument" || !strings.Contains(oe.Message, "1:00:00") {
			t.Fatalf("got %#v", err)
		}
		if out != "" {
			t.Fatalf("out %q", out)
		}
	}

	// Not FLAC (unexpected): ffprobe decides.
	run = func(ctx context.Context, c command) ([]byte, []byte, error) {
		if filepath.Base(c.Name) == "ffprobe" {
			return []byte("N/A\n"), nil, nil
		}
		return nil, nil, os.WriteFile(c.Args[len(c.Args)-1], []byte("RIFF...."), 0o600)
	}
	if _, _, err := Trim(context.Background(), Tools{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}, "in.mp3", time.Hour, time.Second); err == nil {
		t.Fatal("a clip ffprobe finds no duration in should fail")
	}
}

func TestCaptureArgsPerOS(t *testing.T) {
	cases := []struct {
		os, device string
		input      []string
		env        []string
	}{
		{"darwin", "", []string{"-f", "avfoundation", "-i", ":0"}, nil},
		{"darwin", "2", []string{"-f", "avfoundation", "-i", ":2"}, nil},
		{"darwin", ":MacBook Pro Microphone", []string{"-f", "avfoundation", "-i", ":MacBook Pro Microphone"}, nil},
		{"linux", "", []string{"-f", "pulse", "-i", "default"}, nil},
		{"linux", "alsa:hw:1,0", []string{"-f", "alsa", "-i", "hw:1,0"}, nil},
		{"linux", "hw:1", []string{"-f", "alsa", "-i", "hw:1"}, nil},
		{"linux", "pulse:alsa_input.usb", []string{"-f", "pulse", "-i", "alsa_input.usb"}, nil},
		{"linux", "alsa_input.usb", []string{"-f", "pulse", "-i", "alsa_input.usb"}, nil},
		{"windows", "Microphone (USB)", []string{"-f", "dshow", "-i", "audio=Microphone (USB)"}, nil},
		{"windows", "audio=Line In", []string{"-f", "dshow", "-i", "audio=Line In"}, nil},
		{"freebsd", "", []string{"-f", "oss", "-i", "/dev/dsp"}, nil},
	}
	for _, c := range cases {
		withOS(t, c.os)
		f := &fakeRunner{}
		withRunner(t, f)
		out, cleanup, err := Capture(context.Background(), Tools{FFmpeg: "ffmpeg", Sox: "sox"}, 10, c.device)
		if err != nil {
			t.Fatalf("%s %q: %v", c.os, c.device, err)
		}
		want := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, c.input...)
		want = append(want, "-t", "10", "-vn", "-ac", "1", "-ar", "44100", "-c:a", "pcm_s16le", out)
		if got := f.calls[0].Args; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s %q:\n got %q\nwant %q", c.os, c.device, got, want)
		}
		if filepath.Base(f.calls[0].Name) != "ffmpeg" {
			t.Errorf("should use ffmpeg, used %s", f.calls[0].Name)
		}
		cleanup()
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("cleanup left %s", out)
		}
	}
}

func TestCaptureLinuxFallsBackToALSA(t *testing.T) {
	withOS(t, "linux")
	f := &fakeRunner{fail: map[string]error{"ffmpeg": errors.New("exit status 1")}, failN: 1,
		stderr: map[string]string{"ffmpeg": "default: Connection refused\n"}}
	withRunner(t, f)
	out, cleanup, err := Capture(context.Background(), Tools{FFmpeg: "ffmpeg"}, 5, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(f.calls) != 2 {
		t.Fatalf("calls %d", len(f.calls))
	}
	if got := strings.Join(f.calls[1].Args, " "); !strings.Contains(got, "-f alsa -i default") || !strings.HasSuffix(got, out) {
		t.Fatalf("fallback args %q", got)
	}
}

func TestCaptureWindowsDefaultDevice(t *testing.T) {
	withOS(t, "windows")
	listing := `[dshow @ 0000020] "Integrated Camera" (video)
[dshow @ 0000020]   Alternative name "@device_pnp_\\?\usb"
[dshow @ 0000020] "Microphone Array (Realtek(R) Audio)" (audio)
[dshow @ 0000020]   Alternative name "@device_cm_{33D9A762}"
dummy: Immediate exit requested
`
	f := &fakeRunner{stderr: map[string]string{"ffmpeg": listing}}
	withRunner(t, f)
	_, cleanup, err := Capture(context.Background(), Tools{FFmpeg: "ffmpeg"}, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := strings.Join(f.calls[0].Args, " "); got != "-hide_banner -list_devices true -f dshow -i dummy" {
		t.Fatalf("listing args %q", got)
	}
	if got := strings.Join(f.calls[1].Args, "|"); !strings.Contains(got, "-i|audio=Microphone Array (Realtek(R) Audio)|") {
		t.Fatalf("capture args %q", got)
	}
}

func TestParseDshowAudioDevicesOldFormat(t *testing.T) {
	listing := `[dshow @ 0000] DirectShow video devices (some may be both video and audio devices)
[dshow @ 0000]  "Integrated Camera"
[dshow @ 0000]     Alternative name "@device_pnp"
[dshow @ 0000] DirectShow audio devices
[dshow @ 0000]  "Line In (High Definition Audio)"
[dshow @ 0000]     Alternative name "@device_cm"
[dshow @ 0000]  "Stereo Mix"
`
	got := parseDshowAudio(listing)
	if strings.Join(got, ",") != "Line In (High Definition Audio),Stereo Mix" {
		t.Fatalf("got %q", got)
	}
}

func TestCaptureWithSox(t *testing.T) {
	withOS(t, "darwin")
	f := &fakeRunner{}
	withRunner(t, f)
	out, cleanup, err := Capture(context.Background(), Tools{Sox: "/opt/homebrew/bin/sox"}, 8, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	want := []string{"-q", "-d", "-c", "1", "-r", "44100", "-b", "16", out, "trim", "0", "8"}
	if got := f.calls[0].Args; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if len(f.calls[0].Env) != 0 {
		t.Fatalf("env %q", f.calls[0].Env)
	}

	f2 := &fakeRunner{}
	withRunner(t, f2)
	_, cleanup2, err := Capture(context.Background(), Tools{Sox: "sox"}, 8, "hw:2")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if strings.Join(f2.calls[0].Env, ",") != "AUDIODEV=hw:2" {
		t.Fatalf("env %q", f2.calls[0].Env)
	}
}

func TestCaptureErrors(t *testing.T) {
	withOS(t, "darwin")
	_, _, err := Capture(context.Background(), Tools{}, 10, "")
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Exit != output.ExitUsage || oe.Code != "missing_tool" {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(oe.Hint, "brew install ffmpeg") || !strings.Contains(oe.Message, "sox") {
		t.Fatalf("hint %q message %q", oe.Hint, oe.Message)
	}
	if _, _, err := Capture(context.Background(), Tools{FFmpeg: "ffmpeg"}, 0, ""); err == nil {
		t.Fatal("zero seconds should fail")
	}

	f := &fakeRunner{fail: map[string]error{"ffmpeg": errors.New("exit status 1")}, stderr: map[string]string{"ffmpeg": "[avfoundation] Failed to create AV capture input device: Cannot use Microphone\n"}}
	withRunner(t, f)
	_, _, err = Capture(context.Background(), Tools{FFmpeg: "ffmpeg"}, 10, "")
	if !errors.As(err, &oe) || oe.Code != "capture_failed" || !strings.Contains(oe.Message, "Cannot use Microphone") {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(oe.Hint, "Privacy & Security") {
		t.Fatalf("macOS hint should mention microphone permission: %q", oe.Hint)
	}
}

func TestCaptureCancelled(t *testing.T) {
	withOS(t, "darwin")
	old := run
	run = func(ctx context.Context, c command) ([]byte, []byte, error) { return nil, nil, context.Canceled }
	t.Cleanup(func() { run = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := Capture(ctx, Tools{FFmpeg: "ffmpeg"}, 10, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

// TestRealFFmpeg exercises Trim and Duration against the real tools when
// they are installed.
func TestRealFFmpeg(t *testing.T) {
	tools := Find()
	if tools.FFmpeg == "" || tools.FFprobe == "" {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	if out, err := exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=20", src).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test audio: %v %s", err, out)
	}
	d, ok := Duration(tools, src)
	if !ok || d < 19*time.Second || d > 21*time.Second {
		t.Fatalf("duration %v %v", d, ok)
	}
	clip, cleanup, err := Trim(context.Background(), tools, src, 5*time.Second, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Past the end: ffmpeg succeeds but the clip holds no audio.
	_, _, err = Trim(context.Background(), tools, src, 30*time.Second, 12*time.Second)
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Code != "invalid_argument" {
		t.Fatalf("trim past the end: %#v", err)
	}
	// Without ffprobe the FLAC header alone tells.
	_, _, err = Trim(context.Background(), Tools{FFmpeg: tools.FFmpeg}, src, 30*time.Second, 12*time.Second)
	if !errors.As(err, &oe) || oe.Code != "invalid_argument" {
		t.Fatalf("trim past the end without ffprobe: %#v", err)
	}
	cd, ok := Duration(tools, clip)
	if !ok || cd < 2500*time.Millisecond || cd > 3500*time.Millisecond {
		t.Fatalf("clip duration %v %v", cd, ok)
	}
}
