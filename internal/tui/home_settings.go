package tui

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
)

// settingRow is one setting as the Settings section shows it.
type settingRow struct {
	key, value, source, desc string
}

// settingsSection shows every audd config key and changes them through
// audd config set and unset.
type settingsSection struct {
	h       *home
	rows    []settingRow
	cur     int
	edit    *field // open editor
	panel   *cmdPanel
	err     error
	profile string
	path    string
	// orig is the value the editor started with: enter without a change
	// saves nothing.
	orig string
	// undo puts back the setting saved last, while its message shows.
	undo *settingUndo
	// fresh: nothing typed in the editor yet.
	fresh bool
	// typed is the value last sent to be saved (the token too).
	typed string
}

func newSettingsSection(h *home) section { return &settingsSection{h: h, panel: newPanel(h)} }

func (s *settingsSection) title() string { return "Settings" }

func (s *settingsSection) init() tea.Cmd { s.load(); return nil }

// load reads the settings the way the commands will: from the config
// file, the environment, and the credential store.
func (s *settingsSection) load() {
	cfg, err := config.Load()
	if err != nil {
		s.err = err
		return
	}
	s.err = nil
	name := config.ProfileName(s.h.flags.Profile, cfg)
	p := cfg.Profile(name)
	s.profile, s.path = name, cfg.Path
	s.rows = s.rows[:0]
	// The token comes last: it is changed least, and with care.
	keys := append([]string{}, config.Keys...)
	keys = append(slices.DeleteFunc(keys, func(k string) bool { return k == "token" }), "token")
	for _, k := range keys {
		r := settingRow{key: k, desc: config.KeyDescriptions[k]}
		if k == "token" && s.h.a.Secrets != nil {
			// Where this computer actually keeps it.
			r.desc = "API token, kept in " + secrets.Where(s.h.a.Secrets)
		}
		if k == "token" {
			t, src, err := config.ResolveToken(s.h.flags.Token, name, s.h.a.Secrets)
			switch {
			case err != nil:
				r.value, r.source = "(could not read)", ""
			case t == "":
				r.value, r.source = "not set", ""
			default:
				r.value, r.source = config.MaskToken(t), tokenSourceWords(src, s.h.testToken)
			}
		} else {
			r.value, r.source = config.SettingSource(p, k, os.Getenv)
		}
		s.rows = append(s.rows, r)
	}
}

// tokenSourceWords says where the API token comes from, in plain words.
func tokenSourceWords(src config.TokenSource, test bool) string {
	switch {
	case test:
		return "test token for this session"
	case src == config.SourceFlag:
		return "from --token"
	case src == config.SourceEnv:
		return "from AUDD_API_TOKEN"
	case src == config.SourceConfig:
		return "saved token"
	case src == config.SourceLogin:
		return "from audd login"
	}
	return string(src)
}

// editable says why a setting can not be changed here ("" when it can).
func (s *settingsSection) notEditable(r settingRow) string {
	switch r.source {
	case "from AUDD_API_TOKEN":
		return "The token comes from AUDD_API_TOKEN; change it in your shell"
	case "from --token", "test token for this session":
		return "This session's token was given when audd started; sign in or paste a token in Account to save one"
	case config.SettingEnv:
		return "This setting comes from an environment variable; change it in your shell"
	}
	return ""
}

// isDefault reports whether a setting has its default value (nothing to
// unset).
func (s *settingsSection) isDefault(r settingRow) bool {
	return r.source == config.SettingDefault || r.key == "token" && r.value == "not set"
}

func (s *settingsSection) selected() *settingRow {
	if s.cur < 0 || s.cur >= len(s.rows) {
		return nil
	}
	return &s.rows[s.cur]
}

// editor is the field for changing a setting.
func (s *settingsSection) editor(r settingRow) *field {
	switch r.key {
	case "format":
		f := enumField("value", settingLabel("format"), "←/→ to choose, enter to save. auto: a table on a terminal, JSON when piped", []string{"auto", "table", "json", "jsonl", "csv"})
		f.setValue(r.value)
		return f
	case "streams.background_recorder":
		f := enumField("value", settingLabel(r.key), "←/→ to choose, enter to save", []string{"on", "off"})
		f.setValue(onOff(r.value))
		return f
	case "token":
		f := textField("value", settingLabel("token"), "Paste the API token from https://dashboard.audd.io, then enter")
		f.masked = true
		f.input.EchoMode = textinput.EchoPassword
		f.input.EchoCharacter = '•'
		f.input.Focus()
		return f
	}
	f := &field{kind: fInt, name: "value", label: settingLabel(r.key), help: "A whole number, then enter", input: newInput()}
	if _, err := strconv.Atoi(r.value); err == nil {
		// The value in effect, from the file, the environment, or the
		// default.
		f.setValue(r.value)
	}
	f.input.Focus()
	return f
}

// startEdit opens the editor on the selected setting.
func (s *settingsSection) startEdit() {
	if r := s.selected(); r != nil {
		s.edit = s.editor(*r)
		s.orig = s.edit.value()
		// The first key typed replaces the value, as if it was selected.
		s.fresh = s.edit.isText() && s.orig != ""
	}
}

// askUnset asks before unsetting the selected setting, and says what
// that does.
func (s *settingsSection) askUnset() tea.Cmd {
	r := s.selected()
	if r == nil {
		return nil
	}
	what := "It goes back to its default."
	if r.key == "token" {
		what = "audd then has no API token for this profile until you set one or sign in."
	}
	key := r.key
	if key != "token" {
		// A setting of audd itself: unset right away, with undo.
		s.undo = &settingUndo{key: key, value: r.value}
		return s.panel.start(runReq{args: []string{"config", "unset", key}})
	}
	return s.h.askFirst(&confirmState{
		owner:    "settings",
		question: fmt.Sprintf("Unset %s in profile %s?", key, s.profile),
		action:   "Unset " + key,
		notes:    []string{what},
		command:  "audd config unset " + key,
		cancel:   "Cancelled. " + settingLabel(key) + " unchanged.",
		yes: func() tea.Cmd {
			return s.panel.start(runReq{args: []string{"config", "unset", key}})
		},
	})
}

func (s *settingsSection) setArgs() []string {
	r := s.selected()
	if r == nil || s.edit == nil {
		return nil
	}
	if r.key == "token" {
		return []string{"config", "set", "token", "-"}
	}
	v := s.edit.value()
	if r.key == "streams.background_recorder" {
		v = map[string]string{"on": "true", "off": "false"}[v]
	}
	if r.key == "format" && v == "auto" {
		return []string{"config", "unset", "format"}
	}
	if v == "" {
		v = "VALUE"
	}
	return []string{"config", "set", r.key, v}
}

func (s *settingsSection) update(msg tea.Msg) tea.Cmd {
	if mine, done := s.panel.handle(msg); mine {
		if done {
			s.load()
			if !s.panel.res.ok() && s.panel.args[1] == "set" && s.typed != "" {
				// Not saved: the editor stays, with what was typed and
				// why, to fix it.
				s.startEdit()
				if s.edit != nil {
					s.edit.setValue(s.typed)
					s.fresh = false
					if s.panel.args[2] != "token" {
						s.edit.problem = capFirst(s.panel.res.err.Message)
						s.panel.res = nil
					}
				}
			}
			if s.panel.res.ok() {
				key := s.panel.args[2]
				what := settingLabel(key) + " is back to its default"
				switch {
				case key == "token" && s.panel.args[1] == "unset":
					what = "API token removed from profile " + s.profile
				case key == "token":
					what = "Token saved for profile " + s.profile
				case s.panel.args[1] == "set":
					what = settingLabel(key) + " set to " + onOff(s.panel.args[3])
					if w := settingWarning(key, s.panel.args[3]); w != "" {
						what += " · " + w
					}
				}
				if key == "token" {
					// The saved token runs from now on, not a session one.
					s.h.useToken(false)
					s.load()
					return tea.Batch(s.h.setFlash(what), s.h.loadHeader())
				}
				if s.undo != nil && s.undo.key == key && s.undo.msg == "" {
					what += " · u undo"
					s.undo.msg = what
				}
				return s.h.setFlash(what)
			}
		}
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	ks := k.String()
	if s.edit != nil {
		switch ks {
		case "esc":
			s.edit = nil
			return nil
		case "enter":
			v := s.edit.value()
			switch {
			case v == s.orig:
				s.edit = nil
				return s.h.setFlash("No change")
			case v == "":
				return s.h.setFlash("Enter a value, or esc to cancel")
			case s.edit.check() != "":
				s.edit.problem = s.edit.check()
				return nil
			}
			return s.askSave(v)
		case "left":
			if s.edit.kind == fEnum && s.edit.choice == 0 {
				return s.h.setFlash("This is the first option · esc leaves")
			}
		case "ctrl+u":
			if s.edit.isText() {
				s.edit.input.SetValue("")
				s.edit.problem = ""
			}
			return nil
		case "up", "down", "tab", "shift+tab":
			return s.h.setFlash("Enter saves, esc cancels")
		}
		switch s.edit.kind {
		case fEnum:
			f := &form{fields: []*field{s.edit}}
			f.update(k)
			return nil
		}
		if s.fresh && k.Type == tea.KeyRunes {
			s.edit.input.SetValue("")
		}
		s.fresh = false
		var cmd tea.Cmd
		s.edit.input, cmd = s.edit.input.Update(k)
		// A number is checked as it is typed.
		s.edit.problem = s.edit.formatProblem()
		return cmd
	}
	switch ks {
	case "down":
		s.cur = min(len(s.rows)-1, s.cur+1)
	case "up":
		s.cur = max(0, s.cur-1)
	case "enter", "right":
		return s.edit1()
	}
	return nil
}

// edit1 starts editing the selected setting, or says why it can not be
// changed here.
func (s *settingsSection) edit1() tea.Cmd {
	if r := s.selected(); r != nil {
		if why := s.notEditable(*r); why != "" {
			return s.h.setFlash(why)
		}
	}
	s.startEdit()
	return nil
}

// askSave saves a new value. Settings of audd itself save right away,
// with undo; the token, which changes what runs everywhere, asks first.
func (s *settingsSection) askSave(v string) tea.Cmd {
	r := *s.selected()
	args := s.setArgs()
	run := func() tea.Cmd {
		s.edit = nil
		if r.key == "format" && v == "auto" {
			return s.panel.start(runReq{args: []string{"config", "unset", "format"}})
		}
		req := runReq{args: args}
		s.typed = v
		if r.key == "token" {
			req.stdin = v
		}
		return s.panel.start(req)
	}
	if r.key != "token" {
		s.undo = &settingUndo{key: r.key, value: r.value, wasDefault: r.source == config.SettingDefault}
		return run()
	}
	old := r.value
	if r.source != "" {
		old += " (" + r.source + ")"
	}
	return s.h.askFirst(&confirmState{
		owner:    "settings",
		question: "Replace the API token of profile " + s.profile + "?",
		notes:    []string{"Now: " + old, "New: " + config.MaskToken(v), "Commands in this profile use the new token from now on."},
		action:   "Replace the token",
		topic:    "Replace the token?",
		command:  displayCommand(args),
		cancel:   "Cancelled. API token unchanged.",
		yes:      run,
	})
}

// settingUndo is how to put back the setting changed last.
type settingUndo struct {
	key, value string
	wasDefault bool
	msg        string // the message the undo goes with
}

// undoLast puts the setting changed last back.
func (s *settingsSection) undoLast() tea.Cmd {
	u := s.undo
	s.undo = nil
	if u.wasDefault {
		return s.panel.start(runReq{args: []string{"config", "unset", u.key}})
	}
	return s.panel.start(runReq{args: []string{"config", "set", u.key, u.value}})
}

// actions are the buttons for the selected setting.
func (s *settingsSection) actions() []button {
	if s.edit != nil {
		return nil
	}
	var b []button
	if r := s.selected(); r != nil {
		if s.notEditable(*r) == "" {
			b = append(b, button{label: "Edit", key: "e", do: s.edit1})
			if !s.isDefault(*r) {
				b = append(b, button{label: "Unset", key: "d", do: s.askUnset})
			}
		}
	}
	b = append(b, button{label: "Reload", key: "r", do: func() tea.Cmd { s.load(); return s.h.setFlash("Reloaded") }})
	if s.undo != nil && s.undo.msg != "" {
		// Until the next change, or until you leave Settings.
		b = append(b, button{label: "Undo", key: "u", do: s.undoLast})
	}
	return b
}

func (s *settingsSection) downExits() bool { return s.edit == nil && s.cur >= len(s.rows)-1 }

func (s *settingsSection) view(w, h int) string {
	st := s.h.st
	var b strings.Builder
	b.WriteString(st.Bold.Render("Profile "+s.profile) + "\n\n")
	if s.err != nil {
		e := output.AsError(s.err)
		return b.String() + errorText(st, &runError{Message: e.Message, Hint: e.Hint}, w)
	}
	keyW := 0
	for _, r := range s.rows {
		keyW = max(keyW, len(settingLabel(r.key)))
	}
	for i, r := range s.rows {
		val := r.value
		if r.key == "streams.background_recorder" {
			val = onOff(val)
		}
		if s.edit != nil && i == s.cur {
			if s.edit.kind == fEnum {
				val = st.Bold.Render("‹ " + s.edit.value() + " ›")
			} else {
				s.edit.input.Width = max(8, w-keyW-6)
				val = s.edit.input.View()
			}
		}
		line := padRight(settingLabel(r.key), keyW+2) + val
		switch {
		case s.edit != nil && i == s.cur:
			line += "  " + st.Dim.Render("editing")
		case r.source != "":
			line += "  " + st.Dim.Render("("+r.source+")")
		}
		line = s.h.listRow(line, i == s.cur)
		desc := st.Dim.Render(r.key + " · " + r.desc)
		if s.edit != nil && i == s.cur && s.edit.problem != "" {
			desc = st.Warn.Render(s.edit.problem)
		} else if s.edit != nil && i == s.cur {
			if w := settingWarning(r.key, s.edit.value()); w != "" {
				desc = st.Warn.Render(w)
			}
		}
		row := truncate(line, w) + "\n" + truncate("    "+desc, w)
		b.WriteString(s.h.mark(row, func() tea.Cmd {
			if s.edit != nil && s.cur == i {
				return nil
			}
			if s.cur == i {
				s.startEdit()
				return nil
			}
			s.cur, s.edit = i, nil
			return nil
		}) + "\n")
	}
	if s.edit != nil {
		b.WriteString("\n" + styleLines(st.Dim, output.Wrap(s.edit.help+"; esc cancels.", w)) + "\n")
	}
	if s.panel.res != nil && s.panel.res.err != nil {
		if len(s.panel.args) > 2 && s.panel.args[2] == "token" && s.panel.args[1] == "set" {
			b.WriteString("\n" + styleLines(st.Warn, output.Wrap(tokenSaveFailed(s.panel.res.err), w)) + "\n")
		} else {
			b.WriteString("\n" + errorText(st, s.panel.res.err, w) + "\n")
		}
	}
	b.WriteString("\n" + styleLines(st.Dim, output.Wrap(fmt.Sprintf("Settings apply to profile %s and are saved in %s. Environment variables (env) win over the file.", s.profile, s.path), w)))
	return b.String()
}

func (s *settingsSection) keys() []keyHelp {
	switch {
	case s.edit != nil && s.edit.kind == fEnum:
		return []keyHelp{{"←/→", "change"}, {"enter", "save"}, {"esc", "cancel"}}
	case s.edit != nil:
		return []keyHelp{{"type", "the value"}, {"enter", "save"}, {"esc", "cancel"}}
	}
	k := []keyHelp{{"↑/↓", "move"}}
	if r := s.selected(); r != nil && s.notEditable(*r) == "" {
		k = append(k, keyHelp{"→/enter", "edit"})
	}
	return k
}

func (s *settingsSection) crumbs() []string {
	if s.edit != nil {
		return []string{settingLabel(s.selected().key)}
	}
	return nil
}

func (s *settingsSection) command() string {
	if s.edit != nil {
		return displayCommand(s.setArgs())
	}
	if s.panel.args != nil && s.panel.running {
		return displayCommand(s.panel.args)
	}
	if r := s.selected(); r != nil {
		return "audd config get " + r.key
	}
	return "audd config list"
}

func (s *settingsSection) capturing() bool { return s.edit != nil && s.edit.isText() }

// leftExits: left goes back: from an edit it cancels (in typed text once
// the cursor is at the start).
func (s *settingsSection) leftExits() bool {
	switch {
	case s.edit != nil && s.edit.isText():
		return s.edit.input.Position() == 0
	case s.edit != nil && s.edit.kind == fEnum:
		return false // ← changes the choice; esc leaves
	}
	return true
}

func (s *settingsSection) wheel(dir int) tea.Cmd {
	if s.edit == nil {
		s.cur = max(0, min(len(s.rows)-1, s.cur+dir))
	}
	return nil
}

func (s *settingsSection) back() bool {
	if s.edit != nil {
		s.edit = nil
		return true
	}
	return false
}

// settingLabels name the settings in plain words; the key is shown dim.
var settingLabels = map[string]string{
	"format":                      "Output format",
	"max_requests":                "Request limit per command",
	"concurrency":                 "Parallel requests",
	"streams.background_recorder": "Start the recorder automatically",
	"token":                       "API token",
}

// settingWarning is a caution about a value that works but may hurt.
func settingWarning(key, v string) string {
	n, err := strconv.Atoi(v)
	if err != nil {
		return ""
	}
	switch {
	case key == "concurrency" && n > 32:
		return "This may hit rate limits"
	case key == "max_requests" && n > 100000:
		return "That is a lot of requests for one command"
	}
	return ""
}

// onOff shows a true/false setting as on/off.
func onOff(v string) string {
	switch v {
	case "true":
		return "on"
	case "false":
		return "off"
	}
	return v
}

func settingLabel(key string) string {
	if l := settingLabels[key]; l != "" {
		return l
	}
	return key
}

// left: leaving Settings ends its undo.
func (s *settingsSection) left() { s.undo = nil }
