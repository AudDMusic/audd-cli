package agent

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed skill.md.tmpl
var skillTemplate string

var skillTmpl = template.Must(template.New("skill").Parse(skillTemplate))

// Target is a coding agent's instructions file format.
type Target string

// Targets audd agent-setup can write.
const (
	TargetClaude   Target = "claude"    // Claude Code skill: .claude/skills/audd/SKILL.md
	TargetCursor   Target = "cursor"    // Cursor rule: .cursor/rules/audd.mdc
	TargetAgentsMD Target = "agents-md" // a section in AGENTS.md (Codex and others)
)

// AllTargets lists the targets in a fixed order.
var AllTargets = []Target{TargetClaude, TargetCursor, TargetAgentsMD}

// Markers around the audd section in AGENTS.md, so running agent-setup
// again replaces the section instead of adding another.
const (
	BeginMarker = "<!-- audd:begin (written by audd agent-setup; edits inside are replaced) -->"
	EndMarker   = "<!-- audd:end -->"
)

// RelPath is where a target's file lives, relative to the project root.
func (t Target) RelPath() string {
	switch t {
	case TargetClaude:
		return filepath.Join(".claude", "skills", "audd", "SKILL.md")
	case TargetCursor:
		return filepath.Join(".cursor", "rules", "audd.mdc")
	default:
		return "AGENTS.md"
	}
}

// Render returns the instructions for a target. For AGENTS.md it is the
// section including its markers.
func Render(t Target) (string, error) {
	data := struct {
		Target       Target
		Heading, Sub string
		MCPHint      bool
	}{Target: t, Heading: "#", Sub: "##", MCPHint: t == TargetClaude}
	if t == TargetAgentsMD {
		data.Heading, data.Sub = "##", "###"
	}
	var b bytes.Buffer
	if err := skillTmpl.Execute(&b, data); err != nil {
		return "", err
	}
	s := strings.TrimSpace(b.String()) + "\n"
	if t == TargetAgentsMD {
		s = BeginMarker + "\n" + s + EndMarker + "\n"
	}
	return s, nil
}

// ErrUnclosedSection means an AGENTS.md has an audd begin marker with no
// end marker after it, so the end of the audd section is unknown.
var ErrUnclosedSection = errors.New("AGENTS.md has an audd begin marker but no " + EndMarker + " after it")

// MergeAgentsMD puts section into an AGENTS.md: it replaces an existing
// audd section, or appends one after the existing text. A begin marker
// without an end marker is ErrUnclosedSection: guessing where the section
// ends could delete the user's own text.
func MergeAgentsMD(existing, section string) (string, error) {
	// Match on the start of the begin marker, so a section written by any
	// version is replaced.
	if i := strings.Index(existing, "<!-- audd:begin"); i >= 0 {
		j := strings.Index(existing[i:], EndMarker)
		if j < 0 {
			return "", ErrUnclosedSection
		}
		end := i + j + len(EndMarker)
		if end < len(existing) && existing[end] == '\n' {
			end++
		}
		return existing[:i] + section + existing[end:], nil
	}
	if strings.TrimSpace(existing) == "" {
		return section, nil
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + section, nil
}

// WriteResult describes one file agent-setup wrote.
type WriteResult struct {
	Target  Target `json:"target"`
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Changed bool   `json:"changed"`
}

// Install writes a target's file under dir. It is safe to run again: the
// file (or the AGENTS.md section) is replaced, and nothing is written when
// it is already current.
func Install(dir string, t Target) (WriteResult, error) {
	path := filepath.Join(dir, t.RelPath())
	res := WriteResult{Target: t, Path: path}
	content, err := Render(t)
	if err != nil {
		return res, err
	}
	old, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		res.Created = true
	case err != nil:
		return res, err
	}
	if t == TargetAgentsMD {
		if content, err = MergeAgentsMD(string(old), content); err != nil {
			return res, err
		}
	}
	if !res.Created && string(old) == content {
		return res, nil
	}
	res.Changed = true
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".audd-*")
	if err != nil {
		return res, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return res, err
	}
	if err := tmp.Close(); err != nil {
		return res, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return res, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return res, fmt.Errorf("writing %s: %w", path, err)
	}
	return res, nil
}

// Detect returns the targets whose agent is set up in dir: a .claude or
// .cursor directory, or an AGENTS.md file.
func Detect(dir string) []Target {
	var out []Target
	if st, err := os.Stat(filepath.Join(dir, ".claude")); err == nil && st.IsDir() {
		out = append(out, TargetClaude)
	}
	if st, err := os.Stat(filepath.Join(dir, ".cursor")); err == nil && st.IsDir() {
		out = append(out, TargetCursor)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
		out = append(out, TargetAgentsMD)
	}
	return out
}
