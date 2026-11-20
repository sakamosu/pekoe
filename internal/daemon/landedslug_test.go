package daemon

import (
	"testing"

	"pekoe/internal/state"
)

// A new session that takes the slug of a landed one starts clean: the
// landed session's prompts, comments and risks are not inherited (undo
// would pick its last prompt, whose snapshot ref is gone).
func TestNewOverLandedSlugStartsClean(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	for i := 0; i < 100; i++ {
		if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"}); s.State != state.Creating {
			break
		}
		sleep()
	}
	for i := 1; i <= 3; i++ {
		if err := d.DB.AddPrompt("feat-x", i, "p", "refs/pekoe/feat-x/p"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.DB.AddComment("feat-x", state.Comment{Path: "a.txt", Line: 1, Text: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.SetRisks("feat-x", []state.Risk{{Path: "a.txt", Text: "old"}}); err != nil {
		t.Fatal(err)
	}
	d.update("feat-x", func(x *state.Session) { x.State = state.Landed; x.Prompts = 3 })

	n := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	if n.Slug != "feat-x" {
		t.Fatalf("slug %s", n.Slug)
	}
	ps, _ := d.DB.Prompts("feat-x")
	for _, p := range ps {
		if p.N > 0 { // #0 is the new session's own start snapshot
			t.Fatalf("prompts of the landed session carried over: %+v", ps)
		}
	}
	if cs, _ := d.DB.Comments("feat-x", false); len(cs) != 0 {
		t.Fatalf("comments carried over: %+v", cs)
	}
	if rs, _ := d.DB.Risks("feat-x"); len(rs) != 0 {
		t.Fatalf("risks carried over: %+v", rs)
	}
}

// session.new over a landed slug: the cursor it returns is from after the
// old session's removed, so the documented
// wait --slug <slug> --on started,failed,removed --since <cursor>
// waits for the new session, not the old one's removal.
func TestNewOverLandedCursorAfterOldRemoved(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	main := agentAs("main")
	callAs[newRes](t, d, main, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitCreated(t, d, "feat-x")
	d.update("feat-x", func(x *state.Session) { x.State = state.Landed })

	n := callAs[newRes](t, d, main, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "go"})
	w := callAs[waitRes](t, d, main, "wait", map[string]any{"slugs": []string{"feat-x"}, "on": []string{"started", "failed", "removed"}, "since": n.Cursor, "timeout_s": 30})
	if len(w.Changes) == 0 || w.Changes[0].Kind != kindStarted {
		t.Fatalf("the new session's wait got %s, want feat-x:started", w.kinds())
	}
}

// A session that takes a landed session's slug is not the parent of the
// old one's children: wait --mine returns its own children only.
func TestWaitMineNotOldLifesChildren(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitCreated(t, d, "feat-x")
	callAs[newRes](t, d, agentAs("feat-x"), "session.new", map[string]any{"repo": repo, "branch": "feat/y"}) // the old feat-x's child
	waitCreated(t, d, "feat-y")
	d.update("feat-x", func(x *state.Session) { x.State = state.Landed })
	n := call[newRes](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	waitCreated(t, d, "feat-x")
	callAs[newRes](t, d, agentAs("feat-x"), "session.new", map[string]any{"repo": repo, "branch": "feat/z"}) // the new one's
	waitCreated(t, d, "feat-z")

	callAs[state.Report](t, d, agentAs("feat-y"), "report.post", map[string]any{"summary": "old child", "status": "done"})
	callAs[state.Report](t, d, agentAs("feat-z"), "report.post", map[string]any{"summary": "new child", "status": "done"})
	w := callAs[waitRes](t, d, agentAs("feat-x"), "wait", map[string]any{"mine": true, "on": []string{"report"}, "since": n.Cursor, "timeout_s": 30})
	if w.kinds() != "feat-z:report" {
		t.Fatalf("the new feat-x's wait --mine got %s, want feat-z:report only", w.kinds())
	}
}
