package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/paths"
	"pekoe/internal/state"
)

func knows(d *Daemon, id string) bool {
	for _, r := range d.knownRepos() {
		if r.ID == id {
			return true
		}
	}
	return false
}

// repo.forget: refused while a session is left or a config file is there
// (unless --config), then drops landed sessions, what the state DB
// remembers and the config; the repository, its branches and the stats
// stay, nothing finds it again, and opening it makes it known again.
func TestRepoForget(t *testing.T) {
	d, _, app := setup(t)
	s := readySession(t, d, app, "feat/x")
	id := s.RepoID
	if !knows(d, id) || d.DB.Get(repoKVPrefix+id) == "" {
		t.Fatalf("app not known / remembered after session.new")
	}

	// human only
	c := dialPipe(t, d)
	if e := c.call("repo.forget", map[string]any{"repo": "app"}, ""); !strings.Contains(e, "only a person can do this") {
		t.Fatalf("agent: error = %q", e)
	}

	// a session that has not landed: refused, named
	err := callErr(d, "repo.forget", map[string]any{"repo": "app"})
	if err == nil || !strings.Contains(err.Error(), s.Slug) || !strings.Contains(err.Error(), "pekoe rm") {
		t.Fatalf("live session: %v", err)
	}

	// landed, but its worktree is still there: refused too
	d.update(s.Slug, func(x *state.Session) { x.State = state.Landed })
	if err := callErr(d, "repo.forget", map[string]any{"repo": "app"}); err == nil || !strings.Contains(err.Error(), "still there") {
		t.Fatalf("landed with worktree: %v", err)
	}
	d.update(s.Slug, func(x *state.Session) { x.State = state.Running })
	d.update(s.Slug, func(x *state.Session) { x.Landing, x.PR = true, 7 })
	if err := d.finishLanding(s.Slug); err != nil {
		t.Fatal(err)
	}
	branches := branchList(t, app)

	// a config file a person wrote: kept, and refused without --config
	os.MkdirAll(paths.ReposConfigDir(), 0o755)
	cfg := paths.RepoConfig(id)
	os.WriteFile(cfg, []byte("agent = \"claude\"\n"), 0o644)
	err = callErr(d, "repo.forget", map[string]any{"repo": "app"})
	if err == nil || !strings.Contains(err.Error(), "--config") {
		t.Fatalf("config file: %v", err)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("config removed without --config: %v", err)
	}

	// dry run: says what goes, changes nothing
	plan := call[RepoForget](t, d, "repo.forget", map[string]any{"repo": "app", "config": true, "dry_run": true})
	if plan.ID != id || !plan.Remembered || len(plan.Landed) != 1 || plan.Landed[0] != s.Slug || plan.ConfigFile != cfg || plan.ConfigGone {
		t.Fatalf("dry run: %+v", plan)
	}
	if _, err := d.get(s.Slug); err != nil || !knows(d, id) {
		t.Fatalf("dry run changed something: %v", err)
	}

	res := call[RepoForget](t, d, "repo.forget", map[string]any{"repo": app, "config": true})
	if res.ID != id || !res.ConfigGone || len(res.Landed) != 1 {
		t.Fatalf("forget: %+v", res)
	}
	if _, err := d.get(s.Slug); err == nil {
		t.Fatal("landed session kept")
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("config kept: %v", err)
	}
	if d.DB.Get(repoKVPrefix+id) != "" {
		t.Fatal("still remembered")
	}
	// gone from repo.list and from what tapi is told, every time it is asked
	for i := 0; i < 2; i++ {
		for _, r := range call[[]KnownRepo](t, d, "repo.list", nil) {
			if r.ID == id {
				t.Fatalf("repo.list still lists it: %+v", r)
			}
		}
		if rs, _ := d.tapiAddDirs(); len(rs) != 0 {
			t.Fatalf("tapi still knows: %+v", rs)
		}
	}
	if d.DB.Get(repoKVPrefix+id) != "" {
		t.Fatal("remembered again")
	}
	// untouched: the repository, its branches, the stats
	if fi, err := os.Stat(app); err != nil || !fi.IsDir() {
		t.Fatalf("repository: %v", err)
	}
	if got := branchList(t, app); got != branches {
		t.Fatalf("branches %q, were %q", got, branches)
	}
	if recs, _ := d.DB.SessionRecords(time.Now().Add(-time.Hour)); len(recs) != 1 || recs[0].PR != 7 || recs[0].MergedAt == nil {
		t.Fatalf("stats records: %+v", recs)
	}
	if st := call[Stats](t, d, "stats", map[string]any{"days": 1}); st.Merged != 1 {
		t.Fatalf("stats: %+v", st)
	}

	// nothing left to forget
	if err := callErr(d, "repo.forget", map[string]any{"repo": app}); err == nil || !strings.Contains(err.Error(), "nothing to forget") {
		t.Fatalf("again: %v", err)
	}

	// open makes it known again
	call[state.Session](t, d, "session.open", map[string]any{"path": app, "resume": "new"})
	if !knows(d, id) {
		t.Fatal("not known after open")
	}
}

// Remembered only (no session, no config): forgotten without --config.
func TestRepoForgetRememberedOnly(t *testing.T) {
	d, _, _ := setup(t)
	web := gitRepo(t, "x", "web")
	call[[]KnownRepo](t, d, "repo.list", map[string]any{"cwd": web})
	r := call[KnownRepo](t, d, "repo.resolve", map[string]any{"repo": "web"})
	res := call[RepoForget](t, d, "repo.forget", map[string]any{"repo": "web"})
	if !res.Remembered || len(res.Landed) != 0 || res.ConfigFile != "" {
		t.Fatalf("forget: %+v", res)
	}
	if knows(d, r.ID) {
		t.Fatal("still known")
	}
}

func branchList(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "branch", "--list").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// repo.forget and a session.new of the same repository at the same time:
// one comes wholly before the other. Either forget refuses (the session
// is there), or the session is made after it and pekoe knows the
// repository again. Never does forget succeed and leave a session of the
// repository behind with repo:<id> and its config file deleted under it.
func TestRepoForgetRacesNew(t *testing.T) {
	d, _, app := setup(t)
	id := call[KnownRepo](t, d, "repo.resolve", map[string]any{"repo": app}).ID
	cfg := paths.RepoConfig(id)
	os.MkdirAll(paths.ReposConfigDir(), 0o755)
	const n = 20
	hits := 0
	for i := 0; i < n; i++ {
		call[[]KnownRepo](t, d, "repo.list", map[string]any{"cwd": app}) // remembered
		os.WriteFile(cfg, []byte("agent = \"claude\"\n"), 0o644)
		var wg sync.WaitGroup
		var ferr, nerr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			nerr = callErr(d, "session.new", map[string]any{"repo": app, "branch": fmt.Sprintf("f/x%d", i)})
		}()
		go func() {
			defer wg.Done()
			ferr = callErr(d, "repo.forget", map[string]any{"repo": app, "config": true})
		}()
		wg.Wait()
		live := 0
		d.mu.Lock()
		for _, s := range d.sessions {
			if s.RepoID == id {
				live++
			}
		}
		d.mu.Unlock()
		_, cfgErr := os.Stat(cfg)
		if ferr == nil && nerr == nil && live > 0 && os.IsNotExist(cfgErr) && d.DB.Get(repoKVPrefix+id) == "" {
			hits++
		}
		waitCreations(t, d)
		d.mu.Lock()
		var slugs []string
		for slug := range d.sessions {
			slugs = append(slugs, slug)
		}
		d.mu.Unlock()
		for _, slug := range slugs {
			callErr(d, "worktree.rm", map[string]any{"slug": slug, "force": true})
		}
	}
	if hits > 0 {
		t.Fatalf("%d/%d: forget succeeded while session.new made a session of the repository (repo:%s and its config deleted under it)", hits, n, id)
	}
}

// A repo.list while repo.forget --config deletes (the TUI reloads it on
// the session.removed forget sends for a landed session) must not find
// the config file left after repo:<id> is gone, locate the repository and
// remember it again: forget would succeed and pekoe still know it.
func TestRepoForgetRacesRepoList(t *testing.T) {
	d, _, app := setup(t)
	id := call[KnownRepo](t, d, "repo.resolve", map[string]any{"repo": app}).ID
	cfg := paths.RepoConfig(id)
	os.MkdirAll(paths.ReposConfigDir(), 0o755)
	call[[]KnownRepo](t, d, "repo.list", map[string]any{"cwd": app}) // remembered
	os.WriteFile(cfg, []byte("agent = \"claude\"\n"), 0o644)
	ran := false
	defer func(f func()) { forgetBetween = f }(forgetBetween)
	forgetBetween = func() {
		ran = true
		d.mu.Lock()
		d.locateMiss = nil // searched for at once
		d.mu.Unlock()
		callErr(d, "repo.list", map[string]any{})
	}
	call[RepoForget](t, d, "repo.forget", map[string]any{"repo": app, "config": true})
	if !ran {
		t.Fatal("forgetBetween did not run")
	}
	if knows(d, id) || d.DB.Get(repoKVPrefix+id) != "" {
		t.Fatalf("forget succeeded but the repository is known again (repo:%s written back)", id)
	}
}
