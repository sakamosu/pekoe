package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// reopenSetup: mergeSetup's "main" with its PR's head the worktree's real
// HEAD, reviewed there, CI green and its work reported done: the
// Pipeline shows it waiting on the person's merge.
func reopenSetup(t *testing.T) (*Daemon, string) {
	t.Helper()
	d, _ := mergeSetup(t)
	s, _ := d.snapshot("main")
	head := strings.TrimSpace(gitOut(t, s.Path, "rev-parse", "HEAD"))
	d.update("main", func(x *state.Session) { x.PRStatus.HeadSHA = head })
	review(t, d, "pass", head)
	callAs[state.Report](t, d, agentAs("main"), "report.post", map[string]any{"summary": "landed", "status": "done"})
	d.setState("main", state.Done, "")
	time.Sleep(2 * time.Millisecond)
	return d, s.Path
}

func flowSteps(f webFlow) string {
	var out []string
	for _, s := range f.Steps {
		out = append(out, s.Name+":"+s.Status)
	}
	return strings.Join(out, " ")
}

func mainFlow(t *testing.T, d *Daemon) webFlow {
	t.Helper()
	s, err := d.snapshot("main")
	if err != nil {
		t.Fatal(err)
	}
	return d.webFlowOf(s, d.mergeCheck(s))
}

// tell: pekoe send to main, and the agent takes it (UserPromptSubmit).
func tell(t *testing.T, d *Daemon, text string) {
	t.Helper()
	d.DB.AddSend(&state.SendRecord{At: time.Now(), From: TapiSlug, To: "main", Kind: "send", Text: text, Via: "socket", OK: true})
	time.Sleep(2 * time.Millisecond)
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": text})
	time.Sleep(2 * time.Millisecond)
}

// The Pipeline goes back to impl when an instruction comes in after the
// last report (docs/gui-design.md「Pipeline」): (a) while the agent works
// on it, impl is now and the rest is to do again; (b) it stopped with
// nothing changed (a question answered): back to waiting on the merge;
// (c) it stopped with a commit the PR lacks: impl done, review to do;
// (d) the review asked again is now; a push puts the PR in charge again.
func TestPipelineReopen(t *testing.T) {
	const ready = "impl:done review:done pr:done ci:done merge:now"
	t.Run("a: running after an instruction", func(t *testing.T) {
		d, _ := reopenSetup(t)
		if f := mainFlow(t, d); flowSteps(f) != ready || f.Reopened {
			t.Fatalf("before: %s %v", flowSteps(f), f.Reopened)
		}
		tell(t, d, "also unify X")
		f := mainFlow(t, d)
		if got := flowSteps(f); got != "impl:now review:todo pr:todo ci:todo merge:todo" || !f.Reopened {
			t.Fatalf("running: %s %v", got, f.Reopened)
		}
		if f.PR != 42 || f.Steps[2].Detail != "#42: to be pushed again (pekoe land)" {
			t.Fatalf("the PR's number stays: %d %+v", f.PR, f.Steps[2])
		}
		// waiting on a permission: still reopened (the box shows you)
		d.setState("main", state.Waiting, "permission")
		if f := mainFlow(t, d); flowSteps(f) != "impl:now review:todo pr:todo ci:todo merge:todo" || !f.Reopened {
			t.Fatalf("waiting: %s", flowSteps(f))
		}
	})
	t.Run("b: a question answered, nothing changed", func(t *testing.T) {
		d, _ := reopenSetup(t)
		tell(t, d, "why did you pick X?")
		d.setState("main", state.Done, "")
		f := mainFlow(t, d)
		if got := flowSteps(f); got != ready || f.Reopened {
			t.Fatalf("stopped: %s %v %+v", got, f.Reopened, f.Merge)
		}
		if !f.Merge.Ready || !strings.Contains(f.Merge.Conds[len(f.Merge.Conds)-1].Detail, "it stopped after what +tapi sent") {
			t.Fatalf("merge: %+v", f.Merge)
		}
	})
	t.Run("c: a commit the PR lacks, then d: the review asked again, then pushed", func(t *testing.T) {
		d, path := reopenSetup(t)
		tell(t, d, "also unify X")
		os.WriteFile(filepath.Join(path, "later.txt"), []byte("x\n"), 0o644)
		// stopped with the change not committed: the same as a commit
		d.setState("main", state.Done, "")
		if got := flowSteps(mainFlow(t, d)); got != "impl:done review:todo pr:todo ci:todo merge:todo" {
			t.Fatalf("uncommitted: %s", got)
		}
		run(t, path, "git", "add", "later.txt")
		run(t, path, "git", "commit", "-qm", "unify X")
		// a pass of the PR's old head does not count for the new work, nor
		// a pass from before the instruction
		if got := flowSteps(mainFlow(t, d)); got != "impl:done review:todo pr:todo ci:todo merge:todo" {
			t.Fatalf("old pass: %s", got)
		}
		f := mainFlow(t, d)
		if got := flowSteps(f); got != "impl:done review:todo pr:todo ci:todo merge:todo" || f.Reopened {
			t.Fatalf("committed: %s %v", got, f.Reopened)
		}
		if f.Merge == nil || f.Merge.Ready {
			t.Fatalf("merge: %+v", f.Merge)
		}
		// d: the agent asks for the review again (its sub-agent works)
		head := strings.TrimSpace(gitOut(t, path, "rev-parse", "HEAD"))
		d.setState("main", state.Running, "")
		review(t, d, "pending", head)
		if got := flowSteps(mainFlow(t, d)); got != "impl:done review:now pr:todo ci:todo merge:todo" {
			t.Fatalf("review pending: %s", got)
		}
		review(t, d, "pass", mergeHead) // a pass, of some other commit
		if got := flowSteps(mainFlow(t, d)); got != "impl:done review:now pr:todo ci:todo merge:todo" {
			t.Fatalf("pass of another commit: %s", got)
		}
		review(t, d, "pass", head)
		if got := flowSteps(mainFlow(t, d)); got != "impl:done review:done pr:todo ci:todo merge:todo" {
			t.Fatalf("review passed: %s", got)
		}
		// pekoe land pushed it: the PR's head is the new one, its checks run
		d.update("main", func(x *state.Session) {
			x.PRStatus.HeadSHA, x.PRStatus.Pass, x.PRStatus.Pending = head, 0, 3
		})
		if got := flowSteps(mainFlow(t, d)); got != "impl:done review:done pr:done ci:now merge:todo" {
			t.Fatalf("pushed: %s", got)
		}
	})
	// no instruction since the last report, or one before any PR or
	// review: the steps as they were
	t.Run("the usual flow", func(t *testing.T) {
		d, _ := reopenSetup(t)
		d.setState("main", state.Running, "")
		if got := flowSteps(mainFlow(t, d)); got != "impl:done review:done pr:done ci:done merge:todo" {
			t.Fatalf("running with no instruction: %s", got)
		}
		// a branch before its review and PR, told more while it works
		fresh := state.Session{Slug: "fresh", Branch: "fresh", State: state.Running, PromptAt: time.Now(),
			Report: &state.ReportSummary{Status: "progress", At: time.Now().Add(-time.Minute)}}
		if f := d.webFlowOf(&fresh, nil); f.Reopened || flowSteps(f) != "impl:now review:todo pr:todo ci:todo merge:todo" {
			t.Fatalf("before review and PR: %s %v", flowSteps(f), f.Reopened)
		}
	})
}

// The instruction a session began with is no reopening: pekoe land before
// any report (no review asked for) shows the PR and its checks as ever.
func TestPipelineFirstPromptNotReopened(t *testing.T) {
	d, _, _ := inboxSetup(t)
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "do the work"})
	d.update("main", func(x *state.Session) {
		x.PR, x.Landing = 42, true
		x.PRStatus = &state.PRStatus{State: "OPEN", Pass: 2, Pending: 2, HeadSHA: mergeHead}
	})
	f := mainFlow(t, d)
	if got := flowSteps(f); got != "impl:done review:todo pr:done ci:now merge:todo" || f.Reopened {
		t.Fatalf("landed while running: %s %v", got, f.Reopened)
	}
	// a PR closed while it is reopened stays ✗
	d.update("main", func(x *state.Session) { x.PRStatus.State = "CLOSED" })
	tell(t, d, "one more thing")
	if got := flowSteps(mainFlow(t, d)); got != "impl:now review:todo pr:fail ci:todo merge:todo" {
		t.Fatalf("closed: %s", got)
	}
}

// A branch not past impl: impl is now while the agent works; once it has
// stopped, done when the worktree holds work (changes not committed or
// commits beyond the base), not begun (○) when it holds none — a checkout
// of main left as it was.
func TestPipelineImplStopped(t *testing.T) {
	d, _, _ := inboxSetup(t)
	impl := func() string {
		t.Helper()
		return mainFlow(t, d).Steps[0].Status
	}
	if got := impl(); got != stepNow {
		t.Fatalf("running: %s", got)
	}
	d.setState("main", state.Done, "")
	if got := impl(); got != stepTodo {
		t.Fatalf("done, no changes: %s", got)
	}
	d.setState("main", state.Idle, "")
	if got := impl(); got != stepTodo {
		t.Fatalf("idle, no changes: %s", got)
	}
	s, _ := d.snapshot("main")
	os.WriteFile(filepath.Join(s.Path, "work.txt"), []byte("x\n"), 0o644)
	d.refreshDiffstat("main")
	if got := impl(); got != stepDone {
		t.Fatalf("idle, uncommitted: %s", got)
	}
	// committed on top of the base (no origin here: the local main)
	run(t, s.Path, "git", "switch", "-qc", "feat/x")
	run(t, s.Path, "git", "add", "work.txt")
	run(t, s.Path, "git", "commit", "-qm", "work")
	d.update("main", func(x *state.Session) { x.Base = "main" })
	d.stopped.Delete("main")
	if got := impl(); got != stepDone {
		t.Fatalf("idle, a commit beyond the base: %s", got)
	}
	d.setState("main", state.Waiting, "permission")
	if got := impl(); got != stepNow {
		t.Fatalf("waiting: %s", got)
	}
	d.setState("main", state.Failed, "")
	d.update("main", func(x *state.Session) { x.Error = "spawn failed" })
	if got := impl(); got != stepFail {
		t.Fatalf("failed: %s", got)
	}
}
