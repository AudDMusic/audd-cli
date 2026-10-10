package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

func useSigninMethod(t *testing.T, m string) {
	old := signinMethod
	signinMethod = func() string { return m }
	t.Cleanup(func() { signinMethod = old })
}

func TestSignedOutFirstScreen(t *testing.T) {
	useSigninMethod(t, "browser")
	f := &fakeRun{}
	h := newTestHome(t, f, homeOpts{signedOut: true})
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		v := runHome(t, newTestHome(t, f, homeOpts{signedOut: true}), size[0], size[1], "Try with the test token", "")
		testutil.Golden(t, "home_signedout_"+itoa(size[0])+"x"+itoa(size[1]), []byte(v))
	}
	sized(h, 120, 40)
	if h.activeID() != "signin" || !strings.Contains(h.View(), "signed out") || !strings.Contains(h.View(), "$ audd login --browser") {
		t.Fatalf("signed out:\n%s", h.View())
	}
}

func TestSigninPasteToken(t *testing.T) {
	useSigninMethod(t, "device")
	f := &fakeRun{}
	f.reply("config set token -", `{"schema_version":1,"key":"token","value":"0123…cdef","profile":"default"}`, "", 0)
	h := sized(newTestHome(t, f, homeOpts{signedOut: true}), 120, 40)
	press(h, key("down"), key("enter"))
	if v := h.View(); !strings.Contains(v, "Paste an API token") || !strings.Contains(v, "$ audd config set token -") {
		t.Fatalf("paste:\n%s", v)
	}
	press(h, typeText("0123456789abcdef0123456789abcdef")...)
	if strings.Contains(h.View(), "0123456789abcdef") {
		t.Fatal("the pasted token is shown")
	}
	press(h, key("enter"))
	f.waitFor(t, "config set token -")
	if f.stdins[0] != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("stdin: %q", f.stdins[0])
	}
	if h.activeID() != "recognize" || h.showSignin {
		t.Fatalf("after saving, Recognize opens: %s", h.activeID())
	}
}

func TestSigninTestToken(t *testing.T) {
	f := &fakeRun{}
	h := sized(newTestHome(t, f, homeOpts{signedOut: true}), 120, 40)
	press(h, key("down"), key("down"))
	if !strings.Contains(h.View(), "$ audd recognize song.mp3 --token test") {
		t.Fatalf("command:\n%s", h.View())
	}
	press(h, key("enter"))
	if !h.testToken || h.flags.Token != "test" || !strings.Contains(h.View(), "test token: 10 requests a day, standard endpoint only") {
		t.Fatalf("test token:\n%s", h.View())
	}
	if args := h.sessionArgs(); strings.Join(args, " ") != "--token test" {
		t.Fatalf("runs use the test token: %q", args)
	}
}

func TestSigninDeviceCode(t *testing.T) {
	useSigninMethod(t, "device")
	f := &fakeRun{}
	release := make(chan struct{})
	f.on("login --device", func(args []string, io RunIO) int {
		io.Stdout.Write([]byte(`{"schema_version":1,"type":"login_pending","method":"device","verification_uri":"https://audd.example/device","verification_uri_complete":"https://audd.example/device?code=BCDF-GHJK","user_code":"BCDF-GHJK","expires_in_seconds":900}` + "\n"))
		<-release
		io.Stdout.Write([]byte(`{"schema_version":1,"type":"result","profile":"default","account":"user@example.com"}` + "\n"))
		return 0
	})
	h := newTestHome(t, f, homeOpts{signedOut: true})
	v := runHome(t, h, 100, 30, "Try with the test token", "B C D F - G H J K", key("enter"))
	close(release)
	if !strings.Contains(v, "https://audd.example/device?code=BCDF-GHJK") || !strings.Contains(v, "$ audd login --device") {
		t.Fatalf("device sign-in:\n%s", v)
	}
	_ = tea.KeyEnter
}
