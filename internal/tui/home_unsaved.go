package tui

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
