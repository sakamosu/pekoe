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

// rm of a session whose worktree directory is gone (deleted by hand, or
// restore found it missing) removes git's registration of it and its
// refs: a new session of the same branch then gets its worktree, where git
// used to refuse ("missing but already registered worktree").
func TestRmOfMissingWorktreeThenNew(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	s := waitState(t, d, "feat-x", state.Idle, state.Running, state.Waiting)
	if err := os.RemoveAll(s.Path); err != nil {
		t.Fatal(err)
	}
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-x"})
	if out := gitOut(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, "feat-x") {
		t.Errorf("git still has the missing worktree registered:\n%s", out)
	}
	if out := gitOut(t, repo, "for-each-ref", "refs/pekoe/feat-x/"); out != "" {
		t.Errorf("refs/pekoe/feat-x/ left:\n%s", out)
	}
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	n := waitState(t, d, "feat-x", state.Idle, state.Running, state.Waiting, state.Failed)
	if n.State == state.Failed {
		t.Errorf("new feat/x after rm of its missing worktree failed: %s", n.Error)
	}
}

// rm without force of a session being created, whose setup has written
// files into the worktree its creation made: those are not work to lose,
// so rm stops the creation and removes it (as session.cancel does). It
// used to stop the creation and then refuse (uncommitted changes),
// leaving a failed session behind a refusal.
func TestRmWithoutForceOfCreatingSession(t *testing.T) {
	d, _, repo := setup(t)
	goFile := filepath.Join(t.TempDir(), "go")
	writeRepoConfig(t, d, repo, "[hooks]\nsetup = [\"touch made-by-setup; "+waitGo(goFile)+"\"]\n")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitFile(t, filepath.Join(s.Path, "made-by-setup"))
	if err := callErr(d, "worktree.rm", map[string]any{"slug": "feat-x"}); err != nil {
		cur, _ := d.snapshot("feat-x")
		t.Fatalf("rm: %v (state now %s)", err, cur.State)
	}
	if _, err := d.snapshot("feat-x"); err == nil {
		t.Errorf("feat-x still there after rm")
	}
	if fileExists(s.Path) {
		t.Errorf("worktree %s left", s.Path)
	}
}

// rm without force of a session whose creation reuses an existing
// worktree with uncommitted changes is refused before anything is
// stopped: the creation goes on, and the session is not left failed.
func TestRmWithoutForceRefusesBeforeStoppingCreation(t *testing.T) {
	d, _, repo := setup(t)
	wt := filepath.Join(os.Getenv("PEKOE_WORKTREES"), "app", "feat-x")
	run(t, repo, "git", "worktree", "add", "-q", "-b", "feat/x", wt)
	os.WriteFile(filepath.Join(wt, "work.txt"), []byte("not committed\n"), 0o644)
	goFile := filepath.Join(t.TempDir(), "go")
	writeRepoConfig(t, d, repo, "[hooks]\nsetup = [\""+waitGo(goFile)+"\"]\n")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitStepRunning(t, d, "feat-x", "setup")
	err := callErr(d, "worktree.rm", map[string]any{"slug": "feat-x"})
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("rm without force of a reused dirty worktree: %v", err)
	}
	if cur, _ := d.snapshot("feat-x"); cur.State != state.Creating {
		t.Errorf("refused rm left feat-x %s (%s); want creating", cur.State, cur.Error)
	}
	os.WriteFile(goFile, nil, 0o644)
	waitState(t, d, "feat-x", state.Idle, state.Running)
	if !fileExists(filepath.Join(wt, "work.txt")) {
		t.Errorf("the uncommitted work is gone")
	}
}

// A land whose [hooks] test is still running when a person rm's the
// session and makes a new one of the same branch: the land stops there.
// It used to go on and land the new session (fast-forward its branch,
// kill its agent, remove its worktree, mark it landed).
func TestLandStopsWhenSessionIsReplaced(t *testing.T) {
	d, _, repo := setup(t)
	dir := t.TempDir()
	started, goFile := filepath.Join(dir, "started"), filepath.Join(dir, "go")
	writeRepoConfig(t, d, repo, "[hooks]\ntest = \"touch "+started+"; "+waitGo(goFile)+"\"\n")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	s := waitState(t, d, "feat-x", state.Running, state.Idle)
	os.WriteFile(filepath.Join(s.Path, "b.txt"), []byte("b\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "b")
	mainBefore := strings.TrimSpace(gitOut(t, repo, "rev-parse", "main"))
	landErr := make(chan error, 1)
	go func() { landErr <- callErr(d, "land", map[string]any{"slug": "feat-x"}) }()
	waitFile(t, started) // land is in its test
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-x", "force": true})
	n := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "NEW"})
	waitState(t, d, "feat-x", state.Running, state.Idle)
	os.WriteFile(goFile, nil, 0o644)
	err := <-landErr
	if err == nil || !strings.Contains(err.Error(), "removed while landing") {
		t.Errorf("land of the removed session: %v", err)
	}
	cur := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
	if cur.State == state.Landed || !fileExists(n.Path) {
		t.Errorf("the old session's land finished the new feat-x (state %s, worktree there %v)", cur.State, fileExists(n.Path))
	}
	if cur.Test.Status != "none" {
		t.Errorf("the old land's test result went on the new session: %+v", cur.Test)
	}
	if after := strings.TrimSpace(gitOut(t, repo, "rev-parse", "main")); after != mainBefore {
		t.Errorf("main moved from %s to %s: the old land fast-forwarded it", mainBefore, after)
	}
}

// rm of an opened checkout pekoe did not make, whose directory is not
// there now (a disk not mounted), only forgets the session: git's
// registration of that worktree stays, as rm leaves such a checkout alone
// when it is there.
func TestRmOfMissingOpenedCheckoutKeepsRegistration(t *testing.T) {
	d, _, repo := setup(t)
	wt := filepath.Join(t.TempDir(), "mine")
	run(t, repo, "git", "worktree", "add", "-q", "-b", "feat/mine", wt)
	o := call[state.Session](t, d, "session.open", map[string]any{"path": wt, "resume": "new"})
	if o.Managed {
		t.Fatalf("%s outside the worktrees root opened as managed", wt)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": o.Slug})
	if out := gitOut(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(out, "feat/mine") {
		t.Errorf("rm removed git's registration of a checkout pekoe did not make:\n%s", out)
	}
}
