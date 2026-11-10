package daemon

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// sockPeerPID is the pid of the process at the other end of a unix socket
// (LOCAL_PEERPID: the process that connected).
func sockPeerPID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	pid, serr := 0, error(nil)
	if err := rc.Control(func(fd uintptr) {
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, err
	}
	return pid, serr
}

// parentPID is pid's parent process.
func parentPID(pid int) (int, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if k.Proc.P_pid != int32(pid) {
		return 0, errors.New("no such process")
	}
	return int(k.Eproc.Ppid), nil
}
