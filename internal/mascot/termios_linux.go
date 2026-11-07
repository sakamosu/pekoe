//go:build linux

package mascot

import "golang.org/x/sys/unix"

const (
	ioctlGet = unix.TCGETS
	ioctlSet = unix.TCSETS
)
