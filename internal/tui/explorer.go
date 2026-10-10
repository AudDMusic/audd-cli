package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/streams"
)

func init() {
	app.RunExplorer = RunExplorer
}

type tabID int

const (
	tabRecent tabID = iota
	tabJobs
	tabStreams
	tabUsage
)

var tabNames = []string{"recent", "jobs", "streams", "usage"}
var tabTitles = []string{"Recent", "Jobs", "Streams", "Usage"}

// Tabs lists the explorer tab names accepted by RunExplorer.
func Tabs() []string { return append([]string(nil), tabNames...) }

// parseTab accepts "recent", "jobs", "streams", "usage", and "jobs/<id>"
// (or "jobs:<id>") to open a job's items.
func parseTab(s string) (tabID, string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return tabRecent, "", nil
	}
	name, arg := s, ""
	if i := strings.IndexAny(s, "/:"); i >= 0 {
		name, arg = s[:i], s[i+1:]
	}
	for i, n := range tabNames {
		if n != name {
			continue
		}
		switch {
		case arg == "" || tabID(i) == tabJobs:
			return tabID(i), arg, nil
		case tabID(i) == tabStreams:
			if _, err := parseStreamIDs(arg); err == nil {
				return tabStreams, arg, nil
			}
		}
	}
	return 0, "", output.Errf(output.ExitUsage, "invalid_argument", "audd browse --tab recent|jobs|streams|usage", "unknown tab %q", s)
}

// parseStreamIDs reads the radio IDs of "streams/<id>,<id>", which limits
// the Streams tab to those streams (audd streams watch 1 2).
func parseStreamIDs(arg string) ([]int, error) {
	var ids []int
	for _, part := range strings.Split(arg, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a radio ID", part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// explorerExportDir is where e writes exports (the working directory).
var explorerExportDir = "."

// RunExplorer opens the interactive explorer on a tab: recent, jobs,
// streams, or usage ("jobs/<id>" opens one job; "streams/<id>,<id>" shows
// only those streams). Without a terminal it
// prints the tab's data as JSON instead.
func RunExplorer(ctx context.Context, a *app.App, tab string) error {
	t, arg, err := parseTab(tab)
	if err != nil {
		return err
	}
	data, err := NewExplorerData(a)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts := a.Out.Options()
	if t == tabStreams {
		ensureRecorder(a)
	}
	if !(a.Out.IsHuman() && opts.StdoutTTY && opts.StdinTTY) {
		return printTabData(ctx, a, data, t, arg)
	}
	m := newExplorer(ctx, a, data, t, arg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithInput(a.In), tea.WithOutput(m.arts.out))
	final, err := p.Run()
	m.arts.finish()
	if err != nil {
		return err
	}
	if fm, ok := final.(*explorer); ok && fm.after != nil {
		return fm.after()
	}
	return nil
}

// printTabData is the non-interactive form of a tab.
func printTabData(ctx context.Context, a *app.App, d ExplorerData, t tabID, arg string) error {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	// CSV gets flat rows, one per song, instead of the JSON document.
	csv := a.Out.Format() == output.FormatCSV
	switch t {
	case tabRecent:
		if d.Recent == nil {
			return app.NotImplemented("the results history")
		}
		items, err := d.Recent(ctx, 200)
		if err != nil {
			return err
		}
		if csv {
			return a.Out.Result(recentCSVRows(items), nil)
		}
		return a.Out.Result(map[string]any{"recent": nonNil(items)}, nil)
	case tabJobs:
		if arg != "" {
			if d.JobItems == nil {
				return app.NotImplemented("jobs")
			}
			items, err := d.JobItems(ctx, arg)
			if err != nil {
				return err
			}
			if csv {
				return a.Out.Result(jobItemCSVRows(arg, items), nil)
			}
			return a.Out.Result(map[string]any{"job": arg, "items": nonNil(items)}, nil)
		}
		if d.Jobs == nil {
			return app.NotImplemented("jobs")
		}
		jobs, err := d.Jobs(ctx)
		if err != nil {
			return err
		}
		if csv {
			return a.Out.Result(jobCSVRows(jobs), nil)
		}
		return a.Out.Result(map[string]any{"jobs": nonNil(jobs)}, nil)
	case tabStreams:
		if d.Feed == nil {
			return app.NotImplemented("stream data")
		}
		ids, _ := parseStreamIDs(arg)
		stations, err := selectStations(ctx, d.Feed, ids)
		if err != nil {
			return err
		}
		data := loadAll(ctx, d.Feed, stations, 1)
		if csv {
			return a.Out.Result(stationRows(data, now()), nil)
		}
		return a.Out.Result(onceDoc(data, now()), nil)
	default:
		if d.Usage == nil {
			return app.NotImplemented("usage")
		}
		u, err := d.Usage(ctx, 30)
		if err != nil {
			return err
		}
		if csv {
			// The same columns as the explorer's usage export.
			type dayCSV struct {
				Date     string `json:"date"`
				Requests int    `json:"requests"`
			}
			days := make([]dayCSV, 0, len(u.Days))
			for _, d := range u.Days {
				days = append(days, dayCSV{d.Date, d.Requests})
			}
			return a.Out.Result(days, nil)
		}
		if u.Raw != nil {
			return a.Out.Result(u.Raw, nil)
		}
		return a.Out.Result(u, nil)
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// level is one list screen: a tab's root list or a drill-down.
type level struct {
	kind    string // recent, jobs, items, streams, plays
	arg     string // job ID or radio ID
	title   string
	cols    []column
	rows    []row
	cursor  int
	offset  int
	filter  string
	detail  bool
	dscroll int
	loading bool
	err     error
}

func (l *level) visible() []row {
	if l.filter == "" {
		return l.rows
	}
	f := strings.ToLower(l.filter)
	var out []row
	for _, r := range l.rows {
		if strings.Contains(r.search, f) {
			out = append(out, r)
		}
	}
	return out
}

func (l *level) selected() *row {
	v := l.visible()
	if l.cursor < 0 || l.cursor >= len(v) {
		return nil
	}
	return &v[l.cursor]
}

// setRows replaces the rows and keeps the cursor on the same key.
func (l *level) setRows(rows []row) {
	var key string
	if r := l.selected(); r != nil {
		key = r.key
	}
	l.rows = rows
	l.loading = false
	l.err = nil
	v := l.visible()
	l.cursor = 0
	for i, r := range v {
		if r.key == key {
			l.cursor = i
			break
		}
	}
	if l.cursor >= len(v) {
		l.cursor = len(v) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
}

type inputPurpose int

const (
	inputNone inputPurpose = iota
	inputFilter
	inputAddURL
	inputAddID
	inputSetURL
)

// Explorer messages.
type (
	rowsMsg struct {
		tab  tabID
		kind string
		arg  string
		rows []row
		err  error
	}
	usageMsg struct {
		u   *account.Usage
		err error
	}
	streamsTickMsg struct{}
	// actionMsg is the reply to a stream action. reload names the tab
	// whose top list to reload, which may no longer be the open tab.
	actionMsg struct {
		flash  string
		reload bool
		tab    tabID
	}
)

type explorer struct {
	ctx  context.Context
	a    *app.App
	d    ExplorerData
	tab  tabID
	tabs [4][]*level
	w, h int
	now  func() time.Time

	usage     *account.Usage
	usageErr  error
	usageLoad bool

	input    textinput.Model
	purpose  inputPurpose
	pending  map[string]string // values collected across prompts
	confirm  string            // question waiting for y/n
	onYes    func() tea.Cmd
	exportAs bool // waiting for c/j after e
	help     bool
	flash    string
	flashN   int
	osc      string
	after    func() error // runs after the program exits (job resume)

	arts  *artStore
	st    output.Styles
	r     *lipgloss.Renderer
	color bool

	// embedded is set when the explorer is part of interactive mode: it
	// shows only the allowed tabs, never quits, and leaves cover art,
	// the clipboard, and help to the screen around it.
	embedded bool
	allowed  []tabID
	// onResume, when set, resumes a job (r, R) instead of quitting.
	onResume func(id string, retry bool) tea.Cmd
}

func newExplorer(ctx context.Context, a *app.App, d ExplorerData, t tabID, arg string) *explorer {
	return newExplorerArts(ctx, a, d, t, arg, setupArt(a, false))
}

// newEmbeddedExplorer is an explorer showing only tabs, for interactive
// mode. arts is the art store of the screen it is part of.
func newEmbeddedExplorer(ctx context.Context, a *app.App, d ExplorerData, tabs []tabID, arts *artStore) *explorer {
	m := newExplorerArts(ctx, a, d, tabs[0], "", arts)
	m.embedded = true
	m.allowed = append([]tabID(nil), tabs...)
	return m
}

// capturing reports whether a prompt is open, so every key goes to it.
func (m *explorer) capturing() bool {
	return m.purpose != inputNone || m.confirm != "" || m.exportAs
}

// takeOSC returns and clears the clipboard sequence waiting to be sent.
func (m *explorer) takeOSC() string {
	s := m.osc
	m.osc = ""
	return s
}

// nextTab is the tab d steps away among the tabs shown.
func (m *explorer) nextTab(d int) tabID {
	tabs := m.allowed
	if len(tabs) == 0 {
		tabs = []tabID{tabRecent, tabJobs, tabStreams, tabUsage}
	}
	i := 0
	for j, t := range tabs {
		if t == m.tab {
			i = j
		}
	}
	return tabs[(i+d+len(tabs))%len(tabs)]
}

func newExplorerArts(ctx context.Context, a *app.App, d ExplorerData, t tabID, arg string, arts *artStore) *explorer {
	st := a.Out.Styles()
	now := a.Now
	if now == nil {
		now = time.Now
	}
	ti := textinput.New()
	ti.Prompt = ""
	ti.Cursor.SetMode(cursor.CursorStatic)
	m := &explorer{ctx: ctx, a: a, d: d, tab: t, w: 100, h: 30, now: now, input: ti,
		st: st, r: st.Renderer, color: !a.Out.Options().NoColor, arts: arts}
	m.tabs[tabRecent] = []*level{{kind: "recent", title: "Recognized with audd", cols: recentCols, loading: true}}
	m.tabs[tabJobs] = []*level{{kind: "jobs", title: "Jobs", cols: jobCols, loading: true}}
	m.tabs[tabStreams] = []*level{{kind: "streams", title: "Streams", cols: streamCols, loading: true}}
	if t == tabStreams && arg != "" {
		l := m.tabs[tabStreams][0]
		l.arg = arg
		ids, _ := parseStreamIDs(arg)
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		if len(ids) == 1 {
			l.title = "Stream " + parts[0]
		} else if len(ids) > 1 {
			l.title = "Streams " + strings.Join(parts, ", ")
		}
	}
	m.tabs[tabUsage] = nil
	if t == tabJobs && arg != "" {
		m.tabs[tabJobs] = append(m.tabs[tabJobs], &level{kind: "items", arg: arg, title: "Job " + arg, cols: jobItemCols, loading: true})
	}
	return m
}

func (m *explorer) Init() tea.Cmd {
	cmds := []tea.Cmd{m.load(m.tab, m.top())}
	if m.tab == tabJobs && len(m.tabs[tabJobs]) > 1 {
		cmds = append(cmds, m.load(tabJobs, m.tabs[tabJobs][0]))
	}
	cmds = append(cmds, tea.Tick(5*time.Second, func(time.Time) tea.Msg { return streamsTickMsg{} }))
	return tea.Batch(cmds...)
}

func (m *explorer) top() *level {
	s := m.tabs[m.tab]
	if len(s) == 0 {
		return nil
	}
	return s[len(s)-1]
}

// load fetches a level's rows (or the usage view) in the background.
func (m *explorer) load(t tabID, l *level) tea.Cmd {
	ctx, d, now := m.ctx, m.d, m.now
	if t == tabUsage {
		if d.Usage == nil {
			m.usageErr = app.NotImplemented("usage")
			return nil
		}
		m.usageLoad = true
		return func() tea.Msg {
			u, err := d.Usage(ctx, 30)
			return usageMsg{u: u, err: err}
		}
	}
	if l == nil {
		return nil
	}
	kind, arg := l.kind, l.arg
	return func() tea.Msg {
		msg := rowsMsg{tab: t, kind: kind, arg: arg}
		switch kind {
		case "recent":
			if d.Recent == nil {
				msg.err = errors.New("the results history is not available in this build")
				break
			}
			items, err := d.Recent(ctx, 500)
			msg.rows, msg.err = recentRows(items), err
		case "jobs":
			if d.Jobs == nil {
				msg.err = errors.New("jobs are not available in this build")
				break
			}
			jobs, err := d.Jobs(ctx)
			msg.rows, msg.err = jobRows(jobs), err
		case "items":
			if d.JobItems == nil {
				msg.err = errors.New("jobs are not available in this build")
				break
			}
			items, err := d.JobItems(ctx, arg)
			msg.rows, msg.err = jobItemRows(items), err
		case "streams":
			if d.Feed == nil {
				msg.err = errors.New("stream data is not available in this build")
				break
			}
			ids, _ := parseStreamIDs(arg)
			stations, err := selectStations(ctx, d.Feed, ids)
			if err != nil {
				msg.err = err
				break
			}
			msg.rows = streamRows(loadAll(ctx, d.Feed, stations, 1), now())
		case "plays":
			if d.Feed == nil {
				msg.err = errors.New("stream data is not available in this build")
				break
			}
			id, _ := strconv.Atoi(arg)
			plays, err := d.Feed.Plays(ctx, id, historyLimit)
			msg.rows, msg.err = playRows(plays), err
		}
		return msg
	}
}

func (m *explorer) find(t tabID, kind, arg string) *level {
	for _, l := range m.tabs[t] {
		if l.kind == kind && l.arg == arg {
			return l
		}
	}
	return nil
}

func (m *explorer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.arts.resized()
		return m, nil
	case artMsg:
		m.arts.handle(msg)
		return m, nil
	case rowsMsg:
		if l := m.find(msg.tab, msg.kind, msg.arg); l != nil {
			if msg.err != nil {
				l.loading, l.err = false, msg.err
			} else {
				l.setRows(msg.rows)
			}
		}
		return m, nil
	case usageMsg:
		m.usageLoad = false
		m.usage, m.usageErr = msg.u, msg.err
		return m, nil
	case streamsTickMsg:
		var cmds []tea.Cmd
		if m.tab == tabStreams {
			for _, l := range m.tabs[tabStreams] {
				cmds = append(cmds, m.load(tabStreams, l))
			}
		}
		cmds = append(cmds, tea.Tick(5*time.Second, func(time.Time) tea.Msg { return streamsTickMsg{} }))
		return m, tea.Batch(cmds...)
	case actionMsg:
		var cmds []tea.Cmd
		if msg.flash != "" {
			cmds = append(cmds, m.setFlash(msg.flash))
		}
		if msg.reload && len(m.tabs[msg.tab]) > 0 {
			cmds = append(cmds, m.load(msg.tab, m.tabs[msg.tab][0]))
		}
		return m, tea.Batch(cmds...)
	case clearCopyMsg:
		m.osc = ""
		return m, nil
	case clearFlashMsg:
		if msg.id == m.flashN {
			m.flash = ""
		}
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *explorer) setFlash(s string) tea.Cmd {
	m.flash = s
	m.flashN++
	id := m.flashN
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return clearFlashMsg{id: id} })
}

func (m *explorer) switchTab(t tabID) tea.Cmd {
	if t == m.tab {
		return nil
	}
	m.tab = t
	if t == tabUsage {
		if m.usage == nil && !m.usageLoad {
			return m.load(tabUsage, nil)
		}
		return nil
	}
	if t == tabStreams {
		if note, err := streams.EnsureRecorder(m.a); err == nil && note != "" {
			m.flash = note
		}
	}
	l := m.tabs[t][0]
	if l.loading || len(m.tabs[t]) == 1 {
		return m.load(t, m.top())
	}
	return nil
}
