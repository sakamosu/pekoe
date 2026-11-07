package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boba/internal/state"
)

// fakeGH puts a `gh` on PATH that answers like GitHub and logs its calls.
func fakeGH(t *testing.T, prState string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "gh.log")
	script := `#!/bin/sh
echo "$@" >> ` + log + `
case "$1 $2" in
  "pr create") echo "https://github.com/me/app/pull/42" ;;
  "pr view")
    case "$*" in
      *headRefName*) echo '{"headRefName":"their-branch","baseRefName":"main","title":"Their PR"}' ;;
      *state*) echo '{"state":"` + prState + `"}' ;;
      *) echo '{"number":42}' ;;
    esac ;;
  "api --method") cat > ` + filepath.Join(dir, "review.json") + `; echo '{}' ;;
esac
`
	os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func withOrigin(t *testing.T, repo string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	run(t, repo, "git", "init", "-q", "--bare", "-b", "main", bare)
	run(t, repo, "git", "remote", "add", "origin", bare)
	run(t, repo, "git", "push", "-q", "-u", "origin", "main")
	run(t, repo, "git", "remote", "set-head", "origin", "main")
	return bare
}

func TestLandWithPRAndMergeTeardown(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	gh := fakeGH(t, "MERGED")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/land"})
	for i := 0; i < 100; i++ {
		if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.State != state.Creating {
			break
		}
		sleep()
	}
	if !strings.HasPrefix(s.Path, os.Getenv("BOBA_WORKTREES")) {
		t.Fatalf("worktree outside ~/worktrees: %s", s.Path)
	}
	os.WriteFile(filepath.Join(s.Path, "b.txt"), []byte("b\n"), 0o644)
	if _, err := d.Call("land", mustJSON(map[string]any{"slug": s.Slug})); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("land must refuse a dirty worktree: %v", err)
	}
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "b")
	res := call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
	if res["pr"].(float64) != 42 {
		t.Fatalf("land: %v", res)
	}
	// pushed to origin
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if !strings.Contains(string(out), "pr create --fill --head feat/land --base main") {
		t.Fatalf("gh calls: %s", out)
	}
	run(t, repo, "git", "--git-dir", bare, "rev-parse", "--verify", "refs/heads/feat/land")
	// merge detected → teardown, worktree removed, landed
	d.watchMerges()
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if s.State != state.Landed {
		t.Fatalf("expected landed after merge, got %s", s.State)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatal("worktree not removed after merge")
	}
}

func TestReviewSomeoneElsesPR(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	// someone pushes a PR head
	other := filepath.Join(t.TempDir(), "other")
	run(t, repo, "git", "clone", "-q", bare, other)
	os.WriteFile(filepath.Join(other, "a.txt"), []byte("1\n2\nthree\n"), 0o644)
	run(t, other, "git", "commit", "-qam", "their change")
	run(t, other, "git", "push", "-q", "origin", "HEAD:refs/pull/7/head")
	gh := fakeGH(t, "OPEN")

	s := call[state.Session](t, d, "review.pr", map[string]any{"repo": repo, "number": 7})
	if !s.PRReview || s.PR != 7 || s.State != state.Reviewing {
		t.Fatalf("pr session: %+v", s)
	}
	rv := call[ReviewView](t, d, "review.files", map[string]any{"slug": s.Slug})
	if rv.Total != 1 || rv.Files[0].Path != "a.txt" {
		t.Fatalf("pr diff: %+v", rv)
	}
	h := rv.Files[0].Hunks[0]
	call[any](t, d, "review.comment", map[string]any{"slug": s.Slug, "path": "a.txt", "line": 3, "hunk_id": h.ID, "text": "why three?"})
	res := call[map[string]any](t, d, "review.flush", map[string]any{"slug": s.Slug})
	if res["to"] != "gh#7" {
		t.Fatalf("comments must go to GitHub, got %v", res)
	}
	body, _ := os.ReadFile(filepath.Join(gh, "review.json"))
	if !strings.Contains(string(body), `"path":"a.txt"`) || !strings.Contains(string(body), `"line":3`) || !strings.Contains(string(body), "why three?") {
		t.Fatalf("review payload: %s", body)
	}
	if _, err := d.Call("land", mustJSON(map[string]any{"slug": s.Slug})); err == nil {
		t.Fatal("someone else's PR must not be landable")
	}
}

func TestWatchPRsPicksUpExternalPR(t *testing.T) {
	d, _, repo := setup(t)
	fakeGH(t, "OPEN")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/pr"})
	for i := 0; i < 100; i++ {
		if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.State != state.Creating {
			break
		}
		sleep()
	}
	if s.PR != 0 {
		t.Fatalf("PR set before any gh lookup: %d", s.PR)
	}
	d.watchPRs()
	if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.PR != 42 {
		t.Fatalf("expected PR #42 from gh, got %d", s.PR)
	}
}
