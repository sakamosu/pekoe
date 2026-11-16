package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"pekoe/internal/state"
)

// A slow test run from an earlier Stop must not overwrite the result of the
// run a later Stop started (it tested older code).
func TestOlderTestRunDoesNotOverwrite(t *testing.T) {
	d, _, repo := setup(t)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	counter := filepath.Join(t.TempDir(), "n")
	// the first run is slow and fails, the second is quick and passes
	test := `n=$(cat ` + counter + ` 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + counter + `; if [ $n = 1 ]; then sleep 1; exit 1; fi; exit 0`
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte("[hooks]\ntest = '"+test+"'\n"), 0o644)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	done := make(chan struct{})
	go func() { d.runTest("main"); close(done) }()
	for i := 0; i < 100; i++ { // the first run has started
		if b, _ := os.ReadFile(counter); len(b) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.runTest("main")
	<-done
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if s.Test.Status != "pass" {
		t.Fatalf("the older run's result won: %+v", s.Test)
	}
}

// A [hooks] test whose command is not there (the shell's exit 127, as
// `yarn test` on a machine without yarn) did not run: it is "error" with
// the shell's message, not a failing test. A test that runs and fails is
// still "fail".
func TestMissingTestCommandIsNotAFailure(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	repoHooks(t, d, repo, `pekoe-no-such-command-for-test test`)
	d.runTest("main")
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if s.Test.Status != "error" || s.Test.Summary != "command not found: pekoe-no-such-command-for-test" {
		t.Fatalf("a missing command: %+v", s.Test)
	}
	repoHooks(t, d, repo, `echo '1 failed'; exit 1`)
	d.runTest("main")
	s = call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if s.Test.Status != "fail" {
		t.Fatalf("a failing test: %+v", s.Test)
	}
}

// Taking [hooks] test out of repos/<id>.toml (or commenting it out) clears
// the last result at the next Stop: a ✗ from before must not stay.
func TestRemovedTestHookDropsTheOldResult(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	repoHooks(t, d, repo, `exit 1`)
	d.runTest("main")
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.Test.Status != "fail" {
		t.Fatalf("before: %+v", s.Test)
	}
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte("[hooks]\n# test = 'exit 1'\n"), 0o644)
	d.runTest("main")
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.Test.Status != "none" {
		t.Fatalf("the old result stayed: %+v", s.Test)
	}
}

// The summary of a test command the shell could not start names what was
// missing, whichever shell said it. bash puts the reason after the
// command, so the whole line cut at 60 characters lost it.
func TestNotFoundSummary(t *testing.T) {
	for out, want := range map[string]string{
		"zsh:1: command not found: yarn\n":                                     "command not found: yarn",
		"/bin/bash: line 1: pekoe-no-such-command-for-test: command not found": "command not found: pekoe-no-such-command-for-test",
		"bash: yarn: command not found\n":                                      "command not found: yarn",
		"sh: 1: yarn: not found\n":                                             "command not found: yarn",
		"$ jest\n/bin/sh: jest: command not found\n":                           "command not found: jest",
		"zsh:1: no such file or directory: ./run\n":                            "no such file: ./run",
		"/bin/bash: line 1: ./run-tests: No such file or directory":            "no such file: ./run-tests",
		"something else\n":                                                     "",
	} {
		if got := notFound(out); got != want {
			t.Errorf("notFound(%q) = %q, want %q", out, got, want)
		}
	}
	if s := testResult("/bin/bash: line 1: pekoe-no-such-command-for-test: command not found\n", exit127(t)).Summary; s != "command not found: pekoe-no-such-command-for-test" {
		t.Errorf("summary of exit 127 from bash: %q", s)
	}
}

// exit127 is the error of a process that exited 127.
func exit127(t *testing.T) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", "exit 127").Run()
	if err == nil {
		t.Fatal("exit 127 succeeded")
	}
	return err
}
