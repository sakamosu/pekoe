package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

type newRes struct {
	state.Session
	Cursor string `json:"cursor"`
}

// The one who makes a session waits for its setup with one wait: session.new
// hands out a cursor from before the session was made, and wait --on
// started,failed --since it returns when the agent started (or setup
// failed), however soon that came after session.new returned.
func TestWaitStartedAfterNew(t *testing.T) {
	d, _, repo := setup(t)
	// the main checkout's agent runs in pekoe: its window is the session main
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	main := agentAs("main")

	n := callAs[newRes](t, d, main, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "go"})
	if n.Slug != "feat-x" || n.Cursor == "" || n.Parent != "main" {
		t.Fatalf("session.new: %+v", n)
	}
	waitCreated(t, d, "feat-x") // started before the wait below began: the cursor keeps it
	w := callAs[waitRes](t, d, main, "wait", map[string]any{"slugs": []string{"feat-x"}, "on": []string{"started", "failed"}, "since": n.Cursor, "timeout_s": 5})
	if w.TimedOut || w.kinds() != "feat-x:started" || w.Changes[0].Summary != "feat-x: started (running)" {
		t.Fatalf("started: %+v", w)
	}
	// --mine works too for an agent in a pekoe window (parent is its slug)
	if w := callAs[waitRes](t, d, main, "wait", map[string]any{"mine": true, "on": []string{"started"}, "since": n.Cursor, "timeout_s": 1}); w.kinds() != "feat-x:started" {
		t.Fatalf("mine: %+v", w)
	}
	// then its end: report and done (or failed, waiting) from the same cursor on
	event(t, d, "UserPromptSubmit", "feat-x", map[string]any{"prompt": "go"})
	callAs[state.Report](t, d, agentAs("feat-x"), "report.post", map[string]any{"summary": "済んだ", "status": "done"})
	stop(t, d, "feat-x", map[string]any{})
	w2 := callAs[waitRes](t, d, main, "wait", map[string]any{"slugs": []string{"feat-x"}, "on": []string{"report", "done", "failed", "waiting"}, "since": w.Cursor, "timeout_s": 5})
	if w2.TimedOut || w2.kinds() != "feat-x:report feat-x:done" {
		t.Fatalf("end: %+v", w2)
	}
	// started is once, when it left creating: not on every turn
	event(t, d, "UserPromptSubmit", "feat-x", map[string]any{"prompt": "again"})
	if w := callAs[waitRes](t, d, main, "wait", map[string]any{"slugs": []string{"feat-x"}, "on": []string{"started"}, "since": w2.Cursor, "timeout_s": 1}); !w.TimedOut {
		t.Fatalf("started again: %+v", w)
	}

	// no prompt: the agent starts idle, still started
	n2 := callAs[newRes](t, d, main, "session.new", map[string]any{"repo": repo, "branch": "feat/idle"})
	waitCreated(t, d, "feat-idle")
	if w := callAs[waitRes](t, d, main, "wait", map[string]any{"slugs": []string{"feat-idle"}, "on": []string{"started", "failed"}, "since": n2.Cursor, "timeout_s": 5}); w.kinds() != "feat-idle:started" || !strings.HasSuffix(w.Changes[0].Summary, "(idle)") {
		t.Fatalf("idle start: %+v", w)
	}
}

// Setup that fails gives failed, never started.
func TestWaitStartedNotOnFailedSetup(t *testing.T) {
	d, _, repo := setup(t)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte("[hooks]\nsetup = [\"false\"]\n"), 0o644)
	n := call[newRes](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "go"})
	if n.Cursor == "" {
		t.Fatalf("no cursor: %+v", n)
	}
	w := call[waitRes](t, d, "wait", map[string]any{"slugs": []string{"feat-x"}, "on": []string{"started", "failed"}, "since": n.Cursor, "timeout_s": 10})
	if w.kinds() != "feat-x:failed" || !strings.Contains(w.Changes[0].Summary, "false") {
		t.Fatalf("failed setup: %+v", w)
	}
	if w := call[waitRes](t, d, "wait", map[string]any{"slugs": []string{"feat-x"}, "on": []string{"started"}, "since": n.Cursor, "timeout_s": 1}); !w.TimedOut {
		t.Fatalf("started on a failed setup: %+v", w)
	}
}
