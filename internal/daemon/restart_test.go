package daemon

import (
	"strings"
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
// restore has fallbackDetect read its screen once, which settles the
// session as the Stop would have: done, so that the pekoe skill's wait for
// one session (--on report,done,failed,waiting,removed, without idle)
// returns, with "no report", and the max_running slot is freed.
func TestRestoreRechecksLostStop(t *testing.T) {
	d2, fe := runningAcrossRestart(t)
	cur := d2.changes.cursor()
	fe.mu.Lock()
	fe.lines = []string{"> ", "  ? for shortcuts"} // the agent's prompt is up: idle
	fe.mu.Unlock()
	d2.fallbackDetect() // still within the quiet time from the restore
	if s, _ := d2.snapshot("main"); s.State != state.Running {
		t.Fatalf("right after the restart: %s, want running until the screen is still", s.State)
	}
	quietSinceRestart(d2)
	d2.fallbackDetect()
	if s, _ := d2.snapshot("main"); s.State != state.Done {
		t.Fatalf("after restart the idle agent is %q, want done (Stop hook lost during the restart)", s.State)
	}
	res := call[waitResult](t, d2, "wait", map[string]any{"slugs": []string{"main"}, "on": []string{"report", "done", "failed", "waiting", "removed"}, "since": cur, "timeout_s": 1})
	if len(res.Changes) != 1 || res.Changes[0].Kind != "done" || !strings.Contains(res.Changes[0].Summary, "no report") {
		t.Fatalf("wait --on report,done,failed,waiting,removed after the lost Stop: %+v", res)
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

// The Notification (permission_prompt) is what was lost while pekoed
// restarted: the session comes back running with Claude Code's
// permission dialog (or AskUserQuestion's menu) on screen. The recheck
// makes it wait, as the Notification would have: the permission modal,
// wait --on waiting and the OS notification come, and nothing types into
// the dialog. The answer's hook ends the recheck.
func TestRestoreRecheckLostPermissionNotification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		kind  string
	}{
		{"edit", []string{"Do you want to make this edit to a.ts?", "❯ 1. Yes", "  2. Yes, allow all edits", "  3. No", "", ""}, "permission"},
		{"bash", []string{"Bash command", "  rm -rf build", "Do you want to proceed?", "❯ 1. Yes", "  2. Yes, and don't ask again for rm commands", "  3. No, and tell Claude what to do differently (esc)", ""}, "permission"},
		{"menu", []string{"☐ Format", "Which format should the export use?", "❯ 1. CSV", "  2. JSON", "Enter to select · ↑/↓ to navigate · Esc to cancel"}, "question"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d2, fe := runningAcrossRestart(t)
			cur := d2.changes.cursor()
			fe.mu.Lock()
			fe.lines = tc.lines
			fe.mu.Unlock()
			quietSinceRestart(d2)
			d2.fallbackDetect()
			if s, _ := d2.snapshot("main"); s.State != state.Waiting || s.WaitKind != tc.kind {
				t.Fatalf("%s on screen after restart: %s/%s, want waiting/%s", tc.name, s.State, s.WaitKind, tc.kind)
			}
			res := call[waitResult](t, d2, "wait", map[string]any{"slugs": []string{"main"}, "on": []string{"waiting"}, "since": cur, "timeout_s": 1})
			if len(res.Changes) != 1 {
				t.Fatalf("wait --on waiting: %+v", res)
			}
			// answered: the hooks speak again and the screen is not read
			event(t, d2, "PostToolUse", "main", map[string]any{"session_id": "abc", "tool_name": "Bash"})
			d2.mu.Lock()
			recheck := d2.out["main"].recheck
			d2.mu.Unlock()
			if s, _ := d2.snapshot("main"); s.State != state.Running || recheck {
				t.Fatalf("after the answer: %s, recheck %v; want running from the hooks", s.State, recheck)
			}
		})
	}
}

// A turn that ended while pekoed was away with "trust this folder" on
// screen (a grep of output.go, a quoted README, the reply) is over all the
// same. Claude Code's trust dialog comes before any hook, so the words
// count only for a session with no Claude session id yet; one with hooks
// does not become a Trust prompt that then refuses every send.
func TestRestoreRecheckIgnoresTrustWords(t *testing.T) {
	d2, fe := runningAcrossRestart(t)
	fe.mu.Lock()
	fe.lines = []string{
		"⏺ Bash(grep -n trustRe internal/daemon/output.go)",
		"  ⎿  84:	trustRe = regexp.MustCompile(`(?i)trust this folder|do you trust the files`)",
		"⏺ 見つけました。",
		"╭──────────╮",
		"│ >        │",
		"╰──────────╯",
		"  ? for shortcuts",
	}
	fe.mu.Unlock()
	quietSinceRestart(d2)
	d2.fallbackDetect()
	if s, _ := d2.snapshot("main"); s.State != state.Done || s.PendingTool != nil {
		t.Fatalf("finished turn after restart: %s/%s pending %+v, want done", s.State, s.WaitKind, s.PendingTool)
	}
}

// Before the first hook the trust dialog is still read from the screen.
func TestTrustDialogBeforeFirstHook(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.mu.Lock()
	fe.lines = []string{"Do you trust the files in this folder?", "❯ 1. Yes, proceed", "  2. No, exit", ""}
	fe.mu.Unlock()
	d.mu.Lock()
	d.out["main"].lastOut = time.Now().Add(-2 * time.Second)
	d.mu.Unlock()
	d.fallbackDetect()
	if s, _ := d.snapshot("main"); s.State != state.Waiting || s.PendingTool == nil || s.PendingTool.Name != "Trust" {
		t.Fatalf("trust dialog before the first hook: %s pending %+v, want waiting on Trust", s.State, s.PendingTool)
	}
}

// runTest saves test.status = running before the [hooks] test starts. If
// pekoed goes away meanwhile (make install, then a restart), nobody runs
// it any more: restore must not show it running until the next Stop.
func TestRestoreDropsRunningTest(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.update("main", func(x *state.Session) { x.Test = state.Test{Status: "running", At: time.Now()} })
	d2 := restartWith(t, d, fe)
	if s, _ := d2.snapshot("main"); s.Test.Status == "running" {
		t.Fatalf("after restart nobody runs the test, but test = %+v", s.Test)
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

// A turn that ended while pekoed was away with ordinary words in its reply
// that look like a question ("Do you want to proceed", "[y/N]", "allow
// this") is over all the same: the agent's prompt is up and no Claude Code
// dialog (a "1. Yes" choice under "Do you want to …") or [y/n] last line
// is on screen. The recheck must settle it as done, not keep it running
// (or make it wait for an answer) until new output that never comes.
func TestRestoreRecheckIgnoresQuestionWordsInReply(t *testing.T) {
	for _, reply := range []string{
		"Plan ready. Do you want to proceed with the migration",
		"Run it again with --force [y/N] to overwrite.",
		"If you allow this, the script deletes build/.",
	} {
		t.Run(reply, func(t *testing.T) {
			d2, fe := runningAcrossRestart(t)
			fe.mu.Lock()
			fe.lines = []string{"⏺ " + reply, "", "> ", "  ? for shortcuts"}
			fe.mu.Unlock()
			quietSinceRestart(d2)
			d2.fallbackDetect()
			if s, _ := d2.snapshot("main"); s.State != state.Done {
				t.Fatalf("after restart with %q in the reply: %s, want done (Stop hook lost during the restart)", reply, s.State)
			}
		})
	}
}
