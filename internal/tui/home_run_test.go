package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// testSection is a section that runs whatever the test asks and keeps
// the panel.
type testSection struct {
	stubSection
	p *cmdPanel
}

func (s *testSection) update(msg tea.Msg) tea.Cmd {
	s.p.handle(msg)
	return nil
}
func (s *testSection) view(w, h int) string { return s.p.view(w, h) }
func (s *testSection) command() string      { return s.p.command() }

func withTestSection(h *home) *testSection {
	s := &testSection{stubSection: stubSection{h, "Test"}, p: newPanel(h)}
	h.subs["recognize"] = s
	h.inits["recognize"] = true
	h.cur, h.showSignin, h.focus = 0, false, focusContent
	return s
}

func TestRunStreamsLinesAndResult(t *testing.T) {
	f := &fakeRun{}
	f.on("streams export", func(args []string, io RunIO) int {
		io.Stdout.Write([]byte(`{"type":"result","artist":"A"}` + "\n" + `{"type":"result","artist":"B"}` + "\n"))
		io.Stderr.Write([]byte("Exported 2 plays.\n"))
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{}), 120, 40)
	h.flags.Profile = "work"
	s := withTestSection(h)
	s.p.live = func(l []string, w int) string { return "live" }
	drain(h, wrap("recognize", s.p.start(runReq{args: []string{"streams", "export"}, format: "jsonl", stream: true})), 0)
	if s.p.running || s.p.res == nil || !s.p.res.ok() || len(s.p.lines) != 2 {
		t.Fatalf("result: running %v res %+v lines %q", s.p.running, s.p.res, s.p.lines)
	}
	if got := strings.Join(f.calls[0], " "); got != "streams export --format jsonl --profile work" {
		t.Fatalf("args: %s", got)
	}
	if s.command() != "audd streams export" || !strings.Contains(h.View(), "$ audd streams export") {
		t.Fatalf("the footer shows the command without session flags:\n%s", h.View())
	}
	if len(s.p.res.notes) != 1 || s.p.res.notes[0] != "Exported 2 plays." {
		t.Fatalf("notes: %q", s.p.res.notes)
	}
}

func TestRunErrorAndHint(t *testing.T) {
	f := &fakeRun{}
	f.reply("account", "", `{"schema_version":1,"error":{"code":"login_required","message":"this needs you to sign in to your AudD account","hint":"audd login","retryable":false}}`+"\n", 3)
	h := sized(newTestHome(t, f, homeOpts{}), 120, 40)
	s := withTestSection(h)
	drain(h, wrap("recognize", s.p.start(runReq{args: []string{"account"}})), 0)
	v := h.View()
	if !strings.Contains(v, "Error: this needs you to sign in") || !strings.Contains(v, "Try: audd login") || !strings.Contains(v, "Press enter to open it") {
		t.Fatalf("error view:\n%s", v)
	}
	if s.p.res.err.Code != "login_required" {
		t.Fatalf("%+v", s.p.res.err)
	}
}

// A command's confirmation is asked in the UI; the answer goes back to
// the command.
func TestRunAskYesNoAndTyped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typed string
		keys  []tea.KeyMsg
		want  bool
	}{
		{"yes", "", keys("y"), true},
		{"no", "", keys("n"), false},
		{"esc", "", keys("esc"), false},
		{"typed", "rotate", append(typeText("rotate"), key("enter")), true},
		{"typed wrong then esc", "rotate", append(append(typeText("rotat"), key("enter")), key("esc")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRun{}
			answered := make(chan bool, 1)
			f.on("token rotate", func(args []string, io RunIO) int {
				io.Stderr.Write([]byte("Plan: 1 request.\n"))
				ok := io.Ask("Rotate your AudD API token?")
				answered <- ok
				if !ok {
					io.Stderr.Write([]byte(`{"error":{"code":"declined","message":"cancelled"}}` + "\n"))
					return 6
				}
				return 0
			})
			h := sized(newTestHome(t, f, homeOpts{}), 120, 40)
			s := withTestSection(h)
			drain(h, wrap("recognize", s.p.start(runReq{args: []string{"token", "rotate"}, typed: tc.typed})), 0)
			if h.ask == nil {
				t.Fatal("no confirmation shown")
			}
			v := h.View()
			if !strings.Contains(v, "Rotate your AudD API token?") || !strings.Contains(v, "Plan: 1 request.") || !strings.Contains(v, "$ audd token rotate") {
				t.Fatalf("confirmation view:\n%s", v)
			}
			if tc.typed != "" && !strings.Contains(v, "Type rotate") {
				t.Fatalf("typed confirmation:\n%s", v)
			}
			for _, k := range tc.keys {
				h.Update(k)
			}
			select {
			case ok := <-answered:
				if ok != tc.want {
					t.Fatalf("answer %v, want %v", ok, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no answer reached the command")
			}
			if h.ask != nil {
				t.Fatal("the confirmation stays open")
			}
		})
	}
}

func TestDisplayCommand(t *testing.T) {
	cases := map[string][]string{
		"audd recognize 'my song.mp3' --return apple_music,spotify": {"recognize", "my song.mp3", "--return", "apple_music,spotify"},
		"audd config set token your-api-token":                      {"config", "set", "token", "0123456789abcdef"},
		"audd config set token -":                                   {"config", "set", "token", "-"},
		"audd api getStreams --token your-api-token":                {"api", "getStreams", "--token", "abc"},
		"audd streams add 'https://x.example/a?b=1&c=2' --id 3":     {"streams", "add", "https://x.example/a?b=1&c=2", "--id", "3"},
	}
	for want, args := range cases {
		if got := displayCommand(args); got != want {
			t.Errorf("displayCommand(%q) = %s, want %s", args, got, want)
		}
	}
}

func TestParseResult(t *testing.T) {
	r := parseResult(5, "", "Retrying…\n{\"schema_version\":1,\"error\":{\"code\":\"network\",\"message\":\"timeout\",\"hint\":\"try again\"}}\n")
	if r.err == nil || r.err.Code != "network" || len(r.notes) != 1 {
		t.Fatalf("%+v", r)
	}
	r = parseResult(1, "", "something broke\n")
	if r.err == nil || r.err.Message != "something broke" {
		t.Fatalf("plain stderr: %+v", r)
	}
	if parseDoc("[1]") != nil || parseDoc(`{"a":1}`)["a"] != float64(1) {
		t.Fatal("parseDoc")
	}
	if n := len(parseLines("{\"a\":1}\nnot json\n{\"b\":2}\n")); n != 2 {
		t.Fatalf("parseLines: %d", n)
	}
}
