package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// waitStepRunning waits until slug's creation runs a step whose name starts
// with prefix.
func waitStepRunning(t *testing.T, d *Daemon, slug, prefix string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); sleep10ms() {
		d.mu.Lock()
		for _, st := range d.progress[slug] {
			if strings.HasPrefix(st.Name, prefix) && st.Status == "running" {
				d.mu.Unlock()
				return
			}
		}
		d.mu.Unlock()
	}
	t.Fatalf("%s: step %s never ran", slug, prefix)
}

// waitFile waits until p exists.
func waitFile(t *testing.T, p string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !fileExists(p); sleep10ms() {
		if time.Now().After(end) {
			t.Fatalf("%s never appeared", p)
		}
	}
}

// rm of a session still being created stops its creation and waits for it
// to end: the setup hook that was running does not go on after rm has run
// the teardown and deleted the worktree.
func TestRmDuringSetupStopsCreation(t *testing.T) {
	d, _, repo := setup(t)
	out := t.TempDir()
	marker := filepath.Join(out, "setup-finished")
	writeRepoConfig(t, d, repo, "[hooks]\nsetup = [\"sleep 1; touch "+marker+"\"]\nteardown = [\"touch "+filepath.Join(out, "teardown")+"\"]\n")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitStepRunning(t, d, "feat-x", "setup")
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-x"})
	d.mu.Lock()
	_, still := d.cancels["feat-x"]
	d.mu.Unlock()
	if still {
		t.Errorf("runNew for feat-x is still running after rm returned")
	}
	time.Sleep(2 * time.Second)
	if fileExists(marker) {
		t.Errorf("setup hook finished after rm (teardown ran: %v): whatever setup makes (a DB …) is never torn down", fileExists(filepath.Join(out, "teardown")))
	}
}

// rm of a session being created, then session.new of the same slug: the
// first creation does not go on into the new session (take its cancel, or
// start an agent with the old prompt in the new worktree).
func TestRmThenNewSameSlugKeepsOldCreationOut(t *testing.T) {
	d, fe, repo := setup(t)
	writeRepoConfig(t, d, repo, "[hooks]\nsetup = [\"sleep 1\"]\n")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "OLD-PROMPT"})
	waitStepRunning(t, d, "feat-x", "setup")
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-x", "force": true})
	time.Sleep(300 * time.Millisecond)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "NEW-PROMPT"})
	lostCancel := false
	for end := time.Now().Add(900 * time.Millisecond); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		d.mu.Lock()
		_, ok := d.cancels["feat-x"]
		st := d.sessions["feat-x"].State
		d.mu.Unlock()
		if !ok && st == state.Creating {
			lostCancel = true
		}
	}
	waitState(t, d, "feat-x", state.Running, state.Idle)
	if lostCancel {
		t.Errorf("the new feat-x lost its cancel while still creating (session.cancel can no longer stop its setup)")
	}
	fe.mu.Lock()
	sent := strings.Join(fe.sent, "\n")
	fe.mu.Unlock()
	if strings.Contains(sent, "OLD-PROMPT") {
		t.Errorf("the removed session's creation started an agent with its prompt:\n%s", sent)
	}
	if !strings.Contains(sent, "NEW-PROMPT") {
		t.Errorf("the new session's agent did not get its prompt:\n%s", sent)
	}
}

// rm while finishLand (land after merge) is in its teardown waits for it,
// so a new session of the same slug made right after is not torn down and
// marked landed by the old session's finishLand.
func TestRmDuringFinishLandWaitsForIt(t *testing.T) {
	d, _, repo := setup(t)
	once := filepath.Join(t.TempDir(), "once")
	writeRepoConfig(t, d, repo, "[hooks]\nteardown = [\"if [ -e "+once+" ]; then true; else touch "+once+"; sleep 1.5; fi\"]\n")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitState(t, d, "feat-x", state.Running, state.Idle) // not waitCreations: it cancels the creation
	done := make(chan struct{})
	go func() { d.finishLanding("feat-x"); close(done) }()
	waitFile(t, once) // finishLand is in its teardown
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-x", "force": true})
	select {
	case <-done:
	default:
		t.Errorf("rm returned while finishLand was still tearing feat-x down")
	}
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "NEW"})
	waitState(t, d, "feat-x", state.Running, state.Idle)
	<-done
	cur := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
	if cur.State == state.Landed {
		t.Errorf("the new feat-x was marked landed by the old session's finishLand")
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Errorf("the new feat-x lost its worktree %s: %v", s.Path, err)
	}
}

// finishLand leaves a session alone that replaced the one it tears down
// (removed some other way during its teardown): no worktree removal, no
// landed.
func TestFinishLandLeavesReplacedSession(t *testing.T) {
	d, _, repo := setup(t)
	once := filepath.Join(t.TempDir(), "once")
	writeRepoConfig(t, d, repo, "[hooks]\nteardown = [\"if [ -e "+once+" ]; then true; else touch "+once+"; sleep 1.5; fi\"]\n")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitState(t, d, "feat-x", state.Running, state.Idle) // not waitCreations: it cancels the creation
	done := make(chan struct{})
	go func() { d.finishLanding("feat-x"); close(done) }()
	waitFile(t, once)  // finishLand is in its teardown
	d.forget("feat-x") // gone without rm (which would wait for finishLand)
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "NEW"})
	waitState(t, d, "feat-x", state.Running, state.Idle)
	<-done
	cur := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
	if cur.State == state.Landed {
		t.Errorf("the new feat-x was marked landed by the old session's finishLand")
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Errorf("the new feat-x lost its worktree %s: %v", s.Path, err)
	}
}

// rm of a session pekoe did not create (session.open on the repository's
// own checkout) runs no [hooks] teardown there: no setup ever ran in it, and
// a teardown such as `dropdb app_{{slug}}` would drop the main checkout's DB.
func TestRmOfOpenedCheckoutRunsNoTeardown(t *testing.T) {
	d, _, repo := setup(t)
	marker := filepath.Join(t.TempDir(), "teardown-ran")
	writeRepoConfig(t, d, repo, "[hooks]\nteardown = [\"echo {{slug}} > "+marker+"\"]\n")
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	if s.Managed {
		t.Fatalf("expected an unmanaged session: %+v", s)
	}
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": s.Slug})
	if b, err := os.ReadFile(marker); err == nil {
		t.Errorf("teardown ran in the main checkout %s for slug %s", repo, strings.TrimSpace(string(b)))
	}
	if _, err := d.snapshot(s.Slug); err == nil {
		t.Errorf("%s still there after rm", s.Slug)
	}
}

// rm of a queued session that leaves the queue while rm waits for queueMu
// (pumpQueue starting it) stops the creation that just began, as for any
// creating session.
func TestRmAsQueuedSessionStartsStopsCreation(t *testing.T) {
	d, _, repo := setup(t)
	out := t.TempDir()
	writeRepoConfig(t, d, repo, "[hooks]\nsetup = [\"sleep 1; touch "+out+"/setup-{{slug}}\"]\n")
	setCap(d, 1)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": "go"})
	waitState(t, d, "feat-b", state.Queued)
	d.queueMu.Lock() // as pumpQueue holds it
	rmDone := make(chan struct{})
	go func() {
		defer close(rmDone)
		call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": "feat-b", "force": true})
	}()
	time.Sleep(300 * time.Millisecond) // rm saw it queued and waits for queueMu
	started := d.startQueued("feat-b")
	d.queueMu.Unlock()
	if !started {
		t.Fatal("feat-b did not leave the queue")
	}
	<-rmDone
	d.mu.Lock()
	_, still := d.cancels["feat-b"]
	d.mu.Unlock()
	if still {
		t.Errorf("feat-b's creation still running after rm returned")
	}
	time.Sleep(1500 * time.Millisecond)
	if fileExists(filepath.Join(out, "setup-feat-b")) {
		t.Errorf("feat-b's setup ran to its end after rm")
	}
}
