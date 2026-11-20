package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	// not held, --base given: from the local base as it is, as before
	// (without --base it is fetched too: newbase_test.go)
	n := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/c", "base": "main"})
	waitState(t, d, "feat-c", state.Idle, state.Running)
	if _, err := os.Stat(filepath.Join(n.Path, "merged.txt")); err == nil {
		t.Fatal("a session without --hold, with --base main, started from origin")
	}
}

// --hold with a base that is no branch of origin (a tag) starts from it
// as it is; with base origin/main it starts from origin's main fetched now.
func TestQueueHoldOtherBases(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	run(t, repo, "git", "tag", "v1")
	run(t, repo, "git", "push", "-q", "origin", "v1")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/t", "base": "v1", "after": []string{"feat-a"}, "hold": true})
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/o", "base": "origin/main", "after": []string{"feat-a"}, "hold": true})
	other := filepath.Join(t.TempDir(), "other")
	run(t, repo, "git", "clone", "-q", bare, other)
	os.WriteFile(filepath.Join(other, "merged.txt"), []byte("a\n"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "a")
	run(t, other, "git", "push", "-q", "origin", "main")
	d.update("feat-a", func(x *state.Session) { x.State = state.Landed })
	d.pumpQueue()
	tg := waitState(t, d, "feat-t", state.Idle, state.Running)
	if _, err := os.Stat(filepath.Join(tg.Path, "merged.txt")); err == nil {
		t.Fatal("tag base: started from origin/main")
	}
	o := waitState(t, d, "feat-o", state.Idle, state.Running)
	if _, err := os.Stat(filepath.Join(o.Path, "merged.txt")); err != nil {
		t.Fatalf("origin/main base: not fetched: %v", err)
	}
}

// runNew for a session removed just after it left the queue returns
// (it used to read a nil session and take pekoed down).
func TestRunNewGone(t *testing.T) {
	d, _, repo := setup(t)
	r, err := resolveRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.runNew(&creation{ctx: ctx, cancel: cancel, done: make(chan struct{})}, "nope", r, newP{})
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

// session.kill does not stop a creation (it goes on and starts the agent),
// so it is refused while creating, and while queued (nothing runs yet): a
// stopped mark would take the session off max_running's count, and the
// next one would start beyond the cap.
func TestQueueKillCreatingRefused(t *testing.T) {
	d, _, repo := setup(t)
	writeRepoConfig(t, d, repo, "[hooks]\nsetup = [\"sleep 1\"]\n")
	setCap(d, 1)
	a := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	if a.State != state.Creating {
		t.Fatalf("a: %s", a.State)
	}
	if err := callErr(d, "session.kill", map[string]any{"slug": "feat-a"}); err == nil || !strings.Contains(err.Error(), "being created") {
		t.Errorf("kill while creating: %v", err)
	}
	b := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": "go"})
	if b.State != state.Queued {
		t.Errorf("b started while a is still being created under max_running = 1: %s", b.State)
	}
	if err := callErr(d, "session.kill", map[string]any{"slug": "feat-b"}); err == nil || !strings.Contains(err.Error(), "queued") {
		t.Errorf("kill while queued: %v", err)
	}
	if s, _ := d.snapshot("feat-b"); s.State != state.Queued || s.Stopped {
		t.Errorf("b after the refused kill: %s stopped=%v", s.State, s.Stopped)
	}
	waitState(t, d, "feat-a", state.Running)
	// once its agent runs, kill ends it and the queue goes on
	call[state.Session](t, d, "session.kill", map[string]any{"slug": "feat-a"})
	d.pumpQueue()
	waitState(t, d, "feat-b", state.Creating, state.Running)
	waitCreations(t, d)
}

// A creation counts against max_running even when stopped is set (it starts
// the agent whatever stopped says).
func TestWorkingCountsCreating(t *testing.T) {
	if !working(&state.Session{Managed: true, State: state.Creating, Stopped: true}) {
		t.Error("a stopped creating session does not count")
	}
	if working(&state.Session{Managed: true, State: state.Running, Stopped: true}) {
		t.Error("a stopped running session counts")
	}
}

// session.cancel of a held session while its origin fetch runs stops the
// fetch, and the worktree step does not go on afterwards: no worktree or
// branch is left for a session that no longer exists.
func TestQueueHoldCancelDuringFetch(t *testing.T) {
	d, _, repo := setup(t)
	withOrigin(t, repo)
	// a remote that answers slowly (a stalled network)
	run(t, repo, "git", "remote", "set-url", "origin", "ssh://example.invalid/x.git")
	t.Setenv("GIT_SSH_COMMAND", "sleep 5;false")
	// feat-a with --base: it starts from the local main without fetching
	// (a fresh start would wait the slow remote out, twice, before feat-b)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "base": "main", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	b := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "after": []string{"feat-a"}, "hold": true})
	d.update("feat-a", func(x *state.Session) { x.State = state.Landed })
	d.pumpQueue()
	waitState(t, d, "feat-b", state.Creating)
	time.Sleep(300 * time.Millisecond) // in the fetch
	t0 := time.Now()
	call[map[string]any](t, d, "session.cancel", map[string]any{"slug": "feat-b"})
	if el := time.Since(t0); el > 4*time.Second {
		t.Errorf("cancel waited %s for the fetch", el)
	}
	if _, err := d.snapshot("feat-b"); err == nil {
		t.Fatal("still there")
	}
	waitCreations(t, d)
	if _, err := os.Stat(b.Path); err == nil {
		t.Errorf("worktree %s made after the session was cancelled", b.Path)
	}
	if gitx.BranchExists(repo, "feat/b") {
		t.Errorf("branch feat/b made after the session was cancelled")
	}
}

// A held fetch that takes too long gives up and starts from the local base.
func TestQueueHoldFetchTimeout(t *testing.T) {
	_, _, repo := setup(t)
	withOrigin(t, repo)
	run(t, repo, "git", "remote", "set-url", "origin", "ssh://example.invalid/x.git")
	t.Setenv("GIT_SSH_COMMAND", "sleep 5;false")
	old := fetchBaseTimeout
	fetchBaseTimeout = 200 * time.Millisecond
	t.Cleanup(func() { fetchBaseTimeout = old })
	t0 := time.Now()
	if ref, ok := fetchBase(context.Background(), repo, "main"); ok {
		t.Errorf("fetch from a stalled remote: %s", ref)
	}
	if el := time.Since(t0); el > 4*time.Second {
		t.Errorf("fetch took %s past its timeout", el)
	}
}

// --hold on a session that never lands (one pekoe did not make, such as the
// repository's own checkout) is refused instead of waiting forever.
func TestQueueHoldOnUnmanagedRefused(t *testing.T) {
	d, _, repo := setup(t)
	d.add(&state.Session{Slug: "main", Branch: "main", RepoDir: repo, Path: repo, State: state.Idle, Managed: false, CreatedAt: time.Now()})
	err := callErr(d, "session.new", map[string]any{"repo": repo, "branch": "feat/h", "after": []string{"main"}, "hold": true})
	if err == nil || !strings.Contains(err.Error(), "never lands") {
		t.Fatalf("--hold on an unmanaged session: %v", err)
	}
	if _, err := d.snapshot("feat-h"); err == nil {
		t.Error("made a session though refused")
	}
	// without --hold it is only recorded, as before
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/h", "after": []string{"main"}})
	if len(s.After) != 1 || s.After[0] != "main" {
		t.Errorf("after: %v", s.After)
	}
	waitCreations(t, d)
}

// A held session's fetch that runs into another worktree being added in
// the same repository still starts from origin/<base>. git worktree add
// makes .git/worktrees/<name>/ with gitdir and an empty commondir before
// it writes it, and a fetch then (its connectivity check looks at every
// worktree) fails: "failed to read .git/worktrees/<name>/commondir", then
// "did not send all necessary objects". The test puts such an entry there
// for the first fetch and takes it away before the next one, as the other
// add would be over by then. What git does with the entry in between is
// its own (git 2.56 drops it), so the test only makes it and removes it.
func TestQueueHoldFetchDuringWorktreeAdd(t *testing.T) {
	d, _, repo := setup(t)
	bare := withOrigin(t, repo)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "prompt": "go"})
	waitState(t, d, "feat-a", state.Running)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/o", "base": "main", "after": []string{"feat-a"}, "hold": true})
	other := filepath.Join(t.TempDir(), "other")
	run(t, repo, "git", "clone", "-q", bare, other)
	os.WriteFile(filepath.Join(other, "merged.txt"), []byte("a\n"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "a")
	run(t, other, "git", "push", "-q", "origin", "main")

	half := filepath.Join(repo, ".git", "worktrees", "feat-x")
	real := fetchOrigin
	var calls atomic.Int32
	var first error
	fetchOrigin = func(ctx context.Context, repoDir, branch string) error {
		switch calls.Add(1) {
		case 1: // another session's worktree add is half way
			if err := os.MkdirAll(half, 0o755); err != nil {
				t.Error(err)
			}
			gitdir := filepath.Join(t.TempDir(), "feat-x", ".git") + "\n"
			if err := os.WriteFile(filepath.Join(half, "gitdir"), []byte(gitdir), 0o644); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(filepath.Join(half, "commondir"), nil, 0o644); err != nil {
				t.Error(err)
			}
			first = real(ctx, repoDir, branch)
			return first
		case 2: // ...and is over
			if err := os.RemoveAll(half); err != nil {
				t.Error(err)
			}
		}
		return real(ctx, repoDir, branch)
	}
	t.Cleanup(func() { fetchOrigin = real })

	d.update("feat-a", func(x *state.Session) { x.State = state.Landed })
	d.pumpQueue()
	o := waitState(t, d, "feat-o", state.Idle, state.Running)
	if _, err := os.Stat(filepath.Join(o.Path, "merged.txt")); err != nil {
		t.Fatalf("origin/main not fetched (fetched %d times): %v", calls.Load(), err)
	}
	if first == nil {
		t.Error("the first fetch did not fail on the half-made worktree: this git no longer shows the race, and the test proves nothing")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("fetched %d times, want 2 (the failed one and once more)", n)
	}
}

// fetchBase tries a failed fetch once more, not again, and not at all
// when the creation was cancelled.
func TestFetchBaseRetriesOnce(t *testing.T) {
	_, _, repo := setup(t)
	withOrigin(t, repo)
	real := fetchOrigin
	t.Cleanup(func() { fetchOrigin = real })
	var calls atomic.Int32
	fetchOrigin = func(context.Context, string, string) error {
		calls.Add(1)
		return errors.New("fetch failed")
	}
	if ref, ok := fetchBase(context.Background(), repo, "main"); ok {
		t.Errorf("failing fetch: %s", ref)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("failing fetch: fetched %d times, want 2", n)
	}
	calls.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	fetchOrigin = func(context.Context, string, string) error {
		calls.Add(1)
		cancel()
		return context.Canceled
	}
	if ref, ok := fetchBase(ctx, repo, "main"); ok {
		t.Errorf("cancelled fetch: %s", ref)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("cancelled fetch: fetched %d times, want 1", n)
	}
}
