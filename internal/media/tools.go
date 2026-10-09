package media

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/output"
)

// Tools holds the absolute paths of the local audio tools, or "" for each
// one that is not installed.
type Tools struct {
	FFmpeg, FFprobe, Sox string
}

// Seams for tests.
var (
	lookPath  = exec.LookPath
	currentOS = runtime.GOOS
	run       = runCommand
)

// command is one external tool invocation.
type command struct {
	Name string
	Args []string
	Env  []string // added to the current environment
}

func runCommand(ctx context.Context, c command) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return o.Bytes(), e.Bytes(), err
}

// Find looks up ffmpeg, ffprobe, and sox on PATH.
func Find() Tools {
	find := func(name string) string {
		p, err := lookPath(name)
		if err != nil {
			return ""
		}
		return p
	}
	return Tools{FFmpeg: find("ffmpeg"), FFprobe: find("ffprobe"), Sox: find("sox")}
}

// InstallHint returns the command that installs tool on this system
// (ffprobe ships with ffmpeg).
func InstallHint(tool string) string {
	pkg := tool
	if tool == "ffprobe" {
		pkg = "ffmpeg"
	}
	switch currentOS {
	case "darwin":
		return "brew install " + pkg
	case "windows":
		if pkg == "sox" {
			return "winget install --id ChrisBagwell.SoX (or: choco install sox.portable)"
		}
		return "winget install --id Gyan.FFmpeg (or: choco install ffmpeg)"
	case "linux":
		has := func(name string) bool { _, err := lookPath(name); return err == nil }
		switch {
		case has("apt-get"):
			return "sudo apt install " + pkg
		case has("dnf"):
			if pkg == "ffmpeg" {
				return "sudo dnf install ffmpeg-free"
			}
			return "sudo dnf install " + pkg
		case has("pacman"):
			return "sudo pacman -S " + pkg
		case has("apk"):
			return "sudo apk add " + pkg
		case has("zypper"):
			return "sudo zypper install " + pkg
		}
	}
	site := "https://ffmpeg.org/download.html"
	if pkg == "sox" {
		site = "https://sourceforge.net/projects/sox/"
	}
	return fmt.Sprintf("install %s with your package manager (%s)", pkg, site)
}

// MissingTool is the error for a command that needs a tool that is not
// installed (exit 2, with the install command as the hint).
func MissingTool(tool, purpose string) *output.Error {
	return output.Errf(output.ExitUsage, "missing_tool", InstallHint(tool),
		"%s needs %s, which is not installed", purpose, tool)
}

// lastLines returns the last few non-empty lines of a tool's stderr, for
// error messages.
func lastLines(b []byte, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// tempAudio creates an empty temporary file with ext and returns its path
// and a function that removes it.
func tempAudio(prefix, ext string) (string, func(), error) {
	f, err := os.CreateTemp("", prefix+"*"+ext)
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	f.Close()
	return name, func() { _ = os.Remove(name) }, nil
}
