package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// procEnv is the environment pid was started with (nil when it cannot be
// read: another user's, gone, a zombie). KERN_PROCARGS2 is argc, the
// executable path, NUL padding, argv and then the environment, each
// NUL-terminated; an empty string ends the environment.
func procEnv(pid int) []string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil
	}
	return parseProcArgs2(b)
}

func parseProcArgs2(b []byte) []string {
	if len(b) < 4 {
		return nil
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	i := bytes.IndexByte(b, 0) // the executable path
	if i < 0 {
		return nil
	}
	b = b[i:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	for n := 0; n < argc; n++ {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			return nil
		}
		b = b[i+1:]
	}
	var env []string
	for len(b) > 0 {
		i := bytes.IndexByte(b, 0)
		if i <= 0 {
			break
		}
		env = append(env, string(b[:i]))
		b = b[i+1:]
	}
	return env
}

// procCwds is the working directory of each of pids lsof can tell.
func procCwds(pids []int) map[int]string {
	ps := make([]string, len(pids))
	for i, p := range pids {
		ps[i] = strconv.Itoa(p)
	}
	// lsof exits 1 when one of them is gone; what it printed still counts
	out, _ := exec.Command("lsof", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(ps, ",")).Output()
	return parseLsofCwd(out)
}

// parseLsofCwd reads `lsof -Fpn` output: "p<pid>" lines, each followed by
// its "n<path>".
func parseLsofCwd(out []byte) map[int]string {
	m := map[int]string{}
	pid := 0
	for _, l := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(l, "p"):
			pid, _ = strconv.Atoi(l[1:])
		case strings.HasPrefix(l, "n") && pid > 0:
			m[pid] = l[1:]
		}
	}
	return m
}

// procSid is pid's session id (0: unknown).
func procSid(pid int) int {
	s, err := unix.Getsid(pid)
	if err != nil {
		return 0
	}
	return s
}

// ParentPID is pid's parent process.
func ParentPID(pid int) (int, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if k.Proc.P_pid != int32(pid) {
		return 0, errors.New("no such process")
	}
	return int(k.Eproc.Ppid), nil
}
