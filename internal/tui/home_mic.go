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

// Recognize with Source set to Microphone runs audd listen: it records
// from the microphone and recognizes the recording.

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

// The options of the Source field.
const (
	sourceFile = "File, URL, or folder"
	sourceMic  = "Microphone"
)

// micState is what the microphone mode keeps.
type micState struct {
	asked   bool // the tools and devices were looked up
	tools   *media.Tools
	devices []media.Device
	started time.Time
	tick    int
	secs    int
}

// mic reports whether Source is the microphone.
func (s *recognizeSection) mic() bool { return s.form.value("source") == sourceMic }

// setSource switches Source and puts the cursor where the mode starts:
// the input for files, the Listen button for the microphone.
func (s *recognizeSection) setSource(mic bool) {
	s.phase = "form"
	if mic {
		s.form.get("source").setValue(sourceMic)
	} else {
		s.form.get("source").setValue(sourceFile)
	}
	s.sync()
	if mic {
		s.form.focusName("listen")
	} else {
		s.form.focusName("input")
	}
}

// findDevices looks up the audio tools and input devices, once, the
// first time the microphone is chosen.
func (s *recognizeSection) findDevices() tea.Cmd {
	if !s.mic() || s.m.asked {
		return nil
	}
	s.m.asked = true
	ctx := s.h.ctx
	return func() tea.Msg {
		t := listenTools()
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return listenSetupMsg{tools: t, devices: listenDevices(c, t)}
	}
}

// setupDone takes the tools and devices: with several devices the
// Device field becomes a choice.
func (s *recognizeSection) setupDone(m listenSetupMsg) {
	s.m.tools = &m.tools
	s.m.devices = m.devices
	if len(m.devices) > 1 {
		opts := []string{"default"}
		for _, d := range m.devices {
			opts = append(opts, d.Value)
		}
		cur := s.form.get("device")
		*cur = *enumField("device", "Device", "Input device (←/→ to choose)", opts)
	}
	s.sync()
}

// micArgs is the audd listen command for the form.
func (s *recognizeSection) micArgs() []string {
	args := []string{"listen"}
	if v := s.form.value("seconds"); v != "" && v != "10" {
		args = append(args, "--seconds", v)
	}
	if v := s.form.value("device"); v != "" && v != "default" {
		args = append(args, "--device", v)
	}
	if ret := s.providers(); len(ret) > 0 {
		args = append(args, "--return", strings.Join(ret, ","))
	}
	return args
}

func (s *recognizeSection) micKeys() []keyHelp {
	switch s.phase {
	case "running":
		return []keyHelp{{"s or esc", "stop"}}
	case "result":
		k := []keyHelp{{"r", "listen again"}, {"esc", "back"}}
		if s.rec.link != "" {
			k = append(k, keyHelp{"o", "open the link"}, keyHelp{"c", "copy it"})
		}
		if s.rec.view != nil {
			k = append(k, keyHelp{"d", "details"})
		}
		return k
	}
	return []keyHelp{{"↑/↓", "field"}, {"←/→", "choose"}, {"space", "toggle"}, {"enter", "listen"}, {"esc", "sidebar"}}
}

// startListen records and recognizes.
func (s *recognizeSection) startListen() tea.Cmd {
	if e := s.form.check(); e != "" {
		s.form.err = e
		return nil
	}
	s.form.err = ""
	s.m.secs, _ = strconv.Atoi(s.form.value("seconds"))
	s.runArgs = s.micArgs()
	s.batch = false
	s.bs = nil
	s.plan = ""
	s.rec = recognition{}
	s.phase = "running"
	s.m.started = s.h.now()
	s.m.tick++
	id := s.m.tick
	return tea.Batch(s.panel.start(runReq{args: s.runArgs, spends: true, tag: "listen"}),
		tea.Tick(time.Second, func(time.Time) tea.Msg { return listenTickMsg{id} }))
}

// micRunning is the screen while recording and recognizing.
func (s *recognizeSection) micRunning() string {
	st := s.h.st
	left := s.m.secs - int(s.h.now().Sub(s.m.started)/time.Second)
	if left > 0 {
		return st.Bold.Render(fmt.Sprintf("Listening… %d s", left)) + "\n\n" +
			st.Dim.Render("Hold the microphone near the music. s stops.")
	}
	return st.Bold.Render("Recognizing…")
}

// micNotes are shown under the form: a missing recorder, or the one
// input device.
func (s *recognizeSection) micNotes(w int) string {
	st := s.h.st
	var b strings.Builder
	if t := s.m.tools; t != nil && t.FFmpeg == "" && t.Sox == "" {
		e := media.MissingTool("ffmpeg", "listening to the microphone")
		b.WriteString("\n\n" + styleLines(st.Warn, output.Wrap(e.Message+" (sox works too)", w)))
		b.WriteString("\n" + output.Wrap("Try: "+e.Hint, w))
	}
	if len(s.m.devices) == 1 {
		b.WriteString("\n\n" + st.Dim.Render(truncate("Input: "+s.m.devices[0].Label, w)))
	}
	return b.String()
}
