//go:build linux

package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// On a terminal that never answers queries (docker run -t from a script,
// ssh -tt), commands start at once: nothing waits for the terminal to report
// its background color.
func TestNoWaitOnASilentTerminal(t *testing.T) {
	if binary == "" {
		t.Skipf("audd binary not built: %v", buildErr)
	}
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer ptmx.Close()
	if err := unix.IoctlSetPointerInt(int(ptmx.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("unlock the pseudo-terminal: %v", err)
	}
	n, err := unix.IoctlGetUint32(int(ptmx.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("pseudo-terminal number: %v", err)
	}
	tty, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open the pseudo-terminal: %v", err)
	}
	defer tty.Close()
	go io.Copy(io.Discard, ptmx) // read what audd writes, never answer

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "version")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "TERM=xterm-256color", "HOME=" + dir,
		"AUDD_CONFIG_DIR=" + filepath.Join(dir, "config"), "AUDD_CACHE_DIR=" + filepath.Join(dir, "cache"),
		"AUDD_DATA_DIR=" + filepath.Join(dir, "data"), "AUDD_NO_KEYRING=1", "AUDD_NO_UPDATE_CHECK=1",
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	start := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("audd version: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("audd version took %v on a terminal that does not answer queries", took.Round(100*time.Millisecond))
	}
}
