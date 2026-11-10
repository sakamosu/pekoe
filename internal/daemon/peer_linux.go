package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// sockPeerPID is the pid of the process at the other end of a unix socket
// (SO_PEERCRED: the process that connected).
func sockPeerPID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var serr error
	if err := rc.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	return int(cred.Pid), nil
}

// parentPID is pid's parent process (/proc/<pid>/stat: the field after the
// command, which is in parentheses and may itself hold ") ").
func parentPID(pid int) (int, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, errors.New("bad /proc stat")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, errors.New("bad /proc stat")
	}
	return strconv.Atoi(f[1])
}
