//go:build darwin || linux || freebsd || netbsd || openbsd

package mascot

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// rawWithTimeout turns off echo and canonical mode and makes reads return
// after timeout (VMIN=0, VTIME in tenths of a second).
func rawWithTimeout(tty *os.File, timeout time.Duration) (func(), error) {
	fd := int(tty.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGet)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Lflag &^= unix.ECHO | unix.ICANON
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = uint8(max(1, min(255, int(timeout/(100*time.Millisecond)))))
	if err := unix.IoctlSetTermios(fd, ioctlSet, &t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSet, old) }, nil
}
