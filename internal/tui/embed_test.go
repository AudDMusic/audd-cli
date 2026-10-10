package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AudDMusic/audd-cli/internal/art"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func embeddedExplorer(t *testing.T, tabs ...tabID) *explorer {
	t.Helper()
	oldZone := localZone
	localZone = time.UTC
	t.Cleanup(func() { localZone = oldZone })
	var out, errb bytes.Buffer
	a := npApp(&out, &errb, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, StdinTTY: true, NoColor: true})
	m := newEmbeddedExplorer(context.Background(), a, fakeExplorerData(&fakeStreamsAPI{}), tabs, newArtStore(art.ProtoNone))
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 15})
	return m
}

func TestEmbeddedExplorerOneTab(t *testing.T) {
	m := embeddedExplorer(t, tabStreams)
	m.Update(m.load(tabStreams, m.top())())
	v := m.View()
	if strings.Contains(v, "Recent") || strings.Contains(v, "audd browse") || !strings.Contains(v, "Nina Si") {
		t.Fatalf("one tab, no tab bar:\n%s", v)
	}
	if n := strings.Count(v, "\n") + 1; n != 15 {
		t.Fatalf("view is %d lines, want 15", n)
	}
	if _, cmd := m.key(key("q")); cmd != nil {
		t.Fatal("q must not quit an embedded explorer")
	}
	for _, k := range m.keyList() {
		if k == "q quit" || k == "? help" || k == "tab next" {
			t.Fatalf("hint %q belongs to the screen around it", k)
		}
	}
	m.key(key("d"))
	if !m.capturing() {
		t.Fatal("the remove confirmation captures keys")
	}
}

func TestEmbeddedExplorerTabsAndResume(t *testing.T) {
	m := embeddedExplorer(t, tabRecent, tabJobs)
	if v := m.View(); !strings.Contains(v, "Recent") || !strings.Contains(v, "Jobs") || strings.Contains(v, "Streams") {
		t.Fatalf("tab bar of allowed tabs:\n%s", v)
	}
	m.key(key("tab"))
	if m.tab != tabJobs {
		t.Fatalf("tab: %v", m.tab)
	}
	m.key(key("tab"))
	if m.tab != tabRecent {
		t.Fatalf("tab wraps within the allowed tabs: %v", m.tab)
	}
	m.key(key("tab"))
	m.Update(m.load(tabJobs, m.top())())
	var got string
	m.onResume = func(id string, retry bool) tea.Cmd {
		got = id
		if !retry {
			t.Fatal("R retries failed items")
		}
		return nil
	}
	m.key(key("R"))
	if got != "k3x9" || m.after != nil {
		t.Fatalf("resume hook: %q", got)
	}
}

func TestEmbeddedNowPlaying(t *testing.T) {
	var out, errb bytes.Buffer
	a := npApp(&out, &errb, output.PrinterOptions{Format: output.FormatTable, StdoutTTY: true, StdinTTY: true, NoColor: true})
	f := sampleFeed()
	st, _ := f.Stations(context.Background())
	m := newEmbeddedNowPlaying(context.Background(), a, f, st[:1], NowPlayingOptions{}, newArtStore(art.ProtoNone))
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 15})
	m.Update(m.poll(false)())
	v := m.View()
	if strings.Contains(v, "q quit") || !strings.Contains(v, "Feeling Good") {
		t.Fatalf("embedded now playing:\n%s", v)
	}
	if _, cmd := m.key(key("q")); cmd != nil {
		t.Fatal("q must not quit")
	}
}
