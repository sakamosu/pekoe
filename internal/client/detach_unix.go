//go:build unix

package client

import "syscall"

func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// alive reports whether a process with this pid exists (zombies included).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
