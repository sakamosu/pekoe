package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// symlinkedWorktrees points PEKOE_WORKTREES through a symlink (like /tmp →
// /private/tmp on macOS), so a session's stored path and git's
// --show-toplevel name the same directory differently.
func symlinkedWorktrees(t *testing.T) {
	t.Helper()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "lnk")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEKOE_WORKTREES", filepath.Join(link, "wt"))
}

// NG1: opening a session's worktree by another name of it (through a
// symlink, or the resolved name) is the same worktree: refused, not a
// second session (whose rm would delete the first one's worktree).
func TestOpenRefusesWorktreeBySymlinkedPath(t *testing.T) {
	d, _, repo := setup(t)
	symlinkedWorktrees(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/r"})
	s := waitState(t, d, "feat-r", state.Idle, state.Running, state.Waiting)
	if !strings.Contains(s.Path, "lnk") {
		t.Fatalf("worktree path is not the symlinked one: %s", s.Path)
	}
	for _, p := range []string{s.Path, realpath(s.Path), filepath.Join(s.Path, ".")} {
		_, err := d.Call("session.open", mustJSON(map[string]any{"path": p, "resume": "new"}))
		if err == nil || !strings.Contains(err.Error(), "already managed as feat-r") {
			t.Errorf("open %s: want refused as feat-r, got %v", p, err)
		}
	}
	if ss, _ := d.DB.Sessions(); len(ss) != 1 {
		t.Fatalf("want 1 session, got %d", len(ss))
	}
}

// A duplicate already in the state (made before the fix) must not delete
// the worktree another session still lives in when it is removed.
func TestRmKeepsWorktreeSharedBySymlinkedPath(t *testing.T) {
	d, _, repo := setup(t)
	symlinkedWorktrees(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/r"})
	s := waitState(t, d, "feat-r", state.Idle, state.Running, state.Waiting)
	dup := s
	dup.Slug, dup.Path, dup.Port = "demo-feat-r", realpath(s.Path), s.Port+1
	dup.State, dup.StateSince, dup.CreatedAt = state.Idle, time.Now(), time.Now()
	d.add(&dup)

	res := call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "demo-feat-r", "force": true})
	if res["worktree_deleted"] == true {
		t.Fatal("rm reported deleting the shared worktree")
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("worktree of feat-r was deleted: %v", err)
	}
	if _, err := d.snapshot("feat-r"); err != nil {
		t.Fatalf("feat-r gone: %v", err)
	}
}

// Landing such a duplicate leaves the worktree to the session still in it.
func TestLandKeepsWorktreeSharedBySymlinkedPath(t *testing.T) {
	d, _, repo := setup(t)
	symlinkedWorktrees(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/r"})
	s := waitState(t, d, "feat-r", state.Idle, state.Running, state.Waiting)
	dup := s
	dup.Slug, dup.Path, dup.Port = "demo-feat-r", realpath(s.Path), s.Port+1
	dup.State, dup.StateSince, dup.CreatedAt = state.Idle, time.Now(), time.Now()
	d.add(&dup)

	if err := d.finishLanding("demo-feat-r"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("worktree of feat-r was deleted: %v", err)
	}
}

// The worktree list matches git's (resolved) paths to sessions made under a
// symlinked PEKOE_WORKTREES.
func TestWorktreeListMatchesSymlinkedSession(t *testing.T) {
	d, _, repo := setup(t)
	symlinkedWorktrees(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/r"})
	s := waitState(t, d, "feat-r", state.Idle, state.Running, state.Waiting)
	wts := call[[]WorktreeView](t, d, "worktree.list", map[string]any{})
	found := false
	for _, w := range wts {
		if samePath(w.Path, s.Path) {
			found = true
			if w.Slug != "feat-r" || w.Port != s.Port {
				t.Fatalf("worktree %s not matched to feat-r: %+v", w.Path, w)
			}
		}
	}
	if !found {
		t.Fatalf("feat-r's worktree not listed: %+v", wts)
	}
}
