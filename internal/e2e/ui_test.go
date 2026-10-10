package e2e

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/cli"
	"github.com/AudDMusic/audd-cli/internal/testutil"
)

// Interactive mode end to end: audd ui runs on a pretend terminal (a
// pipe for the keyboard, a buffer for the screen) against the fake AudD
// servers, and the tests press keys and read the screen.

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type uiSession struct {
	t    *testing.T
	keys *io.PipeWriter
	out  *syncBuffer
	from int // screen output before this was seen by an earlier wait
	done chan int
}

// Key sequences a terminal sends.
const (
	kDown  = "\x1b[B"
	kUp    = "\x1b[A"
	kRight = "\x1b[C"
	kEnter = "\r"
	kEsc   = "\x1b"
	kCtrlC = "\x03"
	kCtrlK = "\x0b"
)

// ui starts audd with args on a terminal.
func (e *env) ui(args ...string) *uiSession {
	e.t.Helper()
	pr, pw := io.Pipe()
	s := &uiSession{t: e.t, keys: pw, out: &syncBuffer{}, done: make(chan int, 1)}
	go func() {
		s.done <- cli.Run(context.Background(), args, cli.IO{In: pr, Out: s.out, Err: io.Discard,
			StdinTTY: true, StdoutTTY: true, StderrTTY: true, StdoutWidth: 100, StderrWidth: 100, NoUpdateNotice: true})
		pr.Close()
	}()
	e.t.Cleanup(func() { s.quit() })
	return s
}

// press sends keys (or text) one at a time.
func (s *uiSession) press(keys ...string) {
	s.t.Helper()
	for _, k := range keys {
		if _, err := s.keys.Write([]byte(k)); err != nil {
			s.t.Fatalf("sending %q: %v", k, err)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// typeText sends text a character at a time.
func (s *uiSession) typeText(text string) {
	for _, r := range text {
		s.press(string(r))
	}
}

// wait waits until the screen shows text.
func (s *uiSession) wait(text string) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := s.out.String()
		if i := strings.Index(out[s.from:], text); i >= 0 {
			s.from += i
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	out := s.out.String()
	if d := os.Getenv("AUDD_UI_DUMP"); d != "" {
		_ = os.WriteFile(d, []byte(out), 0o644)
	}
	if len(out) > 4000 {
		out = out[len(out)-4000:]
	}
	s.t.Fatalf("the screen never showed %q; the end of the output:\n%s", text, out)
}

// quit presses Ctrl-C and waits for audd to exit.
func (s *uiSession) quit() int {
	select {
	case code := <-s.done:
		s.done <- code
		return code
	default:
	}
	s.keys.Write([]byte(kCtrlC))
	select {
	case code := <-s.done:
		s.done <- code
		return code
	case <-time.After(15 * time.Second):
		s.t.Fatal("audd did not exit after Ctrl-C")
	}
	return -1
}

func uiEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	t.Setenv("CI", "")
	t.Setenv("AUDD_NO_TUI", "")
	return e
}

func down(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = kDown
	}
	return out
}

func TestUINoArgumentsOpensInteractiveMode(t *testing.T) {
	e := uiEnv(t)
	s := e.ui()
	s.wait("Welcome to AudD")
	s.wait("Try with the test token")
	if code := s.quit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestUIRecognizeFile(t *testing.T) {
	e := uiEnv(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	song := e.file("song.mp3", "song audio")
	s := e.ui("ui", "--section", "recognize")
	s.wait("Recognize music")
	s.typeText(song)
	s.press(down(9)...) // to the Recognize button
	s.press(kEnter)
	s.wait("Imagine Dragons")
	if n := e.api.Count(testutil.EndpointRecognize); n != 1 {
		t.Fatalf("%d recognitions", n)
	}
}

// --section listen opens Recognize with the microphone.
func TestUISectionListen(t *testing.T) {
	e := uiEnv(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	s := e.ui("ui", "--section", "listen")
	s.wait("Identify the music playing near you")
	s.wait("audd listen")
	if code := s.quit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestUIBatchWithConfirmation(t *testing.T) {
	e := uiEnv(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	e.file("music/a.mp3", "first")
	e.file("music/b.mp3", "second")
	s := e.ui("ui", "--section", "recognize")
	s.wait("Recognize music")
	s.typeText(filepath.Join(e.dir, "music"))
	s.press(down(6)...) // Max files
	s.typeText("2")
	s.press(down(3)...) // the Recognize button
	s.press(kEnter)
	s.wait("Recognize 2 files")
	if n := e.api.Count(testutil.EndpointRecognize); n != 0 {
		t.Fatalf("%d requests before the confirmation", n)
	}
	s.press("y")
	s.wait("2 recognized, 0 no match, 0 failed")
	if n := e.api.Count(testutil.EndpointRecognize); n != 2 {
		t.Fatalf("%d recognitions", n)
	}
	// The job is in History.
	s.press(kEsc, "4")
	s.press("\t")
	s.wait("done")
}

func TestUIAddAndRemoveStream(t *testing.T) {
	e := uiEnv(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	s := e.ui("ui", "--section", "streams")
	s.wait("No streams yet")
	s.press("a")
	s.wait("Add a stream")
	s.typeText("https://radio.example/7.mp3")
	s.press(kDown)
	s.typeText("7")
	s.press(kDown, kDown, kEnter) // past "start" to Add
	s.wait("Streams · 1")
	if st := e.streams.Streams(); len(st) != 1 || st[0].RadioID != 7 || !strings.Contains(s.out.String(), "Added stream 7") {
		t.Fatalf("streams: %+v", st)
	}
	s.press("d")
	s.wait("Remove stream 7?")
	s.press("y")
	s.wait("Removed stream 7")
	if st := e.streams.Streams(); len(st) != 0 {
		t.Fatalf("streams after removing: %+v", st)
	}
}

func TestUIBillingLink(t *testing.T) {
	e := uiEnv(t)
	e.loginByPaste()
	s := e.ui("ui", "--section", "account")
	s.wait("Paid until")
	s.press("]", "]")
	s.wait("startup_plan")
	s.press(kDown, "s")
	s.wait("nothing is charged")
	s.wait(testutil.FakePaymentURL)
	if n := len(e.mcp.CallsTo("subscribe_to_plan")); n != 1 {
		t.Fatalf("%d subscribe calls", n)
	}
}

func TestUIRotateToken(t *testing.T) {
	e := uiEnv(t)
	e.loginByPaste()
	s := e.ui("ui", "--section", "account")
	s.wait("Paid until")
	s.press("[", "[")
	s.wait("0123…cdef")
	s.press("R")
	s.wait("Type rotate")
	s.typeText("rotate")
	s.press(kEnter)
	s.wait("Rotated")
	if n := len(e.mcp.CallsTo("rotate_api_token")); n != 1 {
		t.Fatalf("%d rotations", n)
	}
}

func TestUIEditSetting(t *testing.T) {
	e := uiEnv(t)
	s := e.ui("ui", "--section", "settings")
	s.wait("background_recorder")
	s.press(kDown, kEnter, kRight)
	s.wait("audd config set format json")
	s.press(kEnter)
	s.wait("Set format")
	s.quit()
	r := e.ok("config", "get", "format")
	if d := doc(t, r.Stdout); d["value"] != "json" {
		t.Fatalf("config get format: %s", r.Stdout)
	}
}

func TestUIHelpSearch(t *testing.T) {
	e := uiEnv(t)
	t.Setenv("AUDD_API_TOKEN", testutil.PlaceholderToken)
	s := e.ui("ui", "--section", "help")
	s.wait("Getting started")
	s.press("]")
	s.wait("AUDD CLI")
	s.press("/")
	s.typeText("exit codes")
	s.press(kEnter)
	s.wait(`"exit codes" · n next`)
}
