package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

const (
	mergeHead  = "0123456789abcdef0123456789abcdef01234567"
	mergeHead2 = "89abcdef0123456789abcdef0123456789abcdef"
)

// mergeSetup: session "main" landed (pekoe land made PR #42), with every
// merge condition met: a passing review of the PR's head, CI green, CLEAN,
// no conflict forecast, and the session it comes after ("feat-a") landed.
func mergeSetup(t *testing.T) (*Daemon, *fakeEngine) {
	t.Helper()
	d, fe, repo := inboxSetup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a"})
	waitCreated(t, d, "feat-a")
	d.setState("feat-a", state.Landed, "")
	d.update("main", func(x *state.Session) {
		x.PR, x.Landing, x.After = 42, true, []string{"feat-a"}
		x.PRStatus = &state.PRStatus{State: "OPEN", Pass: 3, MergeState: "CLEAN", HeadSHA: mergeHead}
		x.Overlaps = []state.Overlap{{Slug: "feat-b", Files: []string{"README.md"}}} // overlap that merges: fine
	})
	review(t, d, "pass", mergeHead[:10])
	return d, fe
}

func review(t *testing.T, d *Daemon, verdict, sha string) {
	t.Helper()
	callAs[state.Report](t, d, agentAs("main"), "report.post", map[string]any{"summary": "reviewed", "status": "review", "verdict": verdict, "reviewed_sha": sha})
}

// mergeItems are pekoed's merge items about main, newest first.
func mergeItems(t *testing.T, d *Daemon, status string) []state.InboxItem {
	t.Helper()
	var out []state.InboxItem
	for _, it := range call[[]state.InboxItem](t, d, "inbox.list", map[string]any{"status": status}) {
		if it.From == callerPekoed && it.About == "main" {
			out = append(out, it)
		}
	}
	return out
}

func mergeCheckOf(t *testing.T, d *Daemon) state.MergeCheck {
	t.Helper()
	cs := call[[]state.MergeCheck](t, d, "merge.check", map[string]any{"slug": "main"})
	if len(cs) != 1 {
		t.Fatalf("merge.check: %+v", cs)
	}
	return cs[0]
}

// Completion criterion: everything there → exactly one item, from pekoed,
// kind approve, with pekoed's facts first; not made twice.
func TestMergeEvidenceReady(t *testing.T) {
	d, _ := mergeSetup(t)
	if c := mergeCheckOf(t, d); !c.Ready || len(c.Missing) != 0 || len(c.Conds) != 6 || c.PRHead != mergeHead {
		t.Fatalf("check: %+v", c)
	}
	d.checkMerges()
	d.checkMerges()
	its := mergeItems(t, d, "open")
	if len(its) != 1 {
		t.Fatalf("items: %+v", its)
	}
	it := its[0]
	if it.Kind != "approve" || it.From != callerPekoed || !strings.HasPrefix(it.Question, "evidence ready to merge #42") ||
		strings.Join(it.Options, ",") != "merge,wait,send back" || it.Recommend != nil {
		t.Fatalf("item: %+v", it)
	}
	if f := it.Facts; f == nil || f.PR != 42 || f.PRStatus == nil || f.PRStatus.HeadSHA != mergeHead || f.Merge == nil || !f.Merge.Ready {
		t.Fatalf("facts: %+v", it.Facts)
	}
	if !strings.Contains(it.Context, "review: pass in report #") || !strings.Contains(it.Context, "reviewed in the author's session") {
		t.Fatalf("context: %q", it.Context)
	}
	if c := mergeCheckOf(t, d); c.Inbox != it.ID {
		t.Fatalf("merge.check inbox: %+v", c)
	}
	// the next round makes none either (the PR and head are recorded in state.db)
	d.checkMerges()
	if n := len(mergeItems(t, d, "all")); n != 1 {
		t.Fatalf("%d items", n)
	}
}

// Completion criterion: one condition missing → no item, and merge.check
// names it.
func TestMergeEvidenceMissing(t *testing.T) {
	for name, tc := range map[string]struct {
		cond   string
		break_ func(t *testing.T, d *Daemon)
	}{
		"not landed": {"land", func(t *testing.T, d *Daemon) {
			d.update("main", func(x *state.Session) { x.Landing = false })
		}},
		"review failed": {"review", func(t *testing.T, d *Daemon) { review(t, d, "fail", mergeHead) }},
		"review of another commit": {"review", func(t *testing.T, d *Daemon) {
			review(t, d, "pass", mergeHead2)
		}},
		"ci failed": {"ci", func(t *testing.T, d *Daemon) {
			d.update("main", func(x *state.Session) { c := *x.PRStatus; c.Fail = 1; x.PRStatus = &c })
		}},
		"ci running": {"ci", func(t *testing.T, d *Daemon) {
			d.update("main", func(x *state.Session) { c := *x.PRStatus; c.Pending = 1; x.PRStatus = &c })
		}},
		"merge state": {"merge_state", func(t *testing.T, d *Daemon) {
			d.update("main", func(x *state.Session) { c := *x.PRStatus; c.MergeState = "BLOCKED"; x.PRStatus = &c })
		}},
		"conflict": {"conflicts", func(t *testing.T, d *Daemon) {
			d.update("main", func(x *state.Session) {
				x.Overlaps = []state.Overlap{{Slug: "feat-b", Files: []string{"a.txt"}, Conflict: true, ConflictFiles: []string{"a.txt"}}}
			})
		}},
		"after not landed": {"after", func(t *testing.T, d *Daemon) { d.setState("feat-a", state.Done, "") }},
	} {
		t.Run(name, func(t *testing.T) {
			d, _ := mergeSetup(t)
			tc.break_(t, d)
			d.checkMerges()
			if its := mergeItems(t, d, "all"); len(its) != 0 {
				t.Fatalf("made: %+v", its)
			}
			c := mergeCheckOf(t, d)
			if c.Ready || strings.Join(c.Missing, ",") != tc.cond {
				t.Fatalf("check: %+v", c)
			}
		})
	}
}

// Completion criterion: the PR's head moves after the item was made → it
// is withdrawn; a passing review of the new head makes a new one.
func TestMergeEvidenceHeadMoved(t *testing.T) {
	d, _ := mergeSetup(t)
	d.checkMerges()
	first := mergeItems(t, d, "open")[0]
	d.update("main", func(x *state.Session) { c := *x.PRStatus; c.HeadSHA = mergeHead2; x.PRStatus = &c })
	d.checkMerges()
	if its := mergeItems(t, d, "open"); len(its) != 0 {
		t.Fatalf("still open: %+v", its)
	}
	got := inboxGet(t, d, first.ID)
	if got.Status != state.InboxWithdrawn || got.Answer == nil || got.Answer.By != callerPekoed || !strings.Contains(got.Answer.Note, "PR HEAD moved") {
		t.Fatalf("withdrawn: %+v %+v", got, got.Answer)
	}
	review(t, d, "pass", mergeHead2)
	d.checkMerges()
	its := mergeItems(t, d, "open")
	if len(its) != 1 || its[0].ID == first.ID || !strings.Contains(its[0].Question, mergeHead2[:7]) {
		t.Fatalf("new item: %+v", its)
	}
}

// A condition breaks → withdrawn; it comes back on the same head → asked
// again (pekoed withdrew it, the person never answered). An answered one
// is not asked again for the same head.
func TestMergeEvidenceBreaksAndAnswered(t *testing.T) {
	d, _ := mergeSetup(t)
	d.checkMerges()
	first := mergeItems(t, d, "open")[0]
	d.update("main", func(x *state.Session) { c := *x.PRStatus; c.Fail = 1; x.PRStatus = &c })
	d.checkMerges()
	if g := inboxGet(t, d, first.ID); g.Status != state.InboxWithdrawn || !strings.Contains(g.Answer.Note, "ci: 1 failed") {
		t.Fatalf("withdrawn: %+v", g.Answer)
	}
	d.update("main", func(x *state.Session) { c := *x.PRStatus; c.Fail = 0; x.PRStatus = &c })
	d.checkMerges()
	its := mergeItems(t, d, "open")
	if len(its) != 1 || its[0].ID == first.ID {
		t.Fatalf("asked again: %+v", its)
	}
	call[any](t, d, "inbox.answer", map[string]any{"id": its[0].ID, "rev": inboxGet(t, d, its[0].ID).Rev, "choice": mergeWait})
	d.checkMerges()
	if n := len(mergeItems(t, d, "open")); n != 0 {
		t.Fatalf("asked again after wait: %d", n)
	}
	// merged on GitHub: watchMerges lands it, nothing is left open
	d.setState("main", state.Landed, "")
	d.checkMerges()
	if n := len(mergeItems(t, d, "open")); n != 0 {
		t.Fatalf("open after landed: %d", n)
	}
}

// An open item about a session that landed (merged without an answer) is
// withdrawn.
func TestMergeEvidenceWithdrawnOnLanded(t *testing.T) {
	d, _ := mergeSetup(t)
	d.checkMerges()
	it := mergeItems(t, d, "open")[0]
	d.update("main", func(x *state.Session) { x.State, x.Landing = state.Landed, false })
	d.checkMerges()
	if g := inboxGet(t, d, it.ID); g.Status != state.InboxWithdrawn || !strings.Contains(g.Answer.Note, "PR merged") {
		t.Fatalf("withdrawn: %+v", g.Answer)
	}
}

// Completion criterion: send back sends the note to the session, through
// the inbox's typing path (idle, no permission prompt); it needs a note.
func TestMergeEvidenceSendBack(t *testing.T) {
	d, fe := mergeSetup(t)
	d.checkMerges()
	it := mergeItems(t, d, "open")[0]
	if err := callErr(d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "choice": mergeSendBack}); err == nil || !strings.Contains(err.Error(), "needs a note") {
		t.Fatalf("without a note: %v", err)
	}
	d.setState("main", state.Waiting, "permission")
	g := call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": inboxGet(t, d, it.ID).Rev, "choice": mergeSendBack, "note": "テストを足して"})
	if g.Status != state.InboxAnswered || g.Delivery != state.DeliveryPending {
		t.Fatalf("answered: %+v", g)
	}
	d.deliverAllInbox()
	if sentText(fe, "差し戻しました") {
		t.Fatal("typed into a permission prompt")
	}
	n := len(fe.sent)
	d.setState("main", state.Idle, "")
	waitFor(t, func() bool { return inboxGet(t, d, it.ID).Delivery == state.DeliveryDelivered })
	want := "text:[pekoe inbox #" + itoa(it.ID) + "] 人が PR #42 のマージを差し戻しました(メモ: テストを足して) 詳しくは pekoe --json inbox " + itoa(it.ID)
	got := sentSince(fe, n)
	if len(got) != 2 || got[0] != want || got[1] != "keys:Enter" {
		t.Fatalf("typed:\n%s\nwant %s", strings.Join(got, "\n"), want)
	}
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	if len(rs) == 0 || rs[0].From != callerPekoed || rs[0].To != "main" || rs[0].Kind != "inbox" || !rs[0].OK {
		t.Fatalf("send log: %+v", rs)
	}
	// the answers that send nothing: merged, wait
	if inboxRecipient(&state.InboxItem{From: callerPekoed, Kind: "approve", About: "main", Status: state.InboxAnswered,
		Answer: &state.InboxAnswer{Choice: intp(mergeMerge)}}) != "" {
		t.Fatal("merged is sent somewhere")
	}
	// not asked again for the same head
	d.checkMerges()
	if n := len(mergeItems(t, d, "open")); n != 0 {
		t.Fatalf("asked again after send back: %d", n)
	}
}

// A merge item asked before the choices were in English (state.db keeps
// its Japanese options) is read by its choice's index, as before: send
// back still needs a note and still sends it to the session.
func TestMergeEvidenceSendBackOldOptions(t *testing.T) {
	d, fe := mergeSetup(t)
	d.checkMerges()
	it := mergeItems(t, d, "open")[0]
	old, err := d.DB.InboxGet(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	old.Question, old.Options = "#42 をマージしてよい根拠が揃いました(main・HEAD 0123456)", []string{"マージした", "待つ", "差し戻す"}
	if err := d.DB.SaveInbox(old); err != nil {
		t.Fatal(err)
	}
	if err := callErr(d, "inbox.answer", map[string]any{"id": it.ID, "rev": old.Rev, "choice": mergeSendBack}); err == nil || !strings.Contains(err.Error(), "needs a note") {
		t.Fatalf("without a note: %v", err)
	}
	d.setState("main", state.Idle, "")
	n := len(fe.sent)
	g := call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": inboxGet(t, d, it.ID).Rev, "choice": mergeSendBack, "note": "テストを足して"})
	if g.Status != state.InboxAnswered || !mergeSentBack(&g) {
		t.Fatalf("answered: %+v", g)
	}
	waitFor(t, func() bool { return inboxGet(t, d, it.ID).Delivery == state.DeliveryDelivered })
	if got := sentSince(fe, n); len(got) != 2 || !strings.Contains(got[0], "人が PR #42 のマージを差し戻しました(メモ: テストを足して)") {
		t.Fatalf("typed:\n%s", strings.Join(got, "\n"))
	}
	// an old item answered 待つ sends nothing
	if inboxRecipient(&state.InboxItem{From: callerPekoed, Kind: "approve", About: "main", Status: state.InboxAnswered,
		Options: []string{"マージした", "待つ", "差し戻す"}, Answer: &state.InboxAnswer{Choice: intp(mergeWait)}}) != "" {
		t.Fatal("待つ is sent somewhere")
	}
}

// merge.check works without the token (the hub agent reads it) and lists
// every session that has not landed.
func TestMergeCheckWithoutToken(t *testing.T) {
	d, _ := mergeSetup(t)
	c := dialPipe(t, d)
	if e := c.call("merge.check", map[string]any{}, ""); e != "" {
		t.Fatalf("without the token: %q", e)
	}
	var slugs []string
	for _, x := range call[[]state.MergeCheck](t, d, "merge.check", nil) {
		slugs = append(slugs, x.Slug)
	}
	if strings.Join(slugs, ",") != "main" { // feat-a landed
		t.Fatalf("listed: %v", slugs)
	}
}

// A review asked for and under way (verdict pending) is no pass.
func TestMergeEvidenceReviewPending(t *testing.T) {
	d, _ := mergeSetup(t)
	review(t, d, "pending", mergeHead)
	c := mergeCheckOf(t, d)
	if c.Ready || strings.Join(c.Missing, ",") != "review" || c.ReviewStale || !strings.Contains(c.Conds[1].Detail, "independent review in progress") {
		t.Fatalf("check: %+v", c)
	}
	if s, _ := d.snapshot("main"); s.Review == nil || s.Review.Verdict != "pending" || s.Report.Status != "review" {
		t.Fatalf("session: %+v %+v", s.Review, s.Report)
	}
	// a later report does not hide the review under way
	callAs[state.Report](t, d, agentAs("main"), "report.post", map[string]any{"summary": "waiting", "status": "progress"})
	if s, _ := d.snapshot("main"); s.Review == nil || s.Review.Verdict != "pending" {
		t.Fatalf("review after a progress report: %+v", s.Review)
	}
}

// land rebases the reviewed commit onto a newer base, which gives the PR
// a new head: a pass carries over when the head changes the same as the
// reviewed commit (to the byte, in the same place), and not once the
// change differs or lands elsewhere.
func TestMergeEvidenceRebasedReview(t *testing.T) {
	d, _ := mergeSetup(t)
	s, _ := d.snapshot("main")
	repo := s.Path
	commit := func(file, body, msg string) string {
		os.WriteFile(filepath.Join(repo, file), []byte(body), 0o644)
		run(t, repo, "git", "add", "-A")
		run(t, repo, "git", "commit", "-qm", msg)
		return strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	}
	block := "p\nx\ny\nz\nq\n"
	commit("a.txt", block+block+block, "blocks")
	run(t, repo, "git", "branch", "base")
	reviewed := commit("a.txt", "p\nx\ny\nfeature\nz\nq\n"+block+block, "feature")
	run(t, repo, "git", "checkout", "-q", "base")
	commit("b.txt", "b\n", "the base moves")
	run(t, repo, "git", "checkout", "-q", "-b", "rebased")
	run(t, repo, "git", "cherry-pick", reviewed)
	rebased := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	changed := commit("a.txt", "p\nx\ny\nfeature!\nz\nq\n"+block+block, "more")
	// the same line with the same context, in the second block
	run(t, repo, "git", "checkout", "-q", "-b", "elsewhere", "base")
	elsewhere := commit("a.txt", block+"p\nx\ny\nfeature\nz\nq\n"+block, "elsewhere")
	d.update("main", func(x *state.Session) {
		x.Base = "base"
		c := *x.PRStatus
		c.HeadSHA = rebased
		x.PRStatus = &c
	})
	review(t, d, "pass", reviewed[:10])

	c := mergeCheckOf(t, d)
	if !c.Ready || c.ReviewStale || !strings.Contains(c.Conds[1].Detail, "a rebase with the same diff") {
		t.Fatalf("rebased: %+v", c)
	}
	d.update("main", func(x *state.Session) { c := *x.PRStatus; c.HeadSHA = changed; x.PRStatus = &c })
	c = mergeCheckOf(t, d)
	if c.Ready || !c.ReviewStale || strings.Join(c.Missing, ",") != "review" || !strings.Contains(c.Conds[1].Detail, "needs a new review") {
		t.Fatalf("changed: %+v", c)
	}
	d.update("main", func(x *state.Session) { c := *x.PRStatus; c.HeadSHA = elsewhere; x.PRStatus = &c })
	if c = mergeCheckOf(t, d); c.Ready || !c.ReviewStale || !strings.Contains(c.Conds[1].Detail, "needs a new review") {
		t.Fatalf("elsewhere: %+v", c)
	}
	// a head pekoed cannot read (pushed from elsewhere) is not carried
	d.update("main", func(x *state.Session) { c := *x.PRStatus; c.HeadSHA = mergeHead2; x.PRStatus = &c })
	if c = mergeCheckOf(t, d); c.Ready || !strings.Contains(c.Conds[1].Detail, "could not compare") {
		t.Fatalf("unknown head: %+v", c)
	}
}
