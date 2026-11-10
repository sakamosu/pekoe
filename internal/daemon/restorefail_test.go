package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// A session left failed by its setup hook must stay failed across a daemon
// restart: no agent in the half-made worktree, and the error kept.
func TestRestoreLeavesFailedSessionAlone(t *testing.T) {
	d, fe, repo := setup(t)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte("[hooks]\nsetup = [\"false\"]\n"), 0o644)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	var s state.Session
	for i := 0; i < 100; i++ {
		if s = call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"}); s.State != state.Creating {
			break
		}
		sleep()
	}
	if s.State != state.Failed {
		t.Fatalf("setup should fail: %+v", s)
	}
	d.restore()
	if strings.Contains(fe.last(), "spawn:agent") {
		t.Fatalf("restore started an agent in a failed session: %s", fe.last())
	}
	got := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
	if got.State != state.Failed || got.Error != s.Error {
		t.Fatalf("restore must keep the failure: %+v", got)
	}
}

// A session failed only because its worktree was away resumes once the
// directory is back.
func TestRestoreResumesWhenWorktreeIsBack(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.KillSession("main")
	d.update("main", func(x *state.Session) { x.State = state.Failed; x.Error = worktreeMissing + repo })
	d.restore()
	if !strings.Contains(fe.last(), "spawn:agent") {
		t.Fatalf("expected the agent to be resumed: %s", fe.last())
	}
}
