package tui

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"slices"
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
		s.np.flashTo = s.h.setFlash
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

// actions are the buttons for the selected stream.
func (s *nowPlayingSection) actions() []button {
	if s.np == nil || len(s.np.stations) == 0 {
		return nil
	}
	np := s.np
	press := func(r rune) func() tea.Cmd {
		return func() tea.Cmd {
			_, cmd := np.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			return tea.Batch(cmd, s.h.takeOSC(np.takeOSC()))
		}
	}
	var b []button
	switch {
	case np.history:
		b = append(b, button{"Back to the stream", "", press('h')})
	default:
		b = append(b, button{"History", "", press('h')})
	}
	if cur := np.current(); cur != nil && len(cur.Plays) > 0 && cur.Plays[0].SongLink != "" && !np.history {
		b = append(b, button{"Open link", "o", press('o')}, button{"Copy link", "c", press('c')})
	}
	return append(b, button{"Refresh", "r", func() tea.Cmd {
		_, cmd := np.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		return tea.Batch(cmd, s.h.setFlash("Refreshed"))
	}})
}

// downExits: ↓ from the last row of streams goes to the buttons.
func (s *nowPlayingSection) downExits() bool {
	if s.np == nil || s.np.single() {
		return true
	}
	c := s.np.gridCols()
	return s.np.sel+c >= len(s.np.stations)
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
	// wantKey is a row to select once it is in the list (a stream just
	// added).
	wantKey string
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
	p.ex.flashTo = p.h.setFlash
	p.ex.onSetURL = p.askSetURL
	return p.ex.Init()
}

func (p *explorerPane) update(msg tea.Msg) tea.Cmd {
	if p.ex == nil {
		return nil
	}
	if k, ok := msg.(tea.KeyMsg); ok && !p.ex.capturing() {
		if l := p.ex.top(); k.String() == "enter" && l != nil && l.detail {
			return p.h.setFlash("Tab goes to this page's buttons · esc goes back to the list")
		}
	}
	_, cmd := p.ex.Update(msg)
	if p.wantKey != "" {
		if l := p.ex.top(); l != nil {
			for i, r := range l.visible() {
				if r.key == p.wantKey {
					l.cursor, p.wantKey = i, ""
				}
			}
		}
	}
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

// actions are the buttons for what the pane shows: a list, or the page
// of one row (a stream, a job, a song).
func (p *explorerPane) actions() []button {
	if p.ex == nil || p.ex.capturing() {
		return nil
	}
	ex := p.ex
	refresh := button{"Refresh", "r", func() tea.Cmd {
		if ex.tab == tabUsage {
			return tea.Batch(ex.load(tabUsage, nil), p.h.setFlash("Refreshed"))
		}
		if l := ex.top(); l != nil {
			return tea.Batch(ex.load(ex.tab, l), p.h.setFlash("Refreshed"))
		}
		return nil
	}}
	export := button{"Export", "x", p.askExport}
	if ex.tab == tabUsage {
		return []button{refresh, export}
	}
	l := ex.top()
	if l == nil {
		return nil
	}
	r := l.selected()
	jobButtons := func(j *JobRow, jl *level) []button {
		var b []button
		if j == nil {
			return b
		}
		if j.Status != "done" && j.Done < j.Total {
			b = append(b, button{"Resume", "", func() tea.Cmd { return ex.resumeJob(jl, false) }})
		}
		if j.Failed > 0 {
			b = append(b, button{"Retry failed files", "", func() tea.Cmd { return ex.resumeJob(jl, true) }})
		}
		return b
	}
	if l.detail && r != nil {
		var b []button
		switch l.kind {
		case "streams":
			b = append(b,
				button{"History", "", func() tea.Cmd { return ex.drill(l, r) }},
				button{"Edit URL", "e", p.startEditURL},
				button{"Remove", "d", p.askRemove})
		case "jobs":
			j := ex.jobOf(l)
			b = append(b, button{"Files", "", func() tea.Cmd { return ex.drill(l, r) }})
			b = append(b, jobButtons(j, l)...)
			if j != nil {
				cmd := jobCommand(j.Command)
				b = append(b, button{"Copy the batch command", "c", func() tea.Cmd { return p.h.copy(cmd, "the batch command: "+cmd) }})
			}
			return b
		}
		if r.link != "" {
			b = append(b,
				button{"Open link", "o", func() tea.Cmd { return ex.rowAction("o", r) }},
				button{"Copy link", "c", func() tea.Cmd { return ex.rowAction("c", r) }})
		}
		if r.copies["i"] != "" {
			b = append(b, button{"Copy ISRC", "i", func() tea.Cmd { return ex.rowAction("i", r) }})
		}
		if r.copies["u"] != "" {
			b = append(b, button{"Copy UPC", "u", func() tea.Cmd { return ex.rowAction("u", r) }})
		}
		return b
	}
	var b []button
	switch l.kind {
	case "streams":
		b = append(b, button{"Add a stream", "a", func() tea.Cmd { return p.onAdd() }})
	case "items":
		b = append(b, jobButtons(ex.jobOf(l), l)...)
	}
	return append(b, refresh, export)
}

// onAdd opens the add form of the section the pane is in (Streams).
func (p *explorerPane) onAdd() tea.Cmd {
	if s, ok := p.h.subs["streams"].(*streamsSection); ok && s.pane == p {
		s.openAdd()
	}
	return nil
}

// downExits: ↓ on the last row (or the end of a page) goes to the
// buttons.
func (p *explorerPane) downExits() bool {
	if p.ex == nil {
		return true
	}
	l := p.ex.top()
	if l == nil || p.ex.tab == tabUsage {
		return true
	}
	if l.detail {
		return true
	}
	return l.cursor >= len(l.visible())-1
}

// askExport asks which format to export the list in.
func (p *explorerPane) askExport() tea.Cmd {
	formats := []string{"csv", "json"}
	return p.h.askFirst(&confirmState{
		owner:    p.owner(),
		question: "Export this list as CSV or JSON?",
		notes:    []string{"The file is written in " + exportDirShown() + "."},
		cancel:   "Export cancelled",
		choices:  []string{"CSV", "JSON"},
		pick:     func(i int) tea.Cmd { return p.ex.export(formats[i]) },
	})
}

// owner is the section the pane is part of.
func (p *explorerPane) owner() string {
	for id, sec := range p.h.subs {
		switch s := sec.(type) {
		case *streamsSection:
			if s.pane == p {
				return id
			}
		case *historySection:
			if s.pane == p {
				return id
			}
		case *accountSection:
			if s.usage == p {
				return id
			}
		}
	}
	return ""
}

// startEditURL opens the selected stream's URL for editing, starting from
// the URL it has.
func (p *explorerPane) startEditURL() tea.Cmd {
	r := p.ex.top().selected()
	p.ex.pending = map[string]string{"id": r.key}
	return p.ex.startInput(inputSetURL, "New URL for stream "+r.key+": ", p.streamURL(r.key))
}

// askSetURL checks a stream's new URL and asks before changing it.
func (p *explorerPane) askSetURL(id int, url string) tea.Cmd {
	key := strconv.Itoa(id)
	old := p.streamURL(key)
	switch {
	case url == old:
		return p.h.setFlash("No change")
	case !validStreamURL(url):
		return p.h.setFlash("That is not a stream URL: use http(s)://…, twitch:<channel>, youtube:<video_id>, or youtube-ch:<channel_id>")
	}
	ex := p.ex
	return p.h.askFirst(&confirmState{
		owner:    p.owner(),
		question: fmt.Sprintf("Change the URL of stream %d?", id),
		action:   "Change the URL",
		notes:    []string{"Now: " + old, "New: " + url, "AudD starts monitoring the new URL; results so far stay."},
		command:  displayCommand([]string{"streams", "set-url", key, url}),
		cancel:   "Cancelled. Nothing was changed.",
		yes: func() tea.Cmd {
			return ex.streamAction(func(ctx context.Context) error { return ex.d.SetStreamURL(ctx, id, url) }, fmt.Sprintf("Stream %d now uses %s", id, url))
		},
	})
}

// validStreamURL accepts what audd streams add takes: a URL with a host,
// or a twitch:, youtube:, or youtube-ch: name.
func validStreamURL(u string) bool {
	for _, pre := range []string{"twitch:", "youtube:", "youtube-ch:"} {
		if strings.HasPrefix(u, pre) && len(u) > len(pre) {
			return true
		}
	}
	scheme, rest, ok := strings.Cut(u, "://")
	return ok && scheme != "" && rest != "" && !strings.ContainsAny(u, " \t")
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
		owner:    p.owner(),
		question: fmt.Sprintf("Remove stream %d?", id),
		action:   "Remove stream",
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

// actions: Stop while a job resumes, the links of a file's result, or
// the list's buttons.
func (s *historySection) actions() []button {
	if s.resume {
		if s.panel.running {
			return []button{{"Stop", "s", func() tea.Cmd { s.panel.stop(); return s.h.setFlash("Stopping…") }}}
		}
		return s.h.batchButtons(s.bs)
	}
	return s.pane.actions()
}

func (s *historySection) downExits() bool {
	if s.resume {
		return s.panel.running || s.bs.detail || s.bs.cursor >= len(s.bs.results)-1
	}
	return s.pane.downExits()
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
	s.bs = &batchState{jobID: id, selectable: true}
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
		if s.panel.running {
			return nil
		}
		switch k.String() {
		case "up":
			s.bs.move(-1)
		case "down":
			s.bs.move(1)
		case "enter", "right":
			if s.bs.detail {
				return s.h.setFlash("Tab goes to the buttons · esc goes back")
			}
			s.bs.detail = s.bs.selected() != nil
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
		if !s.panel.running {
			out = s.h.batchPage(s.bs, w, h-3)
		}
		if !s.panel.running && s.panel.res != nil && s.panel.res.err != nil {
			out += "\n\n" + errorText(st, s.panel.res.err, w)
		}
		return out
	}
	return s.pane.view(w, h)
}

func (s *historySection) keys() []keyHelp {
	if s.resume {
		switch {
		case s.panel.running:
			return nil
		case s.bs.detail:
			return []keyHelp{{"esc", "back to the files"}}
		}
		return []keyHelp{{"↑/↓", "move"}, {"→/enter", "details"}, {"esc", "back to the jobs"}}
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
		switch {
		case s.panel.running:
			return false
		case s.bs.detail:
			s.bs.detail = false
		default:
			s.resume = false
		}
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
	autoFile  string // the file name suggested, until it is changed
	// recRunning is whether the recorder runs, from its last status
	// ("" when not known).
	recRunning string
}

func newStreamsSection(h *home) section {
	s := &streamsSection{h: h, pane: newExplorerPane(h, tabStreams),
		addPanel: newPanel(h), cbPanel: newPanel(h), recPanel: newPanel(h), dataPanel: newPanel(h)}
	url := textField("url", "URL", "A stream URL, or twitch:<channel>, youtube:<video_id>, youtube-ch:<channel_id>")
	url.required = true
	url.need = "Enter the stream's URL"
	url.placeholder = "https://…"
	id := &field{kind: fInt, name: "id", label: "ID", help: "A number you choose for the stream", required: true, input: newInput()}
	id.need = "Choose a number for the stream"
	id.placeholder = "e.g. 1"
	s.add = newForm(url, id,
		boolField("start", "Send results when songs start", "Each result is sent as soon as a song starts (for live now-playing); results then don't say how long the song played."),
		buttonField("add", "Add"))
	cb := textField("url", "Callback URL", "Where AudD sends (POSTs) every result of every stream on your account")
	cb.required = true
	cb.need = "Enter the callback URL, or use the placeholder"
	cb.placeholder = "https://your-server.example/audd"
	cbFields := []*field{cb}
	for _, p := range providers {
		cbFields = append(cbFields, providerField(p.name, p.label, p.what))
	}
	cbFields = append(cbFields, buttonField("set", "Set"), buttonField("placeholder", "Use the placeholder"))
	s.cbForm = newForm(cbFields...)
	kind := enumField("kind", "Show", "history: recent plays; report: plays and airtime grouped; export: write plays to a file", []string{"history", "report", "export"})
	since := textField("since", "Since", `How far back: 24h, 7d, 30d, a date, or "all"`)
	since.setValue("7d")
	since.placeholder = "e.g. 30d"
	since.validate = validSince
	sid := &field{kind: fInt, name: "id", label: "Stream ID", help: "Only this stream (empty: all)", input: newInput()}
	sid.placeholder = "all streams"
	by := enumField("by", "Group by", "report: group by song, artist, label, or station", []string{"song", "artist", "label", "station"})
	format := enumField("format", "File format", "export: CSV (opens in a spreadsheet) or JSON lines", []string{"csv", "jsonl"})
	file := textField("file", "File name", "export: the file to write, in the folder shown below")
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
	if v := file.value(); s.autoFile == "" || v == s.autoFile {
		s.autoFile = "audd-streams-export-" + s.h.now().Format("20060102") + "." + s.dataForm.value("format")
		file.setValue(s.autoFile)
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
	s.add.resume()
	// Suggest the next free ID.
	next := 1
	if s.pane.ex != nil && len(s.pane.ex.tabs[tabStreams]) > 0 {
		for _, r := range s.pane.ex.tabs[tabStreams][0].rows {
			if n, err := strconv.Atoi(r.key); err == nil && n >= next {
				next = n + 1
			}
		}
	}
	s.add.get("id").placeholder = "e.g. " + strconv.Itoa(next)
}

func (s *streamsSection) update(msg tea.Msg) tea.Cmd {
	if mine, done := s.addPanel.handle(msg); mine {
		if done && s.addPanel.res.ok() {
			s.adding = false
			s.h.streamAdded = true
			id := s.add.value("id")
			s.pane.wantKey = id // the new stream is selected
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
				base, ret := splitReturn(s.cbURL)
				if f := s.cbForm.get("url"); f.value() == "" {
					// The URL in use, and the data it asks for: read-only
					// until enter or e.
					f.setValue(base)
					f.lockable, f.locked = true, true
					for _, p := range providers {
						s.cbForm.get("return:" + p.name).on = slices.Contains(ret, p.name)
					}
				}
			}
		}
		if done && s.cbPanel.run.req.tag == "set" && s.cbPanel.res.ok() {
			s.cbForm.get("url").locked = true
			return tea.Batch(s.h.setFlash("Callback URL set to "+s.cbForm.value("url")+", "+dataWords(s.returnSet())), s.getCallback())
		}
		return nil
	}
	if mine, done := s.recPanel.handle(msg); mine {
		if !done || len(s.recPanel.args) < 3 {
			return nil
		}
		switch s.recPanel.args[2] {
		case "start", "stop":
			// Say so, and show the state it is in now.
			msg := map[string]string{"start": "Started", "stop": "Stopped"}[s.recPanel.args[2]]
			if !s.recPanel.res.ok() {
				msg = "Could not " + s.recPanel.args[2] + " the recorder"
			}
			return tea.Batch(s.h.setFlash(msg), s.recPanel.start(runReq{args: []string{"streams", "recorder", "status"}, format: "table"}))
		case "status":
			s.recRunning = ""
			if s.recPanel.res.ok() {
				s.recRunning = "no"
				if run, _ := recorderFacts(s.recPanel.res.stdout); strings.HasPrefix(run, "yes") {
					s.recRunning = "yes"
				}
			}
		}
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
		_, ht := s.h.contentSize()
		return s.recPanel.key(k, ht)
	case 3:
		act, cmd := s.dataForm.update(k)
		s.syncData()
		if act == "run" {
			s.dataForm.err = ""
			s.exportTo = ""
			args := s.dataArgs()
			s.lastData = args
			switch s.dataForm.value("kind") {
			case "export":
				run := func() tea.Cmd {
					return s.dataPanel.start(runReq{args: args, format: s.dataForm.value("format"), tag: "export"})
				}
				if path := s.exportPath(); fileExists(path) {
					return s.h.askFirst(&confirmState{
						owner:    "streams",
						question: "Replace " + filepath.Base(path) + "?",
						action:   "Replace the file",
						notes:    []string{"The file exists in " + filepath.Dir(path) + "; the export is written over it."},
						command:  s.command(),
						cancel:   "Cancelled. Nothing was written.",
						yes:      run,
					})
				}
				return run()
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
	old := "not set"
	if s.cbSet {
		base, ret := splitReturn(s.cbURL)
		old = base + ", " + dataWords(ret)
	}
	args := s.callbackArgs()
	return s.h.askFirst(&confirmState{
		owner:    "streams",
		question: "Change the callback URL?",
		action:   "Change the callback URL",
		notes: []string{
			"Now: " + old,
			"New: " + s.cbForm.value("url") + ", " + dataWords(s.returnSet()),
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
		action:   "Change its URL",
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

// returnSet are the providers ticked on the Callback page.
func (s *streamsSection) returnSet() []string {
	var ret []string
	for _, p := range providers {
		if s.cbForm.get("return:" + p.name).on {
			ret = append(ret, p.name)
		}
	}
	return ret
}

// splitReturn takes the return= parameter (the data asked for) off a
// callback URL.
func splitReturn(u string) (string, []string) {
	pu, err := neturl.Parse(u)
	if err != nil || pu.RawQuery == "" {
		return u, nil
	}
	q := pu.Query()
	ret := q.Get("return")
	if ret == "" {
		return u, nil
	}
	q.Del("return")
	pu.RawQuery = q.Encode()
	return pu.String(), strings.Split(ret, ",")
}

// dataWords says which providers' data a callback adds.
func dataWords(ret []string) string {
	var names []string
	for _, p := range providers {
		if slices.Contains(ret, p.name) {
			names = append(names, p.label)
		}
	}
	if len(names) == 0 {
		return "with no extra data"
	}
	return "with " + strings.Join(names, ", ") + " data"
}

// fileExists reports whether a file is at path.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// recorderFacts reads whether the recorder runs and where it saves from
// audd streams recorder status.
func recorderFacts(out string) (running, store string) {
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Running":
			running = strings.TrimSpace(v)
		case "Store":
			store = strings.TrimSpace(v)
		}
	}
	return running, store
}

// dataTitle names what History & reports shows: "Plays by artist,
// last 7 days".
func (s *streamsSection) dataTitle() string {
	f := s.dataForm
	t := "Recent plays"
	switch f.value("kind") {
	case "report":
		t = "Plays by " + f.value("by")
	case "export":
		t = "Exported plays"
	}
	switch v := f.value("since"); {
	case v == "" || v == "all":
		t += ", all time"
	case strings.HasSuffix(v, "d"):
		t += ", last " + strings.TrimSuffix(v, "d") + " days"
	case strings.HasSuffix(v, "h"):
		t += ", last " + strings.TrimSuffix(v, "h") + " hours"
	default:
		t += ", since " + v
	}
	if id := f.value("id"); id != "" {
		t += ", stream " + id
	}
	return t
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
	empty := true
	for _, x := range f.visible() {
		if x.isText() && x.value() != "" {
			empty = false
		}
	}
	if !s.h.pageHasKeys() && empty {
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
		intro := styleLines(st.Dim, output.Wrap("The background recorder saves every stream result on this computer, so Recognitions, reports, and Now playing stay complete while audd is closed. It runs in the background; the background recorder setting in Settings starts it with audd.", w))
		var state string
		switch {
		case s.recPanel.running:
			state = st.Dim.Render("Checking…")
		case s.recPanel.res != nil && s.recPanel.res.err != nil:
			state = errorText(st, s.recPanel.res.err, w)
		case s.recPanel.res != nil:
			run, store := recorderFacts(s.recPanel.res.stdout)
			if strings.HasPrefix(run, "yes") {
				state = "The recorder is running"
				if i := strings.Index(run, "pid "); i >= 0 {
					state += " (process " + strings.TrimRight(run[i+4:], ")") + ")"
				}
				state += "."
			} else {
				state = "The recorder is stopped. Press enter to start it."
			}
			if store != "" {
				state += "\n" + st.Dim.Render(output.Wrap("Results are saved in "+store, w))
			}
		}
		return bar + st.Bold.Render("Background recorder") + "\n" + intro + "\n\n" + state
	}
	formView := s.formView(s.dataForm, w)
	if s.dataForm.value("kind") == "export" {
		formView += "\n" + st.Dim.Render("Folder: "+shortPath(exportDirShown(), w-8))
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
			// The folder is shown above: the file name is enough here.
			out += "\n\n" + output.Wrap(fmt.Sprintf("Wrote %s to %s.", output.Plural(n, "play"), filepath.Base(s.exportTo)), w)
		}
	case s.dataPanel.res != nil:
		out += "\n\n" + st.Bold.Render(s.dataTitle()) + "\n" + s.dataPanel.view(w, max(1, h-used-2))
	}
	return out
}

// actions are the buttons of the page: the list's or a stream's, the
// recorder's, an export's.
func (s *streamsSection) actions() []button {
	switch s.page {
	case 0:
		if s.adding {
			return nil
		}
		return s.pane.actions()
	case 2:
		refresh := button{"Refresh", "r", func() tea.Cmd {
			return s.recPanel.start(runReq{args: []string{"streams", "recorder", "status"}, format: "table"})
		}}
		switch s.recRunning {
		case "yes":
			return []button{{"Stop", "", s.askStopRecorder}, refresh}
		case "no":
			return []button{{"Start", "", func() tea.Cmd {
				return s.recPanel.start(runReq{args: []string{"streams", "recorder", "start"}, format: "table"})
			}}, refresh}
		}
		return []button{refresh}
	case 3:
		if s.exportTo != "" && s.dataPanel.res != nil && !s.dataPanel.running {
			path := s.exportTo
			return []button{
				{"Open folder", "o", func() tea.Cmd {
					dir := filepath.Dir(path)
					if err := openURL(dir); err != nil {
						return s.h.setFlash("Could not open the folder: " + err.Error())
					}
					return s.h.setFlash("Opened " + shortPath(dir, 60))
				}},
				{"Copy path", "c", func() tea.Cmd { return s.h.copy(path, "the path "+shortPath(path, 50)) }},
			}
		}
	}
	return nil
}

// askStopRecorder asks before stopping the recorder.
func (s *streamsSection) askStopRecorder() tea.Cmd {
	return s.h.askFirst(&confirmState{
		owner:    "streams",
		question: "Stop the background recorder?",
		notes:    []string{"Stream results stop being saved on this computer until it starts again."},
		command:  "audd streams recorder stop",
		action:   "Stop the recorder",
		cancel:   "The recorder keeps running",
		yes: func() tea.Cmd {
			return s.recPanel.start(runReq{args: []string{"streams", "recorder", "stop"}, format: "table"})
		},
	})
}

// downExits: ↓ at the end of the list goes to the buttons; the forms
// have their own.
func (s *streamsSection) downExits() bool {
	switch s.page {
	case 0:
		return !s.adding && s.pane.downExits()
	case 2:
		return true
	case 3:
		return !s.dataForm.capturing() && s.dataForm.cursor == len(s.dataForm.visible())-1
	}
	return false
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
		return nil
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

func (s *streamsSection) pageEntered() {
	s.add.resume()
	s.cbForm.resume()
	s.dataForm.resume()
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
	switch s.page {
	case 0:
		if s.adding {
			if !s.add.leaveField() {
				s.adding = false
			}
			return true
		}
		return s.pane.back()
	case 1:
		return s.cbForm.leaveField()
	case 3:
		return s.dataForm.leaveField()
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
		// The page of a stream or a job is named for it.
		name := "Details"
		if r := l.selected(); r != nil {
			switch l.kind {
			case "streams":
				name = "Stream " + r.key
			case "jobs":
				name = "Job " + r.key
			}
		}
		out = append(out, name)
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

// shortPath shortens a path to w cells in the middle, keeping the file
// name and its folder: "…/exp/weekly-plays.csv".
func shortPath(p string, w int) string {
	if len([]rune(p)) <= w {
		return p
	}
	sep := string(filepath.Separator)
	parts := strings.Split(p, sep)
	for keep := min(len(parts)-1, 3); keep >= 1; keep-- {
		s := "…" + sep + strings.Join(parts[len(parts)-keep:], sep)
		if len([]rune(s)) <= w || keep == 1 {
			return s
		}
	}
	return p
}

// validSince checks how far back History & reports looks: 24h, 7d, a
// date, or all.
func validSince(v string) string {
	if v == "" || v == "all" {
		return ""
	}
	if n := len(v); n > 1 && strings.ContainsRune("hdw", rune(v[n-1])) {
		if _, err := strconv.Atoi(v[:n-1]); err == nil {
			return ""
		}
	}
	if _, err := time.Parse("2006-01-02", v); err == nil {
		return ""
	}
	return v + " is not a time span: use 24h, 7d, 30d, a date like 2026-10-01, or all"
}
