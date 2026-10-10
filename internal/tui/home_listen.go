package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/media"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// Seams for tests: the audio tools and the input devices.
var (
	listenTools   = media.Find
	listenDevices = media.InputDevices
)

type (
	listenSetupMsg struct {
		tools   media.Tools
		devices []media.Device
	}
	listenTickMsg struct{ id int }
)

// listenSection is audd listen: record from the microphone and recognize.
type listenSection struct {
	h       *home
	form    *form
	panel   *cmdPanel
	tools   *media.Tools
	devices []media.Device
	phase   string // "form", "running", "result"
	started time.Time
	tick    int
	secs    int
	rec     recognition
	runArgs []string
}

func newListenSection(h *home) section {
	s := &listenSection{h: h, panel: newPanel(h), phase: "form"}
	sec := &field{kind: fInt, name: "seconds", label: "Seconds", help: "How long to record (at most 60). 10 to 12 seconds is enough.", input: newInput()}
	sec.setValue("10")
	fields := []*field{sec, textField("device", "Device", "Input device; empty for the system default")}
	for _, p := range providers {
		fields = append(fields, boolField("return:"+p.name, "Add "+p.label+" data", "--return "+p.name))
	}
	fields = append(fields, buttonField("listen", "Listen"))
	s.form = newForm(fields...)
	s.form.focusName("listen")
	return s
}

func (s *listenSection) title() string { return "Listen" }

func (s *listenSection) init() tea.Cmd {
	ctx := s.h.ctx
	return func() tea.Msg {
		t := listenTools()
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return listenSetupMsg{tools: t, devices: listenDevices(c, t)}
	}
}

func (s *listenSection) capturing() bool { return s.phase == "form" && s.form.capturing() }

func (s *listenSection) back() bool {
	switch s.phase {
	case "running":
		s.panel.stop()
		return true
	case "result":
		s.phase = "form"
		return true
	}
	return false
}

func (s *listenSection) args() []string {
	args := []string{"listen"}
	if v := s.form.value("seconds"); v != "" && v != "10" {
		args = append(args, "--seconds", v)
	}
	if v := s.form.value("device"); v != "" && v != "default" {
		args = append(args, "--device", v)
	}
	var ret []string
	for _, p := range providers {
		if s.form.get("return:" + p.name).on {
			ret = append(ret, p.name)
		}
	}
	if len(ret) > 0 {
		args = append(args, "--return", strings.Join(ret, ","))
	}
	return args
}

func (s *listenSection) command() string {
	if s.phase != "form" && s.runArgs != nil {
		return displayCommand(s.runArgs)
	}
	return displayCommand(s.args())
}

func (s *listenSection) keys() []keyHelp {
	switch s.phase {
	case "running":
		return []keyHelp{{"s or esc", "stop"}}
	case "result":
		k := []keyHelp{{"r", "listen again"}, {"esc", "back"}}
		if s.rec.link != "" {
			k = append(k, keyHelp{"o", "open the link"}, keyHelp{"c", "copy it"})
		}
		return k
	}
	return []keyHelp{{"↑/↓", "field"}, {"space", "toggle"}, {"enter", "listen"}}
}

func (s *listenSection) start() tea.Cmd {
	if e := s.form.check(); e != "" {
		s.form.err = e
		return nil
	}
	s.form.err = ""
	s.secs, _ = strconv.Atoi(s.form.value("seconds"))
	s.runArgs = s.args()
	s.phase = "running"
	s.started = s.h.now()
	s.tick++
	id := s.tick
	return tea.Batch(s.panel.start(runReq{args: s.runArgs, spends: true}),
		tea.Tick(time.Second, func(time.Time) tea.Msg { return listenTickMsg{id} }))
}

func (s *listenSection) update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case listenSetupMsg:
		s.tools = &m.tools
		s.devices = m.devices
		if len(m.devices) > 1 {
			opts := []string{"default"}
			for _, d := range m.devices {
				opts = append(opts, d.Value)
			}
			cur := s.form.get("device")
			*cur = *enumField("device", "Device", "Input device (←/→ to choose)", opts)
		}
		return nil
	case listenTickMsg:
		if m.id == s.tick && s.phase == "running" {
			return tea.Tick(time.Second, func(time.Time) tea.Msg { return listenTickMsg{m.id} })
		}
		return nil
	}
	if mine, done := s.panel.handle(msg); mine {
		if done {
			s.phase = "result"
			if s.panel.res.err == nil {
				s.rec = parseRecognition(parseDoc(s.panel.res.stdout))
				if s.rec.view != nil {
					return s.h.arts.want(s.h.ctx, *s.rec.view)
				}
			}
		}
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch s.phase {
	case "running":
		if k.String() == "s" {
			s.panel.stop()
		}
		return nil
	case "result":
		switch k.String() {
		case "enter", "r":
			if res := s.panel.res; k.String() == "enter" && res != nil && res.err != nil && strings.HasPrefix(res.err.Hint, "audd ") {
				return s.h.openPaletteLine(res.err.Hint)
			}
			return s.start()
		case "o":
			if s.rec.link != "" {
				if err := openURL(s.rec.link); err != nil {
					return s.h.setFlash("Could not open the browser: " + err.Error())
				}
				return s.h.setFlash("Opened " + s.rec.link)
			}
		case "c":
			if s.rec.link != "" {
				return s.h.copy(s.rec.link, s.rec.link)
			}
		}
		return nil
	}
	act, cmd := s.form.update(k)
	if act == "listen" {
		return s.start()
	}
	return cmd
}

func (s *listenSection) view(w, h int) string {
	st := s.h.st
	var b strings.Builder
	switch s.phase {
	case "running":
		left := s.secs - int(s.h.now().Sub(s.started)/time.Second)
		if left > 0 {
			b.WriteString(st.Bold.Render(fmt.Sprintf("Listening… %d s", left)) + "\n\n")
			b.WriteString(st.Dim.Render("Hold the microphone near the music. s stops."))
		} else {
			b.WriteString(st.Bold.Render("Recognizing…"))
		}
		return b.String()
	case "result":
		res := s.panel.res
		if res == nil {
			return ""
		}
		if res.err != nil {
			b.WriteString(errorText(st, res.err, w))
			b.WriteString("\n\n" + st.Dim.Render("Press r to try again."))
			return b.String()
		}
		if s.rec.view != nil {
			b.WriteString(s.h.cardText(*s.rec.view, false, w, h))
		} else {
			b.WriteString(s.rec.text + "\n\n" + st.Dim.Render("Press enter to listen again."))
		}
		return b.String()
	}
	b.WriteString(st.Bold.Render("Identify the music playing near you") + "\n\n")
	if s.tools != nil && s.tools.FFmpeg == "" && s.tools.Sox == "" {
		e := media.MissingTool("ffmpeg", "listening to the microphone")
		b.WriteString(st.Warn.Render(output.Wrap(e.Message+" (sox works too)", w)) + "\n")
		b.WriteString(output.Wrap("Try: "+e.Hint, w) + "\n\n")
	}
	b.WriteString(s.form.view(w, st, s.h.color))
	if len(s.devices) == 1 {
		b.WriteString("\n\n" + st.Dim.Render(truncate("Input: "+s.devices[0].Label, w)))
	}
	return b.String()
}
