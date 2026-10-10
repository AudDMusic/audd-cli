package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// providers are the --return metadata blocks, in the order shown.
var providers = []struct{ name, label string }{
	{"apple_music", "Apple Music"}, {"spotify", "Spotify"}, {"deezer", "Deezer"}, {"musicbrainz", "MusicBrainz"},
}

// recognizeSection is audd recognize: one file or URL, or a batch.
type recognizeSection struct {
	h       *home
	form    *form
	browser *fileBrowser
	panel   *cmdPanel
	phase   string // "form", "running", "result"
	wantRun bool   // the plan runs first; then the recognition
	plan    string
	batch   bool
	bs      *batchState
	rec     recognition
	details bool
	runArgs []string
}

func newRecognizeSection(h *home) section {
	s := &recognizeSection{h: h, panel: newPanel(h), phase: "form"}
	in := textField("input", "Input", "A file, URL, folder, or glob. ctrl+o picks a file.")
	in.required = true
	fields := []*field{in}
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
	)
	s.form = newForm(fields...)
	s.sync()
	return s
}

// sync shows the enterprise options only with --enterprise, and the
// metadata options only without it (the enterprise endpoint has none).
func (s *recognizeSection) sync() {
	ent := s.form.get("enterprise").on
	for _, p := range providers {
		s.form.get("return:" + p.name).hidden = ent
	}
	s.form.get("limit").hidden = !ent
	s.form.get("limit").required = ent
	s.form.get("tracklist").hidden = !ent
}

func (s *recognizeSection) title() string { return "Recognize" }
func (s *recognizeSection) init() tea.Cmd { s.form.focus(); return nil }

func (s *recognizeSection) capturing() bool {
	return s.browser != nil || s.phase == "form" && s.form.capturing()
}

func (s *recognizeSection) back() bool {
	switch {
	case s.browser != nil:
		s.browser = nil
		return true
	case s.phase == "result":
		s.phase = "form"
		return true
	case s.phase == "running":
		return true // s stops it
	}
	return false
}

// prefill sets the input (Help: getting started).
func (s *recognizeSection) prefill(input string) {
	s.phase = "form"
	s.form.get("input").setValue(input)
	s.form.focusName("input")
}

// args is the command for the form as it is.
func (s *recognizeSection) args(dry bool) []string {
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
		}
		if f.get("tracklist").on {
			args = append(args, "--tracklist")
		}
	} else {
		var ret []string
		for _, p := range providers {
			if f.get("return:" + p.name).on {
				ret = append(ret, p.name)
			}
		}
		if len(ret) > 0 {
			args = append(args, "--return", strings.Join(ret, ","))
		}
	}
	if v := f.value("max-files"); v != "" {
		args = append(args, "--max-files", v)
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
	return displayCommand(s.args(false))
}

func (s *recognizeSection) keys() []keyHelp {
	switch {
	case s.browser != nil:
		return []keyHelp{{"enter", "open or pick"}, {"s", "pick the folder"}, {"esc", "close"}}
	case s.phase == "running":
		return []keyHelp{{"s", "stop"}}
	case s.phase == "result":
		k := []keyHelp{{"esc", "back to the form"}, {"r", "run again"}}
		if s.rec.link != "" {
			k = append(k, keyHelp{"o", "open the link"}, keyHelp{"c", "copy it"})
		}
		if s.rec.view != nil {
			k = append(k, keyHelp{"d", "details"})
		}
		return k
	}
	return []keyHelp{{"↑/↓", "field"}, {"space", "toggle"}, {"ctrl+o", "pick a file"}, {"enter", "next / press"}, {"esc", "sidebar"}}
}

// validLimit accepts a whole number or the typed word none.
func validLimit(v string) bool {
	if v == "none" {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n > 0
}

// check says what is missing before anything runs.
func (s *recognizeSection) check(forRun bool) string {
	f := s.form
	if f.value("input") == "" {
		return "Choose a file, URL, folder, or glob (ctrl+o picks a file)."
	}
	if f.get("enterprise").on {
		v := f.value("limit")
		if v == "" && forRun {
			return "Enterprise recognition is billed per 12-second chunk: set a limit (chunks per file), or type none to scan whole files."
		}
		if v != "" && !validLimit(v) {
			return "The limit is a number of chunks, or the word none."
		}
	}
	if v := f.value("max-files"); v != "" && !validLimit(v) {
		return "Max files is a number, or the word none."
	}
	if forRun && s.isBatch() && f.value("max-files") == "" {
		return "Folders, globs, and lists need a file limit: set Max files to a number, or type none."
	}
	return ""
}

// isBatch reports whether the input is a batch (folder, glob), the way
// audd recognize decides.
func (s *recognizeSection) isBatch() bool {
	inputs, batch, err := media.ExpandInputs([]string{s.form.value("input")}, strings.NewReader(""))
	for _, in := range inputs {
		in.Cleanup()
	}
	return err == nil && batch
}

// startPlan runs the dry run; with run set the recognition follows.
func (s *recognizeSection) startPlan(run bool) tea.Cmd {
	if e := s.check(run); e != "" {
		s.form.err = e
		return nil
	}
	s.form.err = ""
	s.wantRun = run
	s.plan = ""
	s.batch = s.isBatch()
	s.runArgs = s.args(true)
	return s.panel.start(runReq{args: s.runArgs, format: "table", tag: "plan"})
}

func (s *recognizeSection) startRun() tea.Cmd {
	s.runArgs = s.args(false)
	s.phase = "running"
	s.rec = recognition{}
	if s.batch {
		s.bs = &batchState{}
		return s.panel.start(runReq{args: s.runArgs, format: "jsonl", stream: true, spends: true, tag: "run"})
	}
	return s.panel.start(runReq{args: s.runArgs, spends: true, tag: "run"})
}

func (s *recognizeSection) update(msg tea.Msg) tea.Cmd {
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
		switch ks {
		case "enter":
			if res := s.panel.res; res != nil && res.err != nil && strings.HasPrefix(res.err.Hint, "audd ") {
				return s.h.openPaletteLine(res.err.Hint)
			}
		case "r":
			return s.startPlan(true)
		case "o":
			if s.rec.link != "" {
				if err := openURL(s.rec.link); err != nil {
					return s.h.setFlash("Could not open the browser: " + err.Error())
				}
				return s.h.setFlash("Opened " + s.rec.link)
			}
		case "c":
			if s.rec.link != "" {
				return s.h.copy(s.rec.link, s.rec.link)
			}
		case "d":
			s.details = !s.details
		case "backspace":
			s.phase = "form"
		}
		return nil
	}
	if ks == "ctrl+o" {
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
	}
	return cmd
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
		if s.wantRun {
			return s.startRun()
		}
		return nil
	}
	s.phase = "result"
	if res.err != nil || s.batch {
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
		if res.err != nil {
			if s.batch && s.bs != nil && len(s.bs.results) > 0 {
				b.WriteString(s.bs.view(st, w, h-4, false) + "\n\n")
			}
			b.WriteString(errorText(st, res.err, w))
			return b.String()
		}
		if s.batch && s.bs != nil {
			return s.bs.view(st, w, h, false)
		}
		b.WriteString(st.Dim.Render(truncate(s.form.value("input"), w)) + "\n\n")
		if s.rec.view != nil {
			v := *s.rec.view
			b.WriteString(s.h.cardText(v, s.details, w, h-2))
		} else {
			b.WriteString(s.rec.text)
		}
		if len(res.notes) > 0 {
			b.WriteString("\n\n" + styleLines(st.Dim, output.Wrap(strings.Join(res.notes, "\n"), w)))
		}
		return b.String()
	}
	b.WriteString(st.Bold.Render("Recognize music in a file, URL, or folder") + "\n\n")
	b.WriteString(s.form.view(w, st, s.h.color))
	if s.panel.running && s.panel.run != nil && s.panel.run.req.tag == "plan" {
		b.WriteString("\n\n" + st.Dim.Render("Working out the plan…"))
	} else if s.plan != "" {
		b.WriteString("\n\n" + s.planBlock(w))
	}
	return b.String()
}

func (s *recognizeSection) planBlock(w int) string {
	if s.plan == "" {
		return ""
	}
	return styleLines(s.h.st.Dim, output.Wrap(s.plan, w)) + "\n\n"
}

func (s *recognizeSection) leftExits() bool {
	if s.browser != nil {
		return false
	}
	return s.phase != "form" || s.form.leftExits()
}
