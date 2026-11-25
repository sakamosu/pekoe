package engine

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// procEnv is the environment pid was started with (nil when it cannot be
// read: another user's, gone, a zombie).
func procEnv(pid int) []string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

// procCwds is the working directory of each of pids that can be read.
func procCwds(pids []int) map[int]string {
	m := map[int]string{}
	for _, p := range pids {
		if d, err := os.Readlink("/proc/" + strconv.Itoa(p) + "/cwd"); err == nil {
			m[p] = d
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

// ParentPID is pid's parent process (/proc/<pid>/stat: the field after the
// command, which is in parentheses and may itself hold ") ").
func ParentPID(pid int) (int, error) {
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
