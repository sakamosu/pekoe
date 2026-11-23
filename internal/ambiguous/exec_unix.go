//go:build unix

package ambiguous

import "syscall"

func execve(exe string, argv, env []string) error { return syscall.Exec(exe, argv, env) }
