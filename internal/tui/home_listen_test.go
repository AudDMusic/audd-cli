package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func useListenTools(t *testing.T, tools media.Tools, devs ...media.Device) {
	oldT, oldD := listenTools, listenDevices
	listenTools = func() media.Tools { return tools }
	listenDevices = func(context.Context, media.Tools) []media.Device { return devs }
	t.Cleanup(func() { listenTools, listenDevices = oldT, oldD })
}

func TestListenSnapshots(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"}, media.Device{Value: "alsa_input.a", Label: "a"}, media.Device{Value: "alsa_input.usb", Label: "usb"})
		v := runHome(t, newTestHome(t, &fakeRun{}, homeOpts{start: "listen"}), size[0], size[1], "‹ default ›", "")
		testutil.Golden(t, "home_listen_idle_"+itoa(size[0])+"x"+itoa(size[1]), []byte(v))

		useListenTools(t, media.Tools{})
		// The install hint depends on the system, so no golden file.
		v = runHome(t, newTestHome(t, &fakeRun{}, homeOpts{start: "listen"}), size[0], size[1], "sox works too", "")
		if !strings.Contains(v, "needs ffmpeg") || !strings.Contains(v, "Try: ") {
			t.Fatalf("missing tool:\n%s", v)
		}
	}
}

func TestListenRuns(t *testing.T) {
	useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"}, media.Device{Value: "a"}, media.Device{Value: "usb"})
	f := &fakeRun{}
	f.reply("listen", strings.Replace(warriorsDoc, `"song.mp3"`, `"microphone"`, 1), "Listening for 12 seconds…\n", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "listen"}), 120, 40)
	drain(h, h.activate(), 0)
	s := h.subs["listen"].(*listenSection)
	s.form.get("seconds").setValue("12")
	s.form.get("device").setValue("usb")
	s.form.get("return:spotify").on = true
	if got := h.command(); got != "audd listen --seconds 12 --device usb --return spotify" {
		t.Fatalf("command: %s", got)
	}
	s.form.focusName("listen")
	press(h, key("enter"))
	f.waitFor(t, "listen --seconds 12 --device usb --return spotify")
	if v := h.View(); !strings.Contains(v, "Warriors") || !strings.Contains(v, "o open the link") {
		t.Fatalf("result:\n%s", v)
	}
}
