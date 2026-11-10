package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/gitx"
	"pekoe/internal/paths"
	"pekoe/internal/sandbox"
	"pekoe/internal/state"
)

// Filter and merge drivers have any name (the agent's .gitattributes picks
// one its config defines), so they cannot be switched off by name: they run,
// but inside pekoe's sandbox, where the human token cannot be read.
func TestGitDriversRunInSandbox(t *testing.T) {
	d, _, repo := setup(t)
	d.sbx = sandbox.New(sandbox.DefaultPolicy(d.Global.Web.StateDir), true)
	if err := d.sbx.Check(); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	gitx.SetSandbox(d.sbx.Wrap)
	t.Cleanup(func() { gitx.SetSandbox(nil) })
	s, _ := pair(t, d, repo)
	out := t.TempDir()
	leak := filepath.Join(out, "leak.sh")
	// a clean filter must pass its input through; this one also tries the token
	os.WriteFile(leak, []byte("#!/bin/sh\nprintf '%s: %s\\n' \"$1\" \"$(cat '"+paths.HumanTokenFile()+"' 2>&1)\" >> "+filepath.Join(out, "ran")+"\ncat\n"), 0o755)
	os.WriteFile(filepath.Join(s.Path, ".gitattributes"), []byte("* filter=evil merge=evil\n"), 0o644)
	run(t, s.Path, "git", "add", ".gitattributes")
	run(t, s.Path, "git", "commit", "-qm", "attributes")
	gitConfig(t, filepath.Join(repo, ".git", "config"),
		"filter.evil.clean", leak+" clean",
		"filter.evil.smudge", leak+" smudge",
		"merge.evil.driver", leak+" merge")

	gitOps(t, d, s)
	b, _ := os.ReadFile(filepath.Join(out, "ran"))
	if !strings.Contains(string(b), "clean: ") {
		t.Fatalf("the clean filter did not run (the test tests nothing): %q", b)
	}
	if strings.Contains(string(b), d.humanToken) {
		t.Fatalf("a filter driver read the human token:\n%s", b)
	}
}

// A changed .git file stops pekoed's git in that worktree and says so on
// the session (git_blocked); putting it back clears it.
func TestChangedGitFileBlocksGit(t *testing.T) {
	d, _, repo := setup(t)
	s := newSession(t, d, repo, "feat/x")
	gitFile := filepath.Join(s.Path, ".git")
	orig, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	run(t, repo, "git", "init", "-q", other)
	os.WriteFile(gitFile, []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o644)

	d.refreshDiffstat(s.Slug)
	got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if !strings.Contains(got.GitBlocked, "points at") {
		t.Fatalf("git_blocked: %q", got.GitBlocked)
	}
	if _, err := d.Call("land", mustJSON(map[string]any{"slug": s.Slug})); err == nil || !strings.Contains(err.Error(), "points at") {
		t.Fatalf("land must refuse: %v", err)
	}
	if _, err := d.Call("changes", mustJSON(map[string]any{"slug": s.Slug})); err == nil {
		t.Fatal("changes must refuse")
	}

	// a symlink is not what git worktree add makes either
	os.Remove(gitFile)
	os.Symlink(filepath.Join(other, ".git"), gitFile)
	if err := gitx.CheckPin(s.Path); err == nil {
		t.Fatal("a .git symlink must block git")
	}

	os.Remove(gitFile)
	os.WriteFile(gitFile, orig, 0o644)
	d.refreshDiffstat(s.Slug)
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.GitBlocked != "" {
		t.Fatalf("git_blocked not cleared: %q", got.GitBlocked)
	}
	call[any](t, d, "changes", map[string]any{"slug": s.Slug})

	// rm still works (refs go through the main worktree) and unpins
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": s.Slug, "force": true})
	if err := gitx.CheckPin(s.Path); err != nil {
		t.Fatalf("still pinned after rm: %v", err)
	}
}
