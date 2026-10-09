package cli_test

import (
	"context"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AudDMusic/audd-cli/internal/cli"
	"github.com/AudDMusic/audd-cli/internal/testutil"
	"github.com/mattn/go-runewidth"
)

// screen is a small terminal: it keeps what stdout, stderr, and the echo of
// typed input leave on screen, wrapping at width columns. It understands
// line feeds, carriage returns, cursor up (CSI n A), and erase (CSI J, CSI
// K); other escape sequences take no space.
type screen struct {
	mu       sync.Mutex
	width    int
	rows     [][]rune
	row, col int
	all      strings.Builder // everything written
}

func newScreen(width int) *screen { return &screen{width: width, rows: [][]rune{nil}} }

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all.Write(p)
	rs := []rune(string(p))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j == len(rs) {
				return len(p), nil
			}
			n, _ := strconv.Atoi(string(rs[i+2 : j]))
			switch rs[j] {
			case 'A':
				if n == 0 {
					n = 1
				}
				s.row = max(s.row-n, 0)
				s.col = min(s.col, s.width-1)
			case 'J':
				s.rows = s.rows[:s.row+1]
				s.truncate()
			case 'K':
				s.truncate()
			}
			i = j
		case r == '\n':
			s.newline()
		case r == '\r':
			s.col = 0
		case r < 0x20:
		default:
			w := runewidth.RuneWidth(r)
			if s.col+w > s.width {
				s.newline()
			}
			line := s.rows[s.row]
			for len(line) < s.col {
				line = append(line, ' ')
			}
			if s.col < len(line) {
				line = append(line[:s.col], append([]rune{r}, line[s.col+1:]...)...)
			} else {
				line = append(line, r)
			}
			s.rows[s.row] = line
			s.col += w
		}
	}
	return len(p), nil
}

func (s *screen) truncate() {
	if s.col < len(s.rows[s.row]) {
		s.rows[s.row] = s.rows[s.row][:s.col]
	}
}

func (s *screen) newline() {
	s.row++
	s.col = 0
	if s.row == len(s.rows) {
		s.rows = append(s.rows, nil)
	}
}

// text is what is on screen, one line per row, without trailing blank rows.
func (s *screen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []string
	for _, r := range s.rows {
		lines = append(lines, strings.TrimRight(string(r), " "))
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (s *screen) written() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all.String()
}

// runOnScreen runs audd on a terminal of the given width: stdout and stderr
// both draw on scr, and stdin is in.
func runOnScreen(scr *screen, in io.Reader, args ...string) int {
	return cli.Run(context.Background(), args, cli.IO{In: in, Out: scr, Err: scr,
		StdinTTY: true, StdoutTTY: true, StderrTTY: true, StderrWidth: scr.width})
}

// pasteRedirect makes the "browser" approve and the user paste the address
// it was sent to, echoed on the screen like a terminal does. edit changes
// the address before it is pasted.
func pasteRedirect(t *testing.T, scr *screen, edit func(string) string) io.Reader {
	pr, pw := ioPipe(t)
	setBrowser(t, func(u string) error {
		red, err := testutil.AuthorizeRedirect(u)
		if err != nil {
			return err
		}
		if edit != nil {
			red = edit(red)
		}
		scr.Write([]byte(red + "\n"))
		_, err = pw.Write([]byte(red + "\n"))
		return err
	})
	return pr
}

func TestLoginOnTerminalLeavesOnlyTheSuccessLine(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	accountEnv(t)
	scr := newScreen(40) // the pasted address and the instructions wrap
	code := runOnScreen(scr, pasteRedirect(t, scr, nil), "login", "--browser")
	// At 40 columns both lines wrap onto a second row.
	want := "✓ Signed in as user@example.com (profile\n default).\nSaved your API token; audd uses it for r\necognition."
	if got := scr.text(); code != 0 || got != want {
		t.Fatalf("exit %d, screen:\n%s\n\nwritten:\n%q", code, got, scr.written())
	}
	if !strings.Contains(scr.written(), "paste it here") {
		t.Fatal("the waiting instructions should be shown first")
	}
}

func TestLoginOnTerminalKeepsTheInstructionsOnFailure(t *testing.T) {
	accountEnv(t)
	scr := newScreen(40)
	denied := func(red string) string {
		u, _ := url.Parse(red)
		return "http://" + u.Host + u.Path + "?error=access_denied&state=" + u.Query().Get("state")
	}
	code := runOnScreen(scr, pasteRedirect(t, scr, denied), "login", "--browser")
	// Undo the wrapping: notes wrap at spaces, the long address wherever
	// the terminal cuts it.
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	got := squash(scr.text())
	if code == 0 || !strings.Contains(got, squash("Sign in to AudD in your browser")) || !strings.Contains(got, "error=access_denied") ||
		!strings.Contains(got, squash("Error: sign-in was cancelled in the browser")) {
		t.Fatalf("exit %d, screen:\n%s", code, got)
	}
	if strings.Contains(scr.written(), "\033[J") {
		t.Fatal("nothing should be erased when the sign-in fails")
	}
}

func TestLoginNoBrowserSaysToOpenTheAddress(t *testing.T) {
	accountEnv(t)
	setBrowser(t, func(string) error { t.Error("--no-browser opened a browser"); return nil })
	scr := newScreen(80)
	done := make(chan int, 1)
	go func() { done <- runOnScreen(scr, strings.NewReader(""), "login", "--browser", "--no-browser") }()
	deadline := time.Now().Add(10 * time.Second)
	var authURL string
	for authURL == "" && time.Now().Before(deadline) {
		for _, l := range strings.Split(scr.written(), "\n") {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "http") {
				authURL = l
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if authURL == "" {
		t.Fatalf("no sign-in URL: %q", scr.written())
	}
	if w := scr.written(); !strings.Contains(w, "Open this address in a browser to sign in:") || strings.Contains(w, "If it did not open") {
		t.Fatalf("--no-browser wording: %q", w)
	}
	if err := testutil.FollowAuthorize(authURL); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 || !strings.HasPrefix(scr.text(), "✓ Signed in as user@example.com") {
			t.Fatalf("exit %d, screen:\n%s", code, scr.text())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sign-in did not finish")
	}
}

func TestDeviceLoginOnTerminalLeavesOnlyTheSuccessLine(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	accountEnv(t)
	scr := newScreen(60)
	code := runOnScreen(scr, strings.NewReader(""), "login", "--device")
	want := "✓ Signed in as user@example.com (profile default).\nSaved your API token; audd uses it for recognition."
	if got := scr.text(); code != 0 || got != want {
		t.Fatalf("exit %d, screen:\n%s\n\nwritten:\n%q", code, got, scr.written())
	}
}

func TestStepUpOnTerminalLeavesOnlyTheSuccessLine(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	as, _ := accountEnv(t)
	loginUnticked(t, as, "billing:pay")
	scr := newScreen(80)
	code := runOnScreen(scr, strings.NewReader(""), "billing", "subscribe", "pro_plan")
	got := scr.text()
	if code != 0 || strings.Contains(got, "To sign in") || !strings.Contains(got, "\n✓ Signed in as user@example.com (profile default).\nAmount:") {
		t.Fatalf("exit %d, screen:\n%s\n\nwritten:\n%q", code, got, scr.written())
	}
}
