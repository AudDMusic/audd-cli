package oauth

import (
	"os"
	"runtime"
)

// Method is how a sign-in is approved.
type Method string

// Sign-in methods.
const (
	// MethodAuto picks browser or device from the environment.
	MethodAuto Method = ""
	// MethodBrowser is the authorization code flow with PKCE: a browser on
	// this machine is sent back to a loopback port, or the user pastes the
	// address it was sent to.
	MethodBrowser Method = "browser"
	// MethodDevice is the device authorization grant (RFC 8628): the user
	// opens a page on any device and approves a short code.
	MethodDevice Method = "device"
)

// Environment is what method selection looks at.
type Environment struct {
	GOOS        string
	Getenv      func(string) string
	InContainer bool
	StdinTTY    bool
	StdoutTTY   bool
}

// DetectEnvironment reads the environment of this process. Tests replace it.
var DetectEnvironment = func(stdinTTY, stdoutTTY bool) Environment {
	return Environment{
		GOOS:        runtime.GOOS,
		Getenv:      os.Getenv,
		InContainer: inContainer(),
		StdinTTY:    stdinTTY,
		StdoutTTY:   stdoutTTY,
	}
}

func inContainer() bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func (e Environment) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

// BrowserAvailable reports whether a browser can be opened on this machine:
// a macOS or Windows desktop session, or a Linux or BSD session with a
// display. Never over SSH, and never in a container without a display.
func (e Environment) BrowserAvailable() bool {
	if e.getenv("SSH_CONNECTION") != "" || e.getenv("SSH_TTY") != "" {
		return false
	}
	switch e.GOOS {
	case "darwin", "windows":
		return !e.InContainer
	}
	return e.getenv("DISPLAY") != "" || e.getenv("WAYLAND_DISPLAY") != ""
}

// Method picks the sign-in method: the browser when a person is at a
// terminal on a machine that can open one, otherwise the device flow.
// Without a terminal on stdin or stdout (scripts and agents) it is always
// the device flow, which needs nothing from this machine.
func (e Environment) Method() Method {
	if !e.StdinTTY || !e.StdoutTTY {
		return MethodDevice
	}
	if e.BrowserAvailable() {
		return MethodBrowser
	}
	return MethodDevice
}
