//go:build !darwin && !linux

package engine

import "errors"

// procEnv cannot read another process's environment here: no leftover is
// found, and none is stopped.
func procEnv(pid int) []string { return nil }

func procCwds(pids []int) map[int]string { return nil }

func procSid(pid int) int { return 0 }

// ParentPID cannot read another process's parent here.
func ParentPID(int) (int, error) {
	return 0, errors.New("telling a process's parent is not supported on this system")
}
