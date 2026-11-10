// Package testenv isolates a whole test binary from the user's real pekoe:
// every location internal/paths resolves (data, config, worktrees, the daemon
// socket, the tmux server) points into one temporary directory. Call Main
// from each package's TestMain; tests that need their own place still
// t.Setenv over it.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Main points PEKOE_HOME, XDG_DATA_HOME, XDG_CONFIG_HOME, PEKOE_SOCK,
// PEKOE_TMUX_SOCKET and PEKOE_WORKTREES into a fresh temporary directory,
// runs the tests and removes the directory.
func Main(m *testing.M) {
	// short: $PEKOE_SOCK is a unix socket, about 104 bytes at most on macOS
	dir, err := os.MkdirTemp("", "pk")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"PEKOE_HOME":        filepath.Join(dir, "h"),
		"XDG_DATA_HOME":     filepath.Join(dir, "xd"),
		"XDG_CONFIG_HOME":   filepath.Join(dir, "xc"),
		"PEKOE_SOCK":        filepath.Join(dir, "s"),
		"PEKOE_TMUX_SOCKET": "pekoe-test-" + filepath.Base(dir),
		"PEKOE_WORKTREES":   filepath.Join(dir, "wt"),
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
