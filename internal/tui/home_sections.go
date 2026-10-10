package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams"
)

// hints turns "o open"-style key hints into keyHelp.
func hints(list []string) []keyHelp {
	out := make([]keyHelp, 0, len(list))
	for _, s := range list {
		k, help, _ := strings.Cut(s, " ")
		out = append(out, keyHelp{k, help})
	}
	return out
}

// startRecorder starts the background stream recorder the way the
// streams screens do, and returns its note (shown in the footer, never on
// the terminal under the screen).
func (h *home) startRecorder() tea.Cmd {
	note, err := streams.EnsureRecorder(h.a)
	switch {
	case err != nil:
		if output.AsError(err).Code == "not_implemented" {
			return nil
		}
		return h.setFlash("Could not start the background recorder: " + output.AsError(err).Message)
	case note != "":
		return h.setFlash(note)
	}
	return nil
}

// --- Now playing ---

type npSetupMsg struct {
	feed     Feed
	stations []Station
	err      error
}

type nowPlayingSection struct {
	h   *home
	np  *npModel
	err error
}

func newNowPlayingSection(h *home) section { return &nowPlayingSection{h: h} }

func (s *nowPlayingSection) title() string { return "Now playing" }

func (s *nowPlayingSection) init() tea.Cmd {
	a, ctx := s.h.a, s.h.ctx
	return tea.Batch(s.h.startRecorder(), func() tea.Msg {
		feed, err := NewFeed(a)
		if err != nil {
			return npSetupMsg{err: err}
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		st, err := selectStations(c, feed, nil)
		return npSetupMsg{feed: feed, stations: st, err: err}
	})
}

func (s *nowPlayingSection) update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(npSetupMsg); ok {
		if m.err != nil {
			s.err = m.err
			return nil
		}
		s.np = newEmbeddedNowPlaying(s.h.ctx, s.h.a, m.feed, m.stations, NowPlayingOptions{}, s.h.arts)
		s.np.mark = s.h.mark
		w, ht := s.h.contentSize()
		s.np.w, s.np.h = w, ht
		return s.np.Init()
	}
	if k, ok := msg.(tea.KeyMsg); ok && s.err != nil {
		if k.String() == "r" {
			s.err = nil
			return s.init()
		}
		return nil
	}
	if s.np == nil {
		return nil
	}
	_, cmd := s.np.Update(msg)
	return tea.Batch(cmd, s.h.takeOSC(s.np.takeOSC()))
}

func (s *nowPlayingSection) view(w, h int) string {
	st := s.h.st
	if s.err != nil {
		e := output.AsError(s.err)
		out := errorText(st, &runError{Code: e.Code, Message: e.Message, Hint: e.Hint}, w)
		if e.Code == "no_streams" {
			out += "\n\n" + st.Dim.Render("Add a stream in Streams (press 3, then a).")
		}
		return out + "\n\n" + st.Dim.Render("r tries again.")
	}
	if s.np == nil {
		return st.Dim.Render("Loading your streams…")
	}
	s.np.w, s.np.h = w, h
	return s.np.View()
}

func (s *nowPlayingSection) keys() []keyHelp {
	if s.np == nil {
		return []keyHelp{{"r", "try again"}}
	}
	return hints(s.np.keyList())
}

func (s *nowPlayingSection) command() string { return "audd now-playing" }
func (s *nowPlayingSection) capturing() bool { return false }

func (s *nowPlayingSection) back() bool {
	if s.np != nil && (s.np.history || s.np.zoom) {
		s.np.key(tea.KeyMsg{Type: tea.KeyEsc})
		return true
	}
	return false
}

// crumbs: the stream zoomed into, and its history.
func (s *nowPlayingSection) crumbs() []string {
	if s.np == nil || len(s.np.stations) == 0 || !s.np.zoom && !s.np.history {
		return nil
	}
	out := []string{fmt.Sprintf("Stream %d", s.np.stations[s.np.sel].RadioID)}
	if s.np.history {
		out = append(out, "History")
	}
	return out
}

// --- explorer wrapper shared by History, Streams, and Account usage ---

type explorerPane struct {
	h    *home
	ex   *explorer
	tabs []tabID
	err  error
}

func newExplorerPane(h *home, tabs ...tabID) *explorerPane { return &explorerPane{h: h, tabs: tabs} }

func (p *explorerPane) init() tea.Cmd {
	d, err := NewExplorerData(p.h.a)
	if err != nil {
		p.err = err
		return nil
	}
	p.ex = newEmbeddedExplorer(p.h.ctx, p.h.a, d, p.tabs, p.h.arts)
	p.ex.mark = p.h.mark
	return p.ex.Init()
}

func (p *explorerPane) update(msg tea.Msg) tea.Cmd {
	if p.ex == nil {
		return nil
	}
	if k, ok := msg.(tea.KeyMsg); ok && !p.ex.capturing() {
		// Interactive mode gives each letter one meaning everywhere: r
		// refreshes, x exports, e edits, d removes (after asking), R and
		// F resume a job.
		l := p.ex.top()
		kind := ""
		if l != nil {
			kind = l.kind
		}
		switch k.String() {
		case "x":
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
		case "e":
			if kind != "streams" {
				return p.h.noop("e")
			}
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}}
		case "u":
			if kind == "streams" {
				return p.h.noop("u")
			}
		case "r":
			if p.ex.tab == tabUsage {
				return p.ex.load(tabUsage, nil)
			}
			if l != nil {
				return p.ex.load(p.ex.tab, l)
			}
			return nil
		case "R", "F":
			if kind == "jobs" || kind == "items" {
				return p.ex.resumeJob(l, k.String() == "F")
			}
			return p.h.noop(k.String())
		case "d":
			if kind == "streams" {
				return p.askRemove()
			}
		}
	}
	_, cmd := p.ex.Update(msg)
	return tea.Batch(cmd, p.h.takeOSC(p.ex.takeOSC()))
}

func (p *explorerPane) view(w, h int) string {
	if p.ex != nil {
		p.ex.blur = !p.h.pageHasKeys()
	}
	if p.err != nil {
		e := output.AsError(p.err)
		return errorText(p.h.st, &runError{Message: e.Message, Hint: e.Hint}, w)
	}
	if p.ex == nil {
		return p.h.st.Dim.Render("Loading…")
	}
	p.ex.w, p.ex.h = w, h
	return p.ex.View()
}

func (p *explorerPane) keys() []keyHelp {
	if p.ex == nil {
		return nil
	}
	return hints(p.ex.keyList())
}

func (p *explorerPane) capturing() bool { return p.ex != nil && p.ex.capturing() }

// back closes a prompt, a detail view, a filter, or a drill-down.
func (p *explorerPane) back() bool {
	if p.ex == nil {
		return false
	}
	if p.ex.capturing() {
		p.ex.key(tea.KeyMsg{Type: tea.KeyEsc})
		return true
	}
	l := p.ex.top()
	if l != nil && (l.detail || l.filter != "" || len(p.ex.tabs[p.ex.tab]) > 1) {
		p.ex.key(tea.KeyMsg{Type: tea.KeyEsc})
		return true
	}
	return false
}

// askRemove asks before removing the selected stream, naming it and
// what happens.
func (p *explorerPane) askRemove() tea.Cmd {
	r := p.ex.top().selected()
	if r == nil || p.ex.d.RemoveStream == nil {
		return nil
	}
	id, _ := strconv.Atoi(r.key)
	ex := p.ex
	return p.h.askFirst(&confirmState{
		owner:    "streams",
		question: fmt.Sprintf("Remove stream %d?", id),
		notes:    []string{"URL: " + p.streamURL(r.key), "AudD stops monitoring it; its stored results stay on this computer."},
		command:  fmt.Sprintf("audd streams remove %d", id),
		cancel:   fmt.Sprintf("Kept stream %d", id),
		yes: func() tea.Cmd {
			return ex.streamAction(func(ctx context.Context) error { return ex.d.RemoveStream(ctx, id) }, fmt.Sprintf("Removed stream %d", id))
		},
	})
}

// streamURL is the URL of stream id when the list has it ("" if not).
func (p *explorerPane) streamURL(id string) string {
	if p.ex == nil || len(p.ex.tabs[tabStreams]) == 0 {
		return ""
	}
	for _, r := range p.ex.tabs[tabStreams][0].rows {
		if r.key == id && len(r.cells) > 0 {
			if d, ok := r.data.(map[string]any); ok && str(d["url"]) != "" {
				return str(d["url"])
			}
			if u := r.cells[len(r.cells)-1]; u != "" {
				return u
			}
			return "(no URL)"
		}
	}
	return ""
}

// selectedKey is the key of the selected row ("" for none).
func (p *explorerPane) selectedKey() string {
	if p.ex == nil {
		return ""
	}
	if l := p.ex.top(); l != nil {
		if r := l.selected(); r != nil {
			return r.key
		}
	}
	return ""
}

// reload reloads the top list of a tab.
func (p *explorerPane) reload(t tabID) tea.Cmd {
	if p.ex == nil || len(p.ex.tabs[t]) == 0 {
		return nil
	}
	return p.ex.load(t, p.ex.tabs[t][0])
}

// --- History ---

type historySection struct {
	h       *home
	pane    *explorerPane
	panel   *cmdPanel
	bs      *batchState
	resume  bool // showing a resumed job
	runArgs []string
}

func newHistorySection(h *home) section {
	s := &historySection{h: h, pane: newExplorerPane(h, tabRecent, tabJobs), panel: newPanel(h)}
	return s
}

func (s *historySection) title() string { return "Recognitions" }

func (s *historySection) tabNames() []string {
	if s.pane.ex == nil {
		return []string{tabTitles[tabRecent], tabTitles[tabJobs]}
	}
	var out []string
	for _, t := range s.pane.ex.allowed {
		out = append(out, tabTitles[t])
	}
	return out
}

func (s *historySection) tabIndex() int {
	if s.pane.ex == nil {
		return 0
	}
	for i, t := range s.pane.ex.allowed {
		if t == s.pane.ex.tab {
			return i
		}
	}
	return 0
}

func (s *historySection) selectTab(i int) tea.Cmd {
	if s.pane.ex == nil || i < 0 || i >= len(s.pane.ex.allowed) {
		return nil
	}
	if !s.panel.running {
		s.resume = false
	}
	return s.pane.ex.switchTab(s.pane.ex.allowed[i])
}

func (s *historySection) upExits() bool { return !s.resume && s.pane.upExits() }

func (s *historySection) crumbs() []string {
	if s.resume && s.bs != nil {
		return []string{"Resume job " + s.bs.jobID}
	}
	return s.pane.crumbs()
}

func (s *historySection) init() tea.Cmd {
	cmd := s.pane.init()
	if s.pane.ex != nil {
		s.pane.ex.onResume = s.startResume
	}
	return cmd
}

func (s *historySection) startResume(id string, retry bool) tea.Cmd {
	s.runArgs = []string{"jobs", "resume", id}
	if retry {
		s.runArgs = append(s.runArgs, "--retry-failed")
	}
	s.resume = true
	s.bs = &batchState{jobID: id}
	return s.panel.start(runReq{args: s.runArgs, format: "jsonl", stream: true, spends: true})
}

func (s *historySection) update(msg tea.Msg) tea.Cmd {
	if mine, done := s.panel.handle(msg); mine {
		if l, ok := msg.(runLineMsg); ok && l.pending == nil {
			s.bs.add(l.line)
		}
		if done {
			return s.pane.reload(tabJobs)
		}
		return nil
	}
	if k, ok := msg.(tea.KeyMsg); ok && s.resume {
		switch k.String() {
		case "s":
			s.panel.stop()
		case "esc", "backspace", "enter":
			if !s.panel.running {
				s.resume = false
			}
		}
		return nil
	}
	return s.pane.update(msg)
}

func (s *historySection) view(w, h int) string {
	if s.resume {
		st := s.h.st
		if s.panel.running && s.panel.pending != nil {
			return s.panel.view(w, h)
		}
		out := s.bs.view(st, w, h-3, s.panel.running)
		if !s.panel.running && s.panel.res != nil && s.panel.res.err != nil {
			out += "\n\n" + errorText(st, s.panel.res.err, w)
		}
		return out
	}
	return s.pane.view(w, h)
}

func (s *historySection) keys() []keyHelp {
	if s.resume {
		if s.panel.running {
			return []keyHelp{{"s", "stop"}}
		}
		return []keyHelp{{"esc", "back to the jobs"}}
	}
	return s.pane.keys()
}

func (s *historySection) command() string {
	if s.resume {
		return displayCommand(s.runArgs)
	}
	ex := s.pane.ex
	if ex == nil {
		return "audd browse"
	}
	l := ex.top()
	switch {
	case ex.tab == tabJobs && l != nil && l.kind == "items":
		return "audd jobs show " + l.arg
	case ex.tab == tabJobs:
		return "audd jobs list"
	}
	return "audd browse --tab recent"
}

func (s *historySection) capturing() bool { return !s.resume && s.pane.capturing() }

// back closes a finished resume, and what is open in the list. While a
// resume runs it stays (s stops it), and esc goes to the tab bar.
func (s *historySection) back() bool {
	if s.resume {
		if s.panel.running {
			return false
		}
		s.resume = false
		return true
	}
	return s.pane.back()
}

// --- Streams ---

var streamsPages = []string{"List", "Callback", "Recorder", "History & reports"}

type streamsSection struct {
	h    *home
	page int
	pane *explorerPane
	// add form (a on the list)
	add      *form
	adding   bool
	addPanel *cmdPanel
	// callback
	cbPanel *cmdPanel
	cbForm  *form
	cbURL   string
	cbSet   bool
	// recorder
	recPanel *cmdPanel
	// history, reports, export
	dataForm  *form
	dataPanel *cmdPanel
	exportTo  string
	lastData  []string
	addedID   string // the stream the last add was for
}

func newStreamsSection(h *home) section {
	s := &streamsSection{h: h, pane: newExplorerPane(h, tabStreams),
		addPanel: newPanel(h), cbPanel: newPanel(h), recPanel: newPanel(h), dataPanel: newPanel(h)}
	url := textField("url", "URL", "A stream URL, or twitch:<channel>, youtube:<video_id>, youtube-ch:<channel_id>")
	url.required = true
	url.need = "Enter the stream's URL"
	id := &field{kind: fInt, name: "id", label: "ID", help: "A number you choose for the stream", required: true, input: newInput()}
	id.need = "Choose a number for the stream"
	s.add = newForm(url, id,
		boolField("start", "Send results when songs start", "Live now-playing; results then have no play length"),
		buttonField("add", "Add"))
	cb := textField("url", "Callback URL", "Where AudD sends (POSTs) every result of every stream on your account")
	cb.required = true
	cb.need = "Enter the callback URL, or use the placeholder"
	cbFields := []*field{cb}
	for _, p := range providers {
		cbFields = append(cbFields, boolField("return:"+p.name, "Add "+p.label+" data", "--return "+p.name))
	}
	cbFields = append(cbFields, buttonField("set", "Set"), buttonField("placeholder", "Use the placeholder"))
	s.cbForm = newForm(cbFields...)
	kind := enumField("kind", "Show", "history: recent plays; report: plays and airtime grouped; export: write plays to a file", []string{"history", "report", "export"})
	since := textField("since", "Since", `How far back: 24h, 7d, 30d, a date, or "all"`)
	since.setValue("7d")
	sid := &field{kind: fInt, name: "id", label: "Stream ID", help: "Only this stream (empty: all)", input: newInput()}
	by := enumField("by", "Group by", "report: group by song, artist, label, or station", []string{"song", "artist", "label", "station"})
	format := enumField("format", "File format", "export: JSON lines or CSV", []string{"jsonl", "csv"})
	file := textField("file", "File name", "export: the file to write, in "+exportDirShown())
	s.dataForm = newForm(kind, since, sid, by, format, file, buttonField("run", "Run"))
	s.add.h, s.cbForm.h, s.dataForm.h = h, h, h
	s.cbForm.get("placeholder").novalidate = true
	s.syncData()
	s.recPanel.empty = s.h.st.Dim.Render("Loading…")
	return s
}

func (s *streamsSection) syncData() {
	k := s.dataForm.value("kind")
	s.dataForm.get("by").hidden = k != "report"
	s.dataForm.get("format").hidden = k != "export"
	file := s.dataForm.get("file")
	file.hidden = k != "export"
	// The suggested file name follows the format until it is changed.
	ext := "." + s.dataForm.value("format")
	if v := file.value(); v == "" || strings.HasPrefix(v, "audd-streams-export-") && !strings.HasSuffix(v, ext) {
		file.setValue("audd-streams-export-" + s.h.now().Format("20060102") + ext)
	}
}

func (s *streamsSection) title() string { return "Streams" }

func (s *streamsSection) init() tea.Cmd { return tea.Batch(s.pane.init(), s.h.startRecorder()) }

func (s *streamsSection) tabNames() []string      { return streamsPages }
func (s *streamsSection) tabIndex() int           { return s.page }
func (s *streamsSection) selectTab(i int) tea.Cmd { s.adding = false; return s.openPage(i) }

// openPage switches pages and loads what the page shows.
func (s *streamsSection) openPage(p int) tea.Cmd {
	s.page = max(0, min(len(streamsPages)-1, p))
	switch s.page {
	case 1:
		if s.cbPanel.res == nil && !s.cbPanel.running {
			return s.getCallback()
		}
	case 2:
		return s.recPanel.start(runReq{args: []string{"streams", "recorder", "status"}, format: "table"})
	}
	return nil
}

func (s *streamsSection) getCallback() tea.Cmd {
	return s.cbPanel.start(runReq{args: []string{"streams", "callback", "get"}, tag: "get"})
}

// openAdd opens the add form (Help: getting started).
func (s *streamsSection) openAdd() {
	s.page = 0
	s.adding = true
	s.add.focusName("url")
}

func (s *streamsSection) update(msg tea.Msg) tea.Cmd {
	if mine, done := s.addPanel.handle(msg); mine {
		if done && s.addPanel.res.ok() {
			s.adding = false
			s.h.streamAdded = true
			id := s.add.value("id")
			s.add.get("url").setValue("")
			s.add.get("id").setValue("")
			s.add.get("start").on = false
			return tea.Batch(s.pane.reload(tabStreams), s.h.setFlash("Added stream "+id))
		}
		if done && s.addPanel.res.err != nil {
			s.add.err = s.addPanel.res.err.Message
			if s.addPanel.res.err.Hint != "" {
				s.add.err += "\nTry: " + s.addPanel.res.err.Hint
			}
		}
		return nil
	}
	if mine, done := s.cbPanel.handle(msg); mine {
		if done && s.cbPanel.run.req.tag == "get" && s.cbPanel.res.ok() {
			doc := parseDoc(s.cbPanel.res.stdout)
			s.cbURL, s.cbSet = "", false
			if doc != nil && doc["url"] != nil {
				s.cbURL, s.cbSet = str(doc["url"]), true
				if f := s.cbForm.get("url"); f.value() == "" {
					// The URL in use: read-only until enter or e.
					f.setValue(s.cbURL)
					f.lockable, f.locked = true, true
				}
			}
		}
		if done && s.cbPanel.run.req.tag == "set" && s.cbPanel.res.ok() {
			return tea.Batch(s.h.setFlash("Callback URL set to "+s.cbForm.value("url")), s.getCallback())
		}
		return nil
	}
	if mine, _ := s.recPanel.handle(msg); mine {
		return nil
	}
	if mine, done := s.dataPanel.handle(msg); mine {
		if done && s.dataPanel.run.req.tag == "export" && s.dataPanel.res.ok() {
			return s.writeExport()
		}
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s.pane.update(msg)
	}
	ks := k.String()
	if s.page == 3 && s.dataPanel.res != nil {
		switch ks {
		case "pgdown", "pgup", "ctrl+d", "ctrl+u":
			_, ht := s.h.contentSize()
			return s.dataPanel.key(k, ht/2)
		}
	}
	switch s.page {
	case 0:
		if s.adding {
			act, cmd := s.add.update(k)
			if act == "add" {
				s.add.err = ""
				if url := s.pane.streamURL(s.add.value("id")); url != "" {
					return s.askReplace(url)
				}
				return s.addPanel.start(runReq{args: s.addArgs()})
			}
			return cmd
		}
		if ks == "a" && !s.pane.capturing() {
			s.openAdd()
			return nil
		}
		return s.pane.update(msg)
	case 1:
		act, cmd := s.cbForm.update(k)
		switch act {
		case "placeholder":
			f := s.cbForm.get("url")
			f.setValue(streams.EmptyCallbackURL)
			f.locked = false
			s.cbForm.focusName("set")
		case "set":
			return s.askCallback()
		}
		return cmd
	case 2:
		switch ks {
		case "enter":
			return s.recPanel.start(runReq{args: []string{"streams", "recorder", "start"}, format: "table"})
		case "s":
			return s.recPanel.start(runReq{args: []string{"streams", "recorder", "stop"}, format: "table"})
		case "r":
			return s.recPanel.start(runReq{args: []string{"streams", "recorder", "status"}, format: "table"})
		}
		_, ht := s.h.contentSize()
		return s.recPanel.key(k, ht)
	case 3:
		if s.exportTo != "" && !s.dataForm.capturing() {
			switch ks {
			case "o":
				dir := filepath.Dir(s.exportTo)
				if err := openURL(dir); err != nil {
					return s.h.setFlash("Could not open the folder: " + err.Error())
				}
				return s.h.setFlash("Opened " + dir)
			case "c":
				return s.h.copy(s.exportTo, "the path")
			}
		}
		act, cmd := s.dataForm.update(k)
		s.syncData()
		if act == "run" {
			s.dataForm.err = ""
			s.exportTo = ""
			args := s.dataArgs()
			s.lastData = args
			switch s.dataForm.value("kind") {
			case "export":
				return s.dataPanel.start(runReq{args: args, format: s.dataForm.value("format"), tag: "export"})
			}
			return s.dataPanel.start(runReq{args: args, format: "table"})
		}
		return cmd
	}
	return nil
}

// askCallback asks before changing the account's callback URL, naming
// the old and the new address.
func (s *streamsSection) askCallback() tea.Cmd {
	old := s.cbURL
	if !s.cbSet {
		old = "not set"
	}
	args := s.callbackArgs()
	return s.h.askFirst(&confirmState{
		owner:    "streams",
		question: "Change the callback URL?",
		notes: []string{
			"Now: " + old,
			"New: " + s.cbForm.value("url"),
			"AudD sends every result of every stream on your account to the new address.",
		},
		command: displayCommand(args),
		cancel:  "Callback URL unchanged",
		yes: func() tea.Cmd {
			return s.cbPanel.start(runReq{args: args, tag: "set"})
		},
	})
}

// askReplace: the ID chosen for a new stream is taken, so it offers to
// change that stream's URL instead.
func (s *streamsSection) askReplace(old string) tea.Cmd {
	id, url := s.add.value("id"), s.add.value("url")
	args := []string{"streams", "set-url", id, url}
	return s.h.askFirst(&confirmState{
		owner:    "streams",
		question: fmt.Sprintf("Stream %s exists already. Change its URL instead?", id),
		notes:    []string{"Now: " + old, "New: " + url, "Or press n and choose another ID for a new stream."},
		command:  displayCommand(args),
		cancel:   "Nothing changed; choose another ID",
		yes: func() tea.Cmd {
			return s.addPanel.start(runReq{args: args})
		},
	})
}

func (s *streamsSection) addArgs() []string {
	args := []string{"streams", "add", s.add.value("url"), "--id", s.add.value("id")}
	if s.add.get("start").on {
		args = append(args, "--start")
	}
	return args
}

func (s *streamsSection) callbackArgs() []string {
	args := []string{"streams", "callback", "set", s.cbForm.value("url")}
	var ret []string
	for _, p := range providers {
		if s.cbForm.get("return:" + p.name).on {
			ret = append(ret, p.name)
		}
	}
	if len(ret) > 0 {
		args = append(args, "--return", strings.Join(ret, ","))
	}
	return args
}

func (s *streamsSection) dataArgs() []string {
	f := s.dataForm
	kind := f.value("kind")
	args := []string{"streams", kind}
	if v := f.value("since"); v != "" {
		args = append(args, "--since", v)
	}
	if v := f.value("id"); v != "" {
		args = append(args, "--id", v)
	}
	if kind == "report" && f.value("by") != "song" {
		args = append(args, "--by", f.value("by"))
	}
	return args
}

// writeExport saves the export's output to the file named in the form
// and says where.
func (s *streamsSection) writeExport() tea.Cmd {
	path := s.exportPath()
	if err := os.WriteFile(path, []byte(s.dataPanel.res.stdout), 0o644); err != nil {
		s.exportTo = ""
		return s.h.setFlash("Could not write the export: " + err.Error())
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	s.exportTo = path
	return nil
}

// exportPath is where the export goes: the file name in the export
// folder (the working directory).
func (s *streamsSection) exportPath() string {
	name := s.dataForm.value("file")
	if name == "" {
		name = "audd-streams-export." + s.dataForm.value("format")
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(explorerExportDir, name)
}

// exportDirShown is the export folder as people see it.
func exportDirShown() string {
	if abs, err := filepath.Abs(explorerExportDir); err == nil {
		return abs
	}
	return explorerExportDir
}

// formView draws a page's form; it has its cursor only while the page
// has the keys.
func (s *streamsSection) formView(f *form, w int) string {
	v := f.view(w, s.h.st, s.h.color)
	if !s.h.pageHasKeys() {
		v += "\n" + s.h.st.Dim.Render("Press ↓ or enter to fill this in.")
	}
	return v
}

// wheel moves through the list of streams, or scrolls the output.
func (s *streamsSection) wheel(dir int) tea.Cmd {
	k := tea.KeyMsg{Type: tea.KeyDown}
	if dir < 0 {
		k = tea.KeyMsg{Type: tea.KeyUp}
	}
	_, ht := s.h.contentSize()
	switch {
	case s.page == 0 && !s.adding:
		return s.pane.update(k)
	case s.page == 2:
		return keyWheel(dir, 3, func(k tea.KeyMsg) tea.Cmd { return s.recPanel.key(k, ht) })
	case s.page == 3 && s.dataPanel.res != nil:
		return keyWheel(dir, 3, func(k tea.KeyMsg) tea.Cmd { return s.dataPanel.key(k, ht/2) })
	}
	return nil
}

func (s *streamsSection) view(w, h int) string {
	st := s.h.st
	bar := ""
	switch s.page {
	case 0:
		if s.adding {
			out := st.Bold.Render("Add a stream") + "\n\n" + s.add.view(w, st, s.h.color)
			if s.addPanel.running {
				out += "\n\n" + st.Dim.Render("Adding…")
			}
			return bar + out
		}
		return bar + s.pane.view(w, h)
	case 1:
		var b strings.Builder
		switch {
		case s.cbPanel.running && s.cbPanel.run.req.tag == "get":
			b.WriteString(st.Dim.Render("Reading the callback URL…"))
		case s.cbPanel.res != nil && s.cbPanel.res.err != nil:
			b.WriteString(errorText(st, s.cbPanel.res.err, w))
		case s.cbSet:
			b.WriteString("Callback URL: " + st.Bold.Render(s.cbURL))
		case s.cbPanel.res != nil:
			b.WriteString("No callback URL is set.")
		}
		b.WriteString("\n\n" + styleLines(st.Dim, output.Wrap("AudD sends (POSTs) every stream result to your account's callback URL. audd's background recorder receives results on its own, so without a server of your own "+streams.EmptyCallbackURL+" (it accepts the results and drops them) is fine.", w)))
		b.WriteString("\n\n" + s.formView(s.cbForm, w))
		return bar + b.String()
	case 2:
		intro := styleLines(st.Dim, output.Wrap("The background recorder saves every stream result on this computer, so Recognitions, reports, and Now playing stay complete while audd is closed. It runs in the background; streams.background_recorder in Settings starts it automatically.", w))
		used := strings.Count(intro, "\n") + 3
		return bar + st.Bold.Render("Background recorder") + "\n" + intro + "\n\n" + s.recPanel.view(w, max(1, h-used))
	}
	formView := s.formView(s.dataForm, w)
	if s.dataForm.value("kind") == "export" {
		formView += "\n" + st.Dim.Render(truncate("Folder: "+exportDirShown(), w))
	}
	used := strings.Count(formView, "\n") + 2
	out := bar + formView
	switch {
	case s.dataPanel.running:
		out += "\n\n" + st.Dim.Render("Running…")
	case s.dataPanel.res != nil && s.dataPanel.res.err != nil:
		out += "\n\n" + errorText(st, s.dataPanel.res.err, w)
	case s.dataPanel.res != nil && s.dataPanel.run.req.tag == "export":
		n := strings.Count(strings.TrimSpace(s.dataPanel.res.stdout), "\n") + 1
		if strings.TrimSpace(s.dataPanel.res.stdout) == "" {
			n = 0
		}
		if s.dataForm.value("format") == "csv" && n > 0 {
			n-- // the header row
		}
		if s.exportTo != "" {
			out += "\n\n" + output.Wrap(fmt.Sprintf("Wrote %s to %s", output.Plural(n, "play"), s.exportTo), w) +
				"\n" + st.Dim.Render("o opens the folder, c copies the path.")
		}
	case s.dataPanel.res != nil:
		out += "\n\n" + s.dataPanel.view(w, max(1, h-used-1))
	}
	return out
}

func (s *streamsSection) keys() []keyHelp {
	switch s.page {
	case 0:
		if s.adding {
			return append(s.add.rowKeys("add"), keyHelp{"esc", "cancel"})
		}
		return s.pane.keys()
	case 1:
		return s.cbForm.rowKeys("set")
	case 2:
		return []keyHelp{{"↑/↓", "scroll"}, {"enter", "start"}, {"s", "stop"}, {"r", "refresh"}}
	}
	if s.exportTo != "" && s.dataPanel.res != nil {
		return append(s.dataForm.rowKeys("run"), keyHelp{"o", "open folder"}, keyHelp{"c", "copy path"})
	}
	return append(s.dataForm.rowKeys("run"), keyHelp{"pgdn/pgup", "scroll the results"})
}

func (s *streamsSection) command() string {
	switch s.page {
	case 0:
		if s.adding {
			return displayCommand(s.addArgs())
		}
		if ex := s.pane.ex; ex != nil {
			if ex.confirm != "" {
				return "audd streams remove " + s.pane.selectedKey()
			}
			if ex.purpose == inputSetURL {
				return displayCommand([]string{"streams", "set-url", ex.pending["id"], ex.input.Value()})
			}
			if l := ex.top(); l != nil && l.kind == "plays" {
				return "audd streams history --id " + l.arg
			}
		}
		return "audd streams list"
	case 1:
		if !s.h.pageHasKeys() || s.cbForm.value("url") == "" {
			return "audd streams callback get"
		}
		return displayCommand(s.callbackArgs())
	case 2:
		if s.recPanel.args != nil {
			return displayCommand(s.recPanel.args)
		}
		return "audd streams recorder status"
	}
	args := s.dataArgs()
	if s.dataForm.value("kind") == "export" {
		ext := s.dataForm.value("format")
		return displayCommand(append(args, "--format", ext)) + " > " + shellQuote(s.exportPath())
	}
	return displayCommand(args)
}

func (s *streamsSection) capturing() bool {
	switch s.page {
	case 0:
		return s.adding && s.add.capturing() || s.pane.capturing()
	case 1:
		return s.cbForm.capturing()
	case 3:
		return s.dataForm.capturing()
	}
	return false
}

// back closes the add form, details, or a drill-down; the pages
// themselves are the top, so esc then goes to the tab bar.
func (s *streamsSection) back() bool {
	if s.page == 0 {
		if s.adding {
			s.adding = false
			return true
		}
		return s.pane.back()
	}
	return false
}

func (s *streamsSection) upExits() bool {
	switch s.page {
	case 0:
		return !s.adding && s.pane.upExits()
	case 1:
		return s.cbForm.cursor == 0
	case 2:
		return s.recPanel.scroll.off == 0
	}
	return s.dataForm.cursor == 0
}

func (s *streamsSection) crumbs() []string {
	if s.page == 0 {
		if s.adding {
			return []string{"Add a stream"}
		}
		return s.pane.crumbs()
	}
	return nil
}

// leftExits: in the grid, left moves to the station on the left and
// goes back from the left-most column; zoomed in, it goes back from the
// first station.
func (s *nowPlayingSection) leftExits() bool {
	if s.np == nil || s.np.history || len(s.np.stations) == 0 {
		return true
	}
	if s.np.single() {
		return s.np.sel == 0
	}
	return s.np.sel%s.np.gridCols() == 0
}

// leftExits: left goes back (closing details and drill-downs first),
// except in a prompt being typed.
func (p *explorerPane) leftExits() bool {
	return p.ex == nil || !p.ex.capturing() || p.ex.purpose != inputNone && p.ex.input.Position() == 0
}

func (s *historySection) leftExits() bool { return s.resume || s.pane.leftExits() }

// leftExits: left goes back, except inside typed text in a form.
func (s *streamsSection) leftExits() bool {
	switch s.page {
	case 0:
		if s.adding {
			return s.add.leftExits()
		}
		return s.pane.leftExits()
	case 1:
		return s.cbForm.leftExits()
	case 3:
		return s.dataForm.leftExits()
	}
	return true
}

// upExits: the list is at its top row (or shows no list).
func (p *explorerPane) upExits() bool {
	if p.ex == nil {
		return true
	}
	if !p.atTop() {
		return false
	}
	l := p.ex.top()
	return l == nil || p.ex.tab == tabUsage || l.cursor == 0
}

// crumbs names what is opened in the explorer: a drill-down, details.
func (p *explorerPane) crumbs() []string {
	if p.ex == nil {
		return nil
	}
	var out []string
	levels := p.ex.tabs[p.ex.tab]
	for i, l := range levels {
		if i > 0 {
			out = append(out, l.title)
		}
	}
	if l := p.ex.top(); l != nil && l.detail {
		out = append(out, "Details")
	}
	return out
}

// atTop reports whether the explorer shows its list (no details, no
// drill-down, no prompt), where right can go on to the next page.
func (p *explorerPane) atTop() bool {
	if p.ex == nil {
		return true
	}
	l := p.ex.top()
	return !p.ex.capturing() && p.ex.confirm == "" && (l == nil || !l.detail && len(p.ex.tabs[p.ex.tab]) <= 1)
}

// wheel moves through the list, or scrolls the details.
func (p *explorerPane) wheel(dir int) tea.Cmd {
	k := tea.KeyMsg{Type: tea.KeyDown}
	if dir < 0 {
		k = tea.KeyMsg{Type: tea.KeyUp}
	}
	return p.update(k)
}

func (s *historySection) wheel(dir int) tea.Cmd {
	if s.resume {
		return nil
	}
	return s.pane.wheel(dir)
}
