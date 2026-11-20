package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// waitGo is a hook line that waits until the file goFile exists: a step
// that stays running until the test lets it go, with no time in it.
func waitGo(goFile string) string {
	return "while [ ! -e " + shellQuote(goFile) + " ]; do sleep 0.05; done"
}

// No agent or dev server starts for a session without a worktree: tmux
// starts a window whose directory is missing in $HOME, where the agent's
// sandbox (which lets it write its working directory) would then let it
// write the whole home directory. session.restart (TUI R) and dev.toggle
// used to start them for a queued session that failed (its --hold
// dependency was removed), a landed one, a queued one, or one whose
// directory was deleted.
func TestNoWindowWithoutWorktree(t *testing.T) {
	// heldOn makes feat-b, held on feat-a (running): queued, no worktree
	heldOn := func(t *testing.T, d *Daemon, repo string) state.Session {
		call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
		waitState(t, d, "feat-a", state.Running)
		return call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": "do b", "after": []string{"feat-a"}, "hold": true})
	}
	for _, tc := range []struct {
		name   string
		method string
		window string
		// prepare answers the slug to try, in the state the case is about
		prepare func(t *testing.T, d *Daemon, repo string) string
		want    string
	}{
		{"restart of a queued session that failed", "session.restart", "agent", func(t *testing.T, d *Daemon, repo string) string {
			heldOn(t, d, repo)
			call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-a", "force": true})
			waitState(t, d, "feat-b", state.Failed)
			return "feat-b"
		}, "worktree missing"},
		{"restart of a landed session", "session.restart", "agent", func(t *testing.T, d *Daemon, repo string) string {
			s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
			waitState(t, d, "feat-a", state.Running)
			d.Eng.KillSession("feat-a")
			run(t, repo, "git", "worktree", "remove", "--force", s.Path)
			d.update("feat-a", func(x *state.Session) { x.State, x.Stopped = state.Landed, true })
			return "feat-a"
		}, "has landed"},
		{"restart of a session whose worktree was deleted", "session.restart", "agent", func(t *testing.T, d *Daemon, repo string) string {
			s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
			waitState(t, d, "feat-a", state.Running)
			call[state.Session](t, d, "session.kill", map[string]any{"slug": "feat-a"})
			os.RemoveAll(s.Path)
			return "feat-a"
		}, "worktree missing"},
		{"dev of a queued session", "dev.toggle", "dev", func(t *testing.T, d *Daemon, repo string) string {
			return heldOn(t, d, repo).Slug
		}, "is queued"},
		{"dev of a landed session", "dev.toggle", "dev", func(t *testing.T, d *Daemon, repo string) string {
			s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
			waitState(t, d, "feat-a", state.Running)
			run(t, repo, "git", "worktree", "remove", "--force", s.Path)
			d.update("feat-a", func(x *state.Session) { x.State, x.Stopped = state.Landed, true })
			return "feat-a"
		}, "has landed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, fe, repo := setup(t)
			writeRepoConfig(t, d, repo, "[hooks]\ndev = \"echo dev\"\n")
			slug := tc.prepare(t, d, repo)
			fe.mu.Lock()
			delete(fe.spawn, slug+"~"+tc.window)
			fe.mu.Unlock()
			err := callErr(d, tc.method, map[string]any{"slug": slug})
			fe.mu.Lock()
			sp, spawned := fe.spawn[slug+"~"+tc.window]
			fe.mu.Unlock()
			if spawned {
				t.Errorf("%s of %s started its %s window in %s", tc.method, slug, tc.window, sp.Dir)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s of %s: %v (want %q)", tc.method, slug, err, tc.want)
			}
		})
	}
}

// session.restart (TUI R) of a session still being created is refused,
// as session.kill is: it used to start the agent beside setup, turn the
// session idle (off max_running's count, so the next queued one started
// beyond the cap), and the creation then started the agent once more.
func TestRestartRefusesCreatingSession(t *testing.T) {
	d, fe, repo := setup(t)
	goFile := filepath.Join(t.TempDir(), "go")
	writeRepoConfig(t, d, repo, "[hooks]\nsetup = [\""+waitGo(goFile)+"\"]\n")
	setCap(d, 1)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitStepRunning(t, d, "feat-a", "setup")
	b := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": "go"})
	if b.State != state.Queued {
		t.Fatalf("feat-b: %s", b.State)
	}
	err := callErr(d, "session.restart", map[string]any{"slug": "feat-a"})
	if err == nil || !strings.Contains(err.Error(), "is being created") {
		t.Errorf("restart of creating feat-a: %v", err)
	}
	if err := callErr(d, "session.restart", map[string]any{"slug": "feat-b"}); err == nil || !strings.Contains(err.Error(), "is queued") {
		t.Errorf("restart of queued feat-b: %v", err)
	}
	d.pumpQueue()
	if a, _ := d.snapshot("feat-a"); a.State != state.Creating {
		t.Errorf("feat-a is %s while its setup runs", a.State)
	}
	if b, _ := d.snapshot("feat-b"); b.State != state.Queued {
		t.Errorf("feat-b is %s while feat-a's setup takes the one slot", b.State)
	}
	os.WriteFile(goFile, nil, 0o644)
	waitState(t, d, "feat-a", state.Running)
	fe.mu.Lock()
	spawns := strings.Count(strings.Join(fe.sent, "\n"), "spawn:agent:")
	fe.mu.Unlock()
	if spawns != 1 {
		t.Errorf("feat-a's agent started %d times (want 1)", spawns)
	}
}
