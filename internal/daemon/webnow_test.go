package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

func webNowGet(t *testing.T, d *Daemon) webNow {
	t.Helper()
	w := doWeb(t, testWebHandler(d), webReq{path: "/api/now", from: ipPhone})
	if w.Code != 200 {
		t.Fatalf("now: %d %s", w.Code, w.Body)
	}
	var n webNow
	if err := json.Unmarshal(w.Body.Bytes(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// pekoedMerge stores an answered merge item of pekoed's about slug, its
// merge in the given state.
func pekoedMerge(t *testing.T, d *Daemon, slug string, pr int, st, detail string, at time.Time) state.InboxItem {
	t.Helper()
	it := state.InboxItem{Kind: "approve", From: callerPekoed, About: slug, Question: "merge?", Status: state.InboxOpen, Links: []string{}}
	if err := d.DB.AddInbox(&it); err != nil {
		t.Fatal(err)
	}
	it.Status = state.InboxAnswered
	it.UpdatedAt = at
	it.Answer = &state.InboxAnswer{Choice: intp(mergeMerge), By: callerHuman, At: at,
		Merge: &state.MergeRun{State: st, PR: pr, Head: mergeHead, Detail: detail, At: at}}
	if err := d.DB.SaveInbox(&it); err != nil {
		t.Fatal(err)
	}
	return it
}

// The Now screen: what waits on the person comes first as cards (a merge
// that did not go through, a waiting session, an open inbox item), the
// rest as counts; requests newest first with their flow; pekoed's merges
// that went through in one line, the failed one only under NEEDS YOU.
func TestWebNow(t *testing.T) {
	d, _, repo, _ := webInboxSetup(t)
	old := state.LedgerEntry{Text: "古い依頼", Status: "open"}
	d.DB.AddLedger(&old)
	nw := state.LedgerEntry{Text: "CSV の空行を飛ばして", Status: "open"}
	d.DB.AddLedger(&nw)
	for _, b := range []string{"feat/a", "feat/b", "feat/c"} {
		call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": b})
		waitCreated(t, d, strings.ReplaceAll(b, "/", "-"))
	}
	d.update("feat-a", func(x *state.Session) { x.Parent, x.Request = TapiSlug, old.ID })
	d.update("feat-b", func(x *state.Session) { x.Parent, x.Request = TapiSlug, nw.ID })
	d.setState("feat-a", state.Done, "")
	d.setState("feat-b", state.Running, "")
	d.setState("feat-c", state.Idle, "")
	// main: waiting on a permission
	event(t, d, "PreToolUse", "main", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "pnpm migrate"}})
	event(t, d, "Notification", "main", map[string]any{"notification_type": "permission_prompt", "message": "needs permission"})

	q := ask(t, d, "feat-b", map[string]any{"question": "which db?", "kind": "decision", "options": []string{"pg", "sqlite"}})
	now := time.Now()
	pekoedMerge(t, d, "feat-a", 259, state.MergeMerged, "", now.Add(-2*time.Hour))
	failed := pekoedMerge(t, d, "feat-a", 260, state.MergeFailed, "the PR's head moved", now.Add(-time.Hour))
	pekoedMerge(t, d, "feat-a", 258, state.MergeMerged, "", now.Add(-3*time.Hour))
	// failed, but GitHub says the PR is merged (merged by hand first): history
	pekoedMerge(t, d, "feat-a", 257, state.MergeFailed, "not merged: evidence no longer holds: land: PR #257 is MERGED", now.Add(-30*time.Minute))

	n := webNowGet(t, d)
	var kinds []string
	for _, x := range n.Needs {
		kinds = append(kinds, x.Kind)
	}
	if got := strings.Join(kinds, ","); got != "merge_failed,waiting,inbox" {
		t.Fatalf("needs: %s", got)
	}
	f := n.Needs[0]
	if f.Item == nil || f.Item.ID != failed.ID || f.Item.Answer.Merge.PR != 260 || f.Session == nil || f.Session.Branch != "feat/a" {
		t.Fatalf("failed merge: %+v", f)
	}
	if w := n.Needs[1]; w.Session == nil || w.Session.Slug != "main" || !w.Session.Answerable {
		t.Fatalf("waiting: %+v", w)
	}
	if i := n.Needs[2]; i.Item == nil || i.Item.ID != q.ID || i.Item.Facts == nil || i.Item.Facts.Slug != "feat-b" {
		t.Fatalf("inbox: %+v", i)
	}
	c := n.Counts
	if c.Questions != 1 || c.Ready != 0 || c.Running != 1 || c.Waiting != 1 || c.Review != 1 {
		t.Fatalf("counts: %+v", c)
	}
	if len(n.Merged) != 3 || n.Merged[0].PR != 257 || n.Merged[1].PR != 259 || n.Merged[2].PR != 258 {
		t.Fatalf("merged (newest first, the failed one left out, the merged-anyway one in): %+v", n.Merged)
	}
	if n.RequestsOpen != 2 || len(n.Requests) != 2 {
		t.Fatalf("requests: %d %+v", n.RequestsOpen, n.Requests)
	}
	r0, r1 := n.Requests[0], n.Requests[1]
	if r0.ID != nw.ID || r0.Branch != "feat/b" || r0.Text != nw.Text || r0.Look.Word != "run" || len(r0.Steps) != 5 {
		t.Fatalf("newest request first: %+v", r0)
	}
	if r1.ID != old.ID || r1.Slug != "feat-a" || r1.Steps[0].Status != stepNow {
		t.Fatalf("older request: %+v", r1)
	}

	// pekoed's own merge item counts as ready, not as a question
	mi := state.InboxItem{Kind: "approve", From: callerPekoed, About: "feat-a", Question: "merge #261?", Status: state.InboxOpen, Links: []string{}}
	d.DB.AddInbox(&mi)
	if c := webNowGet(t, d).Counts; c.Ready != 1 || c.Questions != 1 {
		t.Fatalf("with a merge to approve: %+v", c)
	}
}

// Nothing to do: no cards, the zeros, and empty lists (not null) for the page.
func TestWebNowEmpty(t *testing.T) {
	d, _, _ := setup(t)
	w := doWeb(t, testWebHandler(d), webReq{path: "/api/now", from: ipPhone})
	for _, k := range []string{`"needs":[]`, `"requests":[]`, `"merged":[]`} {
		if !strings.Contains(w.Body.String(), k) {
			t.Fatalf("no %s: %s", k, w.Body)
		}
	}
}

// The Sessions headings: done (and failed, approved) to review, idle,
// landed, everything else active; a session in the repository's own
// checkout says so (main).
func TestWebSessionGroups(t *testing.T) {
	for st, want := range map[state.State]string{
		state.Done: groupReview, state.Failed: groupReview, state.Approved: groupReview,
		state.Idle: groupIdle, state.Landed: groupLanded,
		state.Running: groupActive, state.Waiting: groupActive, state.Creating: groupActive,
		state.Queued: groupActive, state.Reviewing: groupActive,
	} {
		if got := webGroup(st); got != want {
			t.Errorf("%s: %s, want %s", st, got, want)
		}
	}

	d, _, repo, h := webInboxSetup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a"})
	waitCreated(t, d, "feat-a")
	d.setState("feat-a", state.Done, "")
	w := doWeb(t, h, webReq{path: "/api/sessions", from: ipPhone})
	var res struct{ Sessions []webSession }
	json.Unmarshal(w.Body.Bytes(), &res)
	got := map[string]webSession{}
	for _, s := range res.Sessions {
		got[s.Slug] = s
	}
	if m := got["main"]; m.Group != groupActive || !m.Main {
		t.Fatalf("main: %+v", m)
	}
	if f := got["feat-a"]; f.Group != groupReview || f.Main {
		t.Fatalf("feat-a: %+v", f)
	}
	if strings.Contains(w.Body.String(), repo) {
		t.Fatalf("leaks paths: %s", w.Body)
	}
}

// A failed merge whose PR is merged anyway is no need: by pekoed's words
// ("PR #n is MERGED"), or by the session (landed, or the PR read MERGED).
func TestMergedAfterAll(t *testing.T) {
	m := func(pr int, detail string) *state.MergeRun {
		return &state.MergeRun{State: state.MergeFailed, PR: pr, Detail: detail}
	}
	for name, c := range map[string]struct {
		m    *state.MergeRun
		s    *state.Session
		want bool
	}{
		"is MERGED":       {m(260, "not merged: evidence no longer holds: land: PR #260 is MERGED"), nil, true},
		"another PR":      {m(260, "not merged: evidence no longer holds: land: PR #26 is MERGED"), nil, false},
		"is CLOSED":       {m(260, "not merged: evidence no longer holds: land: PR #260 is CLOSED"), nil, false},
		"head moved":      {m(260, "not merged: PR HEAD moved (a → b)"), &state.Session{PR: 260, State: state.Done}, false},
		"landed":          {m(260, "not merged: x"), &state.Session{PR: 260, State: state.Landed}, true},
		"read MERGED":     {m(260, "not merged: x"), &state.Session{PR: 260, PRStatus: &state.PRStatus{State: "MERGED"}}, true},
		"other PR merged": {m(260, "not merged: x"), &state.Session{PR: 261, State: state.Landed}, false},
	} {
		if got := mergedAfterAll(c.m, c.s); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// A merge that did not go through stays under NEEDS YOU for its day
// (docs/web.md: the merge history leaves Now after 24 hours), however
// many merges went through after it: only what went through is cut to
// webMergesMax (Now's MERGED BY PEKOED and the Inbox's history alike).
func TestWebNowFailedMergeOutlastsLaterMerges(t *testing.T) {
	d, _, _, h := webInboxSetup(t)
	now := time.Now()
	failed := pekoedMerge(t, d, "gone-a", 300, state.MergeFailed, "checks failed", now.Add(-3*time.Hour))
	pekoedMerge(t, d, "gone-b", 299, state.MergeFailed, "checks failed", now.Add(-25*time.Hour))
	for i := 0; i < webMergesMax+1; i++ {
		pekoedMerge(t, d, "other", 301+i, state.MergeMerged, "", now.Add(-2*time.Hour+time.Duration(i)*time.Minute))
	}
	n := webNowGet(t, d)
	var needs []int
	for _, x := range n.Needs {
		if x.Kind == needMergeFailed {
			needs = append(needs, x.Item.Answer.Merge.PR)
		}
	}
	if len(needs) != 1 || needs[0] != 300 {
		t.Fatalf("needs: the failed merges %v, want [300] (3h ago; #299 was 25h ago)", needs)
	}
	var merged []int
	for _, m := range n.Merged {
		merged = append(merged, m.PR)
	}
	if want := []int{306, 305, 304, 303, 302}; fmt.Sprint(merged) != fmt.Sprint(want) {
		t.Fatalf("merged: %v, want %v (the newest %d)", merged, want, webMergesMax)
	}

	w := doWeb(t, h, webReq{path: "/api/inbox", from: ipPhone})
	var res struct{ Merges []state.InboxItem }
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, it := range res.Merges {
		got = append(got, it.Answer.Merge.PR)
	}
	if want := []int{306, 305, 304, 303, 302, 300}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("inbox merges: %v, want %v (the failure #%d kept)", got, want, failed.Answer.Merge.PR)
	}
}
