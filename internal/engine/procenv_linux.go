package engine

import (
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
