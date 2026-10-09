package art

import (
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// QueryTimeout is how long Query waits for the terminal to answer.
const QueryTimeout = 150 * time.Millisecond

// RemoteQueryTimeout is how long Query waits over SSH, where a reply can
// take a network round trip. A reply that came after the wait would be
// read as keystrokes once the view starts.
const RemoteQueryTimeout = 600 * time.Millisecond

// queryTimeout is how long to wait for the terminal to answer.
func queryTimeout(env func(string) string) time.Duration {
	if env("SSH_CONNECTION") != "" || env("SSH_TTY") != "" || env("SSH_CLIENT") != "" {
		return RemoteQueryTimeout
	}
	return QueryTimeout
}

// Probe is what a terminal said about itself.
type Probe struct {
	// Answered is set when the terminal replied to the device attributes
	// (DA1) query.
	Answered bool
	// Sixel is set when the DA1 reply lists sixel graphics (attribute 4).
	Sixel bool
	// Cell is the cell size from the CSI 16 t reply; zero when unknown.
	Cell CellSize
}

// Terminal is a terminal that can be asked questions: requests are written
// to it and replies read back with a timeout.
type Terminal interface {
	io.Writer
	// ReadTimeout reads what the terminal sent, waiting at most d. It
	// returns 0 and a nil error when nothing arrived in time.
	ReadTimeout(p []byte, d time.Duration) (int, error)
}

var (
	da1Reply  = regexp.MustCompile(`\x1b\[\?([0-9;]*)c`)
	cellReply = regexp.MustCompile(`\x1b\[6;([0-9]+);([0-9]+)t`)
)

// Query asks t for its cell size in pixels (CSI 16 t) and its device
// attributes (DA1, CSI c). Every terminal answers DA1, and answers in
// order, so the DA1 reply marks the end of the replies; Query stops there
// or after timeout.
func Query(t Terminal, timeout time.Duration) Probe {
	var p Probe
	if t == nil {
		return p
	}
	if _, err := io.WriteString(t, "\x1b[16t\x1b[c"); err != nil {
		return p
	}
	deadline := time.Now().Add(timeout)
	var got bytes.Buffer
	buf := make([]byte, 256)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		n, err := t.ReadTimeout(buf, left)
		got.Write(buf[:n])
		if da1Reply.Match(got.Bytes()) || err != nil || n == 0 {
			break
		}
	}
	return parseProbe(got.Bytes())
}

func parseProbe(b []byte) Probe {
	var p Probe
	if m := cellReply.FindSubmatch(b); m != nil {
		h, _ := strconv.Atoi(string(m[1]))
		w, _ := strconv.Atoi(string(m[2]))
		if c := (CellSize{W: w, H: h}); c.Known() {
			p.Cell = c
		}
	}
	if m := da1Reply.FindSubmatch(b); m != nil {
		p.Answered = true
		attrs := strings.Split(string(m[1]), ";")
		// The first value is the terminal class (61-65), not an attribute.
		for _, a := range attrs[min(1, len(attrs)):] {
			if a == "4" {
				p.Sixel = true
			}
		}
	}
	return p
}

// forced returns the protocol AUDD_ART asks for, if it names one.
func forced(env func(string) string) (Protocol, bool) {
	switch strings.ToLower(strings.TrimSpace(env("AUDD_ART"))) {
	case "none", "off", "0", "false", "no":
		return ProtoNone, true
	case "kitty":
		return ProtoKitty, true
	case "iterm2", "iterm":
		return ProtoITerm2, true
	case "sixel":
		return ProtoSixel, true
	case "blocks", "halfblock", "half-block":
		return ProtoHalfBlock, true
	case "blocks256":
		return ProtoHalfBlock256, true
	}
	return ProtoNone, false
}

func inMultiplexer(env func(string) string) bool {
	term := env("TERM")
	return env("TMUX") != "" || strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux")
}

// DetectFullScreen picks the protocol for full-screen views, which redraw
// the screen as text and so need images that can be placed in cells:
//
//   - Kitty and Ghostty: Kitty graphics with Unicode placeholders.
//   - iTerm2 and WezTerm: iTerm2 inline images. WezTerm draws Kitty images
//     but not Unicode placeholders.
//   - VS Code: iTerm2 inline images when its terminal has images turned on,
//     which it reports as sixel support in DA1.
//   - Konsole, and any terminal whose DA1 reply lists sixel: sixel.
//   - Everything else: half-blocks, in true color when available.
//
// t is the terminal to query, or nil when stdin is not a terminal; it is
// asked for DA1 and its cell size unless AUDD_ART turns art off or picks
// half-blocks, or a multiplexer (tmux, screen) is in between, which would
// answer for itself. The wait is longer over SSH. AUDD_ART overrides the
// choice as in Detect.
func DetectFullScreen(env func(string) string, isTTY bool, t Terminal) (Protocol, Probe) {
	var probe Probe
	p := Detect(env, isTTY)
	if p == ProtoNone {
		return p, probe
	}
	f, isForced := forced(env)
	if t != nil && !inMultiplexer(env) && (!isForced || f.IsGraphics()) {
		probe = Query(t, queryTimeout(env))
	}
	if isForced {
		return p, probe
	}
	prog := env("TERM_PROGRAM")
	switch {
	case p == ProtoKitty && (env("WEZTERM_PANE") != "" || prog == "WezTerm"):
		p = ProtoITerm2
	case p == ProtoKitty && env("KONSOLE_VERSION") != "" && env("KITTY_WINDOW_ID") == "":
		p = ProtoSixel
	case !p.IsGraphics() && prog == "vscode" && probe.Sixel:
		p = ProtoITerm2
	case !p.IsGraphics() && probe.Sixel && !inMultiplexer(env):
		p = ProtoSixel
	}
	return p, probe
}
