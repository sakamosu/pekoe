package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/paths"
	"pekoe/internal/sandbox"
	"pekoe/internal/state"
)

// leakRepo commits a leak.sh that reads the human token and writes what it
// got to <out>/<name>.txt (and stdout), plus a Makefile whose test target
// runs it through make → sh, as an agent could leave in a worktree.
func leakRepo(t *testing.T, repo, out string) {
	t.Helper()
	script := `name=$1
got=$(cat "` + paths.HumanTokenFile() + `" 2>&1)
printf 'ran %s: %s\n' "$name" "$got" > "` + out + `/$name.txt"
printf 'a.txt:1: ran %s: %s\n' "$name" "$got"
`
	os.WriteFile(filepath.Join(repo, "leak.sh"), []byte(script), 0o755)
	os.WriteFile(filepath.Join(repo, "Makefile"), []byte("test:\n\tsh leak.sh test\n"), 0o644)
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "leak")
}

func writeRepoConfig(t *testing.T, d *Daemon, repo, cfg string) {
	t.Helper()
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	os.MkdirAll(paths.ReposConfigDir(), 0o700)
	if err := os.WriteFile(paths.RepoConfig(id), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

const leakConfig = `[hooks]
test = "make test"
setup = ["sh leak.sh setup"]
dev = "sh leak.sh dev"
teardown = ["sh leak.sh teardown"]
[review]
gatekeeper = "sh leak.sh gatekeeper"
`

// The commands pekoed runs from a worktree (Stop's test, the gatekeeper,
// setup, dev) run, but cannot read the human token.
func TestHookCommandsCannotReadHumanToken(t *testing.T) {
	d, fe, repo := setup(t)
	d.sbx = sandbox.New(sandbox.DefaultPolicy(d.Global.Web.StateDir), true)
	if err := d.sbx.Check(); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	out := t.TempDir()
	leakRepo(t, repo, out)
	writeRepoConfig(t, d, repo, leakConfig)
	if d.humanToken == "" {
		t.Fatal("no token")
	}
	check := func(name string) {
		t.Helper()
		var b []byte
		waitFor(t, func() bool { b, _ = os.ReadFile(filepath.Join(out, name+".txt")); return len(b) > 0 })
		if !strings.HasPrefix(string(b), "ran "+name+": ") {
			t.Fatalf("%s: %q", name, b)
		}
		if strings.Contains(string(b), d.humanToken) {
			t.Fatalf("%s read the human token: %q", name, b)
		}
	}

	// Stop → [hooks] test and the gatekeeper (it needs a diff)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("changed\n"), 0o644)
	event(t, d, "Stop", "main", map[string]any{"last_assistant_message": "done"})
	check("test")
	check("gatekeeper")
	waitFor(t, func() bool {
		s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
		return s.Test.Status == "pass"
	})
	logFile := filepath.Join(paths.DataDir(), call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}).RepoID, "logs", "main-test.log")
	if b, _ := os.ReadFile(logFile); !strings.Contains(string(b), "ran test") || strings.Contains(string(b), d.humanToken) {
		t.Fatalf("test log: %q", b)
	}
	waitFor(t, func() bool { r, _ := d.DB.Risks("main"); return len(r) > 0 })
	risks, _ := d.DB.Risks("main")
	if !strings.Contains(risks[0].Text, "ran gatekeeper") || strings.Contains(risks[0].Text, d.humanToken) {
		t.Fatalf("risks: %+v", risks)
	}

	// dev: the window's command, run as tmux would run it
	call[state.Session](t, d, "dev.toggle", map[string]any{"slug": "main"})
	sp := fe.spawn["main~dev"]
	if !strings.HasPrefix(sp.Command, "exec ") {
		t.Fatalf("dev not wrapped: %s", sp.Command)
	}
	c := exec.Command("/bin/sh", "-c", sp.Command)
	c.Dir = sp.Dir
	if b, err := c.CombinedOutput(); err != nil || !strings.Contains(string(b), "dev server exited (0)") {
		t.Fatalf("dev: %v %s", err, b)
	}
	check("dev")

	// pekoe new → setup
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	var s state.Session
	waitFor(t, func() bool {
		s = call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
		return s.State != state.Creating
	})
	if s.State == state.Failed {
		t.Fatalf("setup failed: %s", s.Error)
	}
	check("setup")

	// rm → teardown
	call[any](t, d, "worktree.rm", map[string]any{"slug": "feat-x", "force": true})
	check("teardown")
}

// Without a sandbox, nothing from the worktree runs: Stop's test and the
// gatekeeper are skipped with the reason shown, and setup, dev and teardown
// fail saying why.
func TestHookCommandsNotRunWithoutSandbox(t *testing.T) {
	d, fe, repo := setup(t)
	d.sbx = sandbox.Unavailable("bubblewrap (bwrap) is not installed")
	out := t.TempDir()
	leakRepo(t, repo, out)
	writeRepoConfig(t, d, repo, leakConfig)
	ran := func() []string {
		e, _ := os.ReadDir(out)
		var n []string
		for _, f := range e {
			n = append(n, f.Name())
		}
		return n
	}

	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("changed\n"), 0o644)
	event(t, d, "Stop", "main", map[string]any{"last_assistant_message": "done"})
	var s state.Session
	waitFor(t, func() bool {
		s = call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
		return s.Test.Status == "blocked"
	})
	if !strings.Contains(s.Test.Summary, "bwrap") || !strings.Contains(s.Test.Summary, "[sandbox] enable = false") {
		t.Fatalf("no reason: %+v", s.Test)
	}
	waitFor(t, func() bool { r, _ := d.DB.Risks("main"); return len(r) > 0 })
	if r, _ := d.DB.Risks("main"); !strings.HasPrefix(r[0].Text, "gatekeeper not run") || r[0].Path != "" {
		t.Fatalf("gatekeeper: %+v", r)
	}

	if err := callErr(d, "dev.toggle", map[string]any{"slug": "main"}); err == nil || !strings.Contains(err.Error(), "dev server not run") {
		t.Fatalf("dev: %v", err)
	}
	if _, ok := fe.spawn["main~dev"]; ok {
		t.Fatal("dev window spawned")
	}

	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitFor(t, func() bool {
		s = call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
		return s.State != state.Creating
	})
	if s.State != state.Failed || !strings.Contains(s.Error, "not run") {
		t.Fatalf("setup: %s %q", s.State, s.Error)
	}
	if err := callErr(d, "worktree.rm", map[string]any{"slug": "feat-x"}); err == nil || !strings.Contains(err.Error(), "teardown") {
		t.Fatalf("rm without force: %v", err)
	}
	if n := ran(); len(n) > 0 {
		t.Fatalf("ran without a sandbox: %v", n)
	}
}
