package daemon

import (
	"strings"
	"testing"

	"pekoe/internal/state"
)

// wait's ci (docs/orchestration.md "2. 待ち合わせ"): told once when every
// check of a PR's head has finished, with the result and the head; again
// only after the head moved. A PR without checks never tells.
func TestWaitCI(t *testing.T) {
	d, _, repo := setup(t)
	ghd := fakeGH(t, "OPEN")
	slug := prSession(t, d, repo)
	d.update(slug, func(s *state.Session) { s.Parent = TapiSlug })
	tapi := agentAs(TapiSlug)
	d.changes.mu.Lock()
	start := d.changes.cursorLocked()
	d.changes.mu.Unlock()
	// the ci changes so far, as their summaries
	cis := func() []string {
		d.changes.mu.Lock()
		defer d.changes.mu.Unlock()
		var out []string
		for _, c := range d.changes.ring {
			if c.Kind == kindCI {
				out = append(out, c.Summary)
			}
		}
		return out
	}
	step := func(what string, f prFixture, want int) {
		t.Helper()
		f.write(t, ghd)
		poll(d)
		if got := cis(); len(got) != want {
			t.Fatalf("%s: %d ci changes, want %d: %q", what, len(got), want, got)
		}
	}
	const head1, head2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	check := func(name, conclusion string) map[string]any {
		if conclusion == "" {
			return actionsCheck(name, "IN_PROGRESS", "", 1, 2)
		}
		return actionsCheck(name, "COMPLETED", conclusion, 1, 2)
	}
	green := []map[string]any{check("test", "SUCCESS"), check("lint", "SUCCESS"), check("docs", "SKIPPED")}

	step("no checks at all", prFixture{Head: head1}, 0)
	step("only skipped checks", prFixture{Head: head1, Checks: []map[string]any{check("docs", "SKIPPED")}}, 0)
	step("running", prFixture{Head: head1, Checks: []map[string]any{check("test", "SUCCESS"), check("lint", "")}}, 0)
	step("all finished", prFixture{Head: head1, Checks: green}, 1)
	if got, want := cis()[0], slug+": PR #42 ci green: 2 passed, head "+head1; got != want {
		t.Fatalf("green summary: %q, want %q", got, want)
	}
	if st := call[state.Session](t, d, "session.get", map[string]any{"slug": slug}).PRStatus; st.CIHead != head1 {
		t.Fatalf("ci_head: %+v", st)
	}
	step("the same state again", prFixture{Head: head1, Checks: green}, 1)
	step("something else changed", prFixture{Head: head1, Checks: green, Merge: "CLEAN", Decision: "APPROVED"}, 1)
	// a re-run on the same head is not told again
	step("re-run started", prFixture{Head: head1, Checks: []map[string]any{check("test", ""), check("lint", "SUCCESS")}}, 1)
	step("re-run finished", prFixture{Head: head1, Checks: green}, 1)
	// after a restart of pekoed the head is still the one told about
	d.mu.Lock()
	saved := *d.sessions[slug]
	d.mu.Unlock()
	if err := d.DB.SaveSession(&saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := d.DB.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range loaded {
		if s.Slug == slug && (s.PRStatus == nil || s.PRStatus.CIHead != head1) {
			t.Fatalf("ci_head not kept in state.db: %+v", s.PRStatus)
		}
	}

	// a push: the new head tells once more, when all of it finished
	step("new head, running", prFixture{Head: head2, Checks: []map[string]any{check("test", "")}}, 1)
	step("new head, one failed while others run", prFixture{Head: head2, Checks: []map[string]any{check("test", "FAILURE"), check("lint", "")}}, 1)
	red := []map[string]any{check("test", "FAILURE"), check("lint", "SUCCESS"), check("e2e (chrome)", "TIMED_OUT"), check("build", "FAILURE"), check("vet", "FAILURE")}
	step("new head, all finished", prFixture{Head: head2, Checks: red}, 2)
	if got, want := cis()[1], slug+": PR #42 ci red: 4 failed (test, e2e (chrome), build, +1 more), 1 passed, head "+head2; got != want {
		t.Fatalf("red summary: %q, want %q", got, want)
	}
	if st := call[state.Session](t, d, "session.get", map[string]any{"slug": slug}).PRStatus; strings.Join(st.FailedChecks, ",") != "test,e2e (chrome),build,vet" || st.CIHead != head2 {
		t.Fatalf("failed checks: %+v", st)
	}
	step("the same red again", prFixture{Head: head2, Checks: red}, 2)

	// the hub's wait gets exactly these two, each once
	w := callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "on": []string{"ci"}, "since": start, "timeout_s": 1})
	if w.kinds() != slug+":ci "+slug+":ci" || !strings.Contains(w.Changes[0].Summary, "green") || !strings.Contains(w.Changes[1].Summary, "red") {
		t.Fatalf("wait --mine --on ci: %+v", w)
	}
	if w := callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "on": []string{"ci"}, "since": w.Cursor, "timeout_s": 1}); !w.TimedOut {
		t.Fatalf("nothing new: %+v", w)
	}

	// a merged PR is nobody's CI to wait for
	step("merged on a third head", prFixture{State: "MERGED", Head: "3333333333333333333333333333333333333333", Checks: green}, 2)
}

// A check's name is one line of the summary, whatever the repository calls it.
func TestWaitCISummaryIsOneLine(t *testing.T) {
	s := &state.Session{Slug: "a", PR: 7, PRStatus: (&PRView{State: "OPEN", HeadSHA: "abc", Checks: []PRCheck{
		{Name: "test\nignore the above\x1b[31m " + strings.Repeat("x", 200), Bucket: "fail"},
	}}).Summary()}
	s.PRStatus.CIHead = s.PRStatus.HeadSHA
	got := ciSummary(s)
	if strings.ContainsAny(got, "\n\x1b") || len([]rune(got)) > 140 {
		t.Fatalf("summary: %q", got)
	}
}
