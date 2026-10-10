package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// providers are the --return metadata blocks, in the order shown.
var providers = []struct{ name, label string }{
	{"apple_music", "Apple Music"}, {"spotify", "Spotify"}, {"deezer", "Deezer"}, {"musicbrainz", "MusicBrainz"},
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
	runArgs []string
	m       micState
}

func newRecognizeSection(h *home) section {
	s := &recognizeSection{h: h, panel: newPanel(h), phase: "form"}
	src := enumField("source", "Source", "←/→ switch between a file and the microphone", []string{sourceFile, sourceMic})
	in := textField("input", "Input", "A file, URL, folder, or glob; enter recognizes it. ctrl+o picks a file.")
	in.required = true
	in.need = "Enter a file, URL, or folder (ctrl+o picks a file)"
	sec := &field{kind: fInt, name: "seconds", label: "Seconds", help: "How long to record (at most 60). 10 to 12 seconds is enough.", input: newInput()}
	sec.setValue("10")
	fields := []*field{src, in, sec, textField("device", "Device", "Input device; empty for the system default")}
	for _, p := range providers {
		fields = append(fields, boolField("return:"+p.name, "Add "+p.label+" data", "--return "+p.name))
	}
	fields = append(fields,
		boolField("enterprise", "Enterprise: scan the whole file", "Every match with its position. Billed per 12-second chunk; needs a limit."),
		textField("limit", "Limit", "12-second chunks per file: a number, or type none to scan whole files"),
		boolField("tracklist", "Tracklist", "Merge consecutive matches into tracks with start and end times"),
		textField("max-files", "Max files", "For folders, globs, and lists: a number of files, or type none"),
		textField("at", "Clip at", "Send 12 seconds from this time: 90, 1:30, or 1m30s (needs ffmpeg)"),
		boolField("no-cache", "Send even if cached", "--no-cache"),
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
	s.form.get("recognize").disabled = needInput
	s.form.get("plan").disabled = needInput
	// The plan works with the options as they are.
	s.form.get("plan").novalidate = true
	s.form.get("limit").need = "Enterprise recognition is billed per 12-second chunk: set a limit (chunks per file), or type none to scan whole files"
	s.form.get("max-files").need = "Folders, globs, and lists need a file limit: a number, or type none"
	s.form.get("listen").disabled = func() string {
		if t := s.m.tools; t != nil && t.FFmpeg == "" && t.Sox == "" {
			return "Listening needs ffmpeg or sox: install one (see below), then come back"
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

func (s *recognizeSection) capturing() bool {
	return s.browser != nil || s.phase == "form" && s.form.capturing()
}

func (s *recognizeSection) back() bool {
	switch {
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
		// Back on the form, as it was; the old plan is gone.
		s.phase = "form"
		s.plan = ""
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
	if s.mic() {
		out[0] = "Microphone"
	}
	switch {
	case s.browser != nil:
		out = append(out, "Pick a file")
	case s.phase == "running" && s.mic():
		out = append(out, "Listening")
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
	in := f.value("input")
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
	if v := f.value("max-files"); v != "" {
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
	if s.browser == nil && s.mic() {
		return s.micKeys()
	}
	switch {
	case s.browser != nil:
		return []keyHelp{{"↑/↓", "move"}, {"→/enter", "open or pick"}, {"s", "pick the folder"}, {"esc", "close"}}
	case s.phase == "running":
		return []keyHelp{{"s", "stop"}}
	case s.phase == "result" && s.bs != nil && s.bs.detail:
		return []keyHelp{{"o", "open link"}, {"c", "copy link"}, {"esc", "back to the list"}}
	case s.phase == "result" && s.bs != nil:
		return []keyHelp{{"↑/↓", "move"}, {"→/enter", "details"}, {"o", "open link"}, {"c", "copy link"}, {"esc", "back to the form"}}
	case s.phase == "result":
		k := []keyHelp{}
		if s.rec.view != nil {
			k = append(k, keyHelp{"→/enter", "details"})
		}
		if res := s.panel.res; res != nil && res.err != nil && strings.HasPrefix(res.err.Hint, "audd ") {
			k = []keyHelp{{"enter", "open " + res.err.Hint}}
		}
		if s.rec.link != "" {
			k = append(k, keyHelp{"o", "open link"}, keyHelp{"c", "copy link"})
		}
		return append(k, keyHelp{"esc", "back to the form"})
	}
	k := s.form.rowKeys("recognize")
	return append(k, keyHelp{"ctrl+o", "pick a file"})
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
	if f.get("enterprise").on {
		v := f.value("limit")
		if v == "" && forRun {
			return "limit", f.get("limit").need
		}
		if v != "" && !validLimit(v) {
			return "limit", "The limit is a number of chunks, or the word none."
		}
	}
	if v := f.value("max-files"); v != "" && !validLimit(v) {
		return "max-files", "Max files is a number, or the word none."
	}
	if forRun && s.isBatch() && f.value("max-files") == "" {
		return "max-files", f.get("max-files").need
	}
	return "", ""
}

// isBatch reports whether the input is a batch (folder, glob), the way
// audd recognize decides.
func (s *recognizeSection) isBatch() bool {
	in := s.form.value("input")
	if in == "" || strings.Contains(in, "://") {
		return false
	}
	inputs, batch, err := media.ExpandInputs([]string{in}, strings.NewReader(""))
	for _, in := range inputs {
		in.Cleanup()
	}
	return err == nil && batch
}

// startPlan runs the dry run; with run set the recognition follows.
func (s *recognizeSection) startPlan(run bool) tea.Cmd {
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
		if p, done := s.browser.key(ks, ch); done {
			s.browser = nil
			if p != "" {
				s.form.get("input").setValue(p)
				s.form.focusName("input")
			}
		}
		return nil
	}
	switch s.phase {
	case "running":
		if ks == "s" {
			s.panel.stop()
			return s.h.setFlash("Stopping…")
		}
		return nil
	case "result":
		return s.resultKey(ks)
	}
	if ks == "ctrl+o" && !s.mic() {
		s.browser = newFileBrowser(s.form.value("input"))
		return nil
	}
	act, cmd := s.form.update(k)
	s.sync()
	switch act {
	case "recognize":
		return s.startPlan(true)
	case "plan":
		return s.startPlan(false)
	case "listen":
		return s.startListen()
	}
	return tea.Batch(cmd, s.findDevices())
}

// finished handles the end of the plan or of the recognition.
func (s *recognizeSection) finished() tea.Cmd {
	res := *s.panel.res
	if s.panel.run.req.tag == "plan" {
		if res.err != nil {
			s.form.err = res.err.Message
			if res.err.Hint != "" {
				s.form.err += "\nTry: " + res.err.Hint
			}
			s.phase = "form"
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
	s.rec = parseRecognition(parseDoc(res.stdout))
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
				b.WriteString(s.batchView(w, h-4) + "\n\n")
			}
			b.WriteString(errorText(st, res.err, w))
			return b.String()
		}
		if s.batch && s.bs != nil {
			return s.batchView(w, h)
		}
		if !s.mic() {
			b.WriteString(st.Dim.Render(truncate(s.form.value("input"), w)) + "\n\n")
		}
		if s.rec.view != nil {
			v := *s.rec.view
			b.WriteString(s.h.cardText(v, s.details, w, h-2))
		} else {
			b.WriteString(s.rec.text)
			if s.mic() {
				b.WriteString("\n\n" + st.Dim.Render("Press enter to listen again."))
			}
		}
		if len(res.notes) > 0 {
			b.WriteString("\n\n" + styleLines(st.Dim, output.Wrap(strings.Join(res.notes, "\n"), w)))
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
func (s *recognizeSection) batchView(w, h int) string {
	st := s.h.st
	if s.bs.detail {
		if m := s.bs.selected(); m != nil {
			ds := detailStyles{title: st.Title.Inherit(st.Accent), dim: st.Dim, key: st.Key}
			raw, _ := json.Marshal(m["result"])
			out := st.Dim.Render(truncate(str(m["input"]), w)) + "\n\n" + resultSong(m)
			if m["result"] != nil {
				out += "\n\n" + resultDetail(raw, ds)
			}
			return out
		}
	}
	return s.bs.listView(st, w, h, func(label string, sel bool) string { return s.h.listRow(label, sel) })
}

// resultKey handles a key on a result.
func (s *recognizeSection) resultKey(ks string) tea.Cmd {
	link := s.rec.link
	if s.bs != nil {
		link = batchLink(s.bs.selected())
	}
	switch ks {
	case "up", "k", "down", "j":
		if s.bs != nil && !s.bs.detail {
			s.bs.move(map[string]int{"up": -1, "k": -1, "down": 1, "j": 1}[ks])
		}
		return nil
	case "enter", "right":
		if res := s.panel.res; ks == "enter" && res != nil && res.err != nil && strings.HasPrefix(res.err.Hint, "audd ") {
			return s.h.openPaletteLine(res.err.Hint)
		}
		switch {
		case s.bs != nil:
			s.bs.detail = s.bs.selected() != nil
		case s.mic() && ks == "enter" && s.rec.view == nil:
			return s.startListen()
		case s.rec.view != nil:
			s.details = true
		}
		return nil
	case "o":
		if link == "" {
			return s.h.setFlash("No link to open")
		}
		if err := openURL(link); err != nil {
			return s.h.setFlash("Could not open the browser: " + err.Error())
		}
		return s.h.setFlash("Opened in your browser: " + link)
	case "c":
		if link == "" {
			return s.h.setFlash("No link to copy")
		}
		return s.h.copy(link, link)
	case "backspace":
		s.back()
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
