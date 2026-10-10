package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// lastArgs are the arguments of the last run, as the runner got them.
func lastArgs(f *fakeRun) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return strings.Join(f.calls[len(f.calls)-1], " ")
}

// planRun starts Recognize's plan, a run that shows the token used.
func planRun(t *testing.T, h *home, f *fakeRun) string {
	t.Helper()
	s := recognizeOf(h)
	s.form.get("input").setValue("song.mp3")
	drain(h, h.wrap("recognize", s.startPlan(false)), 0)
	f.waitFor(t, "recognize song.mp3 --dry-run")
	return lastArgs(f)
}

// The token that runs is the one the header shows, after every change.
func TestTokenTransitions(t *testing.T) {
	newH := func() (*home, *fakeRun) {
		f := &fakeRun{}
		f.reply("recognize", "Plan: 1 file, 1 request.\n", "", 0)
		f.reply("config set token", `{"schema_version":1}`, "", 0)
		f.reply("auth logout", `{"schema_version":1}`, "", 0)
		f.reply("auth switch", `{"schema_version":1}`, "", 0)
		h := sized(newTestHome(t, f, homeOpts{start: "recognize", loggedIn: true}), 100, 30)
		drain(h, h.activate(), 0)
		drain(h, h.signedIn(true), 0)
		if a := planRun(t, h, f); !strings.Contains(a, "--token test") {
			t.Fatalf("the test token runs: %s", a)
		}
		return h, f
	}
	// Test token, then a pasted token (Account › API token).
	h, f := newH()
	ac := h.subs["account"].(*accountSection)
	ac.tokForm.get("token").setValue("abcdef0123456789abcdef")
	drain(h, h.wrap("account", ac.tokSet.start(runReq{args: []string{"config", "set", "token", "-"}, stdin: "abcdef0123456789abcdef"})), 0)
	pump(h, func() bool { return !h.testToken })
	if a := planRun(t, h, f); strings.Contains(a, "--token") || h.testToken || strings.Contains(h.View(), "test token") {
		t.Fatalf("after pasting a token: %s", a)
	}
	// Test token, then a sign-in.
	h, f = newH()
	drain(h, h.signedIn(false), 0)
	if a := planRun(t, h, f); strings.Contains(a, "--token") {
		t.Fatalf("after signing in: %s", a)
	}
	// Test token, then signing out.
	h, f = newH()
	ac = h.subs["account"].(*accountSection)
	drain(h, h.wrap("account", ac.prof.start(runReq{args: []string{"auth", "logout"}, tag: "logout"})), 0)
	pump(h, func() bool { return !h.testToken })
	if a := planRun(t, h, f); strings.Contains(a, "--token test") {
		t.Fatalf("after signing out: %s", a)
	}
	// Test token, then another profile.
	h, f = newH()
	drain(h, h.switchProfile("work"), 0)
	if a := planRun(t, h, f); strings.Contains(a, "--token test") || !strings.Contains(a, "--profile work") {
		t.Fatalf("after switching profiles: %s", a)
	}
}

// A setting of audd itself saves at once and can be undone with u.
func TestSettingSavesWithUndo(t *testing.T) {
	f := &fakeRun{}
	f.reply("config", `{"schema_version":1}`, "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "settings"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("enter"), key("right"), key("enter"))
	f.waitFor(t, "config set format table")
	pump(h, func() bool { return strings.Contains(h.flash, "u undo") })
	if h.confirm != nil || !strings.Contains(statusText(h.View()), "Output format set to table · u undo") {
		t.Fatalf("saved at once:\n%s", h.View())
	}
	press(h, key("u"))
	f.waitFor(t, "config unset format")
}

// A spending question starts on its action; the plan was just shown.
func TestSpendingDialogStartsOnAction(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize song.mp3 --dry-run", "Plan: 2 requests.\n", "", 0)
	f.on("recognize song.mp3 --format", func(args []string, io RunIO) int {
		io.Ask("Recognize 2 files, using 2 requests?")
		return 0
	})
	h := sized(newTestHome(t, f, homeOpts{start: "recognize"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, typeText("song.mp3")...)
	press(h, key("enter"))
	pump(h, func() bool { return h.ask != nil })
	if h.ask == nil || h.ask.cur != 1 || !strings.Contains(h.View(), "[>Recognize 2 files<]") || strings.Contains(h.View(), "Confirm\n") {
		t.Fatalf("the spending dialog:\n%s", h.View())
	}
	press(h, key("esc"))
}

// Signed out, Recognize offers ways to get a token instead of running.
func TestRecognizeWithoutToken(t *testing.T) {
	f := &fakeRun{}
	f.reply("recognize", "Plan: 1 file, 1 request.\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "recognize", signedOut: true}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, typeText("song.mp3")...)
	press(h, key("enter"))
	v := h.View()
	if h.confirm == nil || len(f.commands()) != 0 || !strings.Contains(v, "[>Use the test token<]") || !strings.Contains(v, "[ Sign in ]") {
		t.Fatalf("no token:\n%s", v)
	}
	press(h, key("enter"))
	f.waitFor(t, "recognize song.mp3 --dry-run")
	if !h.testToken || !strings.Contains(lastArgs(f), "--token test") {
		t.Fatalf("the test token: %s", lastArgs(f))
	}
}

// Recognize opens typing in Input: every printable key goes in, digits
// and [ ] : too; esc stops typing, and then digits jump. There is no
// "selected but not typing" step in between.
func TestRecognizeOpensSelected(t *testing.T) {
	h := navHome(t, homeOpts{})
	press(h, key("1"))
	s := recognizeOf(h)
	foot := footerText(h.View())
	if h.activeID() != "recognize" || !s.form.capturing() || s.form.current().name != "input" ||
		!strings.Contains(foot, "type a path or URL") || !strings.Contains(foot, "esc then 1-7 for sections") {
		t.Fatalf("opens typing in Input:\n%s", h.View())
	}
	press(h, typeText("take [2] 3:1.mp3")...)
	if h.activeID() != "recognize" || s.form.value("input") != "take [2] 3:1.mp3" {
		t.Fatalf("typed: %q in %s", s.form.value("input"), h.activeID())
	}
	press(h, key("left"), key("ctrl+u"))
	if s.form.value("input") != "" {
		t.Fatal("ctrl+u clears the field")
	}
	press(h, typeText("a.mp3")...)
	press(h, key("esc"))
	if s.form.value("input") != "a.mp3" || h.focus == focusContent && s.form.capturing() {
		t.Fatalf("esc stops typing and keeps the value: %q", s.form.value("input"))
	}
	if strings.Contains(h.View(), "type to fill it in") || strings.Contains(h.View(), "enter to type") {
		t.Fatalf("an in-between state:\n%s", h.View())
	}
	press(h, key("3"))
	if h.activeID() != "streams" {
		t.Fatal("after esc a digit jumps")
	}
	press(h, key("1"))
	if !s.form.capturing() || s.form.current().name != "input" || s.form.value("input") != "a.mp3" {
		t.Fatal("back on Recognize: typing in Input again, value kept")
	}
}

// Other pages open with no text field selected, so digits jump.
func TestOtherPagesOpenNotTyping(t *testing.T) {
	h := navHome(t, homeOpts{})
	for _, d := range []string{"2", "3", "4", "5", "6", "7"} {
		press(h, key(d))
		if h.active().capturing() {
			t.Fatalf("%s opened typing:\n%s", h.activeID(), h.View())
		}
	}
}

// The Recorder's enter presses its button, which keeps the keys.
func TestRecorderEnter(t *testing.T) {
	f := &fakeRun{}
	f.reply("streams recorder status", "Running:        no\n", "", 0)
	f.reply("streams recorder start", "Started.\n", "", 0)
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("]"), key("]"), key("down"))
	f.waitFor(t, "streams recorder status")
	pump(h, func() bool { return strings.Contains(h.View(), "[ Start ]") })
	press(h, key("enter"))
	f.waitFor(t, "streams recorder start")
	if !h.btnActive || h.btnCur != 0 {
		t.Fatal("the keys stay on the button")
	}
}

// Now playing: ←/→ are the previous and next stream in every view.
func TestNowPlayingArrows(t *testing.T) {
	h := navHome(t, homeOpts{start: "now-playing"})
	np := h.subs["now-playing"].(*nowPlayingSection).np
	press(h, key("right"))
	if np.sel != 1 {
		t.Fatal("→ is the next stream")
	}
	press(h, key("enter"), key("right"), key("left"), key("left"))
	if np.sel != 0 || h.focus != focusContent || !np.zoom {
		t.Fatalf("zoomed: sel %d focus %v", np.sel, h.focus)
	}
}

// History & reports goes field by field: enter moves on until the last.
func TestReportFormEnterNext(t *testing.T) {
	f := &fakeRun{}
	h := sized(newTestHome(t, f, homeOpts{start: "streams"}), 100, 30)
	drain(h, h.activate(), 0)
	press(h, key("]"), key("]"), key("]"), key("down"), key("down"))
	s := h.subs["streams"].(*streamsSection)
	if s.dataForm.current().name != "since" || !strings.Contains(footerText(h.View()), "enter next field") {
		t.Fatalf("on Since: %s\n%s", s.dataForm.current().name, footerText(h.View()))
	}
	press(h, key("enter"))
	if s.dataForm.current().name != "id" || strings.Contains(strings.Join(f.commands(), " "), "streams history") {
		t.Fatalf("enter goes on: %s %q", s.dataForm.current().name, f.commands())
	}
	if !strings.Contains(h.View(), "7 days ago") || !strings.Contains(h.View(), "recent plays") {
		t.Fatalf("plain words:\n%s", h.View())
	}
}

// The palette's esc clears its filter, then closes.
func TestPaletteEsc(t *testing.T) {
	h := navHome(t, homeOpts{start: "settings"})
	press(h, ctrlK())
	press(h, typeText("zz")...)
	if !strings.Contains(footerText(h.View()), "esc clear the filter") {
		t.Fatalf("footer: %q", footerText(h.View()))
	}
	press(h, key("esc"))
	if !h.paletteO || h.palette.picker.filter.Value() != "" {
		t.Fatal("esc clears the filter")
	}
	press(h, key("esc"))
	if h.paletteO {
		t.Fatal("then esc closes")
	}
	press(h, ctrlK())
	if h.palette.picker.filter.Value() != "" {
		t.Fatal("ctrl+k opens with an empty filter")
	}
}

// Usage shows the cycle and a chart of every day.
func TestUsagePage(t *testing.T) {
	h := sized(newTestHome(t, accountFake(), homeOpts{start: "account", loggedIn: true}), 80, 24)
	drain(h, h.activate(), 0)
	press(h, key("]"))
	pump(h, func() bool { return strings.Count(h.View(), "▇") >= 2 })
	if v := h.View(); strings.Count(v, "▇") < 2 || !strings.Contains(v, "Remaining") {
		t.Fatalf("usage:\n%s", v)
	}
}

// Enterprise matches: where each starts and ends.
func TestEnterpriseMatches(t *testing.T) {
	doc := parseDoc(`{"result":[{"offset":"00:00","songs":[{"artist":"A","title":"One","start_seconds":12,"end_seconds":24}]},{"offset":"00:12","songs":[{"artist":"B","title":"Two","start_seconds":30,"end_seconds":44}]}]}`)
	ms := enterpriseMatches(doc)
	if len(ms) != 2 || ms[0]["start"] != "0:12" || ms[0]["end"] != "0:24" {
		t.Fatalf("matches: %v", ms)
	}
	h := navHome(t, homeOpts{start: "recognize"})
	bs := &batchState{selectable: true, results: ms, title: "2 matches in mix.mp3"}
	if v := h.batchPage(bs, 70, 20); !strings.Contains(v, "Starts at") || !strings.Contains(v, "2 matches") {
		t.Fatalf("list:\n%s", v)
	}
	bs.detail = true
	if v := h.batchPage(bs, 70, 20); !strings.Contains(v, "Starts at 0:12, ends at 0:24") || !strings.Contains(v, "Title") {
		t.Fatalf("details:\n%s", v)
	}
}

// The current plan is found from the account's plan, by key or name.
func TestCurrentPlan(t *testing.T) {
	h := navHome(t, homeOpts{start: "account", loggedIn: true})
	ac := h.subs["account"].(*accountSection)
	ac.plans = []map[string]any{{"plan": "startup_plan", "name": "Startup"}, {"plan": "pro_plan", "name": "Pro"}}
	for _, c := range []struct {
		key, name string
		want      int
	}{{"pro_plan", "", 1}, {"", "Startup plan", 0}, {"", "Pro", 1}, {"", "Other", -1}} {
		h.hdr.planKey, h.hdr.plan = c.key, c.name
		if got := ac.currentPlan(); got != c.want {
			t.Fatalf("%q %q: %d, want %d", c.key, c.name, got, c.want)
		}
	}
}

// Removing a stream goes back to the list; an export file gets the
// format's extension.
func TestStreamRemoveAndExportName(t *testing.T) {
	h := navHome(t, homeOpts{start: "streams"})
	press(h, key("down"), key("enter"), key("d"), key("y"))
	if l := h.subs["streams"].(*streamsSection).pane.ex.top(); l.detail {
		t.Fatal("back on the list after removing")
	}
	s := h.subs["streams"].(*streamsSection)
	s.dataForm.get("file").setValue("weekly-airplay")
	s.dataForm.get("format").setValue("csv")
	if p := s.exportPath(); filepath.Base(p) != "weekly-airplay.csv" {
		t.Fatalf("export path: %s", p)
	}
	_ = tea.KeyMsg{}
}

// Runs in interactive mode print errors as text (Error:, Try:, Docs:
// lines); each becomes the error the page shows, with what to try.
func TestHumanErrorsParse(t *testing.T) {
	for _, c := range []struct{ stderr, msg, hint string }{
		{"Error: no such file or folder: /nope.mp3\nTry: check the path\n", "no such file or folder: /nope.mp3", "check the path"},
		{"Error: found 3 files, more than --max-files 2\nTry: pass --max-files 3, or --max-files none\nDocs: https://docs.audd.io\n", "found 3 files, more than --max-files 2", "pass --max-files 3, or --max-files none"},
		{"Error: --enterprise needs --limit\nTry: add --limit N\n", "--enterprise needs --limit", "add --limit N"},
		{"Error: Wrong API token\n", "Wrong API token", ""},
	} {
		res := parseResult(1, "", c.stderr)
		if res.err == nil || res.err.Message != c.msg || res.err.Hint != c.hint {
			t.Fatalf("%q: %+v", c.stderr, res.err)
		}
		if v := errorText(output.Styles{}, res.err, 100); !strings.Contains(strings.ToLower(v), strings.ToLower(c.msg+"\nTry: "+c.hint)) && c.hint != "" || !strings.Contains(strings.ToLower(v), strings.ToLower(c.msg)) {
			t.Fatalf("shown: %q", v)
		}
	}
}
