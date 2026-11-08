package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// otherRepo makes a second repository with the same name ("app") as the one
// setup made, like a fork: both map to ~/worktrees/app.
func otherRepo(t *testing.T) string {
	other := filepath.Join(t.TempDir(), "app")
	os.MkdirAll(other, 0o755)
	run(t, other, "git", "init", "-q", "-b", "main")
	run(t, other, "git", "commit", "-q", "--allow-empty", "-m", "init")
	return other
}

func registered(t *testing.T, repo, path string) bool {
	t.Helper()
	c := exec.Command("git", "worktree", "list", "--porcelain")
	c.Dir = repo
	out, _ := c.Output()
	real, _ := filepath.EvalSymlinks(path)
	for _, ln := range strings.Split(string(out), "\n") {
		if ln == "worktree "+path || ln == "worktree "+real {
			return true
		}
	}
	return false
}

// A worktree of another repository with the same name is neither reused
// nor deleted, and its uncommitted files survive cancel / rm --force.
func TestNewDoesNotTakeOverAnotherRepositorysWorktree(t *testing.T) {
	d, _, repo := setup(t)
	other := otherRepo(t)
	wt := filepath.Join(os.Getenv("PEKOE_WORKTREES"), "app", "feat-x")
	run(t, other, "git", "worktree", "add", "-q", "-b", "feat/x", wt)
	keep := filepath.Join(wt, "keep.txt")
	os.WriteFile(keep, []byte("not committed"), 0o644)

	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	s := waitCreated(t, d, "feat-x")
	if s.State != state.Failed || !strings.Contains(s.Error, "another repository") {
		t.Fatalf("reused the other repository's worktree: %+v", s)
	}
	if s.OwnDir {
		t.Fatal("a directory found in place is not the session's own")
	}
	call[any](t, d, "session.cancel", map[string]any{"slug": "feat-x"})
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("cancel deleted the other repository's worktree")
	}
	if !registered(t, other, wt) {
		t.Fatal("the other repository lost its worktree")
	}

	// a session recorded for that path (older state) is not allowed to
	// delete it either, even with --force
	d.add(&state.Session{Slug: "old", Branch: "feat/x", Repo: "app", RepoID: "x", RepoDir: repo,
		Path: wt, Managed: true, OwnDir: true, State: state.Idle, StateSince: time.Now(), CreatedAt: time.Now()})
	res := call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "old", "force": true})
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("rm --force deleted the other repository's worktree")
	}
	if res["worktree_deleted"] != false {
		t.Fatalf("rm reports a deletion: %v", res)
	}
}

// A plain directory found where the worktree would go is not deleted by
// cancel: only a directory the session made itself is.
func TestCancelKeepsDirectoryItDidNotMake(t *testing.T) {
	d, _, repo := setup(t)
	dir := filepath.Join(os.Getenv("PEKOE_WORKTREES"), "app", "feat-y")
	os.MkdirAll(dir, 0o755)
	keep := filepath.Join(dir, "notes.txt")
	os.WriteFile(keep, []byte("mine"), 0o644)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/y"})
	s := waitCreated(t, d, "feat-y")
	if s.State != state.Failed {
		t.Fatalf("a non-worktree directory was accepted: %+v", s)
	}
	call[any](t, d, "session.cancel", map[string]any{"slug": "feat-y"})
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("cancel deleted a directory the session did not make")
	}
}
