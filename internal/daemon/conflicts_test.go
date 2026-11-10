package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// twoSessions opens sessions "a" and "b" on worktrees branched from main,
// where f.txt has 12 lines (far enough apart for git to merge two edits).
func twoSessions(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	d, _, repo := setup(t)
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, "line "+string(rune('0'+i%10)))
	}
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "f")
	root := t.TempDir()
	wa, wb := filepath.Join(root, "a"), filepath.Join(root, "b")
	run(t, repo, "git", "worktree", "add", "-q", "-b", "a", wa)
	run(t, repo, "git", "worktree", "add", "-q", "-b", "b", wb)
	for _, p := range []string{wa, wb} {
		call[state.Session](t, d, "session.open", map[string]any{"path": p, "resume": "new"})
	}
	return d, wa, wb
}

// editLine rewrites line n (1-based) of f.txt in dir.
func editLine(t *testing.T, dir string, n int, text string) {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	ls := strings.Split(string(b), "\n")
	ls[n-1] = text
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strings.Join(ls, "\n")), 0o644)
}

func commit(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-qm", "work")
}

func TestConflictForecast(t *testing.T) {
	t.Run("same line conflicts", func(t *testing.T) {
		d, wa, wb := twoSessions(t)
		editLine(t, wa, 2, "from a")
		editLine(t, wb, 2, "from b")
		commit(t, wa)
		commit(t, wb)
		ps := call[[]ConflictPair](t, d, "conflicts.list", map[string]any{})
		if len(ps) != 1 || ps[0].A != "a" || ps[0].B != "b" || ps[0].Status != "conflict" ||
			!slices.Equal(ps[0].Files, []string{"f.txt"}) || !slices.Equal(ps[0].ConflictFiles, []string{"f.txt"}) {
			t.Fatalf("pairs: %+v", ps)
		}
		// the forecast lands on both sessions
		a := call[state.Session](t, d, "session.get", map[string]any{"slug": "a"})
		if len(a.Overlaps) != 1 || a.Overlaps[0].Slug != "b" || !a.Overlaps[0].Conflict {
			t.Fatalf("a overlaps: %+v", a.Overlaps)
		}
		b := call[state.Session](t, d, "session.get", map[string]any{"slug": "b"})
		if len(b.Overlaps) != 1 || b.Overlaps[0].Slug != "a" {
			t.Fatalf("b overlaps: %+v", b.Overlaps)
		}
		// filtering by repo
		if ps := call[[]ConflictPair](t, d, "conflicts.list", map[string]any{"repo_id": "elsewhere"}); len(ps) != 0 {
			t.Fatalf("repo filter: %+v", ps)
		}
		// a stopped session leaves the race and its forecast is cleared
		d.update("b", func(s *state.Session) { s.Stopped = true })
		if ps := call[[]ConflictPair](t, d, "conflicts.list", map[string]any{}); len(ps) != 0 {
			t.Fatalf("stopped session still forecast: %+v", ps)
		}
		a = call[state.Session](t, d, "session.get", map[string]any{"slug": "a"})
		if len(a.Overlaps) != 0 {
			t.Fatalf("a overlaps after b stopped: %+v", a.Overlaps)
		}
	})

	t.Run("different lines auto-merge", func(t *testing.T) {
		d, wa, wb := twoSessions(t)
		editLine(t, wa, 2, "from a")
		editLine(t, wb, 11, "from b")
		commit(t, wa)
		commit(t, wb)
		ps := call[[]ConflictPair](t, d, "conflicts.list", map[string]any{})
		if len(ps) != 1 || ps[0].Status != "overlap" || len(ps[0].ConflictFiles) != 0 {
			t.Fatalf("pairs: %+v", ps)
		}
		a := call[state.Session](t, d, "session.get", map[string]any{"slug": "a"})
		if len(a.Overlaps) != 1 || a.Overlaps[0].Conflict {
			t.Fatalf("a overlaps: %+v", a.Overlaps)
		}
	})

	t.Run("different files do not overlap", func(t *testing.T) {
		d, wa, wb := twoSessions(t)
		editLine(t, wa, 2, "from a")
		os.WriteFile(filepath.Join(wb, "other.txt"), []byte("b\n"), 0o644)
		commit(t, wa)
		commit(t, wb)
		if ps := call[[]ConflictPair](t, d, "conflicts.list", map[string]any{}); len(ps) != 0 {
			t.Fatalf("pairs: %+v", ps)
		}
	})

	t.Run("uncommitted work is overlap only", func(t *testing.T) {
		d, wa, wb := twoSessions(t)
		editLine(t, wa, 2, "from a")
		editLine(t, wb, 2, "from b") // would conflict once committed
		ps := call[[]ConflictPair](t, d, "conflicts.list", map[string]any{})
		if len(ps) != 1 || ps[0].Status != "overlap" {
			t.Fatalf("pairs: %+v", ps)
		}
	})

	t.Run("pending edit warns about files changed elsewhere", func(t *testing.T) {
		d, wa, wb := twoSessions(t)
		editLine(t, wa, 2, "from a")
		commit(t, wa)
		call[[]ConflictPair](t, d, "conflicts.list", map[string]any{}) // a's change set is known now
		event(t, d, "PreToolUse", "b", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(wb, "f.txt")}})
		b := call[state.Session](t, d, "session.get", map[string]any{"slug": "b"})
		if b.PendingTool == nil || b.PendingTool.Touches != "f.txt" || !slices.Equal(b.PendingTool.AlsoIn, []string{"a"}) {
			t.Fatalf("pending tool: %+v", b.PendingTool)
		}
		// the file b is about to edit counts as touched: overlap before any write
		ps := call[[]ConflictPair](t, d, "conflicts.list", map[string]any{})
		if len(ps) != 1 || ps[0].Status != "overlap" || !slices.Equal(ps[0].Files, []string{"f.txt"}) {
			t.Fatalf("pairs: %+v", ps)
		}
		event(t, d, "PreToolUse", "b", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(wb, "new.txt")}})
		b = call[state.Session](t, d, "session.get", map[string]any{"slug": "b"})
		if len(b.PendingTool.AlsoIn) != 0 {
			t.Fatalf("untouched file warned: %+v", b.PendingTool)
		}
	})
}

// Without the token conflicts.list rescans at most every
// agentConflictsEvery; within it the last scan comes back without running
// git again. With the token it rescans every time, as before.
func TestAgentConflictsCached(t *testing.T) {
	d, wa, wb := twoSessions(t)
	agent := &Caller{} // no token
	agentList := func() []ConflictPair {
		t.Helper()
		r, err := d.CallAs(agent, "conflicts.list", nil)
		if err != nil {
			t.Fatal(err)
		}
		return r.([]ConflictPair)
	}
	scanned := func() time.Time {
		d.cf.mu.Lock()
		defer d.cf.mu.Unlock()
		return d.cf.scanned
	}

	// the first call scans: nothing scanned yet
	if ps := agentList(); len(ps) != 0 {
		t.Fatalf("pairs before any edit: %+v", ps)
	}
	first := scanned()
	if first.IsZero() {
		t.Fatal("the first agent call did not scan")
	}

	// within the window: the cached (empty) pairs, no rescan
	editLine(t, wa, 2, "from a")
	editLine(t, wb, 2, "from b")
	if ps := agentList(); len(ps) != 0 {
		t.Fatalf("agent call within %s rescanned: %+v", agentConflictsEvery, ps)
	}
	if !scanned().Equal(first) {
		t.Fatal("agent call within the window ran a scan")
	}

	// with the token: always a fresh scan, and the cache follows it
	if ps := call[[]ConflictPair](t, d, "conflicts.list", map[string]any{}); len(ps) != 1 {
		t.Fatalf("human call: %+v", ps)
	}
	if ps := agentList(); len(ps) != 1 || ps[0].A != "a" {
		t.Fatalf("agent call after a human scan: %+v", ps)
	}
	if ps, _ := d.CallAs(agent, "conflicts.list", json.RawMessage(`{"repo_id":"elsewhere"}`)); len(ps.([]ConflictPair)) != 0 {
		t.Fatalf("cached pairs not filtered by repo: %+v", ps)
	}

	// once the window has passed, the agent's call scans again
	run(t, wb, "git", "checkout", "-q", "--", "f.txt")
	d.cf.mu.Lock()
	d.cf.scanned = time.Now().Add(-agentConflictsEvery - time.Second)
	d.cf.mu.Unlock()
	if ps := agentList(); len(ps) != 0 {
		t.Fatalf("agent call after the window did not rescan: %+v", ps)
	}
	if !scanned().After(first) {
		t.Fatal("no new scan after the window")
	}
}

// conflicts.list works over the socket without the human token.
func TestConflictsListWithoutToken(t *testing.T) {
	d, _, _ := twoSessions(t)
	c := dialPipe(t, d)
	if e := c.call("conflicts.list", map[string]any{}, ""); e != "" {
		t.Fatalf("conflicts.list without the token: %s", e)
	}
}
