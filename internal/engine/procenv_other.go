//go:build !darwin && !linux

package engine

// procEnv cannot read another process's environment here: no leftover is
// found, and none is stopped.
func procEnv(pid int) []string { return nil }

func procCwds(pids []int) map[int]string { return nil }

func procSid(pid int) int { return 0 }
