package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// landAgain makes a session whose earlier PR #41 is prState41 on GitHub
// (merged or closed outside pekoe land: the agent's gh pr create, a
// person), commits more in it and runs land. GitHub's other PRs (the new
// #42) are OPEN.
func landAgain(t *testing.T, prState41 string) (d *Daemon, repo, bare, gh string, s state.Session, res map[string]any) {
	t.Helper()
	d, _, repo = setup(t)
	bare = withOrigin(t, repo)
	onGitHub(t, d, repo)
	gh = fakeGH(t, "OPEN")
	os.WriteFile(filepath.Join(gh, "state-41"), []byte(prState41), 0o644)
	s = call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/again"})
	waitCreated(t, d, s.Slug)
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	d.update(s.Slug, func(x *state.Session) { x.PR = 41; x.PRStatus = &state.PRStatus{State: prState41} })
	os.WriteFile(filepath.Join(s.Path, "more.txt"), []byte("more\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "follow-up work")
	res = call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
	return
}

// land does not reuse a merged PR: the new commit would be pushed in no
// PR, and the merge watcher would tear the session down as landed with
// that commit nowhere in the base.
func TestLandOpensNewPRAfterMerged(t *testing.T) {
	d, repo, bare, gh, s, res := landAgain(t, "MERGED")
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if !strings.Contains(string(out), "pr create") {
		t.Fatalf("land reused merged PR #41 instead of opening a new one: %v (gh calls: %q)", res, out)
	}
	if res["pr"].(float64) != 42 {
		t.Fatalf("land: %v", res)
	}
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if s.PR != 42 || s.PRStatus != nil || !s.Landing {
		t.Fatalf("session after land: pr=%d pr_status=%+v landing=%v", s.PR, s.PRStatus, s.Landing)
	}
	d.watchMerges()
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if s.State == state.Landed {
		mainHas := strings.TrimSpace(gitOut(t, repo, "--git-dir", bare, "log", "--format=%s", "main"))
		t.Errorf("session torn down as landed; the follow-up commit is in no merged PR (origin main: %q)", mainHas)
	}
}

// the same with a closed PR: without a new PR the session stays landing on
// a PR that nothing ever finishes.
func TestLandOpensNewPRAfterClosed(t *testing.T) {
	_, _, _, gh, _, res := landAgain(t, "CLOSED")
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if !strings.Contains(string(out), "pr create") || res["pr"].(float64) != 42 {
		t.Fatalf("land reused closed PR #41: %v (gh calls: %q)", res, out)
	}
}

// an open PR is still the session's: land pushes to it and opens none.
func TestLandKeepsOpenPR(t *testing.T) {
	_, _, _, gh, _, res := landAgain(t, "OPEN")
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if strings.Contains(string(out), "pr create") || res["pr"].(float64) != 41 {
		t.Fatalf("land opened a new PR over open #41: %v (gh calls: %q)", res, out)
	}
}

// a session made with base "origin/main" (session.new takes it; the hold
// queue does too) lands onto origin's main as it is now, and its PR is
// against main: not fetch of a branch "origin/main" (none: the fetch
// fails and the rebase goes onto the stale tracking branch) and not gh pr
// create --base origin/main (no such branch on GitHub).
func TestLandBaseOriginPrefix(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	onGitHub(t, d, repo)
	gh := fakeGH(t, "OPEN")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/ob", "base": "origin/main"})
	waitCreated(t, d, s.Slug)
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	// someone else lands on origin's main meanwhile
	other := filepath.Join(t.TempDir(), "other")
	run(t, repo, "git", "clone", "-q", bare, other)
	os.WriteFile(filepath.Join(other, "o.txt"), []byte("o\n"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "commit", "-qm", "other")
	run(t, other, "git", "push", "-q", "origin", "HEAD:main")
	os.WriteFile(filepath.Join(s.Path, "x.txt"), []byte("x\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "x")
	res := call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if strings.Contains(string(out), "--base origin/main") || !strings.Contains(string(out), "--base main ") {
		t.Errorf("gh pr create not against main: %q", out)
	}
	if !strings.Contains(gitOut(t, s.Path, "log", "--format=%s"), "other") {
		t.Errorf("land did not rebase onto origin's main as it is now: %v", res)
	}
}

// a fetch that fails is said in the steps, not swallowed.
func TestLandFetchFailedInSteps(t *testing.T) {
	d, _, repo := setup(t)
	withOrigin(t, repo)
	onGitHub(t, d, repo)
	fakeGH(t, "OPEN")
	// base "dev" is only here, not on origin: the fetch fails, the push works
	run(t, repo, "git", "branch", "dev")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/nofetch", "base": "dev"})
	waitCreated(t, d, s.Slug)
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	os.WriteFile(filepath.Join(s.Path, "x.txt"), []byte("x\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "x")
	res := call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
	steps, _ := res["steps"].([]any)
	found := false
	for _, st := range steps {
		if strings.Contains(st.(string), "fetch origin dev failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("steps do not say the fetch failed: %v", res)
	}
}

// gh pr create says the branch already has a PR: land takes the branch's
// PR only when it is open, not a merged one gh pr view <branch> answers
// with when none is open.
func TestLandAlreadyExistsTakesOnlyOpenPR(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  float64
	}{{"OPEN", 42}, {"MERGED", 0}} {
		t.Run(tc.state, func(t *testing.T) {
			d, _, repo := setup(t)
			withOrigin(t, repo)
			onGitHub(t, d, repo)
			gh := fakeGH(t, "OPEN")
			os.WriteFile(filepath.Join(gh, "create-exists"), nil, 0o644)
			os.WriteFile(filepath.Join(gh, "branch-pr.json"), []byte(`{"number":42,"state":"`+tc.state+`","closedAt":null}`), 0o644)
			s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/exists"})
			waitCreated(t, d, s.Slug)
			s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
			os.WriteFile(filepath.Join(s.Path, "x.txt"), []byte("x\n"), 0o644)
			run(t, s.Path, "git", "add", ".")
			run(t, s.Path, "git", "commit", "-qm", "x")
			res := call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
			if got, _ := res["pr"].(float64); got != tc.want {
				t.Fatalf("land with the branch's PR %s: %v, want PR %v", tc.state, res, tc.want)
			}
		})
	}
}

// a merged PR with nothing new on the branch (a person merged the agent's
// own PR; land is pressed for the teardown): no new PR (gh pr create
// would fail with "No commits between"), the session lands on the merge.
func TestLandMergedPRWithoutNewCommits(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	onGitHub(t, d, repo)
	gh := fakeGH(t, "OPEN")
	os.WriteFile(filepath.Join(gh, "state-41"), []byte("MERGED"), 0o644)
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/done"})
	waitCreated(t, d, s.Slug)
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	os.WriteFile(filepath.Join(s.Path, "x.txt"), []byte("x\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "x")
	// PR #41 got merged: its commit is on origin's main
	run(t, s.Path, "git", "push", "-q", bare, "HEAD:main")
	d.update(s.Slug, func(x *state.Session) { x.PR = 41; x.PRStatus = &state.PRStatus{State: "MERGED"} })
	res := call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if strings.Contains(string(out), "pr create") || res["pr"].(float64) != 41 {
		t.Fatalf("land opened a new PR with no new commits: %v (gh calls: %q)", res, out)
	}
	d.watchMerges()
	if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.State != state.Landed {
		t.Fatalf("not landed on the merged PR: %s", s.State)
	}
}
