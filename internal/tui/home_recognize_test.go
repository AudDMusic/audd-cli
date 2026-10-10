package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/AudDMusic/audd-cli/internal/testutil"
)

const warriorsDoc = `{"schema_version":1,"input":"song.mp3","cached":false,"result":{"artist":"Imagine Dragons","title":"Warriors","album":"Warriors","release_date":"2014-09-18","label":"Universal Music","song_link":"https://lis.tn/Warriors","isrc":"USUM71409990"}}`

func recognizeOf(h *home) *recognizeSection { return h.subs["recognize"].(*recognizeSection) }

func TestRecognizeCommandFollowsTheForm(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "recognize"}), 120, 40)
	s := recognizeOf(h)
	if got := h.command(); got != "audd recognize '<file>'" {
		t.Fatalf("empty form: %s", got)
	}
	s.form.get("input").setValue("mix.mp3")
	s.form.get("enterprise").on = true
	s.sync()
	s.form.get("limit").setValue("20")
	s.form.get("tracklist").on = true
	if got := h.command(); got != "audd recognize mix.mp3 --enterprise --limit 20 --tracklist" {
		t.Fatalf("enterprise: %s", got)
	}
	if v := h.View(); !strings.Contains(v, "$ audd recognize mix.mp3 --enterprise --limit 20 --tracklist") || strings.Contains(v, "Add Spotify data") {
		t.Fatalf("view:\n%s", v)
	}
	s.form.get("enterprise").on = false
	s.sync()
	s.form.get("return:spotify").on = true
	s.form.get("return:apple_music").on = true
	if got := h.command(); got != "audd recognize mix.mp3 --return apple_music,spotify" {
		t.Fatalf("providers: %s", got)
	}
}

// Enterprise recognition needs a limit the user chose: an empty limit
// refuses to run anything, and "none" has to be typed.
func TestRecognizeEnterpriseNeedsLimit(t *testing.T) {
	f := &fakeRun{}
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 120, 40)
	s := recognizeOf(h)
	s.form.get("input").setValue("mix.mp3")
	s.form.get("enterprise").on = true
	s.sync()
	s.form.focusName("recognize")
	press(h, key("enter"))
	if len(f.commands()) != 0 || !strings.Contains(h.View(), "set a limit") {
		t.Fatalf("ran %q:\n%s", f.commands(), h.View())
	}
	s.form.get("limit").setValue("all")
	press(h, key("enter"))
	if len(f.commands()) != 0 || !strings.Contains(h.View(), "the word none") {
		t.Fatalf("ran %q:\n%s", f.commands(), h.View())
	}
}

func TestRecognizeOneFile(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 1 file, 1 request.\nNothing was sent (--dry-run).\n", "", 0)
	f.reply("recognize song.mp3", warriorsDoc, "", 0)
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		h := newTestHome(t, f, homeOpts{start: "recognize"})
		v := runHome(t, h, size[0], size[1], "Recognize music", "Warriors",
			append(append(typeText("song.mp3"), keyFocus(h, "recognize")...), key("enter"))...)
		testutil.Golden(t, "home_recognize_result_"+itoa(size[0])+"x"+itoa(size[1]), []byte(v))
	}
	f.waitFor(t, "recognize song.mp3 --dry-run")
	cmds := f.commands()
	if len(cmds) < 2 || cmds[0] != "recognize song.mp3 --dry-run" || cmds[1] != "recognize song.mp3" {
		t.Fatalf("the plan runs before the recognition: %q", cmds)
	}
}

// keyFocus is the down presses that move the form cursor from the input
// to the named field.
func keyFocus(h *home, name string) []tea.KeyMsg {
	var out []tea.KeyMsg
	for _, f := range recognizeOf(h).form.visible() {
		if f.name == name {
			break
		}
		out = append(out, key("down"))
	}
	return out
}

func TestRecognizeEmpty(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		h := newTestHome(t, &fakeRun{}, homeOpts{start: "recognize"})
		v := runHome(t, h, size[0], size[1], "Recognize music", "")
		testutil.Golden(t, "home_recognize_empty_"+itoa(size[0])+"x"+itoa(size[1]), []byte(v))
	}
}

// A batch shows its plan, asks before spending, and shows the results.
func TestRecognizeBatchWithConfirmation(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.mp3", "b.mp3"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	f := &fakeRun{}
	f.on("recognize "+dir+" --max-files 2 --dry-run", func(args []string, io RunIO) int {
		io.Stdout.Write([]byte("Plan: 2 files, 2 requests.\nNothing was sent (--dry-run).\n"))
		return 0
	})
	asked := make(chan string, 1)
	f.on("recognize "+dir+" --max-files 2", func(args []string, io RunIO) int {
		io.Stderr.Write([]byte("Plan: 2 files, 2 requests.\n"))
		if !io.Ask("Recognize 2 files, using 2 requests?") {
			asked <- "no"
			return 6
		}
		asked <- "yes"
		io.Stdout.Write([]byte(`{"schema_version":1,"type":"event","event":"job_started","job_id":"j1","total":2,"to_run":2}` + "\n"))
		a, _ := json.Marshal(filepath.Join(dir, "a.mp3"))
		b, _ := json.Marshal(filepath.Join(dir, "b.mp3"))
		io.Stdout.Write([]byte(`{"schema_version":1,"type":"result","job_id":"j1","index":0,"input":` + string(a) + `,"status":"matched","result":{"artist":"Imagine Dragons","title":"Warriors"}}` + "\n"))
		io.Stdout.Write([]byte(`{"schema_version":1,"type":"result","job_id":"j1","index":1,"input":` + string(b) + `,"status":"no_match","result":null}` + "\n"))
		io.Stdout.Write([]byte(`{"schema_version":1,"type":"summary","job_id":"j1","recognized":1,"no_match":1,"failed":0,"requests_spent_this_run":2}` + "\n"))
		return 0
	})
	h := newTestHome(t, f, homeOpts{start: "recognize"})
	toMax := keyFocus(h, "max-files")
	tm := teatest.NewTestModel(t, h, teatest.WithInitialTermSize(120, 40))
	out := tm.Output()
	waitFor := func(s string) {
		t.Helper()
		teatest.WaitFor(t, out, func(b []byte) bool { return bytes.Contains(b, []byte(s)) },
			teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	}
	waitFor("Recognize music")
	for _, k := range typeText(dir) {
		tm.Send(k)
	}
	for _, k := range toMax {
		tm.Send(k)
	}
	for _, k := range typeText("2") {
		tm.Send(k)
	}
	for range 3 { // at, no-cache, Recognize
		tm.Send(key("down"))
	}
	tm.Send(key("enter"))
	waitFor("Recognize 2 files, using 2 requests?")
	tm.Send(key("y"))
	waitFor("Job j1: 1 recognized, 1 no match, 0 failed")
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	v := final.View()
	if <-asked != "yes" {
		t.Fatal("the answer did not reach the command")
	}
	if !strings.Contains(v, "a.mp3") || !strings.Contains(v, "Imagine Dragons — Warriors") || !strings.Contains(v, "no match") {
		t.Fatalf("batch results:\n%s", v)
	}
	// The footer shortens long paths, so check the command itself.
	if got, want := final.(*home).command(), displayCommand([]string{"recognize", dir, "--max-files", "2"}); got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestFileBrowserPicks(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "x.mp3"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o644)
	b := newFileBrowser(dir)
	if len(b.entries) != 2 || b.entries[1].name != "sub" {
		t.Fatalf("entries: %+v", b.entries)
	}
	b.key("down", 10)
	b.key("enter", 10)
	b.key("down", 10)
	p, done := b.key("enter", 10)
	if !done || !strings.HasSuffix(p, filepath.Join("sub", "x.mp3")) {
		t.Fatalf("picked %q %v", p, done)
	}
}
