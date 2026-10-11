package tui

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// The single-file Recognize result of an enterprise scan, in the shape
// audd recognize --enterprise prints ("matches"), is a list: the cursor
// moves to the 3rd match, and o and c act on that match's link.
func TestEnterpriseSingleFileList(t *testing.T) {
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	f := &fakeRun{}
	f.on("recognize mix.mp3 --enterprise --limit 3", func(args []string, io RunIO) int {
		if strings.Contains(strings.Join(args, " "), "--dry-run") {
			io.Stdout.Write([]byte("Plan: 1 file, at most 3 requests.\n"))
			return 0
		}
		io.Stdout.Write([]byte(`{"schema_version":1,"input":"mix.mp3","cached":false,"enterprise":true,"matches":[` +
			`{"artist":"A","title":"One","start_seconds":0,"end_seconds":10,"song_link":"https://lis.tn/one"},` +
			`{"artist":"B","title":"Two","start_seconds":12,"end_seconds":22,"song_link":"https://lis.tn/two"},` +
			`{"artist":"C","title":"Three","start_seconds":24,"end_seconds":34,"song_link":"https://lis.tn/three"}]}` + "\n"))
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	press(h, typeText("mix.mp3")...)
	s.form.get("enterprise").on = true
	s.sync()
	s.form.get("limit").setValue("3")
	press(h, key("enter"))
	pump(h, func() bool { return s.phase == "result" })
	v := h.View()
	if !strings.Contains(v, "3 matches in mix.mp3") || !strings.Contains(v, "C — Three") {
		t.Fatalf("the list:\n%s", v)
	}
	press(h, key("down"), key("down"))
	if v := h.View(); !strings.Contains(v, "› 0:24       0:34      C — Three") {
		t.Fatalf("the cursor on the 3rd match:\n%s", v)
	}
	press(h, key("o"))
	if len(opened) != 1 || opened[0] != "https://lis.tn/three" {
		t.Fatalf("o opened %v", opened)
	}
	press(h, key("c"))
	if st := statusText(h.View()); !strings.Contains(st, "https://lis.tn/three") {
		t.Fatalf("c: %q", st)
	}
}

// q quits right away from the menu; with something typed and not run on
// a page it asks what is lost, on Cancel; ctrl+c twice quits.
func TestQuitAsksWhenSomethingIsLost(t *testing.T) {
	h := navHome(t, homeOpts{})
	if _, cmd := h.Update(key("q")); cmd == nil || !isQuit(cmd) {
		t.Fatal("q from the menu quits")
	}
	h = navHome(t, homeOpts{start: "recognize"})
	press(h, typeText("song.mp3")...)
	press(h, key("esc"), key("down")) // off the text field (on a text field q types)
	// Inside the page q says how to quit.
	if _, cmd := h.Update(key("q")); cmd != nil || h.confirm != nil || !strings.Contains(statusText(h.View()), "Press esc to go back to the menu, then q to quit") {
		t.Fatalf("q in a page:\n%s", h.View())
	}
	press(h, key("esc"))
	_, cmd := h.Update(key("q"))
	v := h.View()
	if cmd != nil || h.confirm == nil || !h.confirm.quit || !strings.Contains(v, "The recognition you set up (song.mp3) is not saved.") || !strings.Contains(v, "[>Cancel<]  [ Quit ]") {
		t.Fatalf("q with typed input:\n%s", v)
	}
	press(h, key("enter"))
	if h.confirm != nil || recognizeOf(h).form.value("input") != "song.mp3" {
		t.Fatal("Cancel keeps everything")
	}
	h.Update(key("ctrl+c"))
	if h.confirm == nil || !h.confirm.quit {
		t.Fatal("ctrl+c asks too")
	}
	if _, cmd := h.Update(key("ctrl+c")); cmd == nil || !isQuit(cmd) {
		t.Fatal("ctrl+c again quits")
	}
}

func isQuit(cmd tea.Cmd) bool {
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// ~ and ~user expand in Recognize: the command shown is the full path,
// which works when pasted.
func TestTildeExpands(t *testing.T) {
	home, _ := os.UserHomeDir()
	h := navHome(t, homeOpts{start: "recognize"})
	press(h, typeText("~/song.mp3")...)
	if c, want := h.command(), displayCommand([]string{"recognize", filepath.Join(home, "song.mp3")}); c != want {
		t.Fatalf("command: %q, want %q", c, want)
	}
	if runtime.GOOS != "windows" {
		if u, err := user.Lookup("root"); err == nil {
			if got, want := expandHome("~root/x.mp3"), filepath.Join(u.HomeDir, "x.mp3"); got != want {
				t.Fatalf("~root: %q, want %q", got, want)
			}
		}
	}
}

// A token change starts the account's sections afresh: no error loaded
// with the old token stays.
func TestTokenChangeReloads(t *testing.T) {
	h := navHome(t, homeOpts{start: "now-playing"})
	np := h.subs["now-playing"].(*nowPlayingSection)
	np.err = output.Errf(3, "token_rejected", "", "Wrong API token")
	drain(h, h.navigate("settings"), 0)
	h.useToken(false)
	if s := h.subs["now-playing"].(*nowPlayingSection); s == np || s.err != nil || h.inits["now-playing"] {
		t.Fatal("Now playing starts again after the token changed")
	}
	drain(h, h.navigate("now-playing"), 0)
	if strings.Contains(h.View(), "Wrong API token") {
		t.Fatalf("the old error is gone:\n%s", h.View())
	}
}

// Failures a retry cannot fix are explained, and no retry is offered for
// them; after a retry the job's files are read again.
func TestJobRetryOnlyWhenItHelps(t *testing.T) {
	rows := jobRows([]JobRow{{ID: "j1", Total: 2, Done: 1, Failed: 1, Status: "partial", FailReason: "the file is not audio AudD can read"}})
	st := detailStyles{}
	if d := rows[0].detail(st); !strings.Contains(d, "This file failed because the file is not audio AudD can read; retrying won't help.") {
		t.Fatalf("detail:\n%s", d)
	}
	if RetryHelps("invalid_audio") || !RetryHelps("network") || !RetryHelps("rate_limited") {
		t.Fatal("which failures a retry fixes")
	}
	// The fake job has one retryable failure: after the retry, the files
	// level is loaded again.
	f := &fakeRun{}
	f.reply("jobs resume k3x9 --retry-failed --dry-run", "Plan: 1 file, at most 1 request.\n", "", 0)
	f.reply("jobs resume k3x9 --retry-failed", `{"schema_version":1,"type":"summary","job_id":"k3x9","recognized":3,"no_match":0,"failed":0}`+"\n", "", 0)
	loads := 0
	h := newTestHome(t, f, homeOpts{start: "recognitions"})
	d := fakeExplorerData(&fakeStreamsAPI{})
	items := d.JobItems
	d.JobItems = func(ctx context.Context, id string) ([]JobItem, error) { loads++; return items(ctx, id) }
	NewExplorerData = func(a *app.App) (ExplorerData, error) { return d, nil }
	drive(t, h, 100, 30, "Warriors", key("tab"), "k3x9", key("down"), key("enter"), key("tab"), key("enter"), "c.mp3")
	// Retry from the Files list: it is read again when the retry ends.
	before := loads
	h.focusButton("Retry 1 file")
	if selectedButton(h) != "Retry 1 file" {
		t.Fatalf("buttons:\n%s", h.View())
	}
	press(h, key("enter"))
	pump(h, func() bool { return h.confirm != nil })
	if h.confirm == nil {
		t.Fatalf("no question:\n%s", h.View())
	}
	press(h, key("right"), key("enter"))
	pump(h, func() bool { return loads > before })
	if loads <= before {
		t.Fatal("the job's files were not read again after the retry")
	}
}

// The export's file name is a placeholder: typing replaces it, enter
// goes on to Run, and the extension follows the format.
func TestExportFileNamePlaceholder(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	s := h.subs["streams"].(*streamsSection)
	press(h, key("]"), key("]"), key("]"), key("down"))
	s.dataForm.get("kind").setValue("export")
	s.syncData()
	if s.dataForm.value("file") != "" || !strings.HasSuffix(s.exportPath(), ".csv") {
		t.Fatalf("empty field, suggested name: %q %q", s.dataForm.value("file"), s.exportPath())
	}
	s.dataForm.focusName("file")
	s.dataForm.focus()
	press(h, typeText("plays.csv")...)
	s.dataForm.get("format").setValue("jsonl")
	if !strings.HasSuffix(s.exportPath(), "plays.jsonl") {
		t.Fatalf("extension follows the format: %s", s.exportPath())
	}
	press(h, key("ctrl+u"))
	if s.dataForm.value("file") != "" {
		t.Fatal("ctrl+u clears it")
	}
	press(h, key("enter"))
	if s.dataForm.current().name != "run" {
		t.Fatalf("enter goes on to Run: %s", s.dataForm.current().name)
	}
}

// A report's note wraps at 80 columns, never cut, and the page offers
// the gaps as a button (no command to copy).
func TestReportNoteWrapsAndShowsGaps(t *testing.T) {
	f := &fakeRun{}
	f.reply("streams report", "SONG  PLAYS  AIRTIME  STATIONS\nA — One  3  9m  1\n\nNote: Some periods weren't recorded: Oct 2 01:24 – Oct 9 01:24, Oct 9 13:00 – 14:30, so these totals miss plays from that time.\nSee the gaps with: audd streams history --since 7d\n", "", 0)
	f.reply("streams history", "STARTED  STREAM  ARTIST — TITLE  LENGTH\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 80, 40)
	drain(h, h.activate(), 0)
	s := h.subs["streams"].(*streamsSection)
	press(h, key("]"), key("]"), key("]"), key("down"))
	s.dataForm.get("kind").setValue("report")
	s.syncData()
	s.dataForm.focusName("run")
	press(h, key("enter"))
	pump(h, func() bool { return s.dataPanel.res != nil })
	v := strings.Join(strings.Fields(strings.ReplaceAll(contentText(h.View()), "│", " ")), " ")
	if !strings.Contains(v, "so these totals miss plays from that time.") || strings.Contains(v, "See the gaps with") || !strings.Contains(v, "[ Show the gaps ]") {
		t.Fatalf("the note:\n%s", h.View())
	}
	for _, b := range h.pageActions() {
		if b.label == "Show the gaps" {
			drain(h, b.do(), 0)
		}
	}
	f.waitFor(t, "streams history --since 7d")
}

// Esc on the Callback page puts the saved URL back; without color a
// placeholder is in parentheses, never mistaken for a value.
func TestCallbackEscAndPlaceholder(t *testing.T) {
	h, _ := callbackHome(t)
	s := h.subs["streams"].(*streamsSection)
	press(h, key("down"), key("enter"))
	press(h, key("ctrl+u"))
	press(h, typeText("https://new.example/x")...)
	// Esc on the URL puts back the URL only.
	press(h, key("esc"))
	if s.cbForm.value("url") != "https://audd.tech/empty/" {
		t.Fatalf("esc restores the URL: %q\n%s", s.cbForm.value("url"), h.View())
	}
	// A box changed stays until Revert changes, which says what it put back.
	s.cbForm.get("return:spotify").on = true
	press(h, key("esc"))
	if !s.cbForm.get("return:spotify").on {
		t.Fatal("esc does not revert the boxes")
	}
	var revert *button
	for _, b := range h.pageActions() {
		if b.label == "Revert changes" {
			b := b
			revert = &b
		}
	}
	if revert == nil {
		t.Fatalf("no Revert changes:\n%s", h.View())
	}
	drain(h, revert.do(), 0)
	if s.cbForm.get("return:spotify").on || !strings.Contains(statusText(h.View()), "Reverted Spotify data to what is saved") {
		t.Fatalf("revert:\n%s", h.View())
	}
	f := textField("x", "X", "")
	f.placeholder = "e.g. https://your-server.example/audd"
	if f.placeholderText(false) != "(e.g. https://your-server.example/audd)" {
		t.Fatalf("placeholder: %q", f.placeholderText(false))
	}
}

// ↑ on the first field leaves the form (no wrap to its buttons), and
// Recognitions opens again at its list.
func TestUpLeavesAndRecognitionsReopensAtList(t *testing.T) {
	h := navHome(t, homeOpts{start: "recognize"})
	s := recognizeOf(h)
	press(h, key("up"))
	if h.focus != focusTabs || s.form.current().name != "input" {
		t.Fatalf("↑ on the first field goes to the tab bar: %v %s", h.focus, s.form.current().name)
	}
	press(h, key("up"))
	if h.focus != focusTabs || s.form.current().name != "input" {
		t.Fatalf("↑ on the tab bar stays, no wrap: %v %s", h.focus, s.form.current().name)
	}
	h = navHome(t, homeOpts{start: "recognitions"})
	press(h, key("down"), key("enter"))
	if !strings.Contains(crumbText(h.View()), "Warriors") && !strings.Contains(h.View(), "Title") {
		t.Fatalf("details:\n%s", h.View())
	}
	press(h, key("1"), key("4"))
	if ex := h.subs["recognitions"].(*historySection).pane.ex; ex.top().detail || len(ex.tabs[ex.tab]) != 1 {
		t.Fatalf("reopens at the list:\n%s", h.View())
	}
}
