package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// land of a session pekoe did not make (session.open of the repository's
// main checkout, on main) is refused, by an agent and by a person: it
// would rebase the checkout and push its commits straight onto origin's
// main, with no PR.
func TestLandRefusesOpenedCheckout(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	onGitHub(t, d, repo)
	ghd := fakeGH(t, "OPEN")
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	before := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	os.WriteFile(filepath.Join(repo, "direct.txt"), []byte("x\n"), 0o644)
	run(t, repo, "git", "add", "direct.txt")
	run(t, repo, "git", "commit", "-qm", "unreviewed commit on main")
	head := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	for who, err := range map[string]error{
		"agent":  callAsErr(d, agentAs("main"), "land", map[string]any{"slug": "main"}),
		"person": callErr(d, "land", map[string]any{"slug": "main"}),
	} {
		if err == nil || !strings.Contains(err.Error(), "main is not a session pekoe made") {
			t.Errorf("%s: land: %v", who, err)
		}
	}
	if got := strings.TrimSpace(gitOut(t, repo, "--git-dir", bare, "rev-parse", "main")); got != before {
		t.Fatalf("origin main moved to %s", got)
	}
	if got := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("the checkout's HEAD moved: %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(ghd, "gh.log")); strings.Contains(string(b), "pr create") {
		t.Fatalf("gh pr create ran: %s", b)
	}
	if s, _ := d.snapshot("main"); s.Landing || s.PR != 0 {
		t.Fatalf("session: landing %v, PR %d", s.Landing, s.PR)
	}
}

// land of a session pekoe made is refused when what it would push is not
// a branch of its own into its base: its branch is its base ("origin/"
// included, as land reads it), or the worktree has another branch
// checked out than the session's.
func TestLandRefusesBaseOrOtherBranch(t *testing.T) {
	for name, tc := range map[string]struct {
		make func(t *testing.T, d *Daemon, s state.Session)
		want string
	}{
		"branch is the base": {func(t *testing.T, d *Daemon, s state.Session) {
			d.update(s.Slug, func(x *state.Session) { x.Base = "feat/g" })
		}, "feat-g is on feat/g, its base"},
		"branch is origin/ base": {func(t *testing.T, d *Daemon, s state.Session) {
			d.update(s.Slug, func(x *state.Session) { x.Base = "origin/feat/g" })
		}, "feat-g is on feat/g, its base"},
		"other branch checked out": {func(t *testing.T, d *Daemon, s state.Session) {
			run(t, s.Path, "git", "checkout", "-q", "-b", "elsewhere")
		}, "the worktree has elsewhere checked out, not the session's branch feat/g"},
	} {
		t.Run(name, func(t *testing.T) {
			d, _, repo := setup(t)
			bare := withOrigin(t, repo)
			onGitHub(t, d, repo)
			fakeGH(t, "OPEN")
			call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/g"})
			s := waitCreated(t, d, "feat-g")
			os.WriteFile(filepath.Join(s.Path, "g.txt"), []byte("g\n"), 0o644)
			run(t, s.Path, "git", "add", "g.txt")
			run(t, s.Path, "git", "commit", "-qm", "g")
			tc.make(t, d, s)
			err := callAsErr(d, agentAs("feat-g"), "land", map[string]any{"slug": "feat-g"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("land: %v", err)
			}
			if out := gitOut(t, repo, "--git-dir", bare, "branch", "--list"); strings.TrimSpace(out) != "* main" && strings.TrimSpace(out) != "main" {
				t.Fatalf("pushed to origin: %q", out)
			}
		})
	}
}
