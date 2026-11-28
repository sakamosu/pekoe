package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// stack is a session (child) stacked on another (parent: its base is the
// parent's branch), both with a commit, the parent's pushed.
type stack struct {
	d                  *Daemon
	repo, bare, gh     string
	parent, child      state.Session
	parentTip, onMainX string
}

func newStack(t *testing.T) *stack {
	t.Helper()
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	onGitHub(t, d, repo)
	gh := fakeGH(t, "OPEN")
	// land's rebase (pekoed's git, in this process) needs a committer
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	p := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/parent"})
	waitCreated(t, d, p.Slug)
	p = call[state.Session](t, d, "session.get", map[string]any{"slug": p.Slug})
	commitFile(t, p.Path, "p.txt", "parent work")
	run(t, p.Path, "git", "push", "-q", "-u", "origin", "feat/parent")
	c := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/child", "base": "feat/parent"})
	waitCreated(t, d, c.Slug)
	c = call[state.Session](t, d, "session.get", map[string]any{"slug": c.Slug})
	if c.Base != "feat/parent" {
		t.Fatalf("child base %q", c.Base)
	}
	commitFile(t, c.Path, "c.txt", "child work")
	return &stack{d: d, repo: repo, bare: bare, gh: gh, parent: p, child: c, parentTip: strings.TrimSpace(gitOut(t, p.Path, "rev-parse", "HEAD"))}
}

func commitFile(t *testing.T, dir, name, msg string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte(msg+"\n"), 0o644)
	run(t, dir, "git", "add", name)
	run(t, dir, "git", "commit", "-qm", msg)
}

// mergeParent lands someone else's commit on origin's main, then merges
// the parent's PR into it (squash: as one new commit) and deletes the
// parent's branch on origin, as GitHub does; the repository fetches with
// --prune (its origin/feat/parent goes, the local branch stays).
func (k *stack) mergeParent(t *testing.T, squash bool) {
	t.Helper()
	o := filepath.Join(t.TempDir(), "o")
	run(t, k.repo, "git", "clone", "-q", k.bare, o)
	commitFile(t, o, "o.txt", "other work")
	if squash {
		run(t, o, "git", "merge", "-q", "--squash", "origin/feat/parent")
		run(t, o, "git", "commit", "-qm", "parent work (#41)")
	} else {
		run(t, o, "git", "merge", "-q", "--no-ff", "-m", "Merge pull request #41", "origin/feat/parent")
	}
	run(t, o, "git", "push", "-q", "origin", "HEAD:main")
	run(t, o, "git", "push", "-q", "origin", "--delete", "feat/parent")
	run(t, k.repo, "git", "fetch", "-q", "--prune", "origin")
}

// land checks the child's PR: into main, with only the child's commit.
func (k *stack) landChild(t *testing.T) map[string]any {
	t.Helper()
	res := call[map[string]any](t, k.d, "land", map[string]any{"slug": k.child.Slug})
	out, _ := os.ReadFile(filepath.Join(k.gh, "gh.log"))
	if !strings.Contains(string(out), "pr create") || strings.Contains(string(out), "--base feat/parent") || !strings.Contains(string(out), "--base main ") {
		t.Errorf("gh pr create not into main: %q (land: %v)", out, res)
	}
	run(t, k.repo, "git", "fetch", "-q", "origin")
	if got := strings.TrimSpace(gitOut(t, k.child.Path, "log", "--format=%s", "origin/main..origin/feat/child")); got != "child work" {
		t.Errorf("the PR's commits beyond main: %q, want only the child's (land: %v)", got, res)
	}
	if got := strings.TrimSpace(gitOut(t, k.child.Path, "diff", "--name-only", "origin/main...origin/feat/child")); got != "c.txt" {
		t.Errorf("the PR's files: %q, want c.txt", got)
	}
	s := call[state.Session](t, k.d, "session.get", map[string]any{"slug": k.child.Slug})
	if s.Base != "main" || s.BaseFrom != "feat/parent" {
		t.Errorf("child after land: base %q, base_from %q", s.Base, s.BaseFrom)
	}
	steps, _ := res["steps"].([]any)
	said := false
	for _, st := range steps {
		said = said || strings.Contains(st.(string), "base feat/parent was merged")
	}
	if !said {
		t.Errorf("steps do not say the base moved: %v", res)
	}
	return res
}

// The case that went wrong: the parent's PR is merged and its branch
// deleted, the child rebased onto origin's main by itself; land must not
// rebase onto the stale local feat/parent (replaying main's other commit
// onto it) and open the PR into feat/parent. Known by GitHub's merged PR.
func TestLandStackedAfterParentMergedGitHub(t *testing.T) {
	k := newStack(t)
	k.mergeParent(t, false)
	run(t, k.child.Path, "git", "rebase", "-q", "origin/main")
	os.WriteFile(filepath.Join(k.gh, "pr-list.json"), []byte(`[{"number":41,"baseRefName":"main","headRefOid":"`+k.parentTip+`"}]`), 0o644)
	k.landChild(t)
}

// the same, squash-merged, the child still on the parent's commit: the
// parent's commit (in main only as the squash) is left out.
func TestLandStackedAfterParentSquashed(t *testing.T) {
	k := newStack(t)
	k.mergeParent(t, true)
	os.WriteFile(filepath.Join(k.gh, "pr-list.json"), []byte(`[{"number":41,"baseRefName":"main","headRefOid":"`+k.parentTip+`"}]`), 0o644)
	k.landChild(t)
}

// known by the parent's session: landed, its PR merged
func TestLandStackedAfterParentSessionLanded(t *testing.T) {
	k := newStack(t)
	k.mergeParent(t, true)
	k.d.update(k.parent.Slug, func(x *state.Session) {
		x.State, x.PR = state.Landed, 41
		x.PRStatus = &state.PRStatus{State: "MERGED", HeadSHA: k.parentTip}
	})
	k.landChild(t)
}

// known by git alone (no gh answer): the branch was on origin, is gone,
// and its last commit is in main. The parent is no session of pekoed's
// (made by hand, or removed), else its own state would tell.
func TestLandStackedAfterParentGoneFromOrigin(t *testing.T) {
	k := newStack(t)
	k.mergeParent(t, false)
	k.d.mu.Lock()
	delete(k.d.sessions, k.parent.Slug)
	k.d.mu.Unlock()
	k.landChild(t)
}

// git alone does not move a base whose session is still at work and has
// not been seen merged: its branch gone from origin with its old commit in
// main says nothing about what it has now. Nor does an older merged PR of
// the branch that is not at its commit here.
func TestLandStackedParentStillWorking(t *testing.T) {
	k := newStack(t)
	k.mergeParent(t, false)
	commitFile(t, k.parent.Path, "p2.txt", "parent goes on")
	os.WriteFile(filepath.Join(k.gh, "pr-list.json"), []byte(`[{"number":41,"baseRefName":"main","headRefOid":"`+k.parentTip+`"}]`), 0o644)
	res := call[map[string]any](t, k.d, "land", map[string]any{"slug": k.child.Slug})
	if s := call[state.Session](t, k.d, "session.get", map[string]any{"slug": k.child.Slug}); s.Base != "feat/parent" || s.BaseFrom != "" {
		t.Fatalf("base moved off a parent still at work: %q from %q (land: %v)", s.Base, s.BaseFrom, res)
	}
}

// the parent landing through pekoe moves its stacked sessions' base right
// away (diffs and forecasts go against main), and the child's land later
// leaves the parent's commit out.
func TestParentLandMovesStackedBase(t *testing.T) {
	k := newStack(t)
	k.mergeParent(t, true)
	k.d.update(k.parent.Slug, func(x *state.Session) {
		x.PR = 41
		x.PRStatus = &state.PRStatus{State: "MERGED", HeadSHA: k.parentTip}
	})
	if err := k.d.finishLand(k.parent.Slug, false); err != nil {
		t.Fatal(err)
	}
	s := call[state.Session](t, k.d, "session.get", map[string]any{"slug": k.child.Slug})
	if s.Base != "main" || s.BaseFrom != "feat/parent" || s.BaseFromSHA != k.parentTip {
		t.Fatalf("child after the parent landed: base %q, base_from %q %q", s.Base, s.BaseFrom, s.BaseFromSHA)
	}
	k.landChild(t)
}

// a base that is still on origin, or never was there, stays.
func TestLandStackedParentNotMerged(t *testing.T) {
	k := newStack(t)
	res := call[map[string]any](t, k.d, "land", map[string]any{"slug": k.child.Slug})
	out, _ := os.ReadFile(filepath.Join(k.gh, "gh.log"))
	if !strings.Contains(string(out), "--base feat/parent ") {
		t.Errorf("PR not into the parent's branch: %q (land: %v)", out, res)
	}
	if s := call[state.Session](t, k.d, "session.get", map[string]any{"slug": k.child.Slug}); s.Base != "feat/parent" || s.BaseFrom != "" {
		t.Errorf("base moved: %q from %q", s.Base, s.BaseFrom)
	}
}

// gh pr create failing on a base origin does not have says so and what to
// do, not only GitHub's "No commits between".
func TestLandPRCreateFailHint(t *testing.T) {
	d, _, repo := setup(t)
	withOrigin(t, repo)
	onGitHub(t, d, repo)
	gh := fakeGH(t, "OPEN")
	os.WriteFile(filepath.Join(gh, "create-nocommits"), nil, 0o644)
	run(t, repo, "git", "branch", "dev")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/hint", "base": "dev"})
	waitCreated(t, d, s.Slug)
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	commitFile(t, s.Path, "x.txt", "x")
	err := callErr(d, "land", map[string]any{"slug": s.Slug})
	if err == nil || !strings.Contains(err.Error(), "dev is not on origin") || !strings.Contains(err.Error(), "gh pr create --base main") {
		t.Fatalf("land error: %v", err)
	}
}

// the child's PR opened into the parent's branch before the merge is
// moved into main (GitHub does that itself only when it deletes the
// branch), not left pointing at the merged branch.
func TestLandStackedMovesOpenPR(t *testing.T) {
	k := newStack(t)
	k.d.update(k.child.Slug, func(x *state.Session) { x.PR = 41 })
	k.mergeParent(t, false)
	os.WriteFile(filepath.Join(k.gh, "pr-list.json"), []byte(`[{"number":40,"baseRefName":"main","headRefOid":"`+k.parentTip+`"}]`), 0o644)
	res := call[map[string]any](t, k.d, "land", map[string]any{"slug": k.child.Slug})
	out, _ := os.ReadFile(filepath.Join(k.gh, "gh.log"))
	if !strings.Contains(string(out), "pr edit 41 --repo github.com/me/app --base main") || strings.Contains(string(out), "pr create") {
		t.Fatalf("open PR #41 not moved into main: %q (land: %v)", out, res)
	}
	if got := strings.TrimSpace(gitOut(t, k.child.Path, "log", "--format=%s", "origin/main..HEAD")); got != "child work" {
		t.Errorf("commits beyond main: %q", got)
	}
}

// a base still on origin is not moved by a merged PR of it: develop stays
// develop after its release PR into main merged.
func TestLandBaseStillOnOriginNotMoved(t *testing.T) {
	d, _, repo := setup(t)
	withOrigin(t, repo)
	onGitHub(t, d, repo)
	gh := fakeGH(t, "OPEN")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	run(t, repo, "git", "branch", "develop")
	run(t, repo, "git", "push", "-q", "-u", "origin", "develop")
	tip := strings.TrimSpace(gitOut(t, repo, "rev-parse", "develop"))
	os.WriteFile(filepath.Join(gh, "pr-list.json"), []byte(`[{"number":7,"baseRefName":"main","headRefOid":"`+tip+`"}]`), 0o644)
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/dev", "base": "develop"})
	waitCreated(t, d, s.Slug)
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	commitFile(t, s.Path, "x.txt", "x")
	res := call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if !strings.Contains(string(out), "--base develop ") {
		t.Errorf("PR not into develop: %q (land: %v)", out, res)
	}
	if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.Base != "develop" || s.BaseFrom != "" {
		t.Errorf("base moved: %q from %q", s.Base, s.BaseFrom)
	}
}
