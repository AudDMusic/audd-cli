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

func TestMicSnapshots(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"}, media.Device{Value: "alsa_input.a", Label: "a"}, media.Device{Value: "alsa_input.usb", Label: "usb"})
		v := runHome(t, newTestHome(t, &fakeRun{}, homeOpts{start: "listen"}), size[0], size[1], "‹ default ›", "")
		testutil.Golden(t, "home_recognize_mic_"+itoa(size[0])+"x"+itoa(size[1]), []byte(v))

		useListenTools(t, media.Tools{})
		// The install hint depends on the system, so no golden file.
		v = runHome(t, newTestHome(t, &fakeRun{}, homeOpts{start: "listen"}), size[0], size[1], "sox works too", "")
		if !strings.Contains(v, "needs ffmpeg") || !strings.Contains(v, "Try: ") {
			t.Fatalf("missing tool:\n%s", v)
		}
	}
}

// Source switches the form between a file and the microphone, with the
// fields and the command of each.
func TestRecognizeSourceSwitch(t *testing.T) {
	useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"}, media.Device{Value: "hw:0", Label: "Built-in"})
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "recognize"}), 120, 40)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	if s.mic() || s.form.current().name != "input" {
		t.Fatalf("Recognize opens on a file, at the input: %s", s.form.current().name)
	}
	if s.m.asked {
		t.Fatal("the devices are looked up only for the microphone")
	}
	press(h, key("up"))
	if s.form.current().name != "source" {
		t.Fatalf("Source is the first field: %s", s.form.current().name)
	}
	press(h, key("right"))
	if !s.mic() {
		t.Fatal("right switches to the microphone")
	}
	if got := h.command(); got != "audd listen" {
		t.Fatalf("command: %s", got)
	}
	v := h.View()
	for _, want := range []string{"Identify the music playing near you", "Seconds", "Device", "[ Listen ]", "Input: Built-in", "$ audd listen"} {
		if !strings.Contains(v, want) {
			t.Fatalf("no %q:\n%s", want, v)
		}
	}
	for _, gone := range []string{"Enterprise", "Max files", "Show the plan", "Clip at"} {
		if strings.Contains(v, gone) {
			t.Fatalf("%q shows with the microphone:\n%s", gone, v)
		}
	}
	// The providers are kept across both sources.
	s.form.get("return:spotify").on = true
	if got := h.command(); got != "audd listen --return spotify" {
		t.Fatalf("command: %s", got)
	}
	// Left on Microphone goes back to a file first, then to the sidebar.
	press(h, key("left"))
	if s.mic() || h.focus != focusContent {
		t.Fatal("left switches back to a file")
	}
	if got := h.command(); got != "audd recognize '<file>' --return spotify" {
		t.Fatalf("command: %s", got)
	}
	if v := h.View(); !strings.Contains(v, "Max files") || strings.Contains(v, "Seconds") {
		t.Fatalf("file fields:\n%s", v)
	}
	press(h, key("left"))
	if h.focus != focusSidebar {
		t.Fatal("left on the first option goes back to the sidebar")
	}
}

func TestMicRuns(t *testing.T) {
	useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"}, media.Device{Value: "a"}, media.Device{Value: "usb"})
	f := &fakeRun{}
	f.reply("listen", strings.Replace(warriorsDoc, `"song.mp3"`, `"microphone"`, 1), "Listening for 12 seconds…\n", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "listen"}), 120, 40)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	if !s.mic() || s.form.current().name != "listen" {
		t.Fatalf("--section listen opens the microphone at the Listen button")
	}
	s.form.get("seconds").setValue("12")
	s.form.get("device").setValue("usb")
	s.form.get("return:spotify").on = true
	if got := h.command(); got != "audd listen --seconds 12 --device usb --return spotify" {
		t.Fatalf("command: %s", got)
	}
	s.form.focusName("listen")
	press(h, key("enter"))
	f.waitFor(t, "listen --seconds 12 --device usb --return spotify")
	if v := h.View(); !strings.Contains(v, "Warriors") || !strings.Contains(v, "o open the link") || !strings.Contains(v, "r listen again") {
		t.Fatalf("result:\n%s", v)
	}
	if got := h.command(); got != "audd listen --seconds 12 --device usb --return spotify" {
		t.Fatalf("command after the run: %s", got)
	}
	press(h, key("esc"))
	if s.phase != "form" || !s.mic() {
		t.Fatal("esc goes back to the microphone form")
	}
}

// listen is not a section of its own: the palette and Help open Recognize
// with the microphone.
func TestListenOpensRecognizeMic(t *testing.T) {
	useListenTools(t, media.Tools{FFmpeg: "/usr/bin/ffmpeg"})
	if i, err := sectionIndex("listen"); err != nil || HomeSections[i] != "recognize" {
		t.Fatalf("--section listen: %d %v", i, err)
	}
	for _, n := range HomeSections {
		if n == "listen" {
			t.Fatal("listen is not in the sidebar")
		}
	}
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "help"}), 120, 40)
	drain(h, h.show("listen", true), 0)
	if h.activeID() != "recognize" || !recognizeOf(h).mic() || recognizeOf(h).m.tools == nil {
		t.Fatalf("show listen: %s mic=%v", h.activeID(), recognizeOf(h).mic())
	}
	if v := h.View(); strings.Contains(v, "Listen\n") || !strings.Contains(v, "7 Help") || strings.Contains(v, "8 ") {
		t.Fatalf("sidebar:\n%s", v)
	}
	press(h, key("esc"), key("3"))
	if h.activeID() != "streams" {
		t.Fatalf("3 is Streams: %s", h.activeID())
	}
	// Getting started on the file sets the source back to a file.
	recognizeOf(h).prefill("https://audd.tech/example.mp3")
	if recognizeOf(h).mic() || recognizeOf(h).form.current().name != "input" {
		t.Fatal("prefill switches to a file")
	}
}
