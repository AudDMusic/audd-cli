package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// runReq is one in-process run of audd.
type runReq struct {
	args  []string // after "audd", without the session's global flags
	stdin string
	// format is the output format: "json" (the default), "jsonl", or
	// "table" (the command's own text, laid out for the content width).
	format string
	// typed, when set, makes the confirmation a word to type.
	typed string
	// stream delivers stdout lines as they arrive (runLineMsg).
	stream bool
	// spends marks runs that may use requests: the header is refreshed
	// after them.
	spends bool
	// tag is for the section's own bookkeeping.
	tag string
}

// run is a run in flight.
type run struct {
	id     int
	req    runReq
	events chan tea.Msg
}

// Run events. Each carries its run so the home model can keep reading.
type (
	runLineMsg struct {
		run  *run
		line string
		// pending is a login_pending record (a sign-in to approve), from
		// stdout or stderr.
		pending map[string]any
	}
	runAskMsg struct {
		run      *run
		question string
		notes    []string // the run's notes so far, such as the plan
		reply    chan bool
	}
	runDoneMsg struct {
		run *run
		res runResult
	}
)

// runResult is what a finished run printed.
type runResult struct {
	code           int
	stdout, stderr string
	err            *runError
	notes          []string // stderr lines other than the error
}

type runError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func (r runResult) ok() bool { return r.code == 0 }

// sessionArgs are the global flags of the session, added to every run.
func (h *home) sessionArgs() []string {
	var out []string
	if h.flags.Profile != "" {
		out = append(out, "--profile", h.flags.Profile)
	}
	if h.flags.Token != "" {
		out = append(out, "--token", h.flags.Token)
	}
	if h.flags.Debug {
		out = append(out, "--debug")
	}
	if h.flags.MaxRequests > 0 && !h.flags.MaxRequestsFromConfig {
		out = append(out, "--max-requests", fmt.Sprint(h.flags.MaxRequests))
	}
	return out
}

// contentWidth is the width of the content pane.
func (h *home) contentWidth() int {
	w, _ := h.contentSize()
	return w
}

// start runs req in the background. The returned command delivers the
// run's first event; the home model keeps reading until it is done.
func (h *home) start(req runReq) tea.Cmd {
	h.nextID++
	r := &run{id: h.nextID, req: req, events: make(chan tea.Msg)}
	h.last = r
	format := req.format
	if format == "" {
		format = "json"
	}
	args := append(append([]string{}, req.args...), "--format", format)
	args = append(args, h.sessionArgs()...)
	ctx, cancel := context.WithCancel(h.ctx)
	h.runs.mu.Lock()
	h.runs.cancels[r.id] = cancel
	h.runs.mu.Unlock()
	h.runs.wg.Add(1)
	width := h.contentWidth()
	go func() {
		defer h.runs.wg.Done()
		defer cancel()
		var errBuf syncBuf
		send := func(m runLineMsg) {
			select {
			case r.events <- m:
			case <-ctx.Done():
			}
		}
		lw := &lineWriter{fn: func(line string) {
			pending := loginPendingDoc(line)
			if req.stream || pending != nil {
				send(runLineMsg{run: r, line: line, pending: pending})
			}
		}}
		ew := &lineWriter{fn: func(line string) {
			if pending := loginPendingDoc(line); pending != nil {
				send(runLineMsg{run: r, pending: pending})
			}
		}}
		ask := func(q string) bool {
			reply := make(chan bool, 1)
			msg := runAskMsg{run: r, question: q, notes: noteLines(errBuf.String()), reply: reply}
			select {
			case r.events <- msg:
			case <-ctx.Done():
				return false
			}
			select {
			case ok := <-reply:
				return ok
			case <-ctx.Done():
				return false
			}
		}
		code := HomeRun(ctx, args, RunIO{Stdin: strings.NewReader(req.stdin), Stdout: lw, Stderr: io.MultiWriter(&errBuf, ew), Ask: ask, Width: width})
		lw.flush()
		ew.flush()
		res := parseResult(code, lw.all.String(), errBuf.String())
		select {
		case r.events <- runDoneMsg{run: r, res: res}:
		case <-h.ctx.Done():
		}
	}()
	return h.runs.wait(r)
}

// wait reads the next event of r.
func (rr *runRegistry) wait(r *run) tea.Cmd {
	return func() tea.Msg { return <-r.events }
}

func (rr *runRegistry) done(id int) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	delete(rr.cancels, id)
}

// cancel stops a run (Ctrl-C for that command).
func (h *home) cancelRun(r *run) {
	if r == nil {
		return
	}
	h.runs.mu.Lock()
	defer h.runs.mu.Unlock()
	if c := h.runs.cancels[r.id]; c != nil {
		c()
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lineWriter keeps everything written and calls fn per complete line.
type lineWriter struct {
	mu   sync.Mutex
	all  bytes.Buffer
	part []byte
	fn   func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.all.Write(p)
	w.part = append(w.part, p...)
	var lines []string
	for {
		i := bytes.IndexByte(w.part, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(w.part[:i]))
		w.part = w.part[i+1:]
	}
	w.mu.Unlock()
	for _, l := range lines {
		w.fn(l)
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	rest := string(w.part)
	w.part = nil
	w.mu.Unlock()
	if strings.TrimSpace(rest) != "" {
		w.fn(rest)
	}
}

// parseResult splits stderr into the error document and notes.
func parseResult(code int, stdout, stderr string) runResult {
	res := runResult{code: code, stdout: stdout, stderr: stderr}
	for _, l := range strings.Split(stderr, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "{") {
			var doc struct {
				Error *runError `json:"error"`
			}
			if json.Unmarshal([]byte(t), &doc) == nil && doc.Error != nil {
				res.err = doc.Error
				continue
			}
			if loginPendingDoc(t) != nil {
				continue
			}
		}
		res.notes = append(res.notes, strings.TrimRight(l, " "))
	}
	if code != 0 && res.err == nil {
		msg := "the command failed"
		if len(res.notes) > 0 {
			msg = res.notes[len(res.notes)-1]
		}
		res.err = &runError{Code: "unexpected", Message: msg}
	}
	return res
}

// loginPendingDoc is line as a login_pending record, or nil.
func loginPendingDoc(line string) map[string]any {
	if !strings.Contains(line, `"login_pending"`) {
		return nil
	}
	if m := parseDoc(line); m != nil && m["type"] == "login_pending" {
		return m
	}
	return nil
}

// noteLines are the non-JSON lines of stderr.
func noteLines(stderr string) []string {
	return parseResult(0, "", stderr).notes
}

// parseDoc reads one JSON document; nil when stdout is not one object.
func parseDoc(s string) map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &m) != nil {
		return nil
	}
	return m
}

// parseLines reads JSON lines, skipping anything else.
func parseLines(s string) []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(nil, 4<<20)
	for sc.Scan() {
		if m := parseDoc(sc.Text()); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// displayCommand is args as a shell command, with token values hidden.
func displayCommand(args []string) string {
	parts := []string{"audd"}
	for i, a := range args {
		switch {
		case i > 0 && args[i-1] == "--token":
			a = "your-api-token"
		case i >= 2 && args[i-2] == "set" && args[i-1] == "token" && a != "-":
			a = "your-api-token"
		}
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=,@+%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// askState is the confirmation a run is waiting for.
type askState struct {
	owner    string
	msg      runAskMsg
	input    textinput.Model
	typed    string
	mismatch bool
}

func newAskState(owner string, m runAskMsg, h *home) *askState {
	s := &askState{owner: owner, msg: m, typed: m.run.req.typed}
	if s.typed != "" {
		ti := textinput.New()
		ti.Prompt = "> "
		ti.Cursor.SetMode(cursor.CursorStatic)
		ti.Focus()
		s.input = ti
	}
	return s
}

// askWaiting is a confirmation that waits for the one on screen.
type askWaiting struct {
	owner string
	msg   runAskMsg
}

// answer replies to the confirmation on screen and shows the next one.
func (h *home) answer(ok bool) {
	if h.ask == nil {
		return
	}
	h.ask.msg.reply <- ok
	h.ask = nil
	if len(h.asks) > 0 {
		next := h.asks[0]
		h.asks = h.asks[1:]
		h.ask = newAskState(next.owner, next.msg, h)
	}
}

// askKey handles a key while a confirmation is open.
func (h *home) askKey(k tea.KeyMsg) tea.Cmd {
	s := h.ask
	key := k.String()
	if key == "ctrl+c" {
		h.answer(false)
		return tea.Quit
	}
	if key == "esc" {
		h.answer(false)
		return h.setFlash("Cancelled")
	}
	if s.typed != "" {
		if key == "enter" {
			if strings.TrimSpace(s.input.Value()) == s.typed {
				h.answer(true)
				return nil
			}
			s.mismatch = true
			return nil
		}
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(k)
		s.mismatch = false
		return cmd
	}
	switch key {
	case "y", "Y":
		h.answer(true)
		return nil
	case "n", "N", "enter":
		h.answer(false)
		return h.setFlash("Cancelled")
	}
	return nil
}

// askView is the confirmation in the content pane.
func (h *home) askView(w, ht int) string {
	s := h.ask
	var b strings.Builder
	b.WriteString(h.st.Bold.Render("Confirm") + "\n\n")
	for _, n := range s.msg.notes {
		b.WriteString(output.Wrap(n, w) + "\n")
	}
	if len(s.msg.notes) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(styleLines(h.st.Warn, output.Wrap(s.msg.question, w)) + "\n\n")
	b.WriteString(h.st.Dim.Render("$ "+displayCommand(s.msg.run.req.args)) + "\n\n")
	if s.typed != "" {
		b.WriteString(fmt.Sprintf("Type %s and press Enter to confirm, or esc to cancel.\n", h.st.Bold.Render(s.typed)))
		b.WriteString(s.input.View() + "\n")
		if s.mismatch {
			b.WriteString(h.st.Warn.Render("That does not match.") + "\n")
		}
	} else {
		b.WriteString("Press y to confirm, n or esc to cancel.\n")
	}
	return b.String()
}
