package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// Several session.new of one slug at once: one is made, the others are
// refused (already exists). Each used to pass the check before any was
// registered, so all succeeded and the last silently replaced the others
// (their prompts lost, two creations in one worktree, rm unable to stop
// the first). A branch with another name but the same slug (feat/x and
// feat-x) is the same session.
func TestNewSameSlugAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  int
	}{
		{"started at once", 0},
		{"queued (max_running)", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, repo := setup(t)
			setCap(d, tc.cap)
			const n = 8
			errs := make([]error, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range errs {
				branch := "feat/x"
				if i%2 == 1 {
					branch = "feat-x"
				}
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					errs[i] = callErr(d, "session.new", map[string]any{"repo": repo, "branch": branch, "prompt": fmt.Sprintf("P%d", i)})
				}(i)
			}
			close(start)
			wg.Wait()
			ok := 0
			for _, err := range errs {
				switch {
				case err == nil:
					ok++
				case !strings.Contains(err.Error(), "session feat-x already exists"):
					t.Errorf("refused with %v", err)
				}
			}
			if ok != 1 {
				t.Fatalf("%d session.new of feat-x succeeded at once (want 1)", ok)
			}
			s := waitState(t, d, "feat-x", state.Running, state.Idle, state.Queued, state.Failed)
			if s.State == state.Failed {
				t.Errorf("feat-x failed: %s", s.Error)
			}
		})
	}
}

// session.new whose worktree path is the worktree another session already
// lives in (opened, under a slug from its branch) is refused, as open
// refuses a second session in one worktree; it used to reuse it.
func TestNewRefusesWorktreeOfAnotherSession(t *testing.T) {
	d, _, repo := setup(t)
	wt := filepath.Join(os.Getenv("PEKOE_WORKTREES"), "app", "foo")
	run(t, repo, "git", "worktree", "add", "-q", "-b", "feat/x", wt)
	o := call[state.Session](t, d, "session.open", map[string]any{"path": wt, "resume": "new"})
	if o.Slug != "feat-x" {
		t.Fatalf("opened as %s", o.Slug)
	}
	err := callErr(d, "session.new", map[string]any{"repo": repo, "branch": "foo"})
	if err == nil || !strings.Contains(err.Error(), "already managed as feat-x") {
		t.Fatalf("session.new into feat-x's worktree: %v", err)
	}
	if _, err := d.snapshot("foo"); err == nil {
		t.Errorf("a session foo was made in feat-x's worktree")
	}
}
