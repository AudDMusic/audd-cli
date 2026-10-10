package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
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
	for _, k := range config.Keys {
		r := settingRow{key: k, desc: config.KeyDescriptions[k]}
		if k == "token" {
			t, src, err := config.ResolveToken(s.h.flags.Token, name, s.h.a.Secrets)
			switch {
			case err != nil:
				r.value, r.source = "(could not read)", ""
			case t == "":
				r.value, r.source = "(not set)", config.SettingDefault
			default:
				r.value, r.source = config.MaskToken(t), src.Describe()
			}
		} else {
			r.value, r.source = config.SettingSource(p, k, os.Getenv)
		}
		s.rows = append(s.rows, r)
	}
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
		f := enumField("value", "format", "←/→ to choose, enter to save", []string{"table", "json", "jsonl", "csv"})
		f.setValue(r.value)
		return f
	case "streams.background_recorder":
		f := enumField("value", r.key, "←/→ to choose, enter to save", []string{"true", "false"})
		f.setValue(r.value)
		return f
	case "token":
		f := textField("value", "token", "Paste the API token from https://dashboard.audd.io, then enter")
		f.masked = true
		f.input.EchoMode = textinput.EchoPassword
		f.input.EchoCharacter = '•'
		f.input.Focus()
		return f
	}
	f := textField("value", r.key, "A whole number, then enter")
	if r.source == config.SettingFile {
		f.setValue(r.value)
	}
	f.input.Focus()
	return f
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
	if v == "" {
		v = "VALUE"
	}
	return []string{"config", "set", r.key, v}
}

func (s *settingsSection) update(msg tea.Msg) tea.Cmd {
	if mine, done := s.panel.handle(msg); mine {
		if done {
			s.load()
			if s.panel.res.ok() {
				what := "Removed " + s.panel.args[2]
				if s.panel.args[1] == "set" {
					what = "Set " + s.panel.args[2]
					if s.panel.args[2] == "token" {
						s.h.signedOut = s.h.noToken()
					}
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
		if ks == "left" && (s.edit.kind == fEnum && s.edit.choice == 0 || s.edit.isText() && s.edit.input.Position() == 0) {
			s.edit = nil
			return nil
		}
		switch ks {
		case "esc":
			s.edit = nil
			return nil
		case "enter":
			args := s.setArgs()
			v := s.edit.value()
			if v == "" {
				return s.h.setFlash("Enter a value, or esc to cancel")
			}
			s.edit = nil
			req := runReq{args: args}
			if args[2] == "token" {
				req.stdin = v
			}
			return s.panel.start(req)
		}
		switch s.edit.kind {
		case fEnum:
			f := &form{fields: []*field{s.edit}}
			f.update(k)
			return nil
		}
		var cmd tea.Cmd
		s.edit.input, cmd = s.edit.input.Update(k)
		return cmd
	}
	switch ks {
	case "down", "j":
		s.cur = min(len(s.rows)-1, s.cur+1)
	case "up", "k":
		s.cur = max(0, s.cur-1)
	case "enter", "e":
		if r := s.selected(); r != nil {
			s.edit = s.editor(*r)
		}
	case "u", "delete", "backspace":
		if r := s.selected(); r != nil {
			return s.panel.start(runReq{args: []string{"config", "unset", r.key}})
		}
	case "r":
		s.load()
	}
	return nil
}

func (s *settingsSection) view(w, h int) string {
	st := s.h.st
	var b strings.Builder
	b.WriteString(st.Bold.Render("Settings") + st.Dim.Render("  profile "+s.profile) + "\n\n")
	if s.err != nil {
		e := output.AsError(s.err)
		return b.String() + errorText(st, &runError{Message: e.Message, Hint: e.Hint}, w)
	}
	keyW := 0
	for _, r := range s.rows {
		keyW = max(keyW, len(r.key))
	}
	for i, r := range s.rows {
		mark := "  "
		if i == s.cur {
			mark = "› "
		}
		val := r.value
		if s.edit != nil && i == s.cur {
			if s.edit.kind == fEnum {
				val = st.Bold.Render("‹ " + s.edit.value() + " ›")
			} else {
				s.edit.input.Width = max(8, w-keyW-6)
				val = s.edit.input.View()
			}
		}
		line := mark + padRight(r.key, keyW+2) + val
		if s.edit == nil || i != s.cur {
			line += "  " + st.Dim.Render("("+r.source+")")
		}
		if i == s.cur && s.h.color && s.edit == nil {
			line = st.Bold.Render(line)
		}
		row := truncate(line, w) + "\n" + truncate("    "+st.Dim.Render(r.desc), w)
		b.WriteString(s.h.mark(row, func() tea.Cmd {
			if s.edit != nil && s.cur == i {
				return nil
			}
			if s.cur == i {
				s.edit = s.editor(r)
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
		b.WriteString("\n" + errorText(st, s.panel.res.err, w) + "\n")
	}
	b.WriteString("\n" + styleLines(st.Dim, output.Wrap(fmt.Sprintf("Settings apply to profile %s and are saved in %s. Environment variables (env) win over the file.", s.profile, s.path), w)))
	return b.String()
}

func (s *settingsSection) keys() []keyHelp {
	if s.edit != nil {
		return []keyHelp{{"enter", "save"}, {"esc", "cancel"}}
	}
	return []keyHelp{{"↑/↓", "setting"}, {"enter", "change"}, {"u", "unset"}, {"r", "reload"}}
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

func (s *settingsSection) capturing() bool { return s.edit != nil }

// leftExits: left leaves from the list; while editing, left at the
// start of the value cancels the edit.
func (s *settingsSection) leftExits() bool { return s.edit == nil }

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
