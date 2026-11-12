package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

func gitWatchItems(t *testing.T, d *Daemon) []state.InboxItem {
	t.Helper()
	its, err := d.DB.InboxList(state.InboxFilter{From: callerPekoed})
	if err != nil {
		t.Fatal(err)
	}
	var out []state.InboxItem
	for _, it := range its {
		if strings.Contains(it.Question, "git hooks / config") {
			out = append(out, it)
		}
	}
	return out
}

func repoHooks(t *testing.T, d *Daemon, repo, test string) {
	t.Helper()
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte("[hooks]\ntest = '''"+test+"'''\n"), 0o644)
}

// A [hooks] test that installs a git hook and sets core.hooksPath (what
// husky does) gets a person an inbox item from pekoed naming the session,
// the hook and what changed; pekoed leaves the change as it is. A run that
// changes nothing watched (user.name, a sample hook) posts nothing.
func TestGitWatchReportsHooksAndConfig(t *testing.T) {
	d, _, repo := setup(t)
	git := filepath.Join(repo, ".git")
	repoHooks(t, d, repo, `printf '#!/bin/sh\necho hi\n' > `+git+`/hooks/post-checkout && chmod +x `+git+`/hooks/post-checkout && git config core.hooksPath .husky/_`)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.runTest("main")
	its := gitWatchItems(t, d)
	if len(its) != 1 {
		t.Fatalf("want 1 item, got %d: %+v", len(its), its)
	}
	it := its[0]
	if it.From != callerPekoed || it.Status != state.InboxOpen || it.About != "main" || it.Kind != "decision" {
		t.Fatalf("item: %+v", it)
	}
	if !strings.Contains(it.Question, "[hooks] test of main") {
		t.Errorf("question does not name the hook and session: %s", it.Question)
	}
	for _, want := range []string{"+ hooks/post-checkout = file, executable, sha256 ", "+ config: core.hookspath = .husky/_", "changed and removed nothing"} {
		if !strings.Contains(it.Context, want) {
			t.Errorf("context lacks %q:\n%s", want, it.Context)
		}
	}
	if _, err := os.Stat(filepath.Join(git, "hooks", "post-checkout")); err != nil {
		t.Fatalf("pekoed removed the hook: %v", err)
	}

	// the same state again, and harmless changes: nothing
	repoHooks(t, d, repo, `git config user.name someone && echo x > `+git+`/hooks/new.sample && true`)
	d.runTest("main")
	if n := len(gitWatchItems(t, d)); n != 1 {
		t.Fatalf("harmless run posted: %d items", n)
	}

	// the hook's content changes, and an alias is added
	repoHooks(t, d, repo, `echo 'curl evil' >> `+git+`/hooks/post-checkout && git config alias.st '!sh -c x'`)
	d.runTest("main")
	its = gitWatchItems(t, d)
	if len(its) != 2 {
		t.Fatalf("want 2 items, got %d", len(its))
	}
	last := its[0]
	if its[1].ID > last.ID {
		last = its[1]
	}
	for _, want := range []string{"~ hooks/post-checkout: file, executable, sha256 ", "+ config: alias.st = !sh -c x"} {
		if !strings.Contains(last.Context, want) {
			t.Errorf("context lacks %q:\n%s", want, last.Context)
		}
	}
}

// A dev server is watched while it runs: checked when it stops.
func TestGitWatchDevServer(t *testing.T) {
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.startDevWatch(&s)
	run(t, repo, "git", "config", "core.fsmonitor", "./x")
	d.endDevWatch(s.Slug)
	its := gitWatchItems(t, d)
	if len(its) != 1 || !strings.Contains(its[0].Question, "[hooks] dev of main") || !strings.Contains(its[0].Context, "core.fsmonitor = ./x") {
		t.Fatalf("items: %+v", its)
	}
	d.endDevWatch(s.Slug) // no watch any more: nothing
	if n := len(gitWatchItems(t, d)); n != 1 {
		t.Fatalf("%d items", n)
	}
}

// Overlapping runs in one repository see one change: one item.
func TestGitWatchOverlappingRunsReportOnce(t *testing.T) {
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	a := d.startGitWatch(&s, "test")
	b := d.startGitWatch(&s, "gatekeeper")
	run(t, repo, "git", "config", "core.sshCommand", "evil")
	d.checkGitRun(a, true)
	d.checkGitRun(b, true)
	its := gitWatchItems(t, d)
	if len(its) != 1 {
		t.Fatalf("want 1 item, got %d", len(its))
	}
	if !strings.Contains(its[0].Context, "also running in this repository then: [hooks] gatekeeper of main") {
		t.Errorf("the other run is not named:\n%s", its[0].Context)
	}
	// later, the same state reached again after a person undid it: told again
	run(t, repo, "git", "config", "--unset", "core.sshCommand")
	c := d.startGitWatch(&s, "test")
	run(t, repo, "git", "config", "core.sshCommand", "evil")
	d.checkGitRun(c, true)
	if n := len(gitWatchItems(t, d)); n != 2 {
		t.Fatalf("re-done change not reported: %d items", n)
	}
}
