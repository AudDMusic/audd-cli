package streams

import (
	"testing"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
)

func TestRecorderNoteShownOncePerProfile(t *testing.T) {
	t.Setenv("AUDD_DATA_DIR", t.TempDir())
	old := app.EnsureRecorder
	t.Cleanup(func() { app.EnsureRecorder = old })
	started := true
	app.EnsureRecorder = func(*app.App) (bool, error) { return started, nil }
	a := app.New()
	a.Profile = &config.Profile{Name: config.DefaultProfile}

	if note, err := EnsureRecorder(a); err != nil || note != RecorderNote {
		t.Fatalf("first start: %q %v", note, err)
	}
	if note, _ := EnsureRecorder(a); note != "" {
		t.Fatalf("second start shows the note again: %q", note)
	}
	started = false
	work := &config.Profile{Name: "work"}
	a.Profile = work
	if note, _ := EnsureRecorder(a); note != "" {
		t.Fatalf("no note when nothing was started: %q", note)
	}
	started = true
	if note, _ := EnsureRecorder(a); note != RecorderNote {
		t.Fatalf("another profile gets its own note: %q", note)
	}
}
