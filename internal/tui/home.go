package tui

import (
	"context"
	"errors"
	"fmt"
	"github.com/AudDMusic/audd-cli/internal/paths"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// Interactive mode (audd ui, and audd with no arguments on a terminal) is
// one full-screen program: a header, a sidebar of sections, the section
// itself, and a footer with the command the current action runs. Every
// action that maps to a command runs that command in-process (HomeRun),
// so limits, plans, confirmations, and retries follow the command line's
// rules; confirmations come back through RunIO.Ask and are asked in the UI.

// RunIO is what an in-process run of audd reads and writes.
type RunIO struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Ask answers the command's confirmations.
	Ask func(question string) bool
	// Width is the width tables are laid out for.
	Width int
}

// Hooks assigned by internal/cli, which this package cannot import.
var (
	// HomeRun runs audd with args (without the program name) and returns
	// its exit code.
	HomeRun = func(ctx context.Context, args []string, io RunIO) int {
		return output.ExitUnexpected
	}
	// HomeCommands builds a fresh command tree (the palette, Help).
	HomeCommands = func() *cobra.Command { return &cobra.Command{Use: "audd"} }
	// HomeApp builds a set-up App for the given global flags, used after
	// switching profiles.
	HomeApp = func(flags app.GlobalFlags) (*app.App, error) {
		return nil, app.NotImplemented("switching profiles")
	}
)

func init() {
	app.RunHome = RunHome
}

// HomeSections are the section names audd ui --section accepts, in
// sidebar order. It also accepts listen: Recognize with the microphone.
var HomeSections = []string{"recognize", "now-playing", "streams", "recognitions", "account", "settings", "help"}

var homeTitles = []string{"Recognize", "Now playing", "Streams", "Recognitions", "Account", "Settings", "Help"}

// isListen reports whether a section name asks for the microphone.
func isListen(name string) bool { return strings.ToLower(strings.TrimSpace(name)) == "listen" }

func sectionIndex(name string) (int, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return -1, nil
	}
	if isListen(name) {
		name = "recognize"
	}
	for i, s := range HomeSections {
		if s == name || strings.ReplaceAll(s, "-", "") == strings.ReplaceAll(name, "-", "") {
			return i, nil
		}
	}
	return 0, output.Errf(output.ExitUsage, "invalid_argument", "audd ui --section "+strings.Join(HomeSections, "|"),
		"unknown section %q", name)
}

// RunHome opens interactive mode on a section ("" for the start screen).
func RunHome(ctx context.Context, a *app.App, section string) error {
	idx, err := sectionIndex(section)
	if err != nil {
		return err
	}
	opts := a.Out.Options()
	if !(opts.StdoutTTY && opts.StdinTTY) {
		return output.Errf(output.ExitUsage, "no_terminal", "audd --help",
			"interactive mode needs a terminal; run commands directly instead")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newHome(ctx, sessionApp(a), idx)
	if isListen(section) {
		m.useMic()
	}
	popts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx), tea.WithInput(a.In), tea.WithOutput(m.arts.out)}
	if mouseEnabled() {
		popts = append(popts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, popts...)
	_, err = p.Run()
	m.arts.finish()
	cancel()
	m.waitRuns(10 * time.Second)
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// sessionApp is a with notes discarded: nothing may write to the terminal
// under the full-screen program.
func sessionApp(a *app.App) *app.App {
	s := *a
	opts := a.Out.Options()
	// The screen is for people whatever --format or AUDD_FORMAT says; the
	// commands it runs choose their own format.
	opts.Format = output.FormatTable
	s.Out = output.NewPrinter(a.Out.Stdout(), io.Discard, opts)
	return &s
}

// section is one screen of interactive mode.
type section interface {
	title() string
	// init runs when the section is first shown.
	init() tea.Cmd
	// update handles keys (when the section has focus) and its own
	// messages.
	update(msg tea.Msg) tea.Cmd
	view(w, h int) string
	// keys are the hints for the footer and the ? overlay.
	keys() []keyHelp
	// command is the command the current action runs ("" for none).
	command() string
	// capturing reports whether a text field or prompt takes every key.
	capturing() bool
	// back handles esc; false when there was nothing to close.
	back() bool
}

type keyHelp struct{ key, help string }

// subMsg is a message for one section (or the palette, or sign-in):
// commands a section returns are wrapped so their replies come back to
// it alone.
type subMsg struct {
	owner string
	gen   int // the session the section belongs to (see home.gen)
	msg   tea.Msg
}

// wrap tags what cmd returns for owner (in session 0; see home.wrap).
func wrap(owner string, cmd tea.Cmd) tea.Cmd { return wrapGen(owner, 0, cmd) }

func wrapGen(owner string, gen int, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		switch m := msg.(type) {
		case nil:
			return nil
		case tea.BatchMsg:
			out := make(tea.BatchMsg, len(m))
			for i, c := range m {
				out[i] = wrapGen(owner, gen, c)
			}
			return out
		case tea.QuitMsg, artMsg, subMsg, headerMsg, clearHomeFlashMsg, clearHomeOSCMsg:
			// Messages for the home model itself, or already tagged.
			return m
		}
		return subMsg{owner: owner, gen: gen, msg: msg}
	}
}

// wrap tags what cmd returns for owner in the current session.
func (h *home) wrap(owner string, cmd tea.Cmd) tea.Cmd { return wrapGen(owner, h.gen, cmd) }

// focusArea is the level that has the keys: the sidebar (1), a
// section's tab bar (2), or the page under it (3, and anything opened
// from it).
type focusArea int

const (
	focusSidebar focusArea = iota
	focusTabs
	focusContent
)

// headerMsg carries the account line of the header.
type headerMsg struct {
	email, plan string
	planKey     string // the plan's identifier, as list_plans names it
	remaining   int
	known       bool
	err         error
}

type clearHomeFlashMsg struct{ id int }
type clearHomeOSCMsg struct{}

type home struct {
	ctx   context.Context
	a     *app.App
	flags app.GlobalFlags // session flags for every run
	w, h  int
	now   func() time.Time

	order []string // sidebar section ids
	subs  map[string]section
	inits map[string]bool
	cur   int // sidebar index
	focus focusArea
	// signin is shown instead of a section (signed out, or chosen).
	showSignin bool

	// backTo are the screens jumps came from (a Help step, a hint, the
	// palette): esc at the top of a screen returns to the last one, and
	// to the sidebar when there is none.
	backTo []origin
	// headRows are the rows above the section's page (breadcrumb, tab
	// bar) in the last frame.
	headRows int
	// tip is the first-run line that states how to move around.
	tip bool
	// confirmedAt is when a dialog was last answered yes (see confirmed).
	confirmedAt time.Time
	// sideBtn is the button under the menu that has the keys: 1
	// Commands, 2 Keys (0: none, a section has them).
	sideBtn int
	// confirm is a question of the screen's own, before something that
	// spends requests or cannot be undone.
	confirm *confirmState
	// tipKeys counts the keys pressed while the tip shows.
	tipKeys int
	// keysScroll scrolls the keys overlay.
	keysScroll scrollView
	// btnActive: the page's buttons have the keys; btnCur is the
	// focused one.
	btnActive bool
	btnCur    int

	// Mouse: what each region of the last frame does when clicked.
	zoneFns []func() tea.Cmd
	hits    []zoneHit

	palette  *palette
	paletteO bool
	keysO    bool // ? overlay
	ask      *askState
	asks     []askWaiting // confirmations waiting behind ask
	// gen counts sessions: switching profiles builds new sections, and
	// messages for the old ones are dropped.
	gen int

	signedOut bool
	testToken bool
	// recognized and streamAdded mark Getting started steps done.
	recognized, listened, streamAdded bool
	hdr                               headerMsg
	hdrLoaded                         bool

	flash     string
	lastFlash string
	flashN    int
	osc       string

	runs   runRegistry
	arts   *artStore
	st     output.Styles
	color  bool
	nextID int
	last   *run // the run start created most recently
}

func newHome(ctx context.Context, a *app.App, start int) *home {
	h := &home{ctx: ctx, a: a, flags: a.Flags, w: 100, h: 30, now: a.Now,
		inits: map[string]bool{}, arts: setupArt(a, false)}
	if h.now == nil {
		h.now = time.Now
	}
	h.st = a.Out.Styles()
	h.color = !a.Out.Options().NoColor
	h.runs.cancels = map[int]context.CancelFunc{}
	h.runs.spending = map[int]string{}
	h.build()
	h.signedOut = h.noToken()
	h.tip = a.Profile != nil && !a.Profile.UITipSeen
	switch {
	case start >= 0:
		h.cur = start
		h.focus = h.entryFocus()
		h.pageEntered()
	case h.signedOut:
		h.showSignin, h.focus = true, focusContent
	default:
		// The sidebar has the keys, so 1-7, ? and q work right away;
		// enter goes into the section.
		h.cur, h.focus = 0, focusSidebar
	}
	return h
}

// build creates the sections for the current session.
func (h *home) build() {
	h.order = append([]string(nil), HomeSections...)
	h.subs = map[string]section{
		"recognize":    newRecognizeSection(h),
		"now-playing":  newNowPlayingSection(h),
		"streams":      newStreamsSection(h),
		"recognitions": newHistorySection(h),
		"account":      newAccountSection(h),
		"settings":     newSettingsSection(h),
		"help":         newHelpSection(h),
		"signin":       newSigninSection(h),
	}
	h.palette = newPalette(h)
	h.subs["palette"] = h.palette
	h.inits = map[string]bool{}
}

// noToken reports whether no API token is set from any source.
func (h *home) noToken() bool {
	if h.flags.Token != "" {
		return false
	}
	if h.a.Profile == nil || h.a.Secrets == nil {
		return true
	}
	t, _, err := config.ResolveToken(h.flags.Token, h.a.Profile.Name, h.a.Secrets)
	return err == nil && t == ""
}

func (h *home) Init() tea.Cmd {
	return tea.Batch(h.activate(), h.loadHeader())
}

// activeID is the id of the screen in the content pane.
func (h *home) activeID() string {
	if h.showSignin {
		return "signin"
	}
	return h.order[h.cur]
}

func (h *home) active() section { return h.subs[h.activeID()] }

// activate initializes the active section the first time it is shown.
func (h *home) activate() tea.Cmd {
	id := h.activeID()
	if h.inits[id] {
		return nil
	}
	h.inits[id] = true
	return h.wrap(id, h.subs[id].init())
}

// show switches the content pane to a section. listen is Recognize with
// the microphone.
func (h *home) show(id string, focus bool) tea.Cmd {
	mic := id == "listen"
	if mic {
		h.useMic()
		id = "recognize"
	}
	if id == "signin" {
		h.showSignin = true
	} else {
		for i, s := range h.order {
			if s == id {
				h.cur = i
			}
		}
		h.showSignin = false
	}
	if focus {
		h.focus = focusContent
	}
	h.btnActive = false
	cmd := h.activate()
	h.pageEntered()
	if h.focus == focusTabs && h.tabs() == nil {
		h.focus = focusContent
	}
	if s, ok := h.subs["recognize"].(*recognizeSection); ok && mic {
		cmd = tea.Batch(cmd, h.wrap("recognize", s.findDevices()))
	}
	return cmd
}

// origin is where a jump came from: the screen and the level that had
// the keys.
type origin struct {
	id    string
	focus focusArea
}

// navigate opens a section the user chose (sidebar, number keys, F1):
// there is nothing to go back to but the sidebar. The keys go to the
// section's tab bar when it has one, else to its page.
func (h *home) navigate(id string) tea.Cmd {
	h.backTo = nil
	cmd := h.show(id, false)
	h.focus = h.entryFocus()
	h.pageEntered()
	return cmd
}

// entryFocus is where the keys go when a section is opened: its tab
// bar, or its page when it has no tabs.
func (h *home) entryFocus() focusArea {
	if h.tabs() != nil {
		return focusTabs
	}
	return focusContent
}

// jump opens a screen from an action in another (a Help step, a hint,
// the palette); esc at its top comes back here.
func (h *home) jump(id string) tea.Cmd {
	from := h.activeID()
	to := id
	if to == "listen" {
		to = "recognize"
	}
	if h.focus != focusSidebar && from != to {
		h.backTo = append(h.backTo, origin{from, h.focus})
	}
	return h.show(id, true)
}

// goBack leaves the top of a screen: to the screen a jump came from (at
// the level it was left from), or to the sidebar, with its section shown
// and highlighted.
func (h *home) goBack() tea.Cmd {
	for len(h.backTo) > 0 {
		o := h.backTo[len(h.backTo)-1]
		h.backTo = h.backTo[:len(h.backTo)-1]
		if o.id != h.activeID() {
			cmd := h.show(o.id, true)
			if o.focus == focusTabs && h.tabs() != nil {
				h.focus = focusTabs
			}
			return cmd
		}
	}
	h.toSidebar()
	return nil
}

// toSidebar moves the keys to the sidebar. The sign-in screen has no
// sidebar item, so the section of the highlighted item comes back.
func (h *home) toSidebar() {
	h.focus = focusSidebar
	h.showSignin = false
}

// profileName is the session's profile.
func (h *home) profileName() string {
	if h.a.Profile != nil {
		return h.a.Profile.Name
	}
	return "default"
}

// useMic switches Recognize to the microphone.
func (h *home) useMic() {
	if s, ok := h.subs["recognize"].(*recognizeSection); ok {
		s.setSource(true)
	}
}

// send delivers a message to a section and wraps its reply.
func (h *home) send(id string, msg tea.Msg) tea.Cmd {
	s := h.subs[id]
	if s == nil {
		return nil
	}
	if !h.inits[id] && id != "palette" {
		h.inits[id] = true
		return tea.Batch(h.wrap(id, s.init()), h.wrap(id, s.update(msg)))
	}
	return h.wrap(id, s.update(msg))
}

// setFlash says something in the status line; it stays until the next
// key or click.
func (h *home) setFlash(s string) tea.Cmd {
	h.flash = s
	h.flashN++
	return nil
}

// noBrowser is said when a link could not be opened: it is copied instead.
const noBrowser = "Couldn't open a browser here. The link is copied — paste it into a browser."

// openLink opens a link in the browser; when that fails, it copies the
// link and says so.
func (h *home) openLink(link string) tea.Cmd {
	if err := openURL(link); err != nil {
		h.osc = oscCopy(link)
		return tea.Batch(h.setFlash(noBrowser),
			tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return clearHomeOSCMsg{} }))
	}
	return h.setFlash("Opened in your browser: " + link)
}

// leaver is a section that forgets something when you leave it (its
// undo).
type leaver interface{ left() }

// leftSection tells the section id that it was left, when it was.
func (h *home) leftSection(id string) {
	if id == h.activeID() {
		return
	}
	if l, ok := h.subs[id].(leaver); ok {
		l.left()
	}
}

// copy puts s on the clipboard with the next frame.
func (h *home) copy(s, what string) tea.Cmd {
	h.osc = oscCopy(s)
	// One line: a long command is shortened in the middle.
	msg := "Copied " + what
	if len([]rune(msg)) > h.w {
		msg = truncMiddle(msg, max(20, h.w))
	}
	return tea.Batch(h.setFlash(msg),
		tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return clearHomeOSCMsg{} }))
}

// takeOSC moves a clipboard sequence a sub-model prepared to the screen.
func (h *home) takeOSC(s string) tea.Cmd {
	if s == "" {
		return nil
	}
	h.osc = s
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return clearHomeOSCMsg{} })
}

func (h *home) Update(msg tea.Msg) (m tea.Model, cmd tea.Cmd) {
	// A bug never closes the window silently: it is said and logged, and
	// the session goes on.
	defer func() {
		if r := recover(); r != nil {
			logPanic(r)
			h.flash = fmt.Sprintf("Something went wrong: %v (your data is safe; details in %s)", r, panicLog())
			m, cmd = h, nil
		}
	}()
	return h.update(msg)
}

// panicLog is the file where panics of interactive mode are written.
func panicLog() string { return filepath.Join(paths.DataDir(), "ui-errors.log") }

func logPanic(r any) {
	_ = os.MkdirAll(filepath.Dir(panicLog()), 0o755)
	f, err := os.OpenFile(panicLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s panic: %v\n%s\n", time.Now().Format(time.RFC3339), r, debug.Stack())
}

func (h *home) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h.w, h.h = msg.Width, msg.Height
		h.arts.resized()
		return h, nil
	case artMsg:
		h.arts.handle(msg)
		return h, nil
	case headerMsg:
		h.hdr, h.hdrLoaded = msg, true
		return h, nil
	case clearHomeFlashMsg:
		if msg.id == h.flashN {
			h.flash = ""
		}
		return h, nil
	case clearHomeOSCMsg:
		h.osc = ""
		return h, nil
	case subMsg:
		return h, h.route(msg)
	case tea.KeyMsg:
		before := h.activeID()
		cmd := h.key(msg)
		h.leftSection(before)
		return h, cmd
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && (msg.Button == tea.MouseButtonLeft) {
			h.flash = ""
		}
		before := h.activeID()
		cmd := h.mouse(msg)
		h.leftSection(before)
		return h, cmd
	}
	return h, nil
}

// route handles a section's message: run events are continued here, and
// questions from runs open the confirmation.
func (h *home) route(m subMsg) tea.Cmd {
	cont := func(r *run) tea.Cmd { return wrapGen(m.owner, m.gen, h.runs.wait(r)) }
	if m.gen != h.gen {
		// A section of an earlier session: keep its runs draining (they
		// were cancelled), answer its questions no, and drop the rest.
		switch ev := m.msg.(type) {
		case runAskMsg:
			ev.reply <- false
			return cont(ev.run)
		case runLineMsg:
			return cont(ev.run)
		case runDoneMsg:
			h.runs.done(ev.run.id)
		}
		return nil
	}
	switch ev := m.msg.(type) {
	case runAskMsg:
		if h.ask != nil {
			// One question at a time; this one waits its turn.
			h.asks = append(h.asks, askWaiting{owner: m.owner, msg: ev})
		} else {
			h.ask = newAskState(m.owner, ev, h)
		}
		return cont(ev.run)
	case runLineMsg:
		return tea.Batch(cont(ev.run), h.send(m.owner, ev))
	case runDoneMsg:
		h.runs.done(ev.run.id)
		cmds := []tea.Cmd{h.send(m.owner, ev)}
		if ev.run.req.spends {
			cmds = append(cmds, h.loadHeader())
		}
		return tea.Batch(cmds...)
	}
	return h.send(m.owner, m.msg)
}

// command is the command for the footer.
func (h *home) command() string {
	return h.withSession(h.rawCommand())
}

func (h *home) rawCommand() string {
	if h.confirm != nil {
		return h.confirm.command
	}
	if h.paletteO {
		return h.palette.command()
	}
	return h.active().command()
}

// withSession adds what the session adds to every run (the test token),
// so the command shown and copied is the one that runs.
func (h *home) withSession(c string) string {
	if !h.testToken || !strings.HasPrefix(c, "audd ") || strings.Contains(c, "--token") {
		return c
	}
	for _, p := range []string{"audd login", "audd logout", "audd auth", "audd config", "audd token", "audd ui"} {
		if strings.HasPrefix(c, p) {
			return c // these do not use the token they would run with
		}
	}
	if i := strings.Index(c, " > "); i >= 0 {
		return c[:i] + " --token test" + c[i:]
	}
	return c + " --token test"
}

// openPalette opens the command palette, on a command's form when path
// is set ("streams add").
func (h *home) openPalette(path string) tea.Cmd {
	h.paletteO = true
	return h.send("palette", paletteOpenMsg{path: path})
}

// loadHeader reads the account line (email, plan, requests left).
func (h *home) loadHeader() tea.Cmd {
	if h.signedOut || h.testToken {
		return nil
	}
	a, ctx := h.a, h.ctx
	return func() tea.Msg {
		b, err := a.Account()
		if err != nil {
			return headerMsg{err: err}
		}
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var m headerMsg
		if p, err := b.Profile(c); err == nil {
			m.email = p.Email
		} else {
			return headerMsg{err: err}
		}
		if s, err := b.Status(c); err == nil && s.Plan != nil {
			m.plan, m.planKey = s.Plan.Name, s.Plan.Key
			if m.plan == "" {
				m.plan = s.Plan.Key
			}
		}
		if u, err := b.Usage(c, 1); err == nil && u.RemainingKnown {
			m.remaining, m.known = u.Remaining, true
		}
		return m
	}
}

// signedIn is called after a token was set or a sign-in finished.
func (h *home) signedIn(testToken bool) tea.Cmd {
	h.useToken(testToken)
	h.showSignin = false
	h.focus = focusContent
	h.backTo = nil
	return tea.Batch(h.loadHeader(), h.show("recognize", true))
}

// useToken makes runs use the public test token, or the profile's own
// token (saved, from audd login, or from the environment). It is called
// on every change of token: the token that runs is the one the header
// shows.
func (h *home) useToken(test bool) {
	h.testToken = test
	h.flags.Token = ""
	if test {
		h.flags.Token = "test"
	}
	if a, err := HomeApp(h.flags); err == nil {
		h.a = sessionApp(a)
	}
	h.signedOut = h.noToken()
	h.hdr, h.hdrLoaded = headerMsg{}, false
	// A result from the other token is not shown as if it was this one's.
	if r, ok := h.subs["recognize"].(*recognizeSection); ok && r.phase == "result" {
		r.phase, r.plan = "form", ""
	}
	h.reloadData()
}

// reloadData starts the sections that show account data afresh (with the
// new token): nothing they loaded with the old one stays shown. The
// section in view keeps its page (Account reloads it itself); the
// others load again when opened.
func (h *home) reloadData() {
	fresh := map[string]func(*home) section{
		"now-playing":  newNowPlayingSection,
		"streams":      newStreamsSection,
		"recognitions": newHistorySection,
		"account":      newAccountSection,
	}
	for id, mk := range fresh {
		if !h.inits[id] || id == h.activeID() {
			continue
		}
		h.subs[id] = mk(h)
		delete(h.inits, id)
	}
}

// switchProfile rebuilds the session for another profile.
func (h *home) switchProfile(name string) tea.Cmd {
	flags := h.flags
	flags.Profile = name
	a, err := HomeApp(flags)
	if err != nil {
		return h.setFlash("Could not switch: " + output.AsError(err).Message)
	}
	// The test token was for this session's profile: the new one runs
	// with its own token.
	flags.Token = ""
	h.flags = flags
	h.a = sessionApp(a)
	h.testToken = false
	h.signedOut = h.noToken()
	h.hdr, h.hdrLoaded = headerMsg{}, false
	// The old sections' runs stop and their messages are dropped.
	h.runs.mu.Lock()
	for _, c := range h.runs.cancels {
		c()
	}
	h.runs.mu.Unlock()
	h.gen++
	h.backTo = nil
	h.build()
	return tea.Batch(h.setFlash("Switched to profile "+name), h.loadHeader(), h.show("account", true))
}

// runRegistry tracks runs in flight, so quitting can wait for them.
type runRegistry struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	cancels map[int]context.CancelFunc
	// spending are the runs that may spend requests, by id: their
	// commands, for the question before quitting.
	spending map[int]string
}

func (h *home) waitRuns(d time.Duration) {
	done := make(chan struct{})
	go func() { h.runs.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// leftExiter is a section that uses the left key in some states (a text
// cursor, the stations grid) and says when it does not, so left goes
// back one level, as esc does.
type leftExiter interface {
	leftExits() bool
}

// leftExits reports whether left goes back one level in s. Sections that
// don't say keep left only while they are capturing keys.
func leftExits(s section) bool {
	if le, ok := s.(leftExiter); ok {
		return le.leftExits()
	}
	return !s.capturing()
}

// noTokenChoices is the one wording of the ways to get a token;
// noTokenChoicesReal leaves out the test token (streams need your own).
const (
	noTokenChoices     = "Use the public test token (10 requests a day, nothing is saved), paste yours from dashboard.audd.io, or sign in."
	noTokenChoicesReal = "Streams need your own token (the public test token does not work for them): paste yours from dashboard.audd.io, or sign in."
)

// tokenWays are the ways to get an API token: label and action. With
// test, the public test token is the first; next runs once it is in use
// (the page stays where it is).
func (h *home) tokenWays(test bool, next func() tea.Cmd) []button {
	var b []button
	if test {
		b = append(b, button{label: "Use the test token", do: func() tea.Cmd {
			h.useToken(true)
			return tea.Batch(h.loadHeader(), h.setFlash("Using the test token: 10 requests a day"), next())
		}})
	}
	return append(b,
		button{label: "Paste a token…", do: func() tea.Cmd {
			cmd := h.jump("signin")
			if si, ok := h.subs["signin"].(*signinSection); ok {
				si.step = "paste"
				si.paste.focusName("token")
				si.paste.resume()
			}
			return cmd
		}},
		button{label: "Sign in", do: func() tea.Cmd {
			cmd := h.jump("signin")
			if si, ok := h.subs["signin"].(*signinSection); ok {
				si.choice = 0
				return tea.Batch(cmd, h.wrap("signin", si.update(tea.KeyMsg{Type: tea.KeyEnter})))
			}
			return cmd
		}},
	)
}

// askToken asks how to get an API token (why says what needs it).
func (h *home) askToken(owner, why string, test bool, next func() tea.Cmd) tea.Cmd {
	ways := h.tokenWays(test, next)
	var choices []string
	for _, w := range ways {
		choices = append(choices, w.label)
	}
	notes := noTokenChoices
	if !test {
		notes = noTokenChoicesReal
	}
	return h.askFirst(&confirmState{
		owner:    owner,
		question: "You need an API token",
		notes:    []string{why + " " + notes},
		choices:  choices,
		topic:    "API token",
		cancel:   "Cancelled. Nothing was sent.",
		pick:     func(i int) tea.Cmd { return ways[i].do() },
	})
}

// tokenButtons are the ways to get an API token, as a page's buttons.
func (h *home) tokenButtons(test bool, next func() tea.Cmd) []button {
	return h.tokenWays(test, next)
}
