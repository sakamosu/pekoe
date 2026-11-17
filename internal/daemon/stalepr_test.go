package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// A branch name used before: GitHub still has the old PR of that name,
// merged (or closed) before this session was made. gh pr view <branch>
// returns it when no PR is open. It is not the session's PR: watchPRs
// and pr.view leave the session without a PR, land opens a new one, and
// the old PR's MERGED does not land the session.
func TestStaleBranchPRIsNotTheSessions(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	onGitHub(t, d, repo)
	gh := fakeGH(t, "OPEN") // the state of the PR land opens (#42)
	old := time.Now().Add(-9 * 24 * time.Hour).UTC().Format(time.RFC3339)
	os.WriteFile(filepath.Join(gh, "branch-pr.json"), []byte(fmt.Sprintf(`{"number":50,"state":"MERGED","closedAt":%q}`, old)), 0o644)
	os.WriteFile(filepath.Join(gh, "pr.json"), []byte(`{"data":{"repository":{"pullRequest":{"number":50,"state":"MERGED"}}}}`), 0o644)

	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "docs/again"})
	for i := 0; i < 100; i++ {
		if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.State != state.Creating {
			break
		}
		sleep()
	}
	d.watchPRs()
	call[any](t, d, "pr.view", map[string]any{"slug": s.Slug})
	if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.PR != 0 {
		t.Fatalf("the old merged PR taken for the session's: #%d", s.PR)
	}

	os.WriteFile(filepath.Join(s.Path, "b.txt"), []byte("b\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "b")
	res := call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
	if res["pr"].(float64) != 42 {
		t.Fatalf("land: %v", res)
	}
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if !strings.Contains(string(out), "pr create --repo github.com/me/app --head docs/again") {
		t.Fatalf("land did not open a new PR; gh calls:\n%s", out)
	}
	run(t, repo, "git", "--git-dir", bare, "rev-parse", "--verify", "refs/heads/docs/again")
	d.watchMerges()
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if s.PR != 42 || !s.Landing || s.State == state.Landed {
		t.Fatalf("session after land: pr #%d landing %v state %s", s.PR, s.Landing, s.State)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("worktree gone: %v", err)
	}
}

// The session's own PR, merged or closed outside pekoe after the session
// was made, is still found by its branch.
func TestBranchPRClosedAfterSessionIsTheSessions(t *testing.T) {
	d, _, repo := setup(t)
	onGitHub(t, d, repo)
	gh := fakeGH(t, "MERGED")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/quick"})
	for i := 0; i < 100; i++ {
		if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.State != state.Creating {
			break
		}
		sleep()
	}
	later := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	os.WriteFile(filepath.Join(gh, "branch-pr.json"), []byte(fmt.Sprintf(`{"number":60,"state":"MERGED","closedAt":%q}`, later)), 0o644)
	d.watchPRs()
	if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.PR != 60 {
		t.Fatalf("expected the session's merged PR #60, got #%d", s.PR)
	}
}
