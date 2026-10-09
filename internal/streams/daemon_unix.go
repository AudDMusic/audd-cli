//go:build !windows

package streams

import "syscall"

func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func kill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
