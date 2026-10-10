package media

import (
	"context"
	"testing"
)

func TestParseAVFoundationAudio(t *testing.T) {
	listing := `[AVFoundation indev @ 0x7f8] AVFoundation video devices:
[AVFoundation indev @ 0x7f8] [0] FaceTime HD Camera
[AVFoundation indev @ 0x7f8] [1] Capture screen 0
[AVFoundation indev @ 0x7f8] AVFoundation audio devices:
[AVFoundation indev @ 0x7f8] [0] MacBook Pro Microphone
[AVFoundation indev @ 0x7f8] [1] USB Audio Device
: Input/output error`
	got := parseAVFoundationAudio(listing)
	if len(got) != 2 || got[0] != (Device{":0", "MacBook Pro Microphone"}) || got[1].Value != ":1" {
		t.Fatalf("%+v", got)
	}
}

func TestParsePactlSources(t *testing.T) {
	listing := "0\talsa_output.pci-0000_00_1f.3.analog-stereo.monitor\tmodule-alsa-card.c\ts16le 2ch 44100Hz\tSUSPENDED\n" +
		"1\talsa_input.pci-0000_00_1f.3.analog-stereo\tmodule-alsa-card.c\ts16le 2ch 44100Hz\tSUSPENDED\n" +
		"2\talsa_input.usb-Blue_Yeti-00.analog-stereo\tmodule-alsa-card.c\ts16le 2ch 48000Hz\tRUNNING\n"
	got := parsePactlSources(listing)
	if len(got) != 2 || got[0].Value != "alsa_input.pci-0000_00_1f.3.analog-stereo" {
		t.Fatalf("%+v", got)
	}
}

func TestInputDevicesLinux(t *testing.T) {
	withOS(t, "linux")
	withLookPath(t, "pactl")
	withRunner(t, &fakeRunner{stdout: map[string]string{"pactl": "1\talsa_input.usb\tm\ts\tRUNNING\n"}})
	got := InputDevices(context.Background(), Tools{})
	if len(got) != 1 || got[0].Value != "alsa_input.usb" {
		t.Fatalf("%+v", got)
	}
	withLookPath(t)
	if InputDevices(context.Background(), Tools{}) != nil {
		t.Fatal("no pactl: no list")
	}
}
