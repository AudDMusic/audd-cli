package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// Messages of the now-playing screen.
type (
	// npPollMsg is a poll reply and npPollNowMsg the timer for the next
	// poll. Both carry the poll generation: only the newest poll is
	// applied and re-arms the timer, so there is one poll loop however
	// often r is pressed.
	npPollMsg struct {
		gen      int
		data     []stationData
		stations []Station // non-nil when the station list was re-read
	}
	npPollNowMsg  struct{ gen int }
	npTickMsg     time.Time
	clearCopyMsg  struct{}
	clearFlashMsg struct{ id int }
)

type npModel struct {
	ctx      context.Context
	feed     Feed
	radioIDs []int // the IDs asked for; nil follows all of the account's streams
	stations []stationData
	sel      int
	zoom     bool // single card while several stations are shown
	history  bool
	help     bool
	w, h     int
	now      func() time.Time
	interval time.Duration
	notify   bool
	loaded   bool
	lastKeys map[int]string
	lastList time.Time
	pollGen  int // generation of the newest poll

	arts   *artStore
	st     output.Styles
	r      *lipgloss.Renderer
	color  bool
	flash  string
	flashN int
	osc    string // clipboard sequence to emit with the next frame

	// embedded: part of interactive mode (see explorer.embedded).
	embedded bool
}

// newEmbeddedNowPlaying is the now-playing screen for interactive mode,
// drawing covers with the screen's art store.
func newEmbeddedNowPlaying(ctx context.Context, a *app.App, feed Feed, stations []Station, opts NowPlayingOptions, arts *artStore) *npModel {
	m := newNowPlayingModelArts(ctx, a, feed, stations, nil, opts, arts)
	m.embedded = true
	return m
}

// takeOSC returns and clears the clipboard sequence waiting to be sent.
func (m *npModel) takeOSC() string {
	s := m.osc
	m.osc = ""
	return s
}

func newNowPlayingModel(ctx context.Context, a *app.App, feed Feed, stations []Station, radioIDs []int, opts NowPlayingOptions) *npModel {
	return newNowPlayingModelArts(ctx, a, feed, stations, radioIDs, opts, setupArt(a, opts.NoArt))
}

func newNowPlayingModelArts(ctx context.Context, a *app.App, feed Feed, stations []Station, radioIDs []int, opts NowPlayingOptions, arts *artStore) *npModel {
	st := a.Out.Styles()
	now := a.Now
	if now == nil {
		now = time.Now
	}
	m := &npModel{
		ctx: ctx, feed: feed, radioIDs: radioIDs, now: now, interval: opts.Interval, notify: opts.Notify,
		lastKeys: map[int]string{}, lastList: now(),
		arts: arts,
		st:   st, r: st.Renderer, color: !a.Out.Options().NoColor,
		w: 100, h: 30,
	}
	if m.interval <= 0 {
		m.interval = 5 * time.Second
	}
	for _, s := range stations {
		m.stations = append(m.stations, stationData{Station: s})
	}
	return m
}

func (m *npModel) Init() tea.Cmd {
	return tea.Batch(m.poll(false), tickEverySecond())
}

func tickEverySecond() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return npTickMsg(t) })
}

// poll starts a poll that supersedes any poll in flight or scheduled.
func (m *npModel) poll(refreshStations bool) tea.Cmd {
	m.pollGen++
	gen := m.pollGen
	if refreshStations {
		m.lastList = m.now()
	}
	ctx, feed, want := m.ctx, m.feed, m.radioIDs
	cur := m.stationList()
	return func() tea.Msg {
		var fresh []Station
		if refreshStations {
			if list, err := selectStations(ctx, feed, want); err == nil {
				fresh, cur = list, list
			}
		}
		return npPollMsg{gen: gen, data: loadAll(ctx, feed, cur, historyLimit), stations: fresh}
	}
}

func (m *npModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.arts.resized()
		return m, nil
	case npTickMsg:
		return m, tickEverySecond()
	case npPollNowMsg:
		if msg.gen != m.pollGen {
			return m, nil // armed by a poll that r replaced
		}
		return m, m.poll(m.now().Sub(m.lastList) >= stationsRefresh)
	case npPollMsg:
		if msg.gen != m.pollGen {
			return m, nil // replaced by a newer poll; drop out-of-order data
		}
		return m, m.applyPoll(msg)
	case artMsg:
		m.arts.handle(msg)
		return m, nil
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

// stationList is the stations the screen shows, in order.
func (m *npModel) stationList() []Station {
	out := make([]Station, len(m.stations))
	for i, s := range m.stations {
		out[i] = s.Station
	}
	return out
}

// setStations replaces the station list with a re-read one, keeping the
// plays already shown for stations that remain.
func (m *npModel) setStations(list []Station) {
	old := map[int]stationData{}
	for _, d := range m.stations {
		old[d.RadioID] = d
	}
	next := make([]stationData, len(list))
	for i, s := range list {
		d := old[s.RadioID]
		d.Station = s
		next[i] = d
	}
	m.stations = next
	m.sel = max(0, min(m.sel, len(m.stations)-1))
}

func (m *npModel) applyPoll(msg npPollMsg) tea.Cmd {
	var cmds []tea.Cmd
	if msg.stations != nil {
		m.setStations(msg.stations)
	}
	for _, d := range msg.data {
		for i := range m.stations {
			if m.stations[i].RadioID != d.RadioID {
				continue
			}
			if d.Err != nil && len(d.Plays) == 0 {
				d.Plays = m.stations[i].Plays // keep what we had
			}
			m.stations[i] = d
		}
		if len(d.Plays) > 0 {
			p := d.Plays[0]
			if k := p.key(); k != m.lastKeys[d.RadioID] {
				if m.loaded && m.notify {
					title, body := songLine(p.ResultView), fmt.Sprintf("Stream %d", d.RadioID)
					cmds = append(cmds, func() tea.Msg { _ = notify(title, body); return nil })
				}
				m.lastKeys[d.RadioID] = k
			}
			cmds = append(cmds, m.arts.want(m.ctx, p.ResultView))
		}
	}
	m.loaded = true
	gen := m.pollGen
	cmds = append(cmds, tea.Tick(m.interval, func(time.Time) tea.Msg { return npPollNowMsg{gen: gen} }))
	return tea.Batch(cmds...)
}
