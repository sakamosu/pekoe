package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"pekoe/internal/state"
)

// pidIn reads the pid a hook's child wrote to f (once it is complete).
func pidIn(t *testing.T, f string) int {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); sleep10ms() {
		b, _ := os.ReadFile(f)
		if s := strings.TrimSpace(string(b)); strings.HasSuffix(string(b), "\n") && s != "" {
			pid, err := strconv.Atoi(s)
			if err != nil {
				t.Fatalf("%s: %q", f, b)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
	}
	t.Fatalf("%s never got a pid", f)
	return 0
}

// processGone waits until pid no longer exists (killed and reaped).
func processGone(pid int) bool {
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); sleep10ms() {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
	}
	return false
}

// rm of a session being created stops the setup hook's children too, not
// only its shell: a child (cd web && npm ci, a make recipe, a subshell)
// whose output goes elsewhere went on after rm's teardown, and one that
// kept the hook's output open made rm wait until it ended.
func TestRmStopsSetupHookChildren(t *testing.T) {
	for _, tc := range []struct {
		name, redirect string
	}{
		{"output elsewhere", " >/dev/null 2>&1"},
		{"output kept open", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, repo := setup(t)
			pidf := filepath.Join(t.TempDir(), "child.pid")
			// a child of the hook's shell (not exec'd: the shell goes on to
			// `true`), which sleeps far longer than the test
			child := "sh -c 'echo $$ > " + pidf + "; exec sleep 300'" + tc.redirect + "; true"
			writeRepoConfig(t, d, repo, "[hooks]\nsetup = [\""+child+"\"]\n")
			call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
			pid := pidIn(t, pidf)
			done := make(chan error, 1)
			go func() { done <- callErr(d, "worktree.rm", map[string]any{"slug": "feat-x", "force": true}) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(20 * time.Second):
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("rm still waits for the setup hook's child (pid %d)", pid)
			}
			if !processGone(pid) {
				t.Errorf("the setup hook's child (pid %d) is still running after rm", pid)
			}
		})
	}
}
