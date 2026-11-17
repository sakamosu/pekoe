package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// landing a session pekoe did not create (session.open on the
// repository's own checkout) runs no [hooks] teardown there, as rm does
// not: no setup ever ran in it, and a teardown such as
// `dropdb app_{{slug}}` would drop the main checkout's DB.
func TestFinishLandOfOpenedCheckoutRunsNoTeardown(t *testing.T) {
	d, _, repo := setup(t)
	marker := filepath.Join(t.TempDir(), "teardown-ran")
	writeRepoConfig(t, d, repo, "[hooks]\nteardown = [\"echo {{slug}} > "+marker+"\"]\n")
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	if s.Managed {
		t.Fatalf("expected an unmanaged session: %+v", s)
	}
	d.update(s.Slug, func(x *state.Session) { x.Landing, x.PR = true, 7 })
	if err := d.finishLand(s.Slug, false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Errorf("teardown ran in the main checkout %s for slug %s", repo, strings.TrimSpace(string(b)))
	}
	if x, _ := d.snapshot(s.Slug); x.State != state.Landed {
		t.Errorf("not landed: %s", x.State)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Errorf("the main checkout went: %v", err)
	}
}

// a session whose landing was held (merged while its agent worked) is
// counted merged when pekoe first saw the merge, not when the hold ended:
// a merge seen at 23:50 and released at 00:10 belongs to the first day.
func TestHeldLandingKeepsFirstSeenMergeTime(t *testing.T) {
	d, _ := landingSetup(t)
	d.setState("feat-x", state.Running, "")
	before := time.Now()
	d.watchMerges()
	waitFor(t, func() bool {
		x := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
		_, busy := d.finishing.Load("feat-x")
		return x.LandHeld != "" && !busy
	})
	seen := time.Now()
	time.Sleep(300 * time.Millisecond)
	d.watchMerges() // still held: the first sighting stays
	time.Sleep(300 * time.Millisecond)
	released := time.Now()
	d.setState("feat-x", state.Done, "")
	waitLanded(t, d, "feat-x")
	recs, err := d.DB.SessionRecords(before.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var got *time.Time
	for _, r := range recs {
		if r.Slug == "feat-x" {
			got = r.MergedAt
		}
	}
	if got == nil {
		t.Fatalf("no merge recorded: %+v", recs)
	}
	if got.Before(before) || got.After(seen) {
		t.Errorf("merged_at %s, want when the merge was first seen (%s … %s); the hold ended at %s",
			got.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), seen.Format(time.RFC3339Nano), released.Format(time.RFC3339Nano))
	}
}

// land whose fetch of origin fails, with the session's earlier PR merged:
// the local base may be behind origin's (without the merged PR's commits),
// so "commits beyond the base" cannot be told. land does not go on to gh
// pr create (GitHub answers "No commits between" when there are none):
// it stops with an error that says the fetch failed and what to do.
func TestLandFetchFailedAfterMergedPR(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	onGitHub(t, d, repo)
	gh := fakeGH(t, "OPEN")
	os.WriteFile(filepath.Join(gh, "state-41"), []byte("MERGED"), 0o644)
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/stale"})
	waitCreated(t, d, s.Slug)
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	os.WriteFile(filepath.Join(s.Path, "x.txt"), []byte("x\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "x")
	// PR #41 got merged on GitHub: its commit is on origin's main, not on
	// the main here
	run(t, s.Path, "git", "push", "-q", bare, "HEAD:main")
	d.update(s.Slug, func(x *state.Session) { x.PR = 41; x.PRStatus = &state.PRStatus{State: "MERGED"} })
	// origin cannot be fetched now (the network is down); push still works
	run(t, repo, "git", "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	run(t, repo, "git", "remote", "set-url", "--push", "origin", bare)
	err := callErr(d, "land", map[string]any{"slug": s.Slug})
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	if strings.Contains(string(out), "pr create") {
		t.Fatalf("land went on to gh pr create against a stale base (gh calls: %q)", out)
	}
	if err == nil || !strings.Contains(err.Error(), "fetch") || !strings.Contains(err.Error(), "#41") {
		t.Fatalf("land: %v, want an error naming the failed fetch and PR #41", err)
	}
	if x, _ := d.snapshot(s.Slug); x.PR != 41 || x.State == state.Landed {
		t.Fatalf("session after land: pr=%d state=%s", x.PR, x.State)
	}
}
