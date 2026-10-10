package tui

import (
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func TestSettingsSection(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		h := newTestHome(t, &fakeRun{}, homeOpts{start: "settings"})
		// A fixed config path, so the frame is the same everywhere.
		t.Setenv("AUDD_CONFIG_DIR", "config-dir")
		v := drive(t, h, size[0], size[1], "background_recorder")
		v = strings.ReplaceAll(v, `\`, "/")
		testutil.Golden(t, "home_settings_"+itoa(size[0])+"x"+itoa(size[1]), []byte(v))
	}

	f := &fakeRun{}
	f.reply("config set", `{"schema_version":1}`, "", 0)
	f.reply("config unset", `{"schema_version":1}`, "", 0)
	t.Setenv("AUDD_FORMAT", "")
	h := newTestHome(t, f, homeOpts{start: "settings"})
	// format: enter opens the choice, → picks json, enter asks, y saves.
	v := drive(t, h, 120, 40, "background_recorder", key("enter"), "‹ auto ›", key("right"), key("right"),
		"$ audd config set format json", key("enter"), "Output format set to json · u undo")
	f.waitFor(t, "config set format json")
	if !strings.Contains(v, "(env)") && !strings.Contains(v, "(default)") {
		t.Fatalf("sources:\n%s", v)
	}

	// The token goes through stdin, never the command line.
	h = newTestHome(t, f, homeOpts{start: "settings"})
	// The token row is last; changing the token always asks.
	drive(t, h, 120, 40, "background_recorder", key("end"), key("down"), key("down"), key("down"), key("down"), key("enter"), "$ audd config set token -",
		typeText("abcdef0123456789"), key("enter"), "Replace the API token of profile default?", key("y"), "Token saved for profile default")
	f.waitFor(t, "config set token -")
	for i, c := range f.commands() {
		if c == "config set token -" && f.stdins[i] != "abcdef0123456789" {
			t.Fatalf("stdin: %q", f.stdins[i])
		}
	}
	h = newTestHome(t, f, homeOpts{start: "settings"})
	drive(t, h, 120, 40, "background_recorder", key("down"), key("down"), key("down"), key("down"), key("d"),
		"Unset token in profile default?", "no API token for this profile", key("y"), "API token removed from profile default")
	f.waitFor(t, "config unset token")
}
