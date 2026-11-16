package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// waitState waits until slug is in st (the queue runs in the background:
// runNew, and pumpQueue in tests is called by hand).
func waitState(t *testing.T, d *Daemon, slug string, st ...state.State) state.Session {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); ; sleep10ms() {
		s, err := d.snapshot(slug)
		if err == nil {
			for _, x := range st {
				if s.State == x {
					return *s
				}
			}
		}
		if time.Now().After(end) {
			t.Fatalf("%s: not %v (%+v, %v)", slug, st, s, err)
		}
	}
}

// changeKinds are the wait changes of slug so far, in order.
func changeKinds(d *Daemon, slug string) []string {
	d.changes.mu.Lock()
	defer d.changes.mu.Unlock()
	var out []string
	for _, c := range d.changes.ring {
		if c.Slug == slug {
			out = append(out, c.Kind)
		}
	}
	return out
}

func setCap(d *Daemon, n int) {
	d.cfgMu.Lock()
	g := *d.Global
	g.Orchestrator.MaxRunning = n
	d.Global = &g
	d.cfgMu.Unlock()
}

// Without a cap and without --hold nothing is queued: session.new starts
// the creation at once, as before.
func TestQueueOffByDefault(t *testing.T) {
	d, _, repo := setup(t)
	for _, b := range []string{"feat/a", "feat/b", "feat/c"} {
		s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": b, "prompt": "go"})
		if s.State != state.Creating {
			t.Fatalf("%s: %s, want creating", b, s.State)
		}
	}
	for _, slug := range []string{"feat-a", "feat-b", "feat-c"} {
		if s := waitState(t, d, slug, state.Running); s.Queue != "" {
			t.Fatalf("queue %q", s.Queue)
		}
		if d.DB.Get(queuedKey(slug)) != "" {
			t.Fatal("kept for the queue")
		}
	}
}

// max_running = 1: the second and third sessions wait, and start one by
// one, oldest first, as the one at work stops.
func TestQueueMaxRunning(t *testing.T) {
	d, fe, repo := setup(t)
	setCap(d, 1)
	a := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	if a.State != state.Creating {
		t.Fatalf("a: %s", a.State)
	}
	waitState(t, d, "feat-a", state.Running)
	b := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": "do b"})
	time.Sleep(5 * time.Millisecond) // c is younger
	c := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/c", "prompt": "do c"})
	for _, s := range []state.Session{b, c} {
		if s.State != state.Queued || !strings.Contains(s.Queue, "max_running = 1") {
			t.Fatalf("%s: %s %q", s.Slug, s.State, s.Queue)
		}
		if _, err := os.Stat(s.Path); err == nil {
			t.Fatalf("%s: worktree made while queued", s.Slug)
		}
	}
	if k := changeKinds(d, "feat-b"); strings.Join(k, ",") != "queued" {
		t.Fatalf("b's changes %v", k)
	}
	// still at work: nothing moves
	d.pumpQueue()
	if s, _ := d.snapshot("feat-b"); s.State != state.Queued {
		t.Fatalf("b started while a works: %s", s.State)
	}
	if err := d.spawnAgent("feat-b", "", "", 0, 0); err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("spawn of a queued session: %v", err)
	}

	d.setState("feat-a", state.Done, "")
	d.pumpQueue()
	waitState(t, d, "feat-b", state.Running)
	if s, _ := d.snapshot("feat-c"); s.State != state.Queued {
		t.Fatalf("c started with b: %s", s.State)
	}
	if k := changeKinds(d, "feat-b"); strings.Join(k, ",") != "queued,started" {
		t.Fatalf("b's changes %v", k)
	}
	if d.DB.Get(queuedKey("feat-b")) != "" {
		t.Fatal("b's queue entry kept after it started")
	}
	// the prompt given to session.new reached the agent
	fe.mu.Lock()
	sp := fe.spawn["feat-b~"+engine.WinAgent]
	fe.mu.Unlock()
	if !strings.Contains(sp.Command, "do b") {
		t.Fatalf("b's agent: %q", sp.Command)
	}

	d.setState("feat-b", state.Idle, "")
	d.pumpQueue()
	waitState(t, d, "feat-c", state.Running)

	// no cap any more: a new session starts at once again
	setCap(d, 0)
	if s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/d", "prompt": "go"}); s.State != state.Creating {
		t.Fatalf("d: %s", s.State)
	}
}

// A queued session can be cancelled (it has nothing on disk yet), and its
// kept start is dropped with it.
func TestQueueCancel(t *testing.T) {
	d, _, repo := setup(t)
	setCap(d, 1)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": "go"})
	call[map[string]any](t, d, "session.cancel", map[string]any{"slug": "feat-b"})
	if _, err := d.snapshot("feat-b"); err == nil {
		t.Fatal("still there")
	}
	if d.DB.Get(queuedKey("feat-b")) != "" {
		t.Fatal("queue entry kept")
	}
}

// --hold: the session does not start before the one it comes after has
// landed; that one failing keeps it waiting (and says so).
func TestQueueHold(t *testing.T) {
	d, _, repo := setup(t)
	if err := callErr(d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "hold": true}); err == nil || !strings.Contains(err.Error(), "--after") {
		t.Fatalf("hold without after: %v", err)
	}
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	b := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": "go", "after": []string{"feat-a"}, "hold": true})
	if b.State != state.Queued || !b.Hold || strings.Join(b.Holding, ",") != "feat-a" || b.Queue != "--hold: waiting for feat-a to land" {
		t.Fatalf("b: %s hold %v %v %q", b.State, b.Hold, b.Holding, b.Queue)
	}
	d.setState("feat-a", state.Done, "")
	d.pumpQueue()
	if s, _ := d.snapshot("feat-b"); s.State != state.Queued {
		t.Fatalf("b started before a landed: %s", s.State)
	}
	d.update("feat-a", func(x *state.Session) { x.State = state.Failed; x.Error = "boom" })
	d.pumpQueue()
	if s, _ := d.snapshot("feat-b"); s.State != state.Queued || s.Queue != "--hold: waiting for feat-a to land (feat-a failed)" {
		t.Fatalf("b after a failed: %s %q", s.State, s.Queue)
	}
	d.update("feat-a", func(x *state.Session) { x.State = state.Landed })
	d.pumpQueue()
	s := waitState(t, d, "feat-b", state.Running)
	if !s.Hold || len(s.Holding) != 0 || s.Queue != "" {
		t.Fatalf("b started: hold %v %v %q", s.Hold, s.Holding, s.Queue)
	}
}

// A held session whose dependency is removed without landing fails (it
// could never start); one removed after it landed only frees it.
func TestQueueHoldDependencyRemoved(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "after": []string{"feat-a"}, "hold": true})
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-a", "force": true})
	s := waitState(t, d, "feat-b", state.Failed)
	if !strings.Contains(s.Error, "feat-a was removed before it landed") {
		t.Fatalf("error %q", s.Error)
	}
	if k := changeKinds(d, "feat-b"); strings.Join(k, ",") != "queued,failed" {
		t.Fatalf("b's changes %v", k)
	}
	if d.DB.Get(queuedKey("feat-b")) != "" {
		t.Fatal("queue entry kept")
	}

	// landed, then removed while the held one still waits for a slot
	setCap(d, 1)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/c", "prompt": "go"})
	waitState(t, d, "feat-c", state.Running)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/e", "prompt": "go"})
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/f", "after": []string{"feat-e"}, "hold": true})
	d.update("feat-e", func(x *state.Session) { x.State = state.Landed })
	d.pumpQueue()
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-e", "force": true})
	f, _ := d.snapshot("feat-f")
	if f.State != state.Queued || len(f.Holding) != 0 || !strings.Contains(f.Queue, "max_running") {
		t.Fatalf("f: %s %v %q", f.State, f.Holding, f.Queue)
	}
	d.setState("feat-c", state.Done, "")
	d.pumpQueue()
	waitState(t, d, "feat-f", state.Idle, state.Running)
}

// A held session starts from origin's base as it is when it starts (the
// merge it waited for is there), and its branch does not track the base.
func TestQueueHoldStartsFromOrigin(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "after": []string{"feat-a"}, "hold": true})
	// a's PR merged on the remote: the local main does not have it
	other := filepath.Join(t.TempDir(), "other")
	run(t, repo, "git", "clone", "-q", bare, other)
	os.WriteFile(filepath.Join(other, "merged.txt"), []byte("a\n"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "a")
	run(t, other, "git", "push", "-q", "origin", "main")
	d.update("feat-a", func(x *state.Session) { x.State = state.Landed })
	d.pumpQueue()
	s := waitState(t, d, "feat-b", state.Idle, state.Running)
	if _, err := os.Stat(filepath.Join(s.Path, "merged.txt")); err != nil {
		t.Fatalf("held session does not have the merge: %v", err)
	}
	if up, err := gitx.Run(s.Path, "rev-parse", "--abbrev-ref", "feat/b@{upstream}"); err == nil {
		t.Fatalf("branch tracks %s", up)
	}
	// not held: from the local base, as before
	n := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/c"})
	waitState(t, d, "feat-c", state.Idle, state.Running)
	if _, err := os.Stat(filepath.Join(n.Path, "merged.txt")); err == nil {
		t.Fatal("a session without --hold started from origin")
	}
}

// A queued session stays queued across a restart while its start is kept,
// and fails like an interrupted creation when it is not.
func TestQueueRestore(t *testing.T) {
	d, _, repo := setup(t)
	setCap(d, 1)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": "go"})
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/c", "prompt": "go"})
	_ = d.DB.Delete(queuedKey("feat-c"))
	for _, slug := range []string{"feat-b", "feat-c"} {
		s, _ := d.snapshot(slug)
		d.restoreQueued(s)
	}
	if s, _ := d.snapshot("feat-b"); s.State != state.Queued {
		t.Fatalf("b: %s", s.State)
	}
	if s, _ := d.snapshot("feat-c"); s.State != state.Failed || !strings.Contains(s.Error, "restart") {
		t.Fatalf("c: %s %q", s.State, s.Error)
	}
}
