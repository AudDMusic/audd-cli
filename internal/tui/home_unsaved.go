package tui

import "github.com/AudDMusic/audd-cli/internal/output"

// What each section has typed and not saved, for quitting (see
// lostOnQuit).

func (s *recognizeSection) unsaved() string {
	if s.phase == "form" && !s.mic() && s.form.value("input") != "" {
		return "The recognition you set up (" + s.form.value("input") + ")"
	}
	return ""
}

func (s *streamsSection) unsaved() string {
	switch {
	case s.adding && (s.add.value("url") != ""):
		return "The stream you were adding"
	case s.page == 1 && s.callbackUnsaved():
		return "Your change to the callback URL"
	}
	return ""
}

func (s *settingsSection) unsaved() string {
	if s.edit != nil && s.edit.value() != s.orig {
		if r := s.selected(); r != nil {
			return "Your change to " + settingLabel(r.key)
		}
	}
	return ""
}

func (s *signinSection) unsaved() string {
	if s.step == "paste" && s.paste.value("token") != "" {
		return "The token you pasted"
	}
	return ""
}

func (s *accountSection) unsaved() string {
	if s.tokPasting && s.tokForm.value("token") != "" {
		return "The token you pasted"
	}
	return ""
}

// upExits: ↑ on the first field of the form goes back to the menu.
func (s *recognizeSection) upExits() bool {
	return s.browser == nil && s.phase == "form" && s.form.cursor == 0
}

// upExits: ↑ on the first setting goes back to the menu.
func (s *settingsSection) upExits() bool { return s.edit == nil && s.cur == 0 }

// left: Recognitions opens again at its list, not inside an old result
// (a resume still running stays).
func (s *historySection) left() {
	if !s.panel.running {
		s.resume = false
	}
	if ex := s.pane.ex; ex != nil {
		for t, levels := range ex.tabs {
			if len(levels) > 0 {
				ex.tabs[t] = levels[:1]
				levels[0].detail = false
			}
		}
	}
}

// shownError is the error on the page (for its sign-in or token
// buttons), nil when none.
func (s *recognizeSection) shownError() *runError {
	if s.phase == "result" && s.panel.res != nil {
		return s.panel.res.err
	}
	return nil
}

func (s *nowPlayingSection) shownError() *runError {
	if s.err == nil {
		return nil
	}
	e := output.AsError(s.err)
	return &runError{Code: e.Code, Message: e.Message, Hint: e.Hint}
}

func (s *accountSection) shownError() *runError {
	for _, p := range []*cmdPanel{s.token, s.pay, s.billing, s.prof} {
		if p != nil && p.res != nil && p.res.err != nil {
			return p.res.err
		}
	}
	return nil
}

func (s *streamsSection) shownError() *runError {
	switch s.page {
	case 0:
		if s.pane.err != nil {
			e := output.AsError(s.pane.err)
			return &runError{Code: e.Code, Message: e.Message, Hint: e.Hint}
		}
		if e := s.pane.listError(); e != nil {
			if e.Code == "token_rejected" {
				e.Hint = "set an API token" // the ways in, as buttons
			}
			return e
		}
	case 1:
		if s.cbPanel.res != nil {
			return s.cbPanel.res.err
		}
	case 2:
		if s.recPanel.res != nil {
			return s.recPanel.res.err
		}
	case 3:
		if s.dataPanel.res != nil {
			return s.dataPanel.res.err
		}
	}
	return nil
}

// listError is the error of the list the pane shows, if any.
func (p *explorerPane) listError() *runError {
	if p.ex == nil {
		return nil
	}
	if l := p.ex.top(); l != nil && l.err != nil && len(l.rows) == 0 {
		e := output.AsError(l.err)
		return &runError{Code: e.Code, Message: e.Message, Hint: e.Hint}
	}
	return nil
}

// inForm: the pages that are forms (see former).
func (s *recognizeSection) inForm() bool { return s.phase == "form" && s.browser == nil }

func (s *streamsSection) inForm() bool {
	return s.page == 0 && s.adding || s.page == 1 || s.page == 3
}

func (s *settingsSection) inForm() bool { return s.edit != nil }

func (s *signinSection) inForm() bool { return s.step == "paste" }

func (s *accountSection) inForm() bool { return s.tokPasting || s.buying }
