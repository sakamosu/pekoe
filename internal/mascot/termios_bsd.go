//go:build darwin || freebsd || netbsd || openbsd

package mascot

import "golang.org/x/sys/unix"

const (
	ioctlGet = unix.TIOCGETA
	ioctlSet = unix.TIOCSETA
)
