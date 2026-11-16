package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// fakeSSHHost swaps sshHostOf for aliases (host → what ssh -G says; a
// host not in it fails) and returns the hosts it was asked about.
func fakeSSHHost(t *testing.T, aliases map[string]string) *[]string {
	t.Helper()
	var asked []string
	old := sshHostOf
	sshHostOf = func(h string) string {
		asked = append(asked, h)
		return aliases[h]
	}
	t.Cleanup(func() { sshHostOf = old })
	return &asked
}

func TestRemoteRepoSSHAlias(t *testing.T) {
	for _, c := range []struct {
		url, want string
		asked     []string
	}{
		// a Host alias of ~/.ssh/config
		{"git@github-work:Owner/Repo.git", "github.com/owner/repo", []string{"github-work"}},
		{"ssh://git@github-work/owner/repo", "github.com/owner/repo", []string{"github-work"}},
		{"git+ssh://git@github-work/owner/repo", "github.com/owner/repo", []string{"github-work"}},
		// ssh over 443: ssh.github.com is github.com
		{"ssh://git@ssh.github.com:443/owner/repo.git", "github.com/owner/repo", []string{"ssh.github.com"}},
		{"git@github.com:owner/repo.git", "github.com/owner/repo", []string{"github.com"}},
		// ssh fails or is not there: the host as written
		{"git@ghe-alias:team/svc.git", "ghe-alias/team/svc", []string{"ghe-alias"}},
		// not over ssh: ssh is not asked
		{"https://github-work/owner/repo", "github-work/owner/repo", nil},
		{"https://ssh.github.com/owner/repo", "ssh.github.com/owner/repo", nil},
		// a host ghRepoFrom does not pass: no repository, ssh not asked
		{"git@-oProxyCommand=x:owner/repo", "", nil},
		{"ssh://git@-oProxyCommand=x/owner/repo", "", nil},
		{"git@gh_work:owner/repo", "", nil},
		{"git@github-work:../repo", "", nil},
		{"git@github-work:owner/--upload-pack=x", "", nil},
		// ssh says a host that does not pass: the host as written
		{"git@bad-answer:owner/repo", "bad-answer/owner/repo", []string{"bad-answer"}},
	} {
		asked := fakeSSHHost(t, map[string]string{
			"github-work":    "github.com",
			"ssh.github.com": "ssh.github.com",
			"github.com":     "github.com",
			"bad-answer":     "-oProxyCommand=x",
		})
		if got := remoteRepo(c.url, sshHostOf); got != c.want {
			t.Errorf("%s: %q, want %q", c.url, got, c.want)
		}
		if !slices.Equal(*asked, c.asked) {
			t.Errorf("%s: ssh -G asked about %q, want %q", c.url, *asked, c.asked)
		}
	}
}

// fakeSSH puts an ssh on PATH that logs its arguments, cwd and environment
// and runs body.
func fakeSSH(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, "args") + "\npwd -P > " + filepath.Join(dir, "cwd") +
		"\nenv > " + filepath.Join(dir, "env") + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestSSHHostname(t *testing.T) {
	t.Setenv("GIT_DIR", "/nowhere/.git")
	t.Setenv("GH_TOKEN", "x")
	dir := fakeSSH(t, "printf 'user git\\nhostname GitHub.com\\nport 443\\n'")
	if h := sshHostname("github-work"); h != "github.com" {
		t.Fatalf("hostname: %q", h)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if strings.TrimSpace(string(args)) != "-G -- github-work" {
		t.Errorf("args: %q", args)
	}
	if cwd, _ := os.ReadFile(filepath.Join(dir, "cwd")); strings.TrimSpace(string(cwd)) != "/" {
		t.Errorf("cwd: %q", cwd)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	for _, k := range []string{"GIT_DIR=", "GH_TOKEN="} {
		if strings.Contains(string(env), k) {
			t.Errorf("ssh gets %s: %s", k, env)
		}
	}

	// a name ghRepoFrom does not pass never reaches ssh
	os.Remove(filepath.Join(dir, "args"))
	for _, h := range []string{"-oProxyCommand=x", "", "a b", "gh_work", ".x"} {
		if got := sshHostname(h); got != "" {
			t.Errorf("%q: %q", h, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "args")); err == nil {
		t.Error("ssh ran for a host that does not pass")
	}

	// ssh fails, says nothing, or says something that does not pass
	for _, body := range []string{"echo 'hostname github.com'; exit 255", "true", "echo 'hostname -oProxyCommand=x'", "echo 'hostname a b'"} {
		fakeSSH(t, body)
		if got := sshHostname("github-work"); got != "" {
			t.Errorf("%s: %q", body, got)
		}
	}
}

func TestPickGHBase(t *testing.T) {
	const (
		me = "github.com/me/app"
		up = "github.com/up/app"
	)
	for _, c := range []struct {
		name string
		rs   []ghRemote
		want ghRepoInfo
	}{
		{"origin only", []ghRemote{{Name: "origin", Repo: me}}, ghRepoInfo{me, me}},
		{"no origin", []ghRemote{{Name: "upstream", Repo: up}}, ghRepoInfo{}},
		{"origin names none", []ghRemote{{Name: "origin"}, {Name: "upstream", Repo: up}}, ghRepoInfo{}},
		{"upstream before origin", []ghRemote{{Name: "origin", Repo: me}, {Name: "upstream", Repo: up}}, ghRepoInfo{up, me}},
		{"github before origin", []ghRemote{{Name: "github", Repo: up}, {Name: "origin", Repo: me}}, ghRepoInfo{up, me}},
		{"origin before others", []ghRemote{{Name: "fork", Repo: up}, {Name: "origin", Repo: me}}, ghRepoInfo{me, me}},
		{"Upstream in any case", []ghRemote{{Name: "origin", Repo: me}, {Name: "Upstream", Repo: up}}, ghRepoInfo{up, me}},
		{"upstream on another host", []ghRemote{{Name: "origin", Repo: me}, {Name: "upstream", Repo: "gitlab.com/up/app"}}, ghRepoInfo{me, me}},
		{"upstream names none", []ghRemote{{Name: "origin", Repo: me}, {Name: "upstream"}}, ghRepoInfo{me, me}},
		{"set-default origin", []ghRemote{{Name: "origin", Repo: me, Resolved: "base"}, {Name: "upstream", Repo: up}}, ghRepoInfo{me, me}},
		{"set-default upstream", []ghRemote{{Name: "origin", Repo: me}, {Name: "upstream", Repo: up, Resolved: "base"}}, ghRepoInfo{up, me}},
		{"set-default other remote", []ghRemote{{Name: "fork", Repo: up, Resolved: "base"}, {Name: "origin", Repo: me}}, ghRepoInfo{up, me}},
		// the first remote with gh-resolved (in gh's order) decides
		{"first resolved", []ghRemote{{Name: "origin", Repo: me, Resolved: "base"}, {Name: "upstream", Repo: up, Resolved: "base"}}, ghRepoInfo{up, me}},
		// a value other than base: owner/name on the remote's host
		{"owner/name", []ghRemote{{Name: "origin", Repo: me, Resolved: "Up/App"}}, ghRepoInfo{up, me}},
		{"host/owner/name", []ghRemote{{Name: "origin", Repo: me, Resolved: "evil.example.com/up/app"}}, ghRepoInfo{up, me}},
		{"URL", []ghRemote{{Name: "origin", Repo: me, Resolved: "https://github.com/up/app.git"}}, ghRepoInfo{up, me}},
		{"same as origin", []ghRemote{{Name: "origin", Repo: me, Resolved: "me/app"}}, ghRepoInfo{me, me}},
		// a value naming no repository: none (gh fails), not a guess
		{"bad value", []ghRemote{{Name: "origin", Repo: me, Resolved: "up"}}, ghRepoInfo{Origin: me}},
		{"empty part", []ghRemote{{Name: "origin", Repo: me, Resolved: "up/"}}, ghRepoInfo{Origin: me}},
		{"too many parts", []ghRemote{{Name: "origin", Repo: me, Resolved: "a/b/c/d"}}, ghRepoInfo{Origin: me}},
		{"dot dot", []ghRemote{{Name: "origin", Repo: me, Resolved: "../app"}}, ghRepoInfo{Origin: me}},
		{"flag", []ghRemote{{Name: "origin", Repo: me, Resolved: "-up/app"}}, ghRepoInfo{Origin: me}},
		{"at file", []ghRemote{{Name: "origin", Repo: me, Resolved: "up/@etc"}}, ghRepoInfo{Origin: me}},
		{"space", []ghRemote{{Name: "origin", Repo: me, Resolved: "up/a b"}}, ghRepoInfo{Origin: me}},
	} {
		if got := pickGHBase(c.rs); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestHeadRef(t *testing.T) {
	if h := (ghRepoInfo{"github.com/me/app", "github.com/me/app"}).headRef("feat/x"); h != "feat/x" {
		t.Errorf("not a fork: %q", h)
	}
	if h := (ghRepoInfo{"github.com/up/app", "github.com/me/app"}).headRef("feat/x"); h != "me:feat/x" {
		t.Errorf("fork: %q", h)
	}
}

func TestParseGHRemotes(t *testing.T) {
	fakeSSHHost(t, map[string]string{"github-work": "github.com"})
	remoteV := "origin\tgit@github-work:me/app.git (fetch)\norigin\tgit@github-work:me/app.git (push)\n" +
		"upstream\thttps://github.com/up/app (fetch)\nupstream\tno_push (push)\nlocal\t/srv/git/app.git (fetch)\n"
	resolved := "remote.upstream.gh-resolved base\nremote.nosuch.gh-resolved base\n"
	got := parseGHRemotes(remoteV, resolved, sshHostOf)
	want := []ghRemote{
		{Name: "origin", Repo: "github.com/me/app"},
		{Name: "upstream", Repo: "github.com/up/app", Resolved: "base"},
		{Name: "local"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}

// What pekoed reads of a real repository's remotes and gh-resolved.
func TestGHRepoInfoOfRepository(t *testing.T) {
	d, _, repo := setup(t)
	fakeSSHHost(t, map[string]string{"github-work": "github.com"})
	read := func() ghRepoInfo {
		d.ghRepos.Delete(repo)
		return d.ghRepoInfoOf(repo)
	}
	run(t, repo, "git", "remote", "add", "origin", "git@github-work:me/app.git")
	if r := read(); r != (ghRepoInfo{"github.com/me/app", "github.com/me/app"}) {
		t.Fatalf("alias: %+v", r)
	}
	run(t, repo, "git", "remote", "add", "upstream", "https://github.com/up/app.git")
	if r := read(); r != (ghRepoInfo{"github.com/up/app", "github.com/me/app"}) {
		t.Fatalf("upstream: %+v", r)
	}
	if d.webBase(repo) != "https://github.com/up/app" {
		t.Fatalf("webBase: %q", d.webBase(repo))
	}
	run(t, repo, "git", "config", "remote.origin.gh-resolved", "base")
	if r := read(); r != (ghRepoInfo{"github.com/me/app", "github.com/me/app"}) {
		t.Fatalf("set-default origin: %+v", r)
	}
	run(t, repo, "git", "config", "remote.origin.gh-resolved", "up/-app")
	if r := read(); r.Base != "" || d.ghRepoOf(repo) != "" {
		t.Fatalf("bad gh-resolved: %+v", r)
	}
}

// For a fork, land pushes to origin as before and opens the PR in the
// base from origin's owner; the rest of gh goes to the base too.
func TestLandFork(t *testing.T) {
	d, _, repo := setup(t)
	withOrigin(t, repo)
	for _, p := range []string{repo, evalSymlinks(t, repo)} {
		d.ghRepos.Store(p, ghRepoInfo{Base: "github.com/up/app", Origin: "github.com/me/app"})
	}
	gh := fakeGH(t, "OPEN")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/fork"})
	for i := 0; i < 100; i++ {
		if s = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); s.State != state.Creating {
			break
		}
		sleep()
	}
	os.WriteFile(filepath.Join(s.Path, "b.txt"), []byte("b\n"), 0o644)
	run(t, s.Path, "git", "add", ".")
	run(t, s.Path, "git", "commit", "-qm", "b")
	res := call[map[string]any](t, d, "land", map[string]any{"slug": s.Slug})
	if res["pr"].(float64) != 42 {
		t.Fatalf("land: %v", res)
	}
	d.watchMerges()
	out, _ := os.ReadFile(filepath.Join(gh, "gh.log"))
	for _, want := range []string{
		"pr create --repo github.com/up/app --head me:feat/fork --base main --title=b --body-file -",
		"pr view 42 --repo github.com/up/app --json state",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("no %q in gh calls:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "me/app") {
		t.Errorf("gh asked about origin:\n%s", out)
	}
	f := d.inboxFacts(s.Slug)
	if f.PRURL != "https://github.com/up/app/pull/42" || f.PRRepo != "github.com/up/app" || f.PushRepo != "github.com/me/app" {
		t.Errorf("facts: %+v", f)
	}
}

// The merge pekoed asks a person about says the PR is in another
// repository than origin.
func TestMergeItemSaysFork(t *testing.T) {
	ghd := fakeGH(t, "OPEN")
	d, _ := mergeSetup(t)
	greenPR(mergeHead).write(t, ghd)
	s, _ := d.snapshot("main")
	run(t, s.RepoDir, "git", "remote", "add", "origin", "https://github.com/me/app.git")
	run(t, s.RepoDir, "git", "remote", "add", "upstream", "https://github.com/up/app.git")
	d.ghRepos.Delete(s.RepoDir)
	d.checkMerges()
	its := mergeItems(t, d, "open")
	if len(its) != 1 {
		t.Fatalf("items: %+v", its)
	}
	it := its[0]
	if !strings.Contains(it.Context, "fork: the PR is in github.com/up/app; the branch was pushed to github.com/me/app (origin)") {
		t.Errorf("context: %s", it.Context)
	}
	if it.Facts.PRRepo != "github.com/up/app" || !slices.Contains(it.Links, "https://github.com/up/app/pull/42") {
		t.Errorf("facts %+v links %q", it.Facts, it.Links)
	}
	for _, l := range ghLog(ghd) {
		if strings.Contains(l, "graphql") && !strings.Contains(l, "owner=up") {
			t.Errorf("gh asked about origin: %s", l)
		}
	}
}
