package gitwatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	ReadConfig = GitReader(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = dir
		return c.Output()
	})
	os.Exit(m.Run())
}

func write(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

func keys(cs []Change) string {
	var ks []string
	for _, c := range cs {
		ks = append(ks, c.Key)
	}
	return strings.Join(ks, ",")
}

func gitDir(t *testing.T) string {
	g := filepath.Join(t.TempDir(), ".git")
	write(t, filepath.Join(g, "HEAD"), "ref: refs/heads/main\n", 0o644)
	write(t, filepath.Join(g, "config"), "[core]\n\tbare = false\n[user]\n\tname = a\n", 0o644)
	write(t, filepath.Join(g, "hooks", "pre-commit.sample"), "#!/bin/sh\n", 0o755)
	return g
}

func TestTakeAndDiff(t *testing.T) {
	g := gitDir(t)
	before := Take(g)
	if len(before) != 0 {
		t.Fatalf("a fresh git dir has watched entries: %v", before)
	}
	// harmless: other settings, samples, files outside hooks/
	write(t, filepath.Join(g, "config"), "[core]\n\tbare = false\n[user]\n\tname = b\n[branch \"x\"]\n\tremote = origin\n", 0o644)
	write(t, filepath.Join(g, "hooks", "commit-msg.sample"), "x", 0o755)
	write(t, filepath.Join(g, "info", "exclude"), "x", 0o644)
	if cs := Diff(before, Take(g)); len(cs) != 0 {
		t.Fatalf("harmless changes reported: %v", cs)
	}

	write(t, filepath.Join(g, "config"), `[core]
	hooksPath = .husky/_
	FSMonitor = ./x
[Alias]
	st = !sh
[includeIf "gitdir:/x/"]
	path = /tmp/inc
[filter "lfs"]
	smudge = evil
[remote "origin"]
	url = ext::sh
	fetch = +refs/heads/*:refs/remotes/origin/*
`, 0o644)
	write(t, filepath.Join(g, "hooks", "post-checkout"), "#!/bin/sh\n", 0o755)
	write(t, filepath.Join(g, "hooks", "notes"), "x", 0o644)
	os.Symlink("/elsewhere", filepath.Join(g, "hooks", "pre-push"))
	write(t, filepath.Join(g, "config.worktree"), "[core]\n\tsshCommand = x\n", 0o644)
	write(t, filepath.Join(g, "worktrees", "w1", "config.worktree"), "[core]\n\teditor = x\n", 0o644)
	// a submodule, one named a/b, and a nested one
	for _, m := range []string{"modules/sub", "modules/a/b", "modules/sub/modules/deep"} {
		write(t, filepath.Join(g, m, "HEAD"), "x\n", 0o644)
		write(t, filepath.Join(g, m, "config"), "[core]\n\tpager = x\n", 0o644)
	}
	write(t, filepath.Join(g, "modules", "sub", "hooks", "post-merge"), "x", 0o755)
	after := Take(g)
	got := keys(Diff(before, after))
	for _, want := range []string{
		"config: core.hookspath", "config: core.fsmonitor", "config: alias.st",
		"config: includeif.gitdir:/x/.path", "config: filter.lfs.smudge", "config: remote.origin.url",
		"config.worktree: core.sshcommand", "worktrees/w1/config.worktree: core.editor",
		"hooks/post-checkout", "hooks/notes", "hooks/pre-push",
		"modules/sub/config: core.pager", "modules/a/b/config: core.pager",
		"modules/sub/modules/deep/config: core.pager", "modules/sub/hooks/post-merge",
	} {
		if !strings.Contains(","+got+",", ","+want+",") {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "remote.origin.fetch") {
		t.Errorf("fetch refspec watched: %s", got)
	}
	if v := after["hooks/post-checkout"]; !strings.HasPrefix(v, "file, executable, sha256 ") {
		t.Errorf("hook: %q", v)
	}
	if v := after["hooks/notes"]; !strings.HasPrefix(v, "file, not executable, ") {
		t.Errorf("hook: %q", v)
	}
	if v := after["hooks/pre-push"]; v != "symlink to /elsewhere" {
		t.Errorf("symlink: %q", v)
	}

	// the executable bit alone, content alone, removal
	os.Chmod(filepath.Join(g, "hooks", "notes"), 0o755)
	write(t, filepath.Join(g, "hooks", "post-checkout"), "#!/bin/sh\ncurl x\n", 0o755)
	os.Remove(filepath.Join(g, "hooks", "pre-push"))
	cs := Diff(after, Take(g))
	if keys(cs) != "hooks/notes,hooks/post-checkout,hooks/pre-push" {
		t.Fatalf("changes: %v", cs)
	}
	d := Describe(cs, 10)
	if !strings.Contains(d, "- hooks/pre-push (was symlink to /elsewhere)") || !strings.Contains(d, "~ hooks/notes: file, not executable") {
		t.Errorf("describe:\n%s", d)
	}
}

// A config git cannot parse is watched by its hash: what git makes of it
// later is unknown.
func TestUnreadableConfig(t *testing.T) {
	g := gitDir(t)
	before := Take(g)
	write(t, filepath.Join(g, "config"), "[core\n\thooksPath = x\n", 0o644)
	cs := Diff(before, Take(g))
	if len(cs) != 1 || cs[0].Key != "config: (git cannot read this file)" {
		t.Fatalf("changes: %v", cs)
	}
}

func TestRisky(t *testing.T) {
	for k, want := range map[string]bool{
		"core.hookspath": true, "core.bare": false, "alias.x": true, "pager.log": true,
		"diff.foo.textconv": true, "diff.renames": false, "merge.x.driver": true, "merge.ff": false,
		"credential.helper": true, "credential.https://x.helper": true, "credential.https://x.username": false,
		"url.ext::x.insteadof": true, "remote.o.fetch": false, "submodule.s.update": true, "submodule.s.url": false,
		"includeif.gitdir:x.path": true, "include.path": true, "user.name": false, "branch.main.remote": false,
	} {
		if Risky(k) != want {
			t.Errorf("Risky(%q) = %v", k, !want)
		}
	}
}

func TestModuleDirs(t *testing.T) {
	g := gitDir(t)
	write(t, filepath.Join(g, "modules", "a", "HEAD"), "x", 0o644)
	write(t, filepath.Join(g, "modules", "a", "config"), "", 0o644)
	write(t, filepath.Join(g, "modules", "notgit", "x"), "", 0o644)
	ms := ModuleDirs(g)
	if len(ms) != 1 || ms[0] != filepath.Join(g, "modules", "a") {
		t.Fatalf("modules: %v", ms)
	}
}

// The directory core.hooksPath names (husky: .husky/_ in the main
// checkout, relative to it) is watched like hooks/.
func TestHooksPathWatched(t *testing.T) {
	g := gitDir(t)
	repo := filepath.Dir(g)
	write(t, filepath.Join(repo, ".husky", "_", "pre-commit"), "#!/bin/sh\n. h\n", 0o755)
	write(t, filepath.Join(g, "config"), "[core]\n\thooksPath = .husky/_\n", 0o644)
	before := Take(g)
	if v := before["hooksPath .husky/_/pre-commit"]; !strings.HasPrefix(v, "file, executable, ") {
		t.Fatalf("hooksPath dir not watched: %v", before)
	}
	write(t, filepath.Join(repo, ".husky", "_", "pre-commit"), "#!/bin/sh\ncurl evil\n", 0o755)
	write(t, filepath.Join(repo, ".husky", "_", "post-checkout"), "#!/bin/sh\n", 0o755)
	if got := keys(Diff(before, Take(g))); got != "hooksPath .husky/_/post-checkout,hooksPath .husky/_/pre-commit" {
		t.Fatalf("changes: %s", got)
	}
	// /dev/null (pekoed's own git) and the default place add nothing
	write(t, filepath.Join(g, "config"), "[core]\n\thooksPath = /dev/null\n", 0o644)
	for k := range Take(g) {
		if strings.HasPrefix(k, "hooksPath") {
			t.Fatalf("watched %s", k)
		}
	}
}
