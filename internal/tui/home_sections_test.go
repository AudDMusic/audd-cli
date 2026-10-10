package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// snap records golden frames at 80×24 and 120×40 after steps (see drive).
func snap(t *testing.T, name string, mk func() *home, steps ...any) {
	t.Helper()
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		v := drive(t, mk(), size[0], size[1], steps...)
		testutil.Golden(t, "home_"+name+"_"+itoa(size[0])+"x"+itoa(size[1]), []byte(v))
	}
}

func TestNowPlayingSection(t *testing.T) {
	snap(t, "nowplaying", func() *home { return newTestHome(t, &fakeRun{}, homeOpts{start: "now-playing"}) }, "Warriors")
	// No streams on the account.
	h := newTestHome(t, &fakeRun{}, homeOpts{start: "now-playing"})
	NewFeed = func(a *app.App) (Feed, error) { return &fakeFeed{}, nil }
	v := runHome(t, h, 100, 30, "no streams", "")
	if !strings.Contains(v, "Try: audd streams add") || !strings.Contains(v, "press 4") {
		t.Fatalf("no streams:\n%s", v)
	}
}

func TestHistorySection(t *testing.T) {
	snap(t, "history", func() *home { return newTestHome(t, &fakeRun{}, homeOpts{start: "history"}) }, "Warriors")
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "history"}), 120, 40)
	drain(h, h.activate(), 0)
	if h.command() != "audd browse --tab recent" {
		t.Fatalf("command: %s", h.command())
	}
	press(h, key("tab"))
	if h.command() != "audd jobs list" {
		t.Fatalf("command: %s", h.command())
	}
}

// r on a job resumes it inside the screen through audd jobs resume.
func TestHistoryResumesJob(t *testing.T) {
	f := &fakeRun{}
	f.reply("jobs resume k3x9 --retry-failed",
		`{"schema_version":1,"type":"result","job_id":"k3x9","index":2,"input":"/music/c.mp3","status":"matched","result":{"artist":"Portishead","title":"Roads"}}`+"\n"+
			`{"schema_version":1,"type":"summary","job_id":"k3x9","recognized":3,"no_match":0,"failed":0,"requests_spent_this_run":1}`+"\n", "", 0)
	h := newTestHome(t, f, homeOpts{start: "history"})
	v := drive(t, h, 120, 40, "Warriors", key("tab"), "k3x9", key("R"), "Job k3x9: 3 recognized")
	if !strings.Contains(v, "Portishead — Roads") || !strings.Contains(v, "$ audd jobs resume k3x9 --retry-failed") {
		t.Fatalf("resume:\n%s", v)
	}
}

func TestStreamsSectionPages(t *testing.T) {
	f := &fakeRun{}
	f.reply("streams callback get", `{"schema_version":1,"url":"https://audd.tech/empty/"}`, "", 0)
	f.reply("streams recorder status", "Running:        yes (pid 4242)\nStore:          /data/streams-default.db\n", "", 0)
	f.reply("streams history", "TIME   STREAM  SONG\n12:00  1       Imagine Dragons — Warriors\n", "", 0)
	snap(t, "streams_list", func() *home { return newTestHome(t, f, homeOpts{start: "streams"}) }, "Nina Si")
	snap(t, "streams_callback", func() *home { return newTestHome(t, f, homeOpts{start: "streams"}) }, "Nina Si", key("]"), "Callback URL: https")
	snap(t, "streams_recorder", func() *home { return newTestHome(t, f, homeOpts{start: "streams"}) }, "Nina Si", key("]"), key("]"), "pid 4242")

	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 120, 40)
	drain(h, h.activate(), 0)
	press(h, key("]"))
	f.waitFor(t, "streams callback get")
	if v := h.View(); !strings.Contains(v, "Callback URL: https://audd.tech/empty/") {
		t.Fatalf("callback:\n%s", v)
	}
	press(h, key("]"), key("]"))
	press(h, key("enter"))
	s := h.subs["streams"].(*streamsSection)
	s.dataForm.focusName("run")
	press(h, key("enter"))
	f.waitFor(t, "streams history --since 7d")
	if v := h.View(); !strings.Contains(v, "Imagine Dragons — Warriors") || !strings.Contains(v, "$ audd streams history --since 7d") {
		t.Fatalf("history:\n%s", v)
	}
}

func TestStreamsExportWritesFile(t *testing.T) {
	dir := t.TempDir()
	old := explorerExportDir
	explorerExportDir = dir
	t.Cleanup(func() { explorerExportDir = old })
	f := &fakeRun{}
	f.reply("streams export", `{"type":"result","radio_id":1}`+"\n"+`{"type":"result","radio_id":2}`+"\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 120, 40)
	drain(h, h.activate(), 0)
	press(h, key("["))
	s := h.subs["streams"].(*streamsSection)
	s.dataForm.get("kind").setValue("export")
	s.dataForm.get("since").setValue("all")
	s.syncData()
	if h.command() != "audd streams export --since all --format jsonl > plays.jsonl" {
		t.Fatalf("command: %s", h.command())
	}
	press(h, key("enter"))
	s.dataForm.focusName("run")
	press(h, key("enter"))
	f.waitFor(t, "streams export --since all")
	files, _ := filepath.Glob(filepath.Join(dir, "audd-streams-export-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("export files: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if s.exportTo != files[0] || strings.Count(string(b), "\n") != 2 || !strings.Contains(h.View(), "Wrote 2 plays to ") {
		t.Fatalf("export: %q\n%s", b, h.View())
	}
}

// a adds a stream with audd streams add; d removes one after a
// confirmation, through the same call as audd streams remove.
func TestStreamsAddAndRemove(t *testing.T) {
	f := &fakeRun{}
	f.reply("streams add", `{"schema_version":1,"radio_id":7,"added":true}`, "", 0)
	api := &fakeStreamsAPI{}
	d := fakeExplorerData(api)
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 120, 40)
	NewExplorerData = func(a *app.App) (ExplorerData, error) { return d, nil }
	drain(h, h.activate(), 0)
	press(h, key("a"))
	press(h, typeText("https://radio.example/7.mp3")...)
	press(h, key("down"))
	press(h, typeText("7")...)
	press(h, key("down"), key(" "))
	if h.command() != "audd streams add https://radio.example/7.mp3 --id 7 --start" {
		t.Fatalf("command: %s", h.command())
	}
	press(h, key("down"), key("enter"))
	f.waitFor(t, "streams add https://radio.example/7.mp3 --id 7 --start")
	if s := h.subs["streams"].(*streamsSection); s.adding || !strings.Contains(h.View(), "Added stream 7") {
		t.Fatalf("after adding:\n%s", h.View())
	}
	press(h, key("d"))
	if !strings.Contains(h.View(), "Remove stream 1?") || h.command() != "audd streams remove 1" {
		t.Fatalf("remove asks:\n%s", h.View())
	}
	press(h, key("y"))
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.removed) != 1 || api.removed[0] != 1 {
		t.Fatalf("removed %v", api.removed)
	}
}

func TestExplorerPaneError(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "history"}), 100, 30)
	NewExplorerData = func(a *app.App) (ExplorerData, error) {
		return ExplorerData{}, output.Errf(3, "no_token", "audd login", "no API token is set")
	}
	drain(h, h.activate(), 0)
	if v := h.View(); !strings.Contains(v, "Error: no API token is set") {
		t.Fatalf("%s", v)
	}
	_ = context.Background
	_ = errors.New
}

// Error states: a failed command shows its message and the hint, and an
// account without streams says how to add one.
func TestErrorSnapshots(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 1 file, 1 request.\n", "", 0)
	f.reply("recognize song.mp3", "", `{"schema_version":1,"error":{"code":"no_token","message":"no API token is set","hint":"audd login","retryable":false}}`+"\n", 3)
	snap(t, "error_recognize", func() *home {
		h := newTestHome(t, f, homeOpts{start: "recognize"})
		r := recognizeOf(h)
		r.form.get("input").setValue("song.mp3")
		r.form.focusName("recognize")
		return h
	}, "Recognize music", key("enter"), "Try: audd login")
	snap(t, "error_nowplaying", func() *home {
		h := newTestHome(t, f, homeOpts{start: "now-playing"})
		NewFeed = func(a *app.App) (Feed, error) { return &fakeFeed{}, nil }
		return h
	}, "no streams")
}

func TestErrorHintOpensCommand(t *testing.T) {
	useSigninMethod(t, "device")
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 1 file, 1 request.\n", "", 0)
	f.reply("recognize song.mp3", "", `{"schema_version":1,"error":{"code":"no_token","message":"no API token is set","hint":"audd login"}}`+"\n", 3)
	h := newTestHome(t, f, homeOpts{start: "recognize"})
	r := recognizeOf(h)
	r.form.get("input").setValue("song.mp3")
	r.form.focusName("recognize")
	drive(t, h, 120, 40, "Recognize music", key("enter"), "Try: audd login", key("enter"), "Welcome to AudD")
	if h.activeID() != "signin" {
		t.Fatalf("audd login opens the sign-in screen: %s", h.activeID())
	}
}
