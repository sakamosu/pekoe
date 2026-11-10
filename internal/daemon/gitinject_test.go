package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// An agent can write its worktree (its .git file included) and maybe the
// repository's .git. git runs commands named in its config (core.fsmonitor
// on every status, hooks, gpg.program, …), and pekoed runs git outside the
// agents' sandbox: none of what an agent writes there may run when pekoed
// does its git work.

// trap is a command that records each run in <dir>/ran ("<name> <args>").
type trap struct{ dir string }

func newTrap(t *testing.T) *trap {
	t.Helper()
	tr := &trap{dir: t.TempDir()}
	script := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> " + filepath.Join(tr.dir, "ran") + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(tr.dir, "trap"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return tr
}

// cmd is the trap under name (what it records itself as).
func (tr *trap) cmd(name string) string {
	p := filepath.Join(tr.dir, name)
	if _, err := os.Stat(p); err != nil {
		b, _ := os.ReadFile(filepath.Join(tr.dir, "trap"))
		os.WriteFile(p, b, 0o755)
	}
	return p
}

// hooks is a hooks directory where every hook is the trap.
func (tr *trap) hooks() string {
	dir := filepath.Join(tr.dir, "hooks")
	os.MkdirAll(dir, 0o755)
	for _, h := range []string{"pre-commit", "post-commit", "post-checkout", "post-merge", "pre-push", "pre-rebase",
		"post-rewrite", "reference-transaction", "pre-auto-gc", "post-index-change", "pre-receive", "update",
		"post-receive", "post-update", "push-to-checkout", "fsmonitor-watchman"} {
		b, _ := os.ReadFile(filepath.Join(tr.dir, "trap"))
		os.WriteFile(filepath.Join(dir, h), b, 0o755)
	}
	return dir
}

func (tr *trap) ran() string {
	b, _ := os.ReadFile(filepath.Join(tr.dir, "ran"))
	return string(b)
}

func (tr *trap) reset() { os.Remove(filepath.Join(tr.dir, "ran")) }

// gitConfig writes key = value into a config file with git itself.
func gitConfig(t *testing.T, file string, kv ...string) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		run(t, filepath.Dir(file), "git", "config", "--file", file, kv[i], kv[i+1])
	}
}

// plainGit runs git as a person (or a tool) would in dir, without pekoe's
// guard: it shows that the trap is armed.
func plainGit(dir string, args ...string) {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_EDITOR=true")
	c.CombinedOutput()
}

// armed fails the test unless plain git in dir sets the trap off.
func armed(t *testing.T, tr *trap, dir string, args ...string) {
	t.Helper()
	plainGit(dir, args...)
	if tr.ran() == "" {
		t.Fatalf("the trap did not go off for plain git %v in %s: the test does not test anything", args, dir)
	}
	tr.reset()
}

// commitIn commits a change to a.txt in dir (before any trap is set).
func commitIn(t *testing.T, dir, content string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o644)
	run(t, dir, "git", "commit", "-qam", "change a.txt")
}

// pair makes two sessions that both committed a change to a.txt (the
// conflict forecast runs git merge-tree on them), and moves main on so a
// land has to rebase.
func pair(t *testing.T, d *Daemon, repo string) (state.Session, state.Session) {
	t.Helper()
	s := newSession(t, d, repo, "feat/x")
	o := newSession(t, d, repo, "feat/y")
	commitIn(t, s.Path, "1\nx\n3\n")
	commitIn(t, o.Path, "1\ny\n3\n")
	os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644)
	run(t, repo, "git", "add", "main.txt")
	run(t, repo, "git", "commit", "-qm", "main moves on")
	return s, o
}

// gitOps drives pekoed through what runs git in s's worktree: a prompt
// (snapshot), an edit and a Stop (diffstat), changes, review, the conflict
// forecast, undo, the PR lookup, and land last. Errors are fine (git may
// be refused); what matters is what ran.
func gitOps(t *testing.T, d *Daemon, s state.Session) {
	t.Helper()
	sl := map[string]any{"slug": s.Slug}
	try := func(method string, params any) {
		b, _ := json.Marshal(params)
		if _, err := d.Call(method, b); err != nil {
			t.Logf("%s: %v", method, err)
		}
	}
	try("session.event", map[string]any{"hook": "UserPromptSubmit", "session": s.Slug, "payload": map[string]any{"prompt": "go"}})
	os.WriteFile(filepath.Join(s.Path, "new.txt"), []byte("new\n"), 0o644)
	try("session.event", map[string]any{"hook": "Stop", "session": s.Slug, "payload": map[string]any{"last_assistant_message": "done"}})
	d.refreshDiffstat(s.Slug)
	try("changes", sl)
	try("review.files", sl)
	try("review.start", sl)
	try("conflicts.list", map[string]any{})
	d.scanConflicts("")
	try("session.undo", sl)
	prForBranch(s.Path, s.Branch)
	os.Remove(filepath.Join(s.Path, "new.txt"))
	try("land", sl)
	time.Sleep(100 * time.Millisecond) // goroutines started by the events
}

func noTrap(t *testing.T, tr *trap) {
	t.Helper()
	if r := tr.ran(); r != "" {
		t.Fatalf("pekoed's git ran what the agent wrote:\n%s", r)
	}
}

// (a) The worktree's .git file pointed at a git directory of the agent's
// making, whose config runs commands.
func TestFakeGitDirRunsNothing(t *testing.T) {
	d, _, repo := setup(t)
	s, _ := pair(t, d, repo)
	tr := newTrap(t)
	fake := t.TempDir()
	common := filepath.Join(fake, "common")
	run(t, repo, "cp", "-R", filepath.Join(repo, ".git"), common)
	b, err := os.ReadFile(filepath.Join(s.Path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	admin := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
	fakeAdmin := filepath.Join(fake, "admin")
	run(t, repo, "cp", "-R", admin, fakeAdmin)
	os.WriteFile(filepath.Join(fakeAdmin, "commondir"), []byte(common+"\n"), 0o644)
	gitConfig(t, filepath.Join(common, "config"),
		"core.fsmonitor", tr.cmd("fsmonitor"),
		"core.hooksPath", tr.hooks(),
		"commit.gpgSign", "true",
		"gpg.program", tr.cmd("gpg"),
		"diff.external", tr.cmd("extdiff"))
	os.WriteFile(filepath.Join(s.Path, ".git"), []byte("gitdir: "+fakeAdmin+"\n"), 0o644)
	armed(t, tr, s.Path, "status")

	gitOps(t, d, s)
	noTrap(t, tr)
}

// (b) Commands in the repository's real config, the worktree's
// config.worktree, or wired to the worktree's .gitattributes.
func TestRepoConfigRunsNothing(t *testing.T) {
	cases := []struct {
		name string
		arm  func(t *testing.T, tr *trap, repo string, s state.Session)
	}{
		{"repository config", func(t *testing.T, tr *trap, repo string, s state.Session) {
			gitConfig(t, filepath.Join(repo, ".git", "config"),
				"core.fsmonitor", tr.cmd("fsmonitor"),
				"core.hooksPath", tr.hooks(),
				"commit.gpgSign", "true",
				"merge.verifySignatures", "true",
				"gpg.program", tr.cmd("gpg"),
				"gpg.format", "openpgp",
				"diff.external", tr.cmd("extdiff"),
				"core.editor", tr.cmd("editor"),
				"sequence.editor", tr.cmd("seqeditor"),
				"core.pager", tr.cmd("pager"),
				"credential.helper", tr.cmd("credential"))
			armed(t, tr, s.Path, "status")
			armed(t, tr, s.Path, "commit", "--allow-empty", "-m", "signed?")
		}},
		{"config.worktree", func(t *testing.T, tr *trap, repo string, s state.Session) {
			gitConfig(t, filepath.Join(repo, ".git", "config"), "extensions.worktreeConfig", "true")
			b, _ := os.ReadFile(filepath.Join(s.Path, ".git"))
			admin := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
			gitConfig(t, filepath.Join(admin, "config.worktree"),
				"core.fsmonitor", tr.cmd("fsmonitor"),
				"core.hooksPath", tr.hooks(),
				"commit.gpgSign", "true",
				"gpg.program", tr.cmd("gpg"))
			armed(t, tr, s.Path, "status")
		}},
		{".gitattributes", func(t *testing.T, tr *trap, repo string, s state.Session) {
			os.WriteFile(filepath.Join(s.Path, ".gitattributes"), []byte("* diff=evil\n"), 0o644)
			run(t, s.Path, "git", "add", ".gitattributes")
			run(t, s.Path, "git", "commit", "-qm", "attributes")
			gitConfig(t, filepath.Join(repo, ".git", "config"),
				"diff.evil.textconv", tr.cmd("textconv"),
				"diff.evil.command", tr.cmd("diffcmd"))
			os.WriteFile(filepath.Join(s.Path, "a.txt"), []byte("changed\n"), 0o644)
			armed(t, tr, s.Path, "diff")
			run(t, s.Path, "git", "checkout", "a.txt")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _, repo := setup(t)
			s, _ := pair(t, d, repo)
			tr := newTrap(t)
			c.arm(t, tr, repo, s)
			gitOps(t, d, s)
			noTrap(t, tr)
		})
	}
}

// The origin and the transport: fetch / push (land, a PR review) and gh
// (which runs git status for pr create) do not run what the repository's
// config names.
func TestOriginConfigRunsNothing(t *testing.T) {
	cases := []struct {
		name string
		arm  func(t *testing.T, tr *trap, repo string)
	}{
		// a local origin runs upload-pack / receive-pack on this machine
		{"upload-pack and receive-pack", func(t *testing.T, tr *trap, repo string) {
			gitConfig(t, filepath.Join(repo, ".git", "config"),
				"remote.origin.uploadpack", tr.cmd("uploadpack"),
				"remote.origin.receivepack", tr.cmd("receivepack"),
				"core.fsmonitor", tr.cmd("fsmonitor"))
			armed(t, tr, repo, "fetch", "origin")
		}},
		{"ext transport", func(t *testing.T, tr *trap, repo string) {
			gitConfig(t, filepath.Join(repo, ".git", "config"),
				"protocol.ext.allow", "always",
				"remote.origin.url", "ext::"+tr.cmd("ext")+" %S")
			armed(t, tr, repo, "fetch", "origin")
		}},
		{"ssh command", func(t *testing.T, tr *trap, repo string) {
			// GIT_SSH_COMMAND / GIT_SSH win over core.sshCommand, and an
			// agent's sandbox sets GIT_SSH_COMMAND (its ssh proxy): without
			// them gone the trap never goes off and this case tests nothing
			for _, k := range []string{"GIT_SSH_COMMAND", "GIT_SSH"} {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}
			gitConfig(t, filepath.Join(repo, ".git", "config"),
				"remote.origin.url", "ssh://git.example.invalid/app.git",
				"core.sshCommand", tr.cmd("ssh"))
			armed(t, tr, repo, "fetch", "origin")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _, repo := setup(t)
			withOrigin(t, repo)
			// gh runs git in the worktree too (git status for pr create)
			gh := t.TempDir()
			os.WriteFile(filepath.Join(gh, "gh"), []byte("#!/bin/sh\ngit status --porcelain >/dev/null 2>&1\ngit remote -v >/dev/null 2>&1\n"+
				"case \"$1 $2\" in \"pr create\") echo https://github.com/me/app/pull/42 ;; \"pr view\") echo '{\"number\":42,\"state\":\"OPEN\"}' ;; esac\n"), 0o755)
			t.Setenv("PATH", gh+string(os.PathListSeparator)+os.Getenv("PATH"))
			s, _ := pair(t, d, repo)
			tr := newTrap(t)
			c.arm(t, tr, repo)
			gitOps(t, d, s)
			d.watchMerges()
			noTrap(t, tr)
		})
	}
}
