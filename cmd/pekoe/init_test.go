package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// initDaemon is a pekoed for init: a draft to write, nothing ignored to pick.
func initDaemon(t *testing.T, repoDir string) *fakeDaemon {
	t.Helper()
	fd := startFakeDaemon(t)
	fd.results = map[string]any{
		"init.detect":  map[string]any{"repo_id": "github.com/o/app", "config_path": "/x/app.toml", "toml": "[hooks]\n"},
		"init.write":   map[string]any{"path": "/x/app.toml", "repo_dir": repoDir},
		"session.list": []map[string]any{},
		"session.open": map[string]any{"slug": "main", "path": repoDir, "port": 3100},
	}
	old := jsonOut
	jsonOut = false
	t.Cleanup(func() { jsonOut = old })
	return fd
}

// withStdin gives the command line as its stdin (the y/N answer).
func withStdin(t *testing.T, line string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(line)
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
}

// captureErr runs f with stderr going to a buffer (stdout as capture).
func captureErr(t *testing.T, f func() error) (stdout, stderr string, err error) {
	t.Helper()
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, e := r.Read(buf)
			b.Write(buf[:n])
			if e != nil {
				break
			}
		}
		done <- b.String()
	}()
	stdout, err = capture(t, f)
	w.Close()
	os.Stderr = old
	return stdout, <-done, err
}

// After writing the config, init starts the main session in the main
// checkout, as `pekoe open <repo> --new` does.
func TestInitOpensMainSession(t *testing.T) {
	repo := t.TempDir()
	fd := initDaemon(t, repo)
	withStdin(t, "y\n")
	out, err := capture(t, func() error { return dispatch("init", repo) })
	if err != nil {
		t.Fatal(err)
	}
	if c := fd.calls(); !reflect.DeepEqual(c, []string{"init.detect", "init.write", "session.list", "session.open"}) {
		t.Fatalf("calls = %v", c)
	}
	var p struct {
		Path   string `json:"path"`
		Resume string `json:"resume"`
	}
	json.Unmarshal(fd.params["session.open"], &p)
	if p.Path != repo || p.Resume != "new" {
		t.Fatalf("session.open params = %s, want the main checkout and a new agent", fd.params["session.open"])
	}
	var lp struct {
		RepoID string `json:"repo_id"`
	}
	json.Unmarshal(fd.params["session.list"], &lp)
	if lp.RepoID != "github.com/o/app" {
		t.Fatalf("session.list params = %s, want this repository's", fd.params["session.list"])
	}
	if !strings.Contains(out, "opened main ("+repo+")") {
		t.Fatalf("stdout:\n%s", out)
	}
}

// A main session that is already there is left alone: init can run again.
func TestInitKeepsExistingMainSession(t *testing.T) {
	repo := t.TempDir()
	for _, s := range []map[string]any{
		{"slug": "app-main", "path": repo, "repo_dir": repo},
		{"slug": "app-main", "path": "/elsewhere/app", "repo_dir": "/elsewhere/app"}, // same checkout, path spelled otherwise
	} {
		fd := initDaemon(t, repo)
		fd.results["session.list"] = []map[string]any{
			{"slug": "feat-x", "path": "/wt/app/feat-x", "repo_dir": repo},
			s,
		}
		out, err := capture(t, func() error { return dispatch("init", repo, "--yes") })
		if err != nil {
			t.Fatal(err)
		}
		if c := fd.calls(); !reflect.DeepEqual(c, []string{"init.detect", "init.write", "session.list"}) {
			t.Fatalf("calls = %v (opened a second main session?)", c)
		}
		if !strings.Contains(out, "main session app-main is already there") {
			t.Fatalf("stdout:\n%s", out)
		}
	}
}

// --no-open writes the config and starts nothing.
func TestInitNoOpen(t *testing.T) {
	repo := t.TempDir()
	fd := initDaemon(t, repo)
	if _, err := capture(t, func() error { return dispatch("init", repo, "--yes", "--no-open") }); err != nil {
		t.Fatal(err)
	}
	if c := fd.calls(); !reflect.DeepEqual(c, []string{"init.detect", "init.write"}) {
		t.Fatalf("calls = %v", c)
	}
}

// N (or an existing config not overwritten, or --print): nothing written,
// nothing started.
func TestInitNotWrittenDoesNotOpen(t *testing.T) {
	repo := t.TempDir()
	for _, c := range []struct {
		name, stdin string
		exists      bool
		args        []string
	}{
		{"N", "n\n", false, nil},
		{"empty answer", "\n", false, nil},
		{"existing config, N", "n\n", true, nil},
		{"--print", "", false, []string{"--print"}},
	} {
		fd := initDaemon(t, repo)
		fd.results["init.detect"].(map[string]any)["exists"] = c.exists
		withStdin(t, c.stdin)
		if _, err := capture(t, func() error { return dispatch(append([]string{"init", repo}, c.args...)...) }); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if calls := fd.calls(); !reflect.DeepEqual(calls, []string{"init.detect"}) {
			t.Fatalf("%s: calls = %v", c.name, calls)
		}
	}
}

// pekoed failing to start the agent (or gone after writing) does not fail
// init: the config is written; it says how to start the session by hand.
func TestInitOpenFailureKeepsInit(t *testing.T) {
	repo := t.TempDir()
	for _, method := range []string{"session.open", "session.list"} {
		fd := initDaemon(t, repo)
		fd.errs = map[string]string{method: "tmux: no server"}
		out, errOut, err := captureErr(t, func() error { return dispatch("init", repo, "--yes") })
		if err != nil {
			t.Fatalf("%s failing: init failed: %v", method, err)
		}
		if strings.Contains(out, "opened") {
			t.Fatalf("%s failing: stdout:\n%s", method, out)
		}
		if !strings.Contains(errOut, "tmux: no server") || !strings.Contains(errOut, "`pekoe open "+repo+" --new`") {
			t.Fatalf("%s failing: stderr:\n%s", method, errOut)
		}
	}
	// a pekoed from before init.write said where the repository is
	fd := initDaemon(t, repo)
	fd.results["init.write"] = map[string]any{"path": "/x/app.toml"}
	_, errOut, err := captureErr(t, func() error { return dispatch("init", repo, "--yes") })
	if err != nil || !strings.Contains(errOut, "main session was not started") {
		t.Fatalf("old pekoed: err = %v, stderr:\n%s", err, errOut)
	}
	if c := fd.calls(); !reflect.DeepEqual(c, []string{"init.detect", "init.write"}) {
		t.Fatalf("old pekoed: calls = %v", c)
	}
}
