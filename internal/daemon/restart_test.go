package daemon

import (
	"sync"
	"testing"
	"time"

	"pekoe/internal/state"
)

// restartWith closes d as pekoed's shutdown does and starts a second
// daemon on the same state.db, with the same tmux (fake engine): the
// agents keep running across the restart.
func restartWith(t *testing.T, d *Daemon, fe *fakeEngine) *Daemon {
	t.Helper()
	d.DB.Close()
	d.releaseLock()
	d2, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d2.Eng = fe
	t.Cleanup(func() { d2.DB.Close(); d2.releaseLock() })
	d2.restore()
	return d2
}

// runningAcrossRestart: a session whose turn is going on when pekoed
// stops, and the daemon that starts after it.
func runningAcrossRestart(t *testing.T) (*Daemon, *fakeEngine) {
	t.Helper()
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	event(t, d, "SessionStart", "main", map[string]any{"session_id": "abc", "cwd": repo})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"session_id": "abc", "prompt": "do it"})
	if s, _ := d.snapshot("main"); s.State != state.Running {
		t.Fatalf("before restart: %s", s.State)
	}
	return restartWith(t, d, fe), fe
}

// quietSinceRestart makes restore's recheck due now: the screen has been
// still for longer than fallbackDetect waits.
func quietSinceRestart(d *Daemon) {
	d.mu.Lock()
	d.out["main"].lastOut = time.Now().Add(-2 * time.Second)
	d.mu.Unlock()
}

// A Stop hook that fires while pekoed is restarting is lost (pekoe event
// cannot dial and returns silently). The agent is idle, its window alive:
// restore has fallbackDetect read its screen once, which brings the session
// back to idle, so `pekoe wait --on done,idle` returns and the
// max_running slot is freed.
func TestRestoreRechecksLostStop(t *testing.T) {
	d2, fe := runningAcrossRestart(t)
	fe.mu.Lock()
	fe.lines = []string{"> ", "  ? for shortcuts"} // the agent's prompt is up: idle
	fe.mu.Unlock()
	d2.fallbackDetect() // still within the quiet time from the restore
	if s, _ := d2.snapshot("main"); s.State != state.Running {
		t.Fatalf("right after the restart: %s, want running until the screen is still", s.State)
	}
	quietSinceRestart(d2)
	d2.fallbackDetect()
	if s, _ := d2.snapshot("main"); s.State != state.Idle {
		t.Fatalf("after restart the idle agent is %q, want idle (Stop hook lost during the restart)", s.State)
	}
}

// An agent still in its turn (the spinner line) or at a prompt stays as it
// is, and a hook that arrives ends the recheck: the screen is not read for
// it any more.
func TestRestoreRecheckKeepsBusyAgent(t *testing.T) {
	d2, fe := runningAcrossRestart(t)
	fe.mu.Lock()
	fe.lines = []string{"✻ Thinking… (12s · esc to interrupt)"}
	fe.mu.Unlock()
	quietSinceRestart(d2)
	d2.fallbackDetect()
	if s, _ := d2.snapshot("main"); s.State != state.Running {
		t.Fatalf("busy agent after restart: %s, want running", s.State)
	}
	event(t, d2, "PostToolUse", "main", map[string]any{"session_id": "abc", "tool_name": "Bash"})
	fe.mu.Lock()
	fe.lines = []string{"> "}
	fe.mu.Unlock()
	d2.mu.Lock()
	recheck := d2.out["main"].recheck
	d2.out["main"].lastOut = time.Now().Add(-2 * time.Second)
	d2.mu.Unlock()
	if recheck {
		t.Fatal("a hook arrived but the recheck mark is still set")
	}
	d2.fallbackDetect()
	if s, _ := d2.snapshot("main"); s.State != state.Running {
		t.Fatalf("after a hook the screen decided: %s, want running (hooks only)", s.State)
	}
}

// An Edit permission dialog that was up when pekoed restarted keeps the
// session waiting: it has no spinner line and no [y/n], only Claude
// Code's "Do you want to make this edit" menu.
func TestRestoreRecheckKeepsPermissionDialog(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	event(t, d, "SessionStart", "main", map[string]any{"session_id": "abc", "cwd": repo})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"session_id": "abc", "prompt": "do it"})
	event(t, d, "Notification", "main", map[string]any{"session_id": "abc", "notification_type": "permission_prompt", "message": "Claude needs your permission to use Edit"})
	if s, _ := d.snapshot("main"); s.State != state.Waiting {
		t.Fatalf("before restart: %s", s.State)
	}
	d2 := restartWith(t, d, fe)
	fe.mu.Lock()
	fe.lines = []string{"Do you want to make this edit to a.ts?", "❯ 1. Yes", "  2. Yes, allow all edits", "  3. No", "", ""}
	fe.mu.Unlock()
	quietSinceRestart(d2)
	d2.fallbackDetect()
	if s, _ := d2.snapshot("main"); s.State != state.Waiting || s.WaitKind != "permission" {
		t.Fatalf("permission dialog after restart: %s/%s, want waiting/permission", s.State, s.WaitKind)
	}
}

// Session.unread is saved in state.db (copyLocked fills it from outState):
// restore must start the outState from it, or every unread mark is gone
// after a restart.
func TestRestoreKeepsUnread(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.mu.Lock()
	d.out["main"].unread = 1234
	d.mu.Unlock()
	d.update("main", func(*state.Session) {}) // persist
	saved, _ := d.DB.Sessions()
	if len(saved) != 1 || saved[0].Unread != 1234 {
		t.Fatalf("saved unread = %+v", saved)
	}
	d2 := restartWith(t, d, fe)
	if s, _ := d2.snapshot("main"); s.Unread != 1234 {
		t.Fatalf("unread after restart = %d, want 1234 (state.db had it)", s.Unread)
	}
}

// Two UserPromptSubmit handled at once (each request runs in its own
// goroutine) must take two numbers: one prompt and its snapshot ref would
// otherwise be overwritten, and Prompts count one.
func TestConcurrentPromptsGetOwnNumbers(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	var wg sync.WaitGroup
	for _, p := range []string{"first", "second"} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			d.snapshotPrompt("main", p)
		}(p)
	}
	wg.Wait()
	ps, _ := d.DB.Prompts("main")
	seen := map[int]string{}
	for _, p := range ps {
		if p.N > 0 {
			seen[p.N] = p.Text
		}
	}
	s, _ := d.snapshot("main")
	if len(seen) != 2 || s.Prompts != 2 {
		t.Fatalf("two prompts: %d stored, Prompts = %d (want 2, 2): %+v", len(seen), s.Prompts, ps)
	}
}

// shutdown waits for the hooks being handled before it closes state.db,
// but not for ever.
func TestWaitHooks(t *testing.T) {
	d, _, _ := setup(t)
	d.hooking.Add(1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		d.hooking.Add(-1)
	}()
	start := time.Now()
	d.waitHooks(5 * time.Second)
	if d.hooking.Load() != 0 {
		t.Fatal("waitHooks returned while a hook was being handled")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("waitHooks took %v", el)
	}
	d.stopping.Add(1)
	defer d.stopping.Add(-1)
	start = time.Now()
	d.waitHooks(100 * time.Millisecond)
	if el := time.Since(start); el > time.Second {
		t.Fatalf("waitHooks with a stuck Stop took %v, want about its bound", el)
	}
}
