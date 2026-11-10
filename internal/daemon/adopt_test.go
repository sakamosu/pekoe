package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startSleeper starts a stand-in for a hand-started agent and reports when
// it exits.
func startSleeper(t *testing.T) (int, <-chan struct{}) {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	t.Cleanup(func() { c.Process.Kill() })
	return c.Process.Pid, done
}

// Two agents in one worktree: pekoe can adopt only one session there and
// cannot tell their conversations apart, so it stops neither (the second
// used to be interrupted and then refused as already managed).
func TestAdoptLeavesTwoAgentsOfOneWorktree(t *testing.T) {
	d, _, repo := setup(t)
	repo = realpath(repo) // lsof reports resolved paths
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	p1, done1 := startSleeper(t)
	p2, done2 := startSleeper(t)
	old := findAgents
	findAgents = func() []adoptCandidate {
		return []adoptCandidate{{PID: p1, Cwd: repo, Where: "ttys001"}, {PID: p2, Cwd: filepath.Join(repo, "sub"), Where: "ttys002"}}
	}
	t.Cleanup(func() { findAgents = old })

	res := call[struct {
		Candidates []adoptCandidate `json:"candidates"`
		Skipped    []adoptCandidate `json:"skipped"`
	}](t, d, "session.adopt", map[string]any{"path": repo})
	if len(res.Candidates) != 0 || len(res.Skipped) != 2 || res.Skipped[0].Skip == "" {
		t.Fatalf("want both skipped: %+v", res)
	}
	call[any](t, d, "session.adopt", map[string]any{"path": repo, "confirm": true})
	select {
	case <-done1:
		t.Fatal("first agent was stopped")
	case <-done2:
		t.Fatal("second agent was stopped")
	case <-time.After(300 * time.Millisecond):
	}
	if ss, _ := d.DB.Sessions(); len(ss) != 0 {
		t.Fatalf("adopted %d sessions", len(ss))
	}
}

// A path outside any git repository is an error, not "every agent".
func TestAdoptPathOutsideGit(t *testing.T) {
	d, _, repo := setup(t)
	repo = realpath(repo)
	pid, _ := startSleeper(t)
	old := findAgents
	findAgents = func() []adoptCandidate { return []adoptCandidate{{PID: pid, Cwd: repo, Where: "ttys001"}} }
	t.Cleanup(func() { findAgents = old })
	_, err := d.Call("session.adopt", mustJSON(map[string]any{"path": t.TempDir()}))
	if err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("want an error, got %v", err)
	}
}
