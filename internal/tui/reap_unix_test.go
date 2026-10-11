//go:build !windows

package tui

import "syscall"

// pidGone reports whether pid no longer exists (reaped, not a zombie).
func pidGone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }
