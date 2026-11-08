package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boba/internal/gitx"
	"boba/internal/paths"
	"boba/internal/state"
)

// gitRepo makes a repository with one commit at <tmp>/<parent>/<name>.
func gitRepo(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), parent, name)
	os.MkdirAll(dir, 0o755)
	run(t, dir, "git", "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-qm", "init")
	return dir
}

func callErr(d *Daemon, method string, params any) error {
	b, _ := json.Marshal(params)
	_, err := d.Call(method, b)
	return err
}

// newSession runs session.new and waits until setup is over.
func newSession(t *testing.T, d *Daemon, repo, branch string) state.Session {
	t.Helper()
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": branch})
	for i := 0; i < 200; i++ {
		got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
		if got.State != state.Creating {
			return got
		}
		sleep()
	}
	t.Fatalf("%s stuck in creating", s.Slug)
	return s
}

func TestSlugCollisionGetsRepoName(t *testing.T) {
	d, _, app := setup(t)
	web := gitRepo(t, "x", "web")
	web2 := gitRepo(t, "y", "web") // another repository with the same short name

	a := newSession(t, d, app, "feat/x")
	w := newSession(t, d, web, "feat/x")
	w2 := newSession(t, d, web2, "feat/x")
	if a.Slug != "feat-x" || w.Slug != "web-feat-x" || w2.Slug != "web-feat-x-2" {
		t.Fatalf("slugs: %s %s %s", a.Slug, w.Slug, w2.Slug)
	}
	if w.Path != paths.Worktree("web", "web-feat-x") {
		t.Fatalf("worktree path: %s", w.Path)
	}
	// the same branch again in the same repository is still "already exists"
	if err := callErr(d, "session.new", map[string]any{"repo": web, "branch": "feat/x"}); err == nil || !strings.Contains(err.Error(), "web-feat-x already exists") {
		t.Fatalf("re-new: %v", err)
	}
	// a slug of this repository is never renamed
	if s := d.uniqueSlug("feat-x", a.RepoID, "app"); s != "feat-x" {
		t.Fatalf("own slug: %s", s)
	}
}

func TestResolveRepoRef(t *testing.T) {
	d, _, app := setup(t)
	web := gitRepo(t, "x", "web")
	other := gitRepo(t, "y", "app") // shares the short name "app"
	for _, p := range []string{app, web} {
		call[state.Session](t, d, "session.open", map[string]any{"path": p, "resume": "new"})
	}
	resolve := func(q, cwd string) (*KnownRepo, error) {
		var r KnownRepo
		b, _ := json.Marshal(map[string]any{"repo": q, "cwd": cwd})
		res, err := d.Call("repo.resolve", b)
		if err != nil {
			return nil, err
		}
		rb, _ := json.Marshal(res)
		json.Unmarshal(rb, &r)
		return &r, nil
	}
	webR, err := resolve("web", "")
	if err != nil || webR.Name != "web" || webR.Sessions != 1 {
		t.Fatalf("short name: %+v %v", webR, err)
	}
	if mustReal(t, webR.Dir) != mustReal(t, web) {
		t.Fatalf("dir: %s", webR.Dir)
	}
	// repo-id
	if r, err := resolve(webR.ID, ""); err != nil || r.ID != webR.ID {
		t.Fatalf("repo-id: %+v %v", r, err)
	}
	// absolute path, relative path, and a path inside a worktree of the repository
	if r, err := resolve(other, ""); err != nil || r.Name != "app" || r.Sessions != 0 {
		t.Fatalf("path: %+v %v", r, err)
	}
	if r, err := resolve("./web", filepath.Dir(web)); err != nil || r.ID != webR.ID {
		t.Fatalf("relative path: %+v %v", r, err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, web, "git", "worktree", "add", "-q", "-b", "side", wt)
	if r, err := resolve(wt, ""); err != nil || r.ID != webR.ID {
		t.Fatalf("worktree path: %+v %v", r, err)
	}
	// a bare directory name next to cwd that is not a known name
	lib := gitRepo(t, "z", "lib")
	if r, err := resolve("lib", filepath.Dir(lib)); err != nil || r.Name != "lib" {
		t.Fatalf("dir name: %+v %v", r, err)
	}
	// once "other" has a session too, "app" is ambiguous and lists both
	call[state.Session](t, d, "session.open", map[string]any{"path": other, "resume": "new"})
	_, err = resolve("app", "")
	if err == nil || !strings.Contains(err.Error(), "matches 2 repositories") || strings.Count(err.Error(), "path_") != 2 {
		t.Fatalf("ambiguous: %v", err)
	}
	// unknown names list the known ones
	_, err = resolve("nope", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unknown repo: nope") || !strings.Contains(err.Error(), "web") {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := resolve(t.TempDir(), ""); err == nil {
		t.Fatal("a directory that is not a repository must fail")
	}
}

func TestKnownReposIncludeConfigOnly(t *testing.T) {
	d, _, app := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": app, "resume": "new"})
	os.MkdirAll(paths.ReposConfigDir(), 0o755)
	os.WriteFile(paths.RepoConfig("github.com_me_my_api"), []byte("agent = \"claude\"\n"), 0o644)
	rs := call[[]KnownRepo](t, d, "repo.list", nil)
	if len(rs) != 2 {
		t.Fatalf("repo.list: %+v", rs)
	}
	var api *KnownRepo
	for i := range rs {
		if rs[i].ID == "github.com_me_my_api" {
			api = &rs[i]
		}
	}
	if api == nil || api.Name != "my_api" || !api.Config || api.Dir != "" || api.Sessions != 0 {
		t.Fatalf("config-only repo: %+v", api)
	}
	r := call[KnownRepo](t, d, "repo.resolve", map[string]any{"repo": "my_api"})
	if r.ID != "github.com_me_my_api" {
		t.Fatalf("resolve config-only: %+v", r)
	}
	if nameFromID("path_Users_me_src_app") != "app" {
		t.Fatal("nameFromID path")
	}
}

func TestRepoQualifiedSlug(t *testing.T) {
	d, fe, app := setup(t)
	web := gitRepo(t, "x", "web")
	a := newSession(t, d, app, "feat/x")
	newSession(t, d, web, "feat/x") // web-feat-x
	appID := a.RepoID

	get := func(ref string) (string, error) {
		b, _ := json.Marshal(map[string]any{"slug": ref})
		res, err := d.Call("session.get", b)
		if err != nil {
			return "", err
		}
		return res.(*state.Session).Slug, nil
	}
	for ref, want := range map[string]string{
		"app/feat-x":      "feat-x",
		"web/feat-x":      "web-feat-x", // the slug as the branch would make it
		"web/web-feat-x":  "web-feat-x", // or the real slug
		appID + "/feat-x": "feat-x",
		"feat-x":          "feat-x",
	} {
		if got, err := get(ref); err != nil || got != want {
			t.Errorf("%s → %q, %v (want %s)", ref, got, err, want)
		}
	}
	for _, ref := range []string{"nope/feat-x", "web/nope", "web/", "/feat-x", "a/b/c"} {
		if _, err := get(ref); err == nil {
			t.Errorf("%s should fail", ref)
		}
	}
	// any method taking {slug} takes the qualified form
	call[any](t, d, "session.send", map[string]any{"slug": "web/feat-x", "text": "hi", "submit": false})
	if !strings.Contains(fe.last(), "hi") {
		t.Fatalf("send: %s", fe.last())
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func repoByName(rs []KnownRepo, name string) *KnownRepo {
	for i := range rs {
		if rs[i].Name == name {
			return &rs[i]
		}
	}
	return nil
}

// repo.list {cwd} (the TUI): the cwd's repository, also from inside one of
// its worktrees, is remembered and marked; configs are located when their
// repo-id spells a path.
func TestRepoListCwdAndRemembered(t *testing.T) {
	d, _, app := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": app, "resume": "new"})

	web := mustReal(t, gitRepo(t, "x", "web"))
	wt := filepath.Join(t.TempDir(), "web-wt")
	run(t, web, "git", "worktree", "add", "-q", "-b", "feat/x", wt)

	my := mustReal(t, gitRepo(t, "y", "my_tool")) // underscore: ambiguous in a path_ id
	myID, _ := gitx.RepoID(my)
	os.MkdirAll(paths.ReposConfigDir(), 0o755)
	os.WriteFile(paths.RepoConfig(myID), []byte(""), 0o644)
	os.WriteFile(paths.RepoConfig("github.com_me_ghost"), []byte(""), 0o644)

	rs := call[[]KnownRepo](t, d, "repo.list", map[string]any{"cwd": wt})
	if r := repoByName(rs, "app"); r == nil || r.Sessions != 1 || r.Cwd {
		t.Fatalf("app: %+v", rs)
	}
	if r := repoByName(rs, "web"); r == nil || !r.Cwd || mustReal(t, r.Dir) != web || r.Sessions != 0 {
		t.Fatalf("web (cwd inside its worktree resolves to the main worktree): %+v", rs)
	}
	if r := repoByName(rs, "my_tool"); r == nil || mustReal(t, r.Dir) != my || !r.Config {
		t.Fatalf("my_tool (config only, path_ id): %+v", rs)
	}
	if r := repoByName(rs, "ghost"); r == nil || r.Dir != "" || !r.Config {
		t.Fatalf("ghost (config, path unknown): %+v", rs)
	}

	// remembered: still listed when asked from elsewhere, and resolvable by name
	rs = call[[]KnownRepo](t, d, "repo.list", map[string]any{"cwd": t.TempDir()})
	if r := repoByName(rs, "web"); r == nil || r.Cwd || mustReal(t, r.Dir) != web {
		t.Fatalf("web should be remembered: %+v", rs)
	}
	for _, r := range rs {
		if r.Cwd {
			t.Fatalf("outside every repository nothing is cwd: %+v", r)
		}
	}
	if r := call[KnownRepo](t, d, "repo.resolve", map[string]any{"repo": "web"}); mustReal(t, r.Dir) != web || r.Cwd {
		t.Fatalf("resolve remembered: %+v", r)
	}
	// without cwd (the CLI) nothing is marked
	for _, r := range call[[]KnownRepo](t, d, "repo.list", nil) {
		if r.Cwd {
			t.Fatalf("no cwd, no mark: %+v", r)
		}
	}
}
