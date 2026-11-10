package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// isolatedConfig points git's global config at a file of the test's and
// leaves the system's out.
func isolatedConfig(t *testing.T, global string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "gitconfig")
	os.WriteFile(f, []byte(global), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", f)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	guard.mu.Lock()
	guard.trusted = nil
	guard.mu.Unlock()
}

func repoWithWorktree(t *testing.T, relative bool) (repo, wt string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "app")
	os.MkdirAll(repo, 0o755)
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--no-gpg-sign", "--allow-empty", "-m", "init")
	wt = filepath.Join(t.TempDir(), "wt", "feat")
	args := []string{"worktree", "add", "-q", "-b", "feat"}
	if relative {
		args = append(args, "--relative-paths")
	}
	gitT(t, repo, append(args, wt)...)
	t.Cleanup(func() { Unpin(wt) })
	return repo, wt
}

// The user's own credential helpers (global scope, url-scoped ones
// included, in order) are kept; the repository's are dropped by the empty
// helper in front of them. Signing settings come from the user's config or
// git's defaults, never the repository's.
func TestOverridesKeepOnlyTheUsersSettings(t *testing.T) {
	isolatedConfig(t, "[credential]\n\thelper = osxkeychain\n[credential \"https://github.com\"]\n\thelper =\n\thelper = !gh auth git-credential\n[commit]\n\tgpgSign = true\n[gpg]\n\tformat = ssh\n")
	repo, _ := repoWithWorktree(t, false)
	gitT(t, repo, "config", "credential.helper", "!evil")
	gitT(t, repo, "config", "gpg.ssh.program", "evil")
	gitT(t, repo, "config", "tag.gpgSign", "true")
	got := map[string][]string{}
	var order []string
	for _, kv := range overrides(repo, nil) {
		got[kv[0]] = append(got[kv[0]], kv[1])
		order = append(order, kv[0]+"="+kv[1])
	}
	want := map[string]string{
		"core.fsmonitor": "false", "core.hookspath": os.DevNull, "protocol.ext.allow": "never",
		"commit.gpgsign": "true", "gpg.format": "ssh", "gpg.ssh.program": "ssh-keygen", "tag.gpgsign": "false",
	}
	for k, v := range want {
		k2 := k
		if k == "core.hookspath" {
			k2 = "core.hooksPath"
		}
		if g := got[k2]; len(g) != 1 || g[0] != v {
			t.Errorf("%s = %v, want %s", k, g, v)
		}
	}
	all := strings.Join(order, "\n")
	if strings.Contains(all, "evil") {
		t.Errorf("the repository's settings were passed on:\n%s", all)
	}
	cred := "credential.helper=\ncredential.helper=osxkeychain\ncredential.https://github.com.helper=\ncredential.https://github.com.helper=!gh auth git-credential"
	if !strings.Contains(all, cred) {
		t.Errorf("credential helpers:\n%s\nwant in order:\n%s", all, cred)
	}
}

// git asks only the user's credential helpers, not one the repository adds.
func TestCredentialHelpersAreTheUsers(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	for _, n := range []string{"good", "evil"} {
		os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\necho "+n+" >> "+mark+"\n"), 0o755)
	}
	isolatedConfig(t, "[credential]\n\thelper = "+filepath.Join(dir, "good")+"\n")
	repo, _ := repoWithWorktree(t, false)
	gitT(t, repo, "config", "credential.helper", filepath.Join(dir, "evil"))
	gitT(t, repo, "config", "credential.https://example.com.helper", filepath.Join(dir, "evil"))
	_, _ = RunRaw(context.Background(), repo, nil, []byte("protocol=https\nhost=example.com\n\n"), "credential", "fill")
	b, _ := os.ReadFile(mark)
	if !strings.Contains(string(b), "good") || strings.Contains(string(b), "evil") {
		t.Fatalf("helpers run: %q", b)
	}
}

// A pinned worktree uses the repository's entry for it whatever its .git
// file says, below its top too, with relative paths (worktree.useRelativePaths)
// as well; a changed .git file stops git there.
func TestPinnedWorktree(t *testing.T) {
	isolatedConfig(t, "")
	for _, relative := range []bool{false, true} {
		repo, wt := repoWithWorktree(t, relative)
		Pin(wt, repo)
		os.MkdirAll(filepath.Join(wt, "sub"), 0o755)
		for _, dir := range []string{wt, filepath.Join(wt, "sub")} {
			top, err := Toplevel(dir)
			if err != nil || !samePath(top, wt) {
				t.Fatalf("relative=%v: toplevel of %s = %q, %v", relative, dir, top, err)
			}
			if b := CurrentBranch(dir); b != "feat" {
				t.Fatalf("relative=%v: branch in %s = %q", relative, dir, b)
			}
		}
		orig, _ := os.ReadFile(filepath.Join(wt, ".git"))
		other := filepath.Join(t.TempDir(), "other")
		gitT(t, t.TempDir(), "init", "-q", other)
		os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o644)
		_, err := Run(filepath.Join(wt, "sub"), "status")
		if !errors.Is(err, ErrTampered) {
			t.Fatalf("relative=%v: status with a changed .git: %v", relative, err)
		}
		if _, err := Env(wt); !errors.Is(err, ErrTampered) {
			t.Fatalf("relative=%v: Env with a changed .git: %v", relative, err)
		}
		// not pinned: git as it finds it (CheckWorktreeOf looks there)
		if err := CheckWorktreeOf(repo, wt); !errors.Is(err, ErrForeignWorktree) && !errors.Is(err, ErrNotWorktree) {
			t.Fatalf("relative=%v: CheckWorktreeOf: %v", relative, err)
		}
		os.WriteFile(filepath.Join(wt, ".git"), orig, 0o644)
		if err := CheckPin(wt); err != nil {
			t.Fatalf("relative=%v: put back: %v", relative, err)
		}
		Unpin(wt)
		if err := CheckPin(wt); err != nil {
			t.Fatal(err)
		}
	}
}

// diff / show / log never run an external diff or textconv, fetch and push
// never the repository's upload-pack / receive-pack.
func TestSubcommandFlags(t *testing.T) {
	isolatedConfig(t, "")
	repo, _ := repoWithWorktree(t, false)
	mark := filepath.Join(t.TempDir(), "ran")
	trap := filepath.Join(t.TempDir(), "trap")
	os.WriteFile(trap, []byte("#!/bin/sh\necho \"$*\" >> "+mark+"\nexit 1\n"), 0o755)
	os.WriteFile(filepath.Join(repo, "f"), []byte("a\n"), 0o644)
	gitT(t, repo, "add", "f")
	gitT(t, repo, "commit", "-qm", "f")
	os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("f diff=evil\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "f"), []byte("b\n"), 0o644)
	gitT(t, repo, "config", "diff.external", trap)
	gitT(t, repo, "config", "diff.evil.textconv", trap)
	bare := filepath.Join(t.TempDir(), "o.git")
	gitT(t, repo, "init", "-q", "--bare", bare)
	gitT(t, repo, "remote", "add", "o", bare)
	gitT(t, repo, "config", "remote.o.uploadpack", trap)
	gitT(t, repo, "config", "remote.o.receivepack", trap)
	for _, args := range [][]string{{"diff"}, {"diff", "HEAD"}, {"show", "HEAD"}, {"log", "-p", "-1"}, {"push", "-q", "o", "HEAD:main"}, {"fetch", "-q", "o"}} {
		if _, err := Run(repo, args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	if b, _ := os.ReadFile(mark); len(b) > 0 {
		t.Fatalf("ran: %s", b)
	}
}
