package daemon

import (
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
	if it.Kind != "approve" || it.From != callerPekoed || !strings.HasPrefix(it.Question, "#42 をマージしてよい根拠が揃いました") ||
		strings.Join(it.Options, ",") != "マージした,待つ,差し戻す" || it.Recommend != nil {
		t.Fatalf("item: %+v", it)
	}
	if f := it.Facts; f == nil || f.PR != 42 || f.PRStatus == nil || f.PRStatus.HeadSHA != mergeHead || f.Merge == nil || !f.Merge.Ready {
		t.Fatalf("facts: %+v", it.Facts)
	}
	if !strings.Contains(it.Context, "review: 報告 #") || !strings.Contains(it.Context, "作者のセッションの中のレビュー") {
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
	if got.Status != state.InboxWithdrawn || got.Answer == nil || got.Answer.By != callerPekoed || !strings.Contains(got.Answer.Note, "HEAD が動いた") {
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
	if g := inboxGet(t, d, first.ID); g.Status != state.InboxWithdrawn || !strings.Contains(g.Answer.Note, "ci: 1 件失敗") {
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
		t.Fatalf("asked again after 待つ: %d", n)
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
	if g := inboxGet(t, d, it.ID); g.Status != state.InboxWithdrawn || !strings.Contains(g.Answer.Note, "マージされた") {
		t.Fatalf("withdrawn: %+v", g.Answer)
	}
}

// Completion criterion: 差し戻す sends the note to the session, through
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
	// the answers that send nothing: マージした, 待つ
	if inboxRecipient(&state.InboxItem{From: callerPekoed, Kind: "approve", About: "main", Status: state.InboxAnswered,
		Answer: &state.InboxAnswer{Choice: intp(mergeMerged)}}) != "" {
		t.Fatal("マージした is sent somewhere")
	}
	// not asked again for the same head
	d.checkMerges()
	if n := len(mergeItems(t, d, "open")); n != 0 {
		t.Fatalf("asked again after 差し戻す: %d", n)
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
