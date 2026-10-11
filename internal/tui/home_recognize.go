package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// providers are the --return metadata blocks, in the order shown.
var providers = []struct{ name, label, what string }{
	{"apple_music", "Apple Music", "link, artwork, track length"},
	{"spotify", "Spotify", "link, artwork, popularity"},
	{"deezer", "Deezer", "link, artwork"},
	{"musicbrainz", "MusicBrainz", "IDs and release details"},
}

// providerField is the box that adds a provider's data to results.
func providerField(name, label, what string) *field {
	return boolField("return:"+name, "Add "+label+" data ("+what+")", "AudD adds "+label+"'s data to each result: "+what+".")
}

// recognizeSection is audd recognize: one file or URL, or a batch; or
// audd listen, with Source set to Microphone.
type recognizeSection struct {
	h       *home
	form    *form
	browser *fileBrowser
	panel   *cmdPanel
	phase   string // "form", "running", "result"
	wantRun bool   // the plan runs first; then the recognition
	plan    string
	planFor string // the dry run the plan is for: it shows while the form still matches
	batch   bool
	bs      *batchState
	rec     recognition
	details bool
	dscroll scrollView // the details of a single result
	runArgs []string
	m       micState
	cnt     fileCount
	// afterCount runs ("run" or "plan") once the count is in.
	afterCount string
	// rootOK is a whole disk the user agreed to scan.
	rootOK string
	// ranCommand is the last command run (it is not unsaved work).
	ranCommand string
}

// fileCount is the count of the files of the input, made in the
// background a moment after the last key.
type fileCount struct {
	of     string // the input it is for
	c      media.Count
	done   bool
	seq    int
	cancel context.CancelFunc
}

// countDueMsg: the input stayed the same for a moment; countMsg: the
// count is in.
type countDueMsg struct{ seq int }
type countMsg struct {
	seq int
	c   media.Count
}

// countDelay is how long the input must stay the same before counting.
var countDelay = 250 * time.Millisecond

// watchInput starts counting again when the input changed: after
// countDelay, cancelling a count still running.
func (s *recognizeSection) watchInput(now bool) tea.Cmd {
	in := s.form.value("input")
	if in == s.cnt.of && (s.cnt.done || s.cnt.cancel != nil || !now) {
		return nil
	}
	if s.cnt.cancel != nil {
		s.cnt.cancel()
		s.cnt.cancel = nil
	}
	s.cnt.of, s.cnt.done, s.cnt.c = in, false, media.Count{}
	s.cnt.seq++
	// A problem with the old path is gone with it.
	s.form.get("input").problem = ""
	if in == "" || strings.Contains(in, "://") || in == "-" {
		s.cnt.done = true
		return nil
	}
	seq := s.cnt.seq
	if now {
		return s.countNow(seq)
	}
	return tea.Tick(countDelay, func(time.Time) tea.Msg { return countDueMsg{seq} })
}

// countNow counts in the background, stopping at max(1000, Max files+1)
// files or after 200ms.
func (s *recognizeSection) countNow(seq int) tea.Cmd {
	ctx, cancel := context.WithCancel(s.h.ctx)
	s.cnt.cancel = cancel
	in := expandHome(s.cnt.of)
	limit := 1000
	if m, err := strconv.Atoi(s.form.value("max-files")); err == nil {
		limit = max(limit, m+1)
	}
	return func() tea.Msg {
		c := media.CountInput(ctx, in, limit, 200*time.Millisecond)
		return countMsg{seq: seq, c: c}
	}
}

// inputNote says what the input holds, once counted.
func (s *recognizeSection) inputNote() string {
	in := s.form.value("input")
	switch {
	case in == "" || strings.Contains(in, "://"):
		return ""
	case !s.counted() && s.h.confirm == nil && s.h.ask == nil:
		return "Counting files…"
	case !s.counted():
		return ""
	case s.cnt.c.Err != nil:
		return ""
	case s.cnt.c.Root:
		return in + " is a whole disk: audd asks before looking through it."
	case s.cnt.c.Batch:
		return "Found " + s.countWords() + " to recognize."
	}
	return ""
}

// counted reports whether the count is in for the current input.
func (s *recognizeSection) counted() bool {
	return s.cnt.done && s.cnt.of == s.form.value("input")
}

// countWords is the count as shown: "3 files", "1000+ files".
func (s *recognizeSection) countWords() string {
	n := s.cnt.c.Files
	if s.cnt.c.More {
		return fmtInt(n) + "+ files"
	}
	return output.Plural(n, "file")
}

func newRecognizeSection(h *home) section {
	s := &recognizeSection{h: h, panel: newPanel(h), phase: "form"}
	src := enumField("source", "Source", "←/→ switch between a file and the microphone", []string{sourceFile, sourceMic})
	in := textField("input", "File, URL, or folder", "A file, a URL, a folder, or a glob such as *.mp3")
	in.required = true
	in.need = "Enter a file, URL, or folder (ctrl+o picks a file)"
	in.typeHint = "a path or URL"
	in.placeholder = "e.g. song.mp3"
	sec := &field{kind: fInt, name: "seconds", label: "Seconds", help: "How long to record (at most 60). 10 to 12 seconds is enough.", input: newInput()}
	sec.setValue("10")
	dev := textField("device", "Device", "The microphone to record from; empty for the system default")
	dev.placeholder = "system default"
	fields := []*field{src, in, sec, dev}
	for _, p := range providers {
		fields = append(fields, providerField(p.name, p.label, p.what))
	}
	limit := textField("limit", "Limit", "12-second chunks per file: a number, or type none to scan whole files")
	limit.placeholder = "e.g. 5, or none"
	maxFiles := textField("max-files", "Max files", "For folders, globs, and lists: a number of files, or type none")
	maxFiles.placeholder = "e.g. 100, or none"
	at := textField("at", "Clip at", "Send 12 seconds from this time: 90, 1:30, or 1m30s (needs ffmpeg)")
	at.placeholder = "e.g. 1:30"
	at.validate = func(v string) string {
		if !validClock(v) {
			return "Clip at must be a time like 1:30 or 90"
		}
		return ""
	}
	fields = append(fields,
		boolField("enterprise", "Enterprise: scan more of the file (needs a limit)", "Every match with its position. Billed per 12-second chunk; the limit is how many chunks per file."),
		limit,
		boolField("tracklist", "Tracklist", "Merge consecutive matches into tracks with start and end times"),
		maxFiles,
		at,
		boolField("no-cache", "Send even if cached", "Recognize again even when an earlier result is saved (uses requests)"),
		buttonField("recognize", "Recognize"),
		buttonField("plan", "Show the plan"),
		buttonField("listen", "Listen"),
	)
	s.form = newForm(fields...)
	s.form.h = h
	needInput := func() string {
		if s.form.value("input") == "" {
			return "Enter a file, URL, or folder first"
		}
		return ""
	}
	_ = needInput // Recognize and the plan say what is missing, at its field
	// The plan works with the options as they are.
	s.form.get("plan").novalidate = true
	s.form.get("limit").need = "Enterprise recognition is billed per 12-second chunk: set a limit (chunks per file), or type none to scan whole files"
	s.form.get("max-files").need = "Folders, globs, and lists need a file limit: a number, or type none"
	s.form.get("listen").quiet = true // the page says why, under the form
	s.form.get("listen").disabled = func() string {
		if t := s.m.tools; t != nil && t.FFmpeg == "" && t.Sox == "" {
			return "Listening needs ffmpeg or sox: how to install it is under the form"
		}
		return ""
	}
	s.sync()
	s.form.focusName("input")
	return s
}

// fileFields are the fields of a file, URL, or folder; micFields those of
// the microphone. The providers are in both.
var (
	fileFields = []string{"input", "enterprise", "limit", "tracklist", "max-files", "at", "no-cache", "recognize", "plan"}
	micFields  = []string{"seconds", "device", "listen"}
)

// sync shows the fields of the current source. For files, it shows the
// enterprise options only with --enterprise, and the metadata options
// only without it (the enterprise endpoint has none).
func (s *recognizeSection) sync() {
	mic := s.mic()
	cur := s.form.current()
	for _, n := range fileFields {
		s.form.get(n).hidden = mic
	}
	for _, n := range micFields {
		s.form.get(n).hidden = !mic
	}
	ent := !mic && s.form.get("enterprise").on
	for _, p := range providers {
		s.form.get("return:" + p.name).hidden = ent
	}
	s.form.get("limit").hidden = !ent
	s.form.get("limit").required = ent
	s.form.get("tracklist").hidden = !ent
	// Max files is needed (and marked) as soon as the input is a folder,
	// a glob, or a list.
	s.form.get("max-files").required = !mic && s.isBatch()
	// The cursor stays on its field when fields above it come and go.
	if cur != nil && !cur.hidden {
		s.form.focusName(cur.name)
	}
}

// providers are the --return blocks ticked.
func (s *recognizeSection) providers() []string {
	var ret []string
	for _, p := range providers {
		if s.form.get("return:" + p.name).on {
			ret = append(ret, p.name)
		}
	}
	return ret
}

func (s *recognizeSection) title() string { return "Recognize" }

func (s *recognizeSection) init() tea.Cmd {
	s.form.focus()
	return s.findDevices()
}

// pageEntered: the page opens typing in its field.
func (s *recognizeSection) pageEntered() { s.form.resume() }

func (s *recognizeSection) capturing() bool {
	return s.browser == nil && s.phase == "form" && s.form.capturing()
}

func (s *recognizeSection) back() bool {
	switch {
	case s.browser == nil && s.phase == "form" && s.form.leaveField():
		// Esc first leaves the field being typed in.
		return true
	case s.browser != nil:
		s.browser = nil
		return true
	case s.phase == "result" && s.bs != nil && s.bs.detail:
		s.bs.detail = false
		return true
	case s.phase == "result" && s.details:
		s.details = false
		return true
	case s.phase == "result":
		// Back on the form, as it was, not typing in it; the old plan
		// is gone.
		s.phase = "form"
		s.plan = ""
		s.form.leaveField()
		return true
	case s.phase == "running" && s.mic():
		s.panel.stop()
		return true
	}
	// A recognition keeps running (s stops it); esc goes to the sidebar.
	return false
}

// crumbs: the source, then what is open (the file picker, the run, the
// result).
func (s *recognizeSection) crumbs() []string {
	out := []string{"File"}
	in := s.form.value("input")
	switch {
	case s.mic():
		out[0] = "Microphone"
	case strings.Contains(in, "://"):
		out[0] = "URL"
	case s.form.get("max-files").required:
		out[0] = "Folder"
	}
	switch {
	case s.browser != nil:
		out = append(out, "Pick a file")
	case s.phase == "running" && s.mic():
		out = append(out, "Listening")
	case s.phase == "running" && s.h.ask != nil:
		// Asking before it starts: nothing runs yet.
	case s.phase == "running":
		out = append(out, "Running")
	case s.phase == "result":
		out = append(out, "Result")
		if s.details || s.bs != nil && s.bs.detail {
			out = append(out, "Details")
		}
	}
	return out
}

// prefill sets the input (Help: getting started).
func (s *recognizeSection) prefill(input string) {
	s.setSource(false)
	s.form.get("input").setValue(input)
}

// args is the command for the form as it is.
func (s *recognizeSection) args(dry bool) []string { return s.argsFor(dry, false) }

// argsFor is the command; shown, it names what is still missing
// (<limit>, <max files>), so the bottom line never shows a command that
// cannot run.
func (s *recognizeSection) argsFor(dry, shown bool) []string {
	f := s.form
	args := []string{"recognize"}
	in := expandHome(f.value("input"))
	if in == "" {
		in = "<file>"
	}
	args = append(args, in)
	ent := f.get("enterprise").on
	if ent {
		args = append(args, "--enterprise")
		if v := f.value("limit"); v != "" {
			args = append(args, "--limit", v)
		} else if shown {
			args = append(args, "--limit", "<limit>")
		}
		if f.get("tracklist").on {
			args = append(args, "--tracklist")
		}
	} else if ret := s.providers(); len(ret) > 0 {
		args = append(args, "--return", strings.Join(ret, ","))
	}
	// Max files is for folders and globs: a single file or a URL does
	// not carry it.
	single := s.counted() && !s.cnt.c.Batch && s.cnt.c.Err == nil || strings.Contains(in, "://")
	if v := f.value("max-files"); v != "" && !single {
		args = append(args, "--max-files", v)
	} else if shown && f.get("max-files").required {
		args = append(args, "--max-files", "<max files>")
	}
	if v := f.value("at"); v != "" {
		args = append(args, "--at", v)
	}
	if f.get("no-cache").on {
		args = append(args, "--no-cache")
	}
	if dry {
		args = append(args, "--dry-run")
	}
	return args
}

func (s *recognizeSection) command() string {
	if s.phase != "form" && s.runArgs != nil {
		return displayCommand(s.runArgs)
	}
	if s.mic() {
		return displayCommand(s.micArgs())
	}
	return displayCommand(s.argsFor(false, true))
}

func (s *recognizeSection) keys() []keyHelp {
	if s.browser == nil && s.mic() && s.phase != "result" {
		return s.micKeys()
	}
	switch {
	case s.browser != nil:
		return []keyHelp{{"↑/↓", "move"}, {"→/enter", "open or pick"}, {"←", "parent folder"}, {"s", "pick this folder"}, {"esc", "close"}}
	case s.phase == "running":
		return nil
	case s.phase == "result" && s.bs != nil && s.bs.detail:
		return []keyHelp{{"esc", "back to the list"}}
	case s.phase == "result" && s.bs != nil:
		return []keyHelp{{"↑/↓", "move"}, {"→/enter", "details"}, {"esc", "back to the form"}}
	case s.phase == "result" && s.details:
		return []keyHelp{{"↑/↓", "scroll"}, {"esc", "back to the result"}}
	case s.phase == "result":
		if res := s.panel.res; res != nil && res.err != nil && res.err.Code != "no_token" && hintKind(res.err.Hint) == "" && strings.HasPrefix(res.err.Hint, "audd ") {
			return []keyHelp{{"enter", "open " + res.err.Hint}, {"esc", "back to the form"}}
		}
		if s.rec.view != nil {
			return []keyHelp{{"→/enter", "details"}, {"esc", "back to the form"}}
		}
		if s.mic() {
			return []keyHelp{{"enter", "listen again"}, {"esc", "back to the form"}}
		}
		return []keyHelp{{"esc", "back to the form"}}
	}
	k := s.form.rowKeys(s.primary())
	if c := s.form.current(); c != nil && c.name == "input" {
		k = append(k, keyHelp{"esc", "done"}, keyHelp{"ctrl+o", "pick a file"})
	}
	return k
}

// primary is what enter in a text field does: "recognize", "recognize
// 3 files".
func (s *recognizeSection) primary() string {
	if s.mic() {
		return "listen"
	}
	if name, e := s.check(true); e != "" {
		if f := s.form.get(name); f != nil {
			if c := s.form.current(); c != nil && c.name == name {
				return "" // the field being typed in: its own hint says what to type
			}
			return "fill in " + f.label
		}
		return "show what is missing"
	}
	switch {
	case !s.counted():
		return "recognize (counting files…)"
	case s.tooMany() > 0:
		return fmt.Sprintf("%s found, Max files is %s: set it to %d?", s.countWords(), s.form.value("max-files"), s.cnt.c.Files)
	case s.cnt.c.Root:
		return "recognize the whole disk"
	case s.isBatch():
		return "recognize " + s.countWords()
	}
	return "recognize"
}

// actions are the result's buttons, and Stop while it runs.
func (s *recognizeSection) actions() []button {
	if s.browser != nil {
		return nil
	}
	switch s.phase {
	case "running":
		return []button{{label: "Stop", key: "s", do: func() tea.Cmd {
			s.panel.stop()
			return s.h.setFlash("Stopping…")
		}}}
	case "result":
	default:
		return nil
	}
	link := s.rec.link
	var b []button
	switch {
	case s.panel.res != nil && s.panel.res.err != nil && s.panel.res.err.Code == "no_token":
		return s.h.tokenButtons(true, func() tea.Cmd { s.phase = "form"; return s.startPlan(true) })
	case s.panel.res != nil && s.panel.res.err != nil:
		return nil // the sign-in or token buttons are added for the hint
	case s.bs != nil:
		// On the list too: the buttons act on the selected row; a retry
		// for the failures it can fix.
		return s.h.batchButtons(s.bs)
	case s.rec.view != nil && !s.details:
		b = append(b, button{label: "Details", key: "", do: func() tea.Cmd { s.details, s.dscroll.off = true, 0; return nil }})
	case s.mic() && s.rec.view == nil:
		b = append(b, button{label: "Listen again", key: "", do: s.startListen})
	}
	if link != "" {
		b = append(b,
			button{label: "Open the song link", key: "o", do: func() tea.Cmd { return s.openLink(link) }},
			button{label: "Copy the song link", key: "c", do: func() tea.Cmd { return s.h.copy(link, "the link "+link) }})
	}
	if v := s.rec.view; v != nil && s.details && s.bs == nil {
		// The same as a result's details in Recognitions.
		if v.ISRC != "" {
			isrc := v.ISRC
			b = append(b, button{label: "Copy ISRC", key: "i", do: func() tea.Cmd { return s.h.copy(isrc, "the ISRC "+isrc) }})
		}
		if v.UPC != "" {
			upc := v.UPC
			b = append(b, button{label: "Copy UPC", do: func() tea.Cmd { return s.h.copy(upc, "the UPC "+upc) }})
		}
	}
	if s.mic() && s.rec.view != nil {
		b = append(b, button{label: "Listen again", key: "", do: s.startListen})
	}
	return b
}

// downExits: ↓ at the end of a result goes to its buttons.
func (s *recognizeSection) downExits() bool {
	switch {
	case s.phase != "result":
		return s.phase == "running"
	case s.bs != nil && !s.bs.detail:
		return s.bs.cursor >= len(s.bs.results)-1
	case s.details:
		_, ht := s.h.contentSize()
		return s.dscroll.off >= len(s.dscroll.lines)-ht+2
	}
	return true
}

func (s *recognizeSection) openLink(link string) tea.Cmd {
	return s.h.openLink(link)
}

// validLimit accepts a whole number or the typed word none.
func validLimit(v string) bool {
	if v == "none" {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n > 0
}

// check says what is missing before anything runs: the field, and the
// problem.
func (s *recognizeSection) check(forRun bool) (string, string) {
	f := s.form
	if f.value("input") == "" {
		return "input", f.get("input").need
	}
	if s.counted() && s.cnt.c.Err != nil {
		e := output.AsError(s.cnt.c.Err)
		// The path as typed, its middle shortened when long (the file
		// name, where typos are, stays).
		in := f.value("input")
		// The path as typed, in full (wrapped, never cut): the typo shows.
		msg := capFirst(strings.Replace(e.Message, expandHome(in), in, 1))
		if e.Hint != "" {
			msg += ": " + e.Hint
		}
		return "input", msg
	}
	if f.get("enterprise").on {
		v := f.value("limit")
		if v == "" && forRun {
			return "limit", f.get("limit").need
		}
		if v != "" && !validLimit(v) {
			if n, err := strconv.Atoi(v); err == nil && n <= 0 {
				return "limit", v + " isn't allowed: the limit is at least 1 chunk, or none"
			}
			return "limit", "The limit is a number of 12-second chunks (at least 1), or the word none"
		}
	}
	if v := f.value("max-files"); v != "" && !validLimit(v) {
		if n, err := strconv.Atoi(v); err == nil && n <= 0 {
			return "max-files", v + " isn't allowed: Max files is at least 1, or none"
		}
		return "max-files", "Max files is a number of files (at least 1), or the word none"
	}
	if forRun && s.isBatch() && f.value("max-files") == "" {
		return "max-files", f.get("max-files").need
	}
	return "", ""
}

// errorField is the form field an error of audd recognize is about.
func errorField(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "--max-files"):
		return "max-files"
	case strings.Contains(t, "--limit"):
		return "limit"
	case strings.Contains(t, "--at"):
		return "at"
	case strings.Contains(t, "no such file"), strings.Contains(t, "no audio or video files"), strings.Contains(t, "cannot read folder"), strings.Contains(t, "check the path"), strings.Contains(t, "url"):
		return "input"
	}
	return ""
}

// tooMany is the file count when it is more than Max files allows
// (audd recognize refuses that), else 0.
func (s *recognizeSection) tooMany() int {
	if !s.isBatch() {
		return 0
	}
	m, err := strconv.Atoi(s.form.value("max-files"))
	if err != nil || m >= s.cnt.c.Files && !s.cnt.c.More {
		return 0
	}
	return s.cnt.c.Files
}

// isBatch reports whether the input is a batch (folder, glob), the way
// audd recognize decides, once the count is in.
func (s *recognizeSection) isBatch() bool { return s.counted() && s.cnt.c.Batch }

// startPlan runs the dry run; with run set the recognition follows.
// needToken asks how to get an API token, when there is none, before
// anything runs; then goes on with next.
func (s *recognizeSection) needToken(next func() tea.Cmd) tea.Cmd {
	return s.h.askToken("recognize", "AudD recognizes music with your API token.", true, next)
}

func (s *recognizeSection) startPlan(run bool) tea.Cmd {
	if !s.mic() && s.form.value("input") != "" && !s.counted() {
		// The count says whether the input is there and is a batch: wait
		// for it, then go on.
		s.afterCount = map[bool]string{true: "run", false: "plan"}[run]
		return tea.Batch(s.watchInput(true), s.h.setFlash("Counting files…"))
	}
	s.afterCount = ""
	if !s.mic() {
		if name, e := s.check(false); e != "" && name == "input" {
			s.form.fail(name, e)
			return nil
		}
	}
	if in := s.form.value("input"); !s.mic() && s.cnt.c.Root && s.rootOK != in {
		return s.h.askFirst(&confirmState{
			owner:    "recognize",
			question: "Recognize every file on the whole disk (" + in + ")?",
			notes:    []string{"audd looks through every folder on the disk; that can take long and find many files. Max files still limits how many are sent."},
			action:   "Scan the whole disk",
			cancel:   "Cancelled. Nothing was sent.",
			onCancel: func() { s.form.focusName("input"); s.form.resume() },
			yes: func() tea.Cmd {
				s.rootOK = in
				return s.startPlan(run)
			},
		})
	}
	if n := s.tooMany(); n > 0 {
		m := s.form.value("max-files")
		want := strconv.Itoa(n)
		if s.cnt.c.More {
			want = "none"
		}
		return s.h.askFirst(&confirmState{
			owner:    "recognize",
			question: fmt.Sprintf("The input has %s, more than Max files %s", s.countWords(), m),
			notes:    []string{"audd recognize stops before sending anything when a batch has more files than Max files. Set Max files to " + want + " to recognize all of them, or Cancel to change it yourself."},
			action:   "Set Max files to " + want,
			topic:    "More files than Max files",
			cancel:   "Cancelled. Nothing was sent.",
			onCancel: func() { s.form.focusName("max-files"); s.form.resume() },
			yes: func() tea.Cmd {
				s.form.get("max-files").setValue(want)
				return s.startPlan(run)
			},
		})
	}
	if s.h.signedOut && run {
		if name, e := s.check(true); e != "" {
			s.form.fail(name, e)
			return nil
		}
		return s.needToken(func() tea.Cmd { return s.startPlan(true) })
	}
	if name, e := s.check(run); e != "" {
		s.form.fail(name, e)
		return nil
	}
	s.form.err = ""
	s.wantRun = run
	s.plan, s.planFor = "", ""
	s.batch = s.isBatch()
	s.runArgs = s.args(true)
	return s.panel.start(runReq{args: s.runArgs, format: "table", tag: "plan"})
}

func (s *recognizeSection) startRun() tea.Cmd {
	s.runArgs = s.args(false)
	s.ranCommand = displayCommand(s.runArgs)
	s.phase = "running"
	s.rec = recognition{}
	s.bs = nil
	s.details = false
	if s.batch {
		s.bs = &batchState{selectable: true}
		return s.panel.start(runReq{args: s.runArgs, format: "jsonl", stream: true, spends: true, tag: "run"})
	}
	return s.panel.start(runReq{args: s.runArgs, spends: true, tag: "run"})
}

func (s *recognizeSection) update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case countDueMsg:
		if m.seq == s.cnt.seq && !s.cnt.done && s.cnt.cancel == nil {
			return s.countNow(m.seq)
		}
		return nil
	case countMsg:
		if m.seq != s.cnt.seq {
			return nil
		}
		s.cnt.done, s.cnt.c, s.cnt.cancel = true, m.c, nil
		s.sync()
		switch s.afterCount {
		case "run":
			return s.startPlan(true)
		case "plan":
			return s.startPlan(false)
		}
		return nil
	case fsListMsg:
		if s.browser != nil {
			s.browser.apply(m)
		}
		return nil
	case listenSetupMsg:
		s.setupDone(m)
		return nil
	case listenTickMsg:
		if m.id == s.m.tick && s.phase == "running" {
			return tea.Tick(time.Second, func(time.Time) tea.Msg { return listenTickMsg{m.id} })
		}
		return nil
	}
	if mine, done := s.panel.handle(msg); mine {
		if l, ok := msg.(runLineMsg); ok && s.bs != nil && l.pending == nil {
			s.bs.add(l.line)
		}
		if done {
			return s.finished()
		}
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	ks := k.String()
	if s.browser != nil {
		_, ch := s.h.contentSize()
		p, done, cmd := s.browser.key(ks, ch)
		if done {
			s.browser = nil
			if p != "" {
				s.form.get("input").setValue(p)
				s.form.focusName("input")
				s.form.resume()
				return s.watchInput(false)
			}
		}
		return cmd
	}
	switch s.phase {
	case "running":
		return nil
	case "result":
		return s.resultKey(ks)
	}
	if ks == "ctrl+o" && !s.mic() {
		b, cmd := newFileBrowser(expandHome(s.form.value("input")))
		s.browser = b
		return cmd
	}
	ent := s.form.get("enterprise").on
	act, cmd := s.form.update(k)
	s.sync()
	if now := s.form.get("enterprise").on; now != ent {
		// Say what the box changed.
		switch {
		case now:
			cmd = tea.Batch(cmd, s.h.setFlash("Enterprise on · set a Limit (12-second chunks per file)"))
		case s.form.value("limit") != "":
			cmd = tea.Batch(cmd, s.h.setFlash("Enterprise off · Limit no longer needed"))
		}
	}
	switch act {
	case "recognize":
		return s.startPlan(true)
	case "plan":
		return s.startPlan(false)
	case "listen":
		return s.startListen()
	}
	return tea.Batch(cmd, s.findDevices(), s.watchInput(false))
}

// finished handles the end of the plan or of the recognition.
func (s *recognizeSection) finished() tea.Cmd {
	res := *s.panel.res
	if s.panel.run.req.tag == "plan" {
		if res.err != nil {
			msg := capFirst(res.err.Message)
			if h := humanHint(res.err.Hint); h != "" {
				msg += "\n" + h
			}
			s.phase = "form"
			if name := errorField(res.err.Message + " " + res.err.Hint); name != "" && s.form.get(name) != nil && !s.form.get(name).hidden {
				// Back in the field the error is about, typing.
				s.form.fail(name, msg)
				return nil
			}
			s.form.err = msg
			return nil
		}
		s.plan = strings.TrimSpace(res.stdout)
		s.planFor = displayCommand(s.args(true))
		if s.wantRun {
			return s.startRun()
		}
		return nil
	}
	s.phase = "result"
	if res.err != nil && res.err.Code == "declined" {
		// Cancelling is not an error: back to the form, as it was.
		s.phase = "form"
		return s.h.setFlash("Cancelled. Nothing was sent.")
	}
	if res.err != nil {
		return nil
	}
	if s.mic() {
		s.h.listened = true
	} else {
		s.h.recognized = true
	}
	if s.batch {
		return nil
	}
	doc := parseDoc(res.stdout)
	s.rec = parseRecognition(doc)
	if ms := enterpriseMatches(doc); len(ms) > 0 {
		// Several matches in one file: a list, like a batch.
		s.bs = &batchState{selectable: true, results: ms, title: output.Plural(len(ms), "match") + " in " + fileName(s.form.value("input"))}
		return nil
	}
	if s.rec.view != nil {
		return s.h.arts.want(s.h.ctx, *s.rec.view)
	}
	return nil
}

func (s *recognizeSection) view(w, h int) string {
	st := s.h.st
	if s.browser != nil {
		return s.browser.view(st, w, h, s.h.color)
	}
	var b strings.Builder
	switch s.phase {
	case "running":
		if s.mic() {
			return s.micRunning()
		}
		if s.batch && s.bs != nil {
			if s.panel.pending != nil {
				return s.panel.view(w, h)
			}
			if len(s.bs.results) > 0 {
				// The results replace the plan.
				return s.bs.view(st, w, h, true)
			}
			return s.planBlock(w) + s.bs.view(st, w, h-strings.Count(s.planBlock(w), "\n"), true)
		}
		b.WriteString(s.planBlock(w))
		b.WriteString(st.Dim.Render("Recognizing " + s.form.value("input") + " …"))
		return b.String()
	case "result":
		res := s.panel.res
		if res == nil {
			return ""
		}
		if res.err != nil && s.mic() {
			out := errorText(st, res.err, w)
			if res.err.Code != "missing_tool" {
				out += "\n\n" + st.Dim.Render("Press enter to listen again.")
			}
			return out
		}
		if res.err != nil {
			if s.batch && s.bs != nil && len(s.bs.results) > 0 {
				if s.bs.detail {
					// A file's details: its own error, not the batch's.
					return s.batchView(w, h)
				}
				b.WriteString(s.batchView(w, h-4) + "\n\n")
			}
			b.WriteString(errorText(st, res.err, w))
			if hintKind(res.err.Hint) == "" && strings.HasPrefix(res.err.Hint, "audd ") {
				// Enter does open it: the command's form, filled in.
				b.WriteString("\n" + st.Dim.Render(output.Wrap("Press enter to open it in Commands.", w)))
			}
			return b.String()
		}
		if s.bs != nil {
			return s.batchView(w, h)
		}
		if !s.mic() {
			b.WriteString(st.Dim.Render(truncate(s.form.value("input"), w)) + "\n\n")
		}
		if s.rec.view != nil {
			v := *s.rec.view
			text := s.h.cardText(v, false, w, h-2)
			if s.details {
				text = strings.Join(detailFields(v, st.Key), "\n")
			}
			lines := strings.Split(text, "\n")
			for i, l := range lines {
				// A click on the song link opens it.
				if s.rec.link != "" && strings.Contains(ansi.Strip(l), s.rec.link) {
					link := s.rec.link
					lines[i] = s.h.mark(l, func() tea.Cmd { return s.openLink(link) })
				}
			}
			if s.details {
				s.dscroll.lines = lines
				b.WriteString(s.dscroll.view(w, max(1, h-2)))
			} else {
				b.WriteString(strings.Join(lines, "\n"))
			}
		} else {
			b.WriteString(s.rec.text)
			if s.mic() {
				b.WriteString("\n\n" + st.Dim.Render("Press enter to listen again."))
			}
		}
		// The notes of the run, without its plan (the plan was for
		// before it ran).
		var notes []string
		for _, n := range res.notes {
			if !strings.HasPrefix(n, "Plan:") {
				notes = append(notes, n)
			}
		}
		if len(notes) > 0 {
			b.WriteString("\n\n" + styleLines(st.Dim, output.Wrap(strings.Join(notes, "\n"), w)))
		}
		return b.String()
	}
	if s.mic() {
		b.WriteString(st.Bold.Render("Identify the music playing near you") + "\n\n")
		b.WriteString(s.form.view(w, st, s.h.color))
		b.WriteString(s.micNotes(w))
		return b.String()
	}
	b.WriteString(st.Bold.Render("Recognize music in a file, URL, or folder") + "\n\n")
	b.WriteString(s.form.view(w, st, s.h.color))
	if note := s.inputNote(); note != "" {
		b.WriteString("\n\n" + styleLines(st.Dim, output.Wrap(note, w)))
	}
	if s.panel.running && s.panel.run != nil && s.panel.run.req.tag == "plan" {
		b.WriteString("\n\n" + st.Dim.Render("Working out the plan…"))
	} else if s.plan != "" && s.planFor == displayCommand(s.args(true)) {
		// A plan only for the options it was made for.
		b.WriteString("\n\n" + s.planBlock(w))
	}
	return b.String()
}

// planBlock is the plan, without the dry run's own "nothing was sent".
func (s *recognizeSection) planBlock(w int) string {
	if s.plan == "" {
		return ""
	}
	var keep []string
	for _, l := range strings.Split(s.plan, "\n") {
		if strings.Contains(l, "Nothing was sent") || strings.Contains(l, "--dry-run") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) == 0 {
		return ""
	}
	return styleLines(s.h.st.Dim, output.Wrap(strings.Join(keep, "\n"), w)) + "\n\n"
}

// batchView is a batch's results: a list to move through, or the
// details of one.
func (s *recognizeSection) batchView(w, h int) string { return s.h.batchPage(s.bs, w, h) }

// resultKey handles a key on a result: moving, and opening details.
func (s *recognizeSection) resultKey(ks string) tea.Cmd {
	switch ks {
	case "up", "down":
		_, ht := s.h.contentSize()
		switch {
		case s.bs != nil && !s.bs.detail:
			s.bs.move(map[string]int{"up": -1, "down": 1}[ks])
		case s.details:
			s.dscroll.key(ks, ht-2)
		}
		return nil
	case "enter", "right":
		if res := s.panel.res; ks == "enter" && res != nil && res.err != nil && res.err.Code != "no_token" && hintKind(res.err.Hint) == "" && strings.HasPrefix(res.err.Hint, "audd ") {
			return s.h.openPaletteLine(res.err.Hint)
		}
		switch {
		case s.bs != nil && s.bs.detail, s.details:
			return s.h.setFlash("Tab goes to the buttons · esc goes back")
		case s.bs != nil:
			s.bs.detail = s.bs.selected() != nil
		case s.mic() && ks == "enter" && s.rec.view == nil:
			return s.startListen()
		case s.rec.view != nil:
			s.details, s.dscroll.off = true, 0
		}
	}
	return nil
}

// leftExits: left goes back (from a result to the form, too), except
// in typed text and in the file picker, where it goes to the parent
// folder.
func (s *recognizeSection) leftExits() bool {
	if s.browser != nil {
		return false
	}
	if s.phase != "form" {
		return true
	}
	return s.form.leftExits()
}

// enterpriseMatches are the songs of an enterprise result, one line each
// with where in the file it is, shaped like batch results.
func enterpriseMatches(doc map[string]any) []map[string]any {
	// audd recognize --enterprise prints "tracks" (with --tracklist)
	// or "matches"; a stored result is the API's chunks ("result").
	if tracks, ok := doc["tracks"].([]any); ok && len(tracks) > 0 {
		var out []map[string]any
		for _, t := range tracks {
			if m, ok := t.(map[string]any); ok {
				out = append(out, map[string]any{"input": str(m["start"]), "start": str(m["start"]), "end": str(m["end"]), "status": "matched", "result": m})
			}
		}
		return out
	}
	chunks, ok := doc["matches"].([]any)
	if !ok {
		chunks, ok = doc["result"].([]any)
	}
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, c := range chunks {
		chunk, _ := c.(map[string]any)
		songs, isChunk := chunk["songs"].([]any)
		if !isChunk {
			songs = []any{chunk}
		}
		for _, x := range songs {
			song, ok := x.(map[string]any)
			if !ok {
				continue
			}
			// Where in the file: the match's start and end when given,
			// else the chunk's offset and the song's timecode.
			start, end := str(chunk["offset"]), ""
			if _, ok := song["start_seconds"]; ok {
				start = clock(time.Duration(num(song["start_seconds"]) * float64(time.Second)))
				end = clock(time.Duration(num(song["end_seconds"]) * float64(time.Second)))
			} else if tc := str(song["timecode"]); tc != "" {
				start = strings.TrimSpace(start + " " + tc)
			}
			out = append(out, map[string]any{"input": start, "start": start, "end": end, "status": "matched", "result": song})
		}
	}
	return out
}

// validClock accepts what audd recognize --at takes: "90", "90.5",
// "1:30", "1:02:03", or "1m30s".
func validClock(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d >= 0
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return false
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 || i > 0 && v >= 60 || i < len(parts)-1 && v != float64(int(v)) {
			return false
		}
	}
	return true
}
