package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// contentText is the right side of each line of a view.
func contentText(v string) string {
	var b strings.Builder
	for _, l := range strings.Split(v, "\n") {
		if _, r, ok := strings.Cut(l, "│"); ok {
			l = r
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// selectedButton is the label of the page button that has the keys.
func selectedButton(h *home) string {
	acts := h.pageActions()
	if !h.btnActive || h.btnCur >= len(acts) {
		return ""
	}
	return acts[h.btnCur].label
}

// A job's page: the command at the bottom shows it; Files is a level
// of its own; cancelling Retry at the question puts the keys back on
// the button; after a retry, the results offer the next steps as
// buttons.
func TestJobPage(t *testing.T) {
	f := &fakeRun{}
	var asked atomic.Int32
	f.on("jobs resume k3x9", func(args []string, io RunIO) int {
		if strings.Contains(strings.Join(args, " "), "--dry-run") {
			io.Stdout.Write([]byte("Plan: 1 file, at most 1 request.\nNothing was sent (--dry-run).\n"))
			return 0
		}
		asked.Add(1)
		io.Stdout.Write([]byte(`{"schema_version":1,"type":"result","job_id":"k3x9","index":2,"input":"/music/c.mp3","status":"failed","error":{"code":"network","message":"timeout"}}` + "\n" +
			`{"schema_version":1,"type":"summary","job_id":"k3x9","recognized":2,"no_match":0,"failed":1,"requests_spent_this_run":1}` + "\n"))
		return 0
	})
	h := newTestHome(t, f, homeOpts{start: "recognitions"})
	drive(t, h, 100, 30, "Warriors", key("tab"), "k3x9", key("down"), key("enter"), "Retry 1 file")
	if c := h.command(); c != "audd jobs show k3x9" {
		t.Fatalf("the job page's command: %q", c)
	}
	// Files, and esc back to the job page.
	press(h, key("tab"), key("enter"))
	if c := crumbText(h.View()); !strings.Contains(c, "Jobs › Job k3x9 › Files") {
		t.Fatalf("Files crumb: %q", c)
	}
	press(h, key("esc"))
	if c := crumbText(h.View()); !strings.HasSuffix(strings.TrimSpace(c), "Job k3x9") {
		t.Fatalf("esc from Files: %q", c)
	}
	// Only failed files left: no Resume, Retry failed files instead.
	press(h, key("tab"))
	if v := h.View(); strings.Contains(v, "[ Resume ]") || !strings.Contains(v, "Retry 1 file") {
		t.Fatalf("buttons:\n%s", v)
	}
	press(h, key("right"))
	if b := selectedButton(h); b != "Retry 1 file" {
		t.Fatalf("on %q", b)
	}
	// It asks with its cost, on Cancel; a double enter sends nothing.
	press(h, key("enter"))
	pump(h, func() bool { return h.confirm != nil })
	if v := h.View(); !strings.Contains(v, "[>Cancel<]") || !strings.Contains(v, "at most 1 request") || strings.Contains(v, "Running") {
		t.Fatalf("the question:\n%s", v)
	}
	press(h, key("enter"))
	if asked.Load() != 0 || selectedButton(h) != "Retry 1 file" || !strings.Contains(statusText(h.View()), "Cancelled") {
		t.Fatalf("cancelled: sent %d, on %q\n%s", asked.Load(), selectedButton(h), h.View())
	}
	// Retry and finish: the next steps are buttons, not a Try line.
	press(h, key("enter"))
	pump(h, func() bool { return h.confirm != nil })
	press(h, key("right"), key("enter"))
	pump(h, func() bool { return strings.Contains(h.View(), "[ Back to the job ]") })
	v := h.View()
	if !strings.Contains(v, "[ Retry 1 file ]") || strings.Contains(v, "Try: audd jobs resume") {
		t.Fatalf("after a retry:\n%s", v)
	}
}

// The sidebar's Commands and Keys are buttons a click presses.
func TestSidebarButtons(t *testing.T) {
	h := sized(newTestHome(t, &fakeRun{}, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	v := h.View()
	if !strings.Contains(v, "[ Commands ]") || !strings.Contains(v, "[ Keys ]") {
		t.Fatalf("sidebar buttons:\n%s", v)
	}
	click(t, h, "[ Commands ]")
	if !h.paletteO {
		t.Fatal("a click on Commands opens the palette")
	}
	press(h, ctrlK())
	click(t, h, "[ Keys ]")
	if !h.keysO {
		t.Fatal("a click on Keys opens the keys")
	}
}

// The palette opens with an empty filter, also after a form, and lists
// the common commands first.
func TestPaletteReopensEmpty(t *testing.T) {
	h := navHome(t, homeOpts{})
	press(h, ctrlK())
	press(h, typeText("recognize")...)
	press(h, key("enter"))
	if h.palette.phase != "form" {
		t.Fatalf("opened: %s", h.palette.phase)
	}
	press(h, ctrlK(), ctrlK())
	press(h, ctrlK())
	if !h.paletteO || h.palette.picker.filter.Value() != "" {
		t.Fatalf("reopened with %q", h.palette.picker.filter.Value())
	}
	press(h, typeText("str")...)
	press(h, key("esc"), key("esc"))
	press(h, ctrlK())
	if h.palette.picker.filter.Value() != "" {
		t.Fatalf("reopened with %q", h.palette.picker.filter.Value())
	}
	if first := h.palette.picker.selected().value; first != "recognize" {
		t.Fatalf("first command: %q", first)
	}
}

// A failed token save says why and keeps the form; a good one names
// the profile. A new token clears the last recognition.
func TestTokenSaveMessages(t *testing.T) {
	f := &fakeRun{}
	var fail atomic.Bool
	fail.Store(true)
	f.on("config set token", func(args []string, io RunIO) int {
		if fail.Load() {
			io.Stderr.Write([]byte(`{"error":{"code":"keyring","message":"the system credential store is locked"}}` + "\n"))
			return 1
		}
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{start: "settings"}), 100, 30)
	drain(h, h.activate(), 0)
	s := h.subs["settings"].(*settingsSection)
	for s.selected().key != "token" {
		press(h, key("down"))
	}
	press(h, key("enter"))
	press(h, typeText("abcdef0123456789abcdef0123456789")...)
	press(h, key("enter"))
	if h.confirm != nil {
		press(h, key("right"), key("enter"))
	}
	pump(h, func() bool { return !s.panel.running && s.panel.res != nil })
	if v := strings.Join(strings.Fields(strings.ReplaceAll(contentText(h.View()), "│", " ")), " "); !strings.Contains(v, "Saving the token failed: the system credential store is locked. Try again, or set AUDD_API_TOKEN.") || s.edit == nil {
		t.Fatalf("failed save, the form stays:\n%s", h.View())
	}
	fail.Store(false)
	press(h, key("enter"))
	if h.confirm != nil {
		press(h, key("right"), key("enter"))
	}
	pump(h, func() bool { return strings.Contains(statusText(h.View()), "Token saved for profile default") })
}

var _ tea.Msg

// Enterprise matches are a list to move through, each with details and
// its own link buttons; the plan goes once the results show.
func TestEnterpriseResultList(t *testing.T) {
	f := &fakeRun{}
	f.on("recognize mix.mp3 --enterprise --limit 2", func(args []string, io RunIO) int {
		if strings.Contains(strings.Join(args, " "), "--dry-run") {
			io.Stdout.Write([]byte("Plan: 1 file, at most 2 requests (enterprise: 2 chunks of 12 seconds).\nNothing was sent (--dry-run).\n"))
			return 0
		}
		io.Stderr.Write([]byte("Plan: 1 file, at most 2 requests (enterprise: 2 chunks of 12 seconds).\n"))
		io.Stdout.Write([]byte(`{"schema_version":1,"enterprise":true,"result":[{"offset":"00:00","songs":[{"artist":"A","title":"One","start_seconds":2,"end_seconds":10,"song_link":"https://lis.tn/one"}]},{"offset":"00:12","songs":[{"artist":"B","title":"Two","start_seconds":14,"end_seconds":22,"song_link":"https://lis.tn/two"}]}]}` + "\n"))
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	s := recognizeOf(h)
	press(h, typeText("mix.mp3")...)
	s.form.get("enterprise").on = true
	s.sync()
	s.form.get("limit").setValue("2")
	press(h, key("enter"))
	pump(h, func() bool { return s.phase == "result" || h.ask != nil })
	if h.ask != nil {
		press(h, key("right"), key("enter"))
		pump(h, func() bool { return s.phase == "result" })
	}
	v := h.View()
	if !strings.Contains(v, "2 matches in mix.mp3") || strings.Contains(v, "Plan:") {
		t.Fatalf("the list, without the plan: %q\n%s", f.commands(), v)
	}
	press(h, key("down"), key("enter"))
	v = h.View()
	if !strings.Contains(v, "Two") || !strings.Contains(v, "[ Open the song link") || !strings.Contains(v, "[ Copy the song link") {
		t.Fatalf("the second match's details:\n%s", v)
	}
}

// The message after copying a long command fits on one line.
func TestCopiedMessageOneLine(t *testing.T) {
	h := navHome(t, homeOpts{start: "recognize"})
	long := "audd recognize " + strings.Repeat("very/long/folder/", 10) + "song.mp3 --return apple_music,spotify"
	drain(h, h.copy(long, "the command: "+long), 0)
	st := statusText(h.View())
	if !strings.HasPrefix(st, "Copied the command: audd recognize") || !strings.Contains(st, "…") || len([]rune(st)) > 100 {
		t.Fatalf("status: %q", st)
	}
}
