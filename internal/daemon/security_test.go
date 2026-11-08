package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/config"
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

// {{branch}} in a setup hook is one literal word for the shell.
func TestSetupHookQuotesBranch(t *testing.T) {
	d, _, repo := setup(t)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	cfg := "[hooks]\nsetup = [\"printf %s {{branch}} > marker\"]\n"
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte(cfg), 0o644)
	branch := "fix$(touch${IFS}pwned)"
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": branch})
	s = waitCreated(t, d, s.Slug)
	if s.State == state.Failed {
		t.Fatalf("setup failed: %s", s.Error)
	}
	if _, err := os.Stat(filepath.Join(s.Path, "pwned")); err == nil {
		t.Fatal("the branch name ran as a command")
	}
	if b, _ := os.ReadFile(filepath.Join(s.Path, "marker")); string(b) != branch {
		t.Fatalf("marker = %q, want the branch name as is", b)
	}
}

// A session id goes on the resume command line only when it is a plain id,
// and then in quotes.
func TestResumeSessionIDIsChecked(t *testing.T) {
	d, fe, repo := setup(t)
	d.Global.Agents["fake"] = config.Agent{Command: "fake", Resume: "fake --resume {{session_id}}"}
	b, _ := json.Marshal(map[string]any{"path": repo, "resume": "x;touch pwned", "agent": "fake"})
	if _, err := d.Call("session.open", b); err == nil {
		t.Fatal("a malformed session id was accepted")
	}
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "abc-123", "agent": "fake"})
	if got := fe.spawn[s.Slug+"~agent"].Command; !strings.HasPrefix(got, "fake --resume 'abc-123';") {
		t.Fatalf("resume command: %s", got)
	}
	// a hook payload cannot plant one either
	event(t, d, "SessionStart", s.Slug, map[string]any{"session_id": "$(touch pwned)"})
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.ClaudeSessionID != "" {
		t.Fatalf("stored %q", got.ClaudeSessionID)
	}
	event(t, d, "SessionStart", s.Slug, map[string]any{"session_id": "0b5c6e1a-2f3d-4e5f-8a9b-0c1d2e3f4a5b"})
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.ClaudeSessionID == "" {
		t.Fatal("a uuid session id was dropped")
	}
}
