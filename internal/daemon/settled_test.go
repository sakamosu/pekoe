package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// The settled condition (settled.go): a PR whose review and CI pass is
// not asked to be merged while its session is still at work — its agent
// running or waiting, work not committed in its worktree, or more sent to
// it after its last report. The detail says which.
func TestMergeSettled(t *testing.T) {
	for name, tc := range map[string]struct {
		make func(t *testing.T, d *Daemon)
		want string
	}{
		"running": {func(t *testing.T, d *Daemon) { d.setState("main", state.Running, "") }, "agent is running"},
		"waiting": {func(t *testing.T, d *Daemon) { d.setState("main", state.Waiting, "permission") }, "agent is waiting (permission)"},
		"uncommitted": {func(t *testing.T, d *Daemon) {
			s, _ := d.snapshot("main")
			os.WriteFile(filepath.Join(s.Path, "README.md"), []byte("changed\n"), 0o644)
		}, "worktree has 1 uncommitted or untracked files"},
		"untracked": {func(t *testing.T, d *Daemon) {
			s, _ := d.snapshot("main")
			os.WriteFile(filepath.Join(s.Path, "new.txt"), []byte("x\n"), 0o644)
		}, "worktree has 1 uncommitted or untracked files"},
		"sent after the report": {func(t *testing.T, d *Daemon) {
			callAs[state.Report](t, d, agentAs("main"), "report.post", map[string]any{"summary": "done", "status": "done"})
			time.Sleep(2 * time.Millisecond)
			d.DB.AddSend(&state.SendRecord{At: time.Now(), From: TapiSlug, To: "main", Kind: "send", Text: "also unify X", Via: "socket", OK: true})
		}, "+tapi sent it more (send, "},
		"sent with no report": {func(t *testing.T, d *Daemon) {
			d.DB.AddSend(&state.SendRecord{At: time.Now(), From: "human", To: "main", Kind: "send", Text: "one more thing", Via: "socket", OK: true})
		}, "and it has not reported done since"},
	} {
		t.Run(name, func(t *testing.T) {
			d, _ := mergeSetup(t)
			tc.make(t, d)
			c := mergeCheckOf(t, d)
			if c.Ready || strings.Join(c.Missing, ",") != "settled" {
				t.Fatalf("check: %+v", c)
			}
			if got := c.Conds[len(c.Conds)-1]; got.Name != "settled" || !strings.Contains(got.Detail, tc.want) {
				t.Fatalf("settled: %+v", got)
			}
			d.checkMerges()
			if its := mergeItems(t, d, "open"); len(its) != 0 {
				t.Fatalf("asked to merge a session at work: %+v", its)
			}
		})
	}
}

// What does not hold the merge: a send that did not reach the agent, one
// the agent reported done after, and pekoed's own merge record.
func TestMergeSettledHolds(t *testing.T) {
	d, _ := mergeSetup(t)
	d.DB.AddSend(&state.SendRecord{At: time.Now(), From: TapiSlug, To: "main", Kind: "send", Text: "x", OK: false, Error: "can't find window"})
	d.DB.AddSend(&state.SendRecord{At: time.Now(), From: TapiSlug, To: "main", Kind: "send", Text: "more", OK: true})
	time.Sleep(2 * time.Millisecond)
	callAs[state.Report](t, d, agentAs("main"), "report.post", map[string]any{"summary": "did it", "status": "done"})
	d.DB.AddSend(&state.SendRecord{At: time.Now(), From: callerPekoed, To: "main", Kind: "merge", Text: "gh pr merge", OK: false})
	if c := mergeCheckOf(t, d); !c.Ready {
		t.Fatalf("check: %+v", c)
	}
}

// Asked, then the session went back to work (tapi sent it more and its
// agent runs): the item is withdrawn with why, and a "merge" answered to
// it before the tick merges nothing (runMerge checks again).
func TestMergeSettledWithdrawsAndRefuses(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	d.setState("main", state.Running, "")
	answerMerge(t, d, it.ID)
	run := waitMergeEnd(t, d, it.ID)
	if run.State != state.MergeFailed || !strings.Contains(run.Detail, "settled: agent is running") {
		t.Fatalf("run: %+v", run)
	}
	if ms := ghMerges(ghd); len(ms) != 0 {
		t.Fatalf("merged: %q", ms)
	}

	// asked again once it is settled; withdrawn when it goes back to work
	d.setState("main", state.Done, "")
	d.checkMerges()
	its := mergeItems(t, d, "open")
	if len(its) != 1 {
		t.Fatalf("not asked again: %+v", its)
	}
	s, _ := d.snapshot("main")
	os.WriteFile(filepath.Join(s.Path, "wip.txt"), []byte("x\n"), 0o644)
	d.checkMerges()
	got := inboxGet(t, d, its[0].ID)
	if got.Status != state.InboxWithdrawn || got.Answer == nil || !strings.Contains(got.Answer.Note, "settled: worktree has 1 uncommitted") {
		t.Fatalf("not withdrawn: %+v %+v", got.Status, got.Answer)
	}
}

// A PR merged outside pekoe (on GitHub) while the session's agent works:
// its window and worktree stay, the PR is recorded merged, land_held and a
// pr change say why. Once the agent is done and nothing is left
// uncommitted, it lands.
func TestMergedWhileWorking(t *testing.T) {
	for name, tc := range map[string]struct {
		hold    func(t *testing.T, d *Daemon, s state.Session)
		release func(t *testing.T, d *Daemon, s state.Session)
		want    string
	}{
		"running": {
			func(t *testing.T, d *Daemon, s state.Session) { d.setState(s.Slug, state.Running, "") },
			func(t *testing.T, d *Daemon, s state.Session) { d.setState(s.Slug, state.Done, "") }, // lands on going done
			"agent is running",
		},
		"uncommitted": {
			func(t *testing.T, d *Daemon, s state.Session) {
				os.WriteFile(filepath.Join(s.Path, "wip.txt"), []byte("x\n"), 0o644)
			},
			func(t *testing.T, d *Daemon, s state.Session) {
				os.Remove(filepath.Join(s.Path, "wip.txt"))
				d.watchMerges() // the next round lands it
			},
			"worktree has 1 uncommitted or untracked files",
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, count := landingSetup(t)
			fe := d.Eng.(*fakeEngine)
			s := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
			d.setState("feat-x", state.Idle, "")
			if !fe.Alive("feat-x", "agent") {
				t.Fatal("no agent window to begin with")
			}
			tc.hold(t, d, s)
			poll(d)
			d.watchMerges()
			var x state.Session
			waitFor(t, func() bool {
				x = call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"})
				_, busy := d.finishing.Load("feat-x")
				return x.LandHeld != "" && !busy
			})
			if x.State == state.Landed || !x.Landing || x.PRStatus == nil || x.PRStatus.State != "MERGED" ||
				!strings.Contains(x.LandHeld, "PR #42 merged while the session is still at work ("+tc.want) {
				t.Fatalf("held session: %+v", x)
			}
			if !fe.Alive("feat-x", "agent") {
				t.Fatal("agent window closed while it worked")
			}
			if _, err := os.Stat(s.Path); err != nil {
				t.Fatal("worktree removed while it worked")
			}
			if got := teardowns(count); len(got) != 0 {
				t.Fatalf("teardown ran: %v", got)
			}
			d.watchMerges() // still at work: held again, told once
			if n := heldChanges(d, "feat-x"); n != 1 {
				t.Fatalf("held told %d times", n)
			}

			tc.release(t, d, s)
			x = waitLanded(t, d, "feat-x")
			if x.LandHeld != "" || x.Landing {
				t.Fatalf("landed: %+v", x)
			}
			if fe.Alive("feat-x", "agent") {
				t.Fatal("agent window left after landing")
			}
			if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
				t.Fatal("worktree not removed after landing")
			}
		})
	}
}

// heldChanges counts the pr changes that told slug's landing was held.
func heldChanges(d *Daemon, slug string) int {
	d.changes.mu.Lock()
	defer d.changes.mu.Unlock()
	n := 0
	for _, c := range d.changes.ring {
		if c.Kind == kindPR && c.Slug == slug && strings.Contains(c.Summary, "still at work") {
			n++
		}
	}
	return n
}

// The time in the settled reason is local, like the rest of what pekoe
// shows (the send log keeps it in UTC). On a machine whose zone is UTC the
// two are the same and this only checks the format.
func TestMergeSettledLocalTime(t *testing.T) {
	d, _ := mergeSetup(t)
	at := time.Now().Add(time.Second).Truncate(time.Second)
	d.DB.AddSend(&state.SendRecord{At: at.UTC(), From: "human", To: "main", Kind: "send", Text: "one more", Via: "socket", OK: true})
	c := mergeCheckOf(t, d)
	got := c.Conds[len(c.Conds)-1].Detail
	if want := "(send, " + at.Local().Format("15:04:05") + ")"; !strings.Contains(got, want) {
		t.Fatalf("settled detail %q, want %q", got, want)
	}
}

// A commit after pekoe land (not pushed, not reviewed) is in no PR: the
// PR's head lacks it, so the session is not settled and nobody is asked
// to merge a PR that would leave it out.
func TestMergeSettledCommitAfterLand(t *testing.T) {
	d, _ := mergeSetup(t)
	s, _ := d.snapshot("main")
	head := strings.TrimSpace(gitOut(t, s.Path, "rev-parse", "HEAD"))
	d.update("main", func(x *state.Session) { x.PRStatus.HeadSHA = head })
	review(t, d, "pass", head)
	d.setState("main", state.Done, "")
	if c := mergeCheckOf(t, d); !c.Ready {
		t.Fatalf("at the PR's head: %+v", c.Conds)
	}
	os.WriteFile(filepath.Join(s.Path, "later.txt"), []byte("x\n"), 0o644)
	run(t, s.Path, "git", "add", "later.txt")
	run(t, s.Path, "git", "commit", "-qm", "later work, not pushed")
	c := mergeCheckOf(t, d)
	if c.Ready || strings.Join(c.Missing, ",") != "settled" {
		t.Fatalf("check: %+v", c)
	}
	if got := c.Conds[len(c.Conds)-1]; !strings.Contains(got.Detail, "HEAD has 1 commit not in PR #42 (run pekoe land)") {
		t.Fatalf("settled: %+v", got)
	}
	d.checkMerges()
	if its := mergeItems(t, d, "open"); len(its) != 0 {
		t.Fatalf("asked to merge without the commit: %+v", its)
	}
}

// A PR merged while the agent worked (land held); the agent commits more
// and stops before pekoe land: the commit is in no PR, so the landing
// stays held (window and worktree kept) and says so. Once the commit is
// gone from HEAD (a person dropped it), it lands.
func TestMergedWhileWorkingCommitAfterMerge(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitCreated(t, d, "feat-x")
	s, _ := d.snapshot("feat-x")
	head := strings.TrimSpace(gitOut(t, s.Path, "rev-parse", "HEAD"))
	d.update("feat-x", func(x *state.Session) {
		x.Landing, x.PR = true, 7
		x.PRStatus = &state.PRStatus{State: "OPEN", HeadSHA: head}
	})
	d.setState("feat-x", state.Running, "")
	d.landMerged("feat-x") // GitHub: merged at head, the agent still at work
	if x, _ := d.snapshot("feat-x"); x.LandHeld == "" || x.State == state.Landed {
		t.Fatalf("not held: %+v", x.State)
	}
	os.WriteFile(filepath.Join(s.Path, "more.txt"), []byte("more\n"), 0o644)
	run(t, s.Path, "git", "add", "more.txt")
	run(t, s.Path, "git", "commit", "-qm", "more work after the merge")
	d.setState("feat-x", state.Done, "") // it stops to ask something before pekoe land
	var x *state.Session
	waitFor(t, func() bool {
		x, _ = d.snapshot("feat-x")
		_, busy := d.finishing.Load("feat-x")
		return !busy && strings.Contains(x.LandHeld, "HEAD has 1 commit not in the merged PR #7")
	})
	if x.State == state.Landed || !x.Landing {
		t.Fatalf("held session: %+v", x)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatal("worktree removed with a commit in no PR")
	}
	d.watchMerges() // still there: held again
	if x, _ := d.snapshot("feat-x"); x.State == state.Landed {
		t.Fatal("landed with a commit in no PR")
	}

	run(t, s.Path, "git", "reset", "-q", "--hard", head)
	d.watchMerges()
	waitLanded(t, d, "feat-x")
}
