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

// land whose fetch of origin fails, with the session's earlier PR #41
// merged: the local base may be behind origin's (without #41's commits),
// which then count as new. GitHub, which has origin's base, decides: with
// nothing new (gh pr create says "No commits between") land keeps #41 and
// does not fail, and #41's merge lands the session; with new commits it
// opens a new PR, as with a fetch that worked.
func TestLandFetchFailedAfterMergedPR(t *testing.T) {
	for _, tc := range []struct {
		name    string
		newWork bool
		want    float64
		landed  bool
	}{{"nothing new", false, 41, true}, {"new commits", true, 42, false}} {
		t.Run(tc.name, func(t *testing.T) {
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
			// PR #41 got merged on GitHub: its commit is on origin's main,
			// not on the main here
			run(t, s.Path, "git", "push", "-q", bare, "HEAD:main")
			d.update(s.Slug, func(x *state.Session) { x.PR = 41; x.PRStatus = &state.PRStatus{State: "MERGED"} })
			if tc.newWork {
				os.WriteFile(filepath.Join(s.Path, "more.txt"), []byte("more\n"), 0o644)
				run(t, s.Path, "git", "add", ".")
				run(t, s.Path, "git", "commit", "-qm", "follow-up work")
			} else {
				os.WriteFile(filepath.Join(gh, "create-nocommits"), nil, 0o644)
			}
			// origin cannot be fetched now (the network is down); push still works
			run(t, repo, "git", "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
			run(t, repo, "git", "remote", "set-url", "--push", "origin", bare)
			var res map[string]any
			if err := callErr(d, "land", map[string]any{"slug": s.Slug}); err != nil {
				out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
				t.Fatalf("land: %v (gh calls: %q)", err, out)
			}
			res = call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug}) // again: the same answer
			if got, _ := res["pr"].(float64); got != tc.want {
				t.Fatalf("land: %v, want PR %v", res, tc.want)
			}
			if x, _ := d.snapshot(s.Slug); !x.Landing || float64(x.PR) != tc.want {
				t.Fatalf("session after land: pr=%d landing=%v", x.PR, x.Landing)
			}
			d.watchMerges()
			if x := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); (x.State == state.Landed) != tc.landed {
				t.Fatalf("after watchMerges: %s, want landed %v", x.State, tc.landed)
			}
		})
	}
}
