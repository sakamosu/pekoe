package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"boba/internal/paths"
	"boba/internal/state"
)

// rm racing with background updates (hooks, PR polling, issue matching)
// must not write the removed session back to state.db.
func TestRmRacingUpdateDoesNotResurrect(t *testing.T) {
	d, _, repo := setup(t)
	for i := 0; i < 30; i++ {
		s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					d.update(s.Slug, func(x *state.Session) { x.Tokens++ })
				}
			}()
		}
		call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": s.Slug, "force": true})
		close(stop)
		wg.Wait()
		list, err := d.DB.Sessions()
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range list {
			if x.Slug == s.Slug {
				t.Fatalf("round %d: removed session %s came back in state.db", i, s.Slug)
			}
		}
	}
}

// A session created before its repository had an origin keeps the path_ id;
// on the next start it is re-filed under the origin id, with the same port,
// and nothing is left under the old id.
func TestRestartRealignsStaleRepoID(t *testing.T) {
	d, _, repo := setup(t)
	main := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	feat := newSession(t, d, repo, "feat/x")
	old := main.RepoID
	if !strings.HasPrefix(old, "path_") || feat.RepoID != old {
		t.Fatalf("ids before origin: %s %s", old, feat.RepoID)
	}
	if feat.Port == 0 {
		t.Fatal("no port")
	}
	pk := &state.Pick{RepoID: old, Slug: feat.Slug, URL: "http://x", File: "/abs/0001.md"}
	if err := d.DB.AddPick(pk); err != nil {
		t.Fatal(err)
	}
	repoPick := &state.Pick{RepoID: old, URL: "http://y", File: "/abs/0002.md"} // not addressed to a session
	d.DB.AddPick(repoPick)
	os.MkdirAll(paths.PreviewDir(old, feat.Slug), 0o755)
	os.WriteFile(filepath.Join(paths.PreviewDir(old, feat.Slug), "preview.json"), []byte(`{"path":"/a"}`), 0o644)
	os.MkdirAll(paths.ReposConfigDir(), 0o755)
	os.WriteFile(paths.RepoConfig(old), []byte("[hooks]\n"), 0o644)

	run(t, repo, "git", "remote", "add", "origin", "https://github.com/acme/app.git")
	const want = "github.com_acme_app"

	// restart: a new daemon on the same state.db
	d.DB.Close()
	d.releaseLock() // as shutdown does: the next bobad needs the lock
	d2, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d2.Eng = newFake()
	t.Cleanup(func() { d2.DB.Close(); d2.releaseLock() })
	d2.restore()

	for _, slug := range []string{main.Slug, feat.Slug} {
		s, err := d2.snapshot(slug)
		if err != nil {
			t.Fatal(err)
		}
		if s.RepoID != want || s.Repo != "app" {
			t.Fatalf("%s: repo %s / %s", slug, s.RepoID, s.Repo)
		}
	}
	saved, _ := d2.DB.Sessions()
	for _, s := range saved {
		if s.RepoID != want {
			t.Fatalf("state.db still has %s under %s", s.Slug, s.RepoID)
		}
	}
	// the port is kept, and the ledger answers for the new id
	if s, _ := d2.snapshot(feat.Slug); s.Port != feat.Port {
		t.Fatalf("port changed: %d -> %d", feat.Port, s.Port)
	}
	if rid, slug, ok := d2.DB.PortOwner(feat.Port); !ok || rid != want || slug != feat.Slug {
		t.Fatalf("port owner: %s %s %v", rid, slug, ok)
	}
	if p, err := d2.DB.AllocPort(want, feat.Slug, 0, func(int) bool { return false }); err != nil || p != feat.Port {
		t.Fatalf("alloc under the new id: %d %v", p, err)
	}
	for _, id := range []int64{pk.ID, repoPick.ID} {
		if p, err := d2.DB.PickByID(id); err != nil || p.RepoID != want || p.File == "" {
			t.Fatalf("pick %d: %+v %v", id, p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(paths.PreviewDir(want, feat.Slug), "preview.json")); err != nil {
		t.Fatalf("preview dir not moved: %v", err)
	}
	if _, err := os.Stat(paths.RepoConfig(want)); err != nil {
		t.Fatalf("config not moved: %v", err)
	}
	if d2.DB.Get("dur:"+old+":worktree") != "" || d2.DB.Get("dur:"+want+":worktree") == "" {
		t.Fatal("step durations not moved")
	}
	if d2.DB.Get(repoKVPrefix+old) != "" {
		t.Fatal("old remembered repo left behind")
	}
	if rs := d2.knownRepos(); len(rs) != 1 || rs[0].ID != want || rs[0].Sessions != 2 {
		t.Fatalf("known repos: %+v", rs)
	}
	// session.list for the repository (boba ls in it) has both
	if l := call[[]state.Session](t, d2, "session.list", map[string]any{"repo_id": want}); len(l) != 2 {
		t.Fatalf("list: %d", len(l))
	}
}

// Resolving the repository (session.open, repo.list cwd, …) realigns too,
// without a restart.
func TestResolveRealignsStaleRepoID(t *testing.T) {
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	run(t, repo, "git", "remote", "add", "origin", "git@github.com:acme/app.git")
	call[[]KnownRepo](t, d, "repo.list", map[string]any{"cwd": repo})
	if got, _ := d.snapshot(s.Slug); got.RepoID != "github.com_acme_app" {
		t.Fatalf("repo id: %s", got.RepoID)
	}
}
