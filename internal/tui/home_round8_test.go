package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// A browser opener is collected when it exits: no zombie processes.
func TestOpenerIsReaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh")
	}
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := startAndReap(cmd); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pidGone(cmd.Process.Pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the process was not reaped")
}

// After a retry whose failure a retry cannot fix, no retry is offered;
// a retryable one is offered as "Retry 1 file".
func TestRetryOfferedOnlyWhenItCanWork(t *testing.T) {
	bad := []map[string]any{{"status": "failed", "error": map[string]any{"code": "invalid_audio", "message": "the audio could not be decoded"}}}
	if n := retryableResults(bad); n != 0 {
		t.Fatalf("invalid audio counted: %d", n)
	}
	net := []map[string]any{{"status": "failed", "error": map[string]any{"code": "network", "message": "reset"}}}
	if n := retryableResults(net); n != 1 {
		t.Fatalf("network not counted: %d", n)
	}
	f := &fakeRun{}
	f.reply("jobs resume k3x9 --retry-failed --dry-run", "Plan: 1 file, at most 1 request.\n", "", 0)
	f.reply("jobs resume k3x9 --retry-failed", `{"schema_version":1,"type":"result","job_id":"k3x9","index":2,"input":"/music/c.mp3","status":"failed","error":{"code":"invalid_audio","message":"the audio could not be decoded"}}`+"\n"+
		`{"schema_version":1,"type":"summary","job_id":"k3x9","recognized":2,"no_match":0,"failed":1}`+"\n", "", 0)
	h := newTestHome(t, f, homeOpts{start: "recognitions"})
	drive(t, h, 100, 30, "Warriors", key("tab"), "k3x9", key("down"), key("enter"), "Retry 1 file")
	h.focusButton("Retry 1 file")
	press(h, key("enter"))
	pump(h, func() bool { return h.confirm != nil })
	press(h, key("right"), key("enter"))
	pump(h, func() bool { return strings.Contains(h.View(), "Back to the job") })
	v := h.View()
	if !strings.Contains(v, "Back to the job") || strings.Contains(v, "[ Retry") || strings.Contains(v, "[>Retry") {
		t.Fatalf("after a failure a retry cannot fix:\n%s", v)
	}
	// Its details say so, in full.
	h.confirmedAt = time.Time{} // not right after the confirmation
	press(h, key("enter"))
	if v := strings.Join(strings.Fields(strings.ReplaceAll(contentText(h.View()), "│", " ")), " "); !strings.Contains(v, "Failed: The audio could not be decoded. Retrying won't help") {
		t.Fatalf("file details:\n%s", h.View())
	}
}

// A batch's error is on the batch, not on each file's details; a path
// error goes as soon as the path changes, also by ctrl+o.
func TestErrorsAreScoped(t *testing.T) {
	f := &fakeRun{}
	dir := t.TempDir()
	for _, n := range []string{"a.mp3", "b.mp3"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	f.reply("recognize "+dir+" --max-files 2 --dry-run", "Plan: 2 files, at most 2 requests.\n", "", 0)
	f.reply("recognize "+dir+" --max-files 2", `{"schema_version":1,"type":"result","index":0,"input":"a.mp3","status":"matched","result":{"artist":"A","title":"One"}}`+"\n"+
		`{"schema_version":1,"type":"result","index":1,"input":"b.mp3","status":"failed","error":{"code":"network","message":"the connection was reset"}}`+"\n"+
		`{"schema_version":1,"type":"summary","recognized":1,"no_match":0,"failed":1}`+"\n",
		`{"schema_version":1,"error":{"code":"partial_failure","message":"1 of 2 files failed","hint":"see the failures with audd jobs show x"}}`+"\n", 5)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	press(h, typeText(dir)...)
	pump(h, func() bool { return s.counted() })
	s.form.get("max-files").setValue("2")
	s.form.focusName("recognize")
	press(h, key("enter"))
	pump(h, func() bool { return h.ask != nil || s.phase == "result" })
	if h.ask != nil {
		press(h, key("right"), key("enter"))
	}
	pump(h, func() bool { return s.phase == "result" && !s.panel.running })
	if v := h.View(); !strings.Contains(v, "1 of 2 files failed") {
		t.Fatalf("the batch error on the list:\n%s", v)
	}
	press(h, key("down"), key("enter"))
	if v := h.View(); strings.Contains(v, "1 of 2 files failed") || !strings.Contains(v, "The connection was reset") {
		t.Fatalf("a file's details: its own error only:\n%s", v)
	}
	// A wrong path: the error goes when the path changes.
	h2 := navHome(t, homeOpts{start: "recognize"})
	s2 := recognizeOf(h2)
	press(h2, typeText("/no/such/folder/with/a/long/name/somewhere/deep/song.mp3")...)
	press(h2, key("enter"))
	pump(h2, func() bool { return s2.form.get("input").problem != "" })
	if p := s2.form.get("input").problem; !strings.Contains(p, "/no/such/folder/with/a/long/name/somewhere/deep/song.mp3") || strings.Contains(p, "…") {
		t.Fatalf("the path, as typed and in full: %q", p)
	}
	press(h2, typeText("x")...)
	if p := s2.form.get("input").problem; p != "" {
		t.Fatalf("the error stays after the path changed: %q", p)
	}
}

// The export form: enter in File name says "next" and goes on; a
// changed form clears the last export's result.
func TestExportFormHintsAndStaleResult(t *testing.T) {
	dir := t.TempDir()
	old := explorerExportDir
	explorerExportDir = dir
	t.Cleanup(func() { explorerExportDir = old })
	f := &fakeRun{}
	f.reply("streams export", "id\n1\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("]"), key("]"), key("]"), key("down"))
	s := h.subs["streams"].(*streamsSection)
	s.dataForm.get("kind").setValue("export")
	s.syncData()
	s.dataForm.focusName("file")
	s.dataForm.focus()
	if foot := footerText(h.View()); !strings.Contains(foot, "enter next") || strings.Contains(foot, "export plays") {
		t.Fatalf("file name footer: %q", foot)
	}
	press(h, key("enter"))
	if foot := footerText(h.View()); !strings.Contains(foot, "enter export plays") {
		t.Fatalf("Run footer: %q", foot)
	}
	press(h, key("enter"))
	pump(h, func() bool { return s.exportTo != "" })
	if !strings.Contains(h.View(), "Wrote") {
		t.Fatalf("the export:\n%s", h.View())
	}
	s.dataForm.focusName("since")
	s.dataForm.focus()
	press(h, key("ctrl+u"))
	press(h, typeText("30d")...)
	if strings.Contains(h.View(), "Wrote") || s.exportTo != "" {
		t.Fatalf("the old export stays after the form changed:\n%s", h.View())
	}
}

// After a URL change through the taken-ID question, Add a stream starts
// empty again, with the next free ID.
func TestAddFormStartsEmptyAgain(t *testing.T) {
	f := &fakeRun{}
	f.reply("streams set-url", `{"schema_version":1}`, "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	s := h.subs["streams"].(*streamsSection)
	press(h, key("down"), key("a"))
	press(h, typeText("https://radio.example/new.mp3")...)
	press(h, key("down"), key("ctrl+u"))
	press(h, typeText("1")...)
	press(h, key("enter"), key("right"), key("enter"))
	f.waitFor(t, "streams set-url 1 https://radio.example/new.mp3")
	pump(h, func() bool { return !s.adding })
	if s.adding {
		t.Fatalf("still adding:\n%s", h.View())
	}
	press(h, key("a"))
	if v := s.add.value("url"); v != "" || s.add.value("id") == "" || s.add.value("id") == "1" {
		t.Fatalf("add form: url %q id %q", v, s.add.value("id"))
	}
}

// A report can be saved in a file (CSV or JSON), from the report run
// again in that format.
func TestSaveThisReport(t *testing.T) {
	dir := t.TempDir()
	old := explorerExportDir
	explorerExportDir = dir
	t.Cleanup(func() { explorerExportDir = old })
	f := &fakeRun{}
	f.on("streams report", func(args []string, io RunIO) int {
		if strings.Contains(strings.Join(args, " "), "--format csv") {
			io.Stdout.Write([]byte("song,plays\nA — One,3\n"))
			return 0
		}
		io.Stdout.Write([]byte("SONG  PLAYS\nA — One  3\n"))
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	s := h.subs["streams"].(*streamsSection)
	press(h, key("]"), key("]"), key("]"), key("down"))
	s.dataForm.get("kind").setValue("report")
	s.syncData()
	s.dataForm.focusName("run")
	press(h, key("enter"))
	pump(h, func() bool { return s.dataPanel.res != nil })
	var save *button
	for _, b := range h.pageActions() {
		if b.label == "Save this report" {
			b := b
			save = &b
		}
	}
	if save == nil {
		t.Fatalf("no Save this report:\n%s", h.View())
	}
	drain(h, h.pressButton(*save), 0)
	// The name the form suggests: by the grouping, and the date.
	path := filepath.Join(dir, "audd-streams-report-by-song-"+h.now().Format("20060102")+".csv")
	pump(h, func() bool { b, _ := os.ReadFile(path); return len(b) > 0 })
	if b, _ := os.ReadFile(path); string(b) != "song,plays\nA — One,3\n" {
		t.Fatalf("the saved report: %q", b)
	}
	// Then where it is, and the folder and path buttons, as after an
	// export.
	pump(h, func() bool { return strings.Contains(h.View(), "Open folder") })
	v := strings.Join(strings.Fields(strings.ReplaceAll(contentText(h.View()), "│", " ")), " ")
	if !strings.Contains(v, "Saved to ") || !strings.Contains(v, "Open folder") || !strings.Contains(v, "Copy path") {
		t.Fatalf("after saving:\n%s", h.View())
	}
}

// Signed out, Streams says how to get in, never "Could not load".
func TestSignedOutStreamsMessage(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "streams", signedOut: true}), 100, 30)
	d := fakeExplorerData(&fakeStreamsAPI{})
	d.Feed = nil
	NewExplorerData = func(a *app.App) (ExplorerData, error) {
		dd := d
		dd.Feed = errFeed{output.Errf(3, "no_token", "audd login", "no API token is set")}
		return dd, nil
	}
	drain(h, h.activate(), 0)
	pump(h, func() bool { return strings.Contains(h.View(), "Sign in or add a token") })
	v := h.View()
	if strings.Contains(v, "Could not load") || !strings.Contains(v, "Sign in or add a token to monitor streams") || strings.Contains(v, "test token ]") || !strings.Contains(v, "[ Sign in ]") {
		t.Fatalf("signed out:\n%s", v)
	}
}

// errFeed is a Feed whose every call fails.
type errFeed struct{ err error }

func (e errFeed) Stations(ctx context.Context) ([]Station, error) { return nil, e.err }
func (e errFeed) Plays(ctx context.Context, id, n int) ([]Play, error) {
	return nil, e.err
}

var _ = errors.New
