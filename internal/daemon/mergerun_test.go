package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"pekoe/internal/state"
)

// mergeRunSetup: mergeSetup with a fake gh whose PR (#42) reads as every
// condition holding at mergeHead, origin on GitHub, and pekoed's merge
// item asked. It returns the fake gh's directory and the item.
func mergeRunSetup(t *testing.T) (*Daemon, *fakeEngine, string, state.InboxItem) {
	t.Helper()
	ghd := fakeGH(t, "OPEN")
	d, fe := mergeSetup(t)
	greenPR(mergeHead).write(t, ghd)
	s, _ := d.snapshot("main")
	run(t, s.RepoDir, "git", "remote", "add", "origin", "https://github.com/me/app.git")
	d.webBases.Delete(s.RepoDir)
	d.checkMerges()
	its := mergeItems(t, d, "open")
	if len(its) != 1 {
		t.Fatalf("items: %+v", its)
	}
	return d, fe, ghd, its[0]
}

func greenPR(head string) prFixture {
	return prFixture{Head: head, Merge: "CLEAN", Checks: []map[string]any{
		actionsCheck("test", "COMPLETED", "SUCCESS", 1, 3),
		actionsCheck("lint", "COMPLETED", "SUCCESS", 1, 4),
		actionsCheck("vet", "COMPLETED", "SUCCESS", 1, 5),
	}}
}

func ghLog(ghd string) []string {
	b, _ := os.ReadFile(filepath.Join(ghd, "gh.log"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func ghMerges(ghd string) []string {
	var out []string
	for _, l := range ghLog(ghd) {
		if strings.HasPrefix(l, "pr merge") {
			out = append(out, l)
		}
	}
	return out
}

func answerMerge(t *testing.T, d *Daemon, id int64) state.InboxItem {
	t.Helper()
	return call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": id, "rev": inboxGet(t, d, id).Rev, "choice": mergeMerge})
}

// waitMergeEnd waits for the item's merge to end (merged or failed).
func waitMergeEnd(t *testing.T, d *Daemon, id int64) *state.MergeRun {
	t.Helper()
	var run *state.MergeRun
	waitFor(t, func() bool {
		it := inboxGet(t, d, id)
		if it.Answer != nil && it.Answer.Merge != nil && it.Answer.Merge.State != state.MergeRunning {
			run = it.Answer.Merge
			return true
		}
		return false
	})
	return run
}

// Completion criterion: "merge" → pekoed reads the PR again, then runs gh pr
// merge with --repo, the method (merge by default) and the item's head; the
// item says it is running, then merged; the send log keeps it.
func TestMergeRunMerges(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	if !strings.Contains(it.Context, `"merge": pekoed checks these again and runs gh pr merge --merge --match-head-commit `+mergeHead[:7]) {
		t.Fatalf("context: %q", it.Context)
	}
	got := answerMerge(t, d, it.ID)
	if got.Status != state.InboxAnswered || got.Answer.Merge == nil || got.Answer.Merge.State != state.MergeRunning ||
		got.Answer.Merge.Head != mergeHead || got.Answer.Merge.PR != 42 {
		t.Fatalf("answered: %+v", got.Answer)
	}
	run := waitMergeEnd(t, d, it.ID)
	if run.State != state.MergeMerged || run.Method != "merge" || !strings.Contains(run.Detail, "merged PR #42 (merge)") {
		t.Fatalf("run: %+v", run)
	}
	want := "pr merge 42 --repo github.com/me/app --merge --match-head-commit " + mergeHead
	if ms := ghMerges(ghd); len(ms) != 1 || ms[0] != want {
		t.Fatalf("gh: %q", ms)
	}
	// read again before the merge, and once more after it (the landing
	// starts then; the item says merged before that read)
	seq := func() string {
		var out []string
		for _, x := range ghLog(ghd) {
			switch {
			case strings.HasPrefix(x, "api graphql -F owner=me -F name=app -F number=42"):
				out = append(out, "read")
			case strings.HasPrefix(x, "pr merge"):
				out = append(out, "merge")
			}
		}
		return strings.Join(out, ",")
	}
	waitFor(t, func() bool { return strings.HasSuffix(seq(), "read,merge,read") })
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	if len(rs) == 0 || rs[0].Kind != "merge" || rs[0].From != callerPekoed || rs[0].To != "main" || !rs[0].OK ||
		!strings.Contains(rs[0].Text, want) || !strings.Contains(rs[0].Text, "inbox #"+itoa(it.ID)) {
		t.Fatalf("send log: %+v", rs)
	}
	if c := mergeCheckOf(t, d); c.Run == nil || c.Run.State != state.MergeMerged {
		t.Fatalf("merge.check run: %+v", c.Run)
	}
	// a merged answer is not asked again for the same head
	d.checkMerges()
	if n := len(mergeItems(t, d, "open")); n != 0 {
		t.Fatalf("asked again: %d", n)
	}
}

// [merge] method of the repository picks squash or rebase; anything else
// merges nothing.
func TestMergeRunMethod(t *testing.T) {
	for _, tc := range []struct{ method, flag, fail string }{
		{"squash", "--squash", ""},
		{"rebase", "--rebase", ""},
		{"fast-forward", "", "[merge] method must be merge, squash or rebase"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			d, _, ghd, it := mergeRunSetup(t)
			s, _ := d.snapshot("main")
			dir := filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos")
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, s.RepoID+".toml"), []byte("[merge]\nmethod = \""+tc.method+"\"\n"), 0o644)
			answerMerge(t, d, it.ID)
			run := waitMergeEnd(t, d, it.ID)
			ms := ghMerges(ghd)
			if tc.fail != "" {
				if run.State != state.MergeFailed || !strings.Contains(run.Detail, tc.fail) || len(ms) != 0 {
					t.Fatalf("run %+v gh %q", run, ms)
				}
				return
			}
			if run.State != state.MergeMerged || run.Method != tc.method || len(ms) != 1 || !strings.Contains(ms[0], " "+tc.flag+" ") {
				t.Fatalf("run %+v gh %q", run, ms)
			}
		})
	}
}

// Completion criteria: a condition that broke since the item was asked, or
// a head that moved, as GitHub says when pekoed reads it again → no gh pr
// merge, and the item says why. pekoed asks again once it holds.
func TestMergeRunChecksAgain(t *testing.T) {
	for name, tc := range map[string]struct {
		pr   func() prFixture
		want string
	}{
		"ci failed": {func() prFixture {
			f := greenPR(mergeHead)
			f.Checks = append(f.Checks, actionsCheck("e2e", "COMPLETED", "FAILURE", 1, 6))
			return f
		}, "evidence no longer holds: ci: 1 failed"},
		"ci running": {func() prFixture {
			f := greenPR(mergeHead)
			f.Checks = append(f.Checks, actionsCheck("e2e", "IN_PROGRESS", "", 1, 6))
			return f
		}, "ci: 1 pending"},
		"not clean":    {func() prFixture { f := greenPR(mergeHead); f.Merge = "BLOCKED"; return f }, "merge_state is BLOCKED"},
		"head moved":   {func() prFixture { return greenPR(mergeHead2) }, "PR HEAD moved (0123456 → 89abcde)"},
		"closed":       {func() prFixture { f := greenPR(mergeHead); f.State = "CLOSED"; return f }, "land: PR #42 is CLOSED"},
		"base changed": {func() prFixture { f := greenPR(mergeHead); f.Base = "release"; return f }, "PR #42 goes into release now, not main"},
	} {
		t.Run(name, func(t *testing.T) {
			d, _, ghd, it := mergeRunSetup(t)
			tc.pr().write(t, ghd)
			answerMerge(t, d, it.ID)
			run := waitMergeEnd(t, d, it.ID)
			if run.State != state.MergeFailed || !strings.Contains(run.Detail, tc.want) || !strings.HasPrefix(run.Detail, "not merged: ") {
				t.Fatalf("run: %+v", run)
			}
			if ms := ghMerges(ghd); len(ms) != 0 {
				t.Fatalf("merged: %q", ms)
			}
		})
	}
}

// A conflict forecast or an after session that is not landed any more is
// read again too.
func TestMergeRunChecksSessionAgain(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	// answered on the rev that still held; the forecast changes right after
	// (the answer was accepted first)
	answerMergeThen(t, d, it.ID, func() { d.setState("feat-a", state.Done, "") })
	run := waitMergeEnd(t, d, it.ID)
	if run.State != state.MergeFailed || !strings.Contains(run.Detail, "after: feat-a is done") {
		t.Fatalf("run: %+v", run)
	}
	if ms := ghMerges(ghd); len(ms) != 0 {
		t.Fatalf("merged: %q", ms)
	}
}

// answerMergeThen answers "merge" with mergeRuns held until f ran: what
// changes between the answer and the merge's own check.
func answerMergeThen(t *testing.T, d *Daemon, id int64, f func()) {
	t.Helper()
	old := ghFound
	gate := make(chan struct{})
	ghFound = func() bool { <-gate; return old() }
	t.Cleanup(func() { ghFound = old })
	answerMerge(t, d, id)
	f()
	close(gate)
}

// Completion criterion: gh fails (GitHub refused it, the head moved under
// --match-head-commit, gh is missing) → not merged, with gh's reason, in
// the send log as failed; pekoed asks again (the person may answer merge
// again), saying the last merge failed.
func TestMergeRunGHFails(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	os.WriteFile(filepath.Join(ghd, "merge-fail"), []byte("GraphQL: Head branch was modified. Review and try the merge again. (mergePullRequest)\n"), 0o644)
	answerMerge(t, d, it.ID)
	run := waitMergeEnd(t, d, it.ID)
	if run.State != state.MergeFailed || !strings.Contains(run.Detail, "Head branch was modified") {
		t.Fatalf("run: %+v", run)
	}
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	if len(rs) == 0 || rs[0].Kind != "merge" || rs[0].OK || !strings.Contains(rs[0].Error, "Head branch was modified") {
		t.Fatalf("send log: %+v", rs)
	}
	if s, _ := d.snapshot("main"); s.State == state.Landed {
		t.Fatal("landed after a failed merge")
	}
	d.checkMerges()
	its := mergeItems(t, d, "open")
	if len(its) != 1 || its[0].ID == it.ID || !strings.Contains(its[0].Context, "last merge (inbox #"+itoa(it.ID)+") failed: not merged: gh pr merge: GraphQL: Head branch was modified") {
		t.Fatalf("asked again: %+v", its)
	}

	// gh missing
	old := ghFound
	ghFound = func() bool { return false }
	t.Cleanup(func() { ghFound = old })
	answerMerge(t, d, its[0].ID)
	if run := waitMergeEnd(t, d, its[0].ID); run.State != state.MergeFailed || !strings.Contains(run.Detail, "gh not found") {
		t.Fatalf("no gh: %+v", run)
	}
}

// Completion criterion: only a person answers. An agent (no token) cannot
// answer pekoed's merge item, nor make one: its approve item is never
// merged on, and a session named pekoed is not given that name.
func TestMergeRunOnlyPekoedsItem(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	c := dialPipe(t, d)
	if e := c.call("inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "choice": mergeMerge}, ""); e == "" {
		t.Fatal("answered pekoed's merge item without the token")
	}
	if g := inboxGet(t, d, it.ID); g.Status != state.InboxOpen {
		t.Fatalf("after the agent: %+v", g)
	}

	// an ask-human approve that offers "merge" first, answered with it
	own := ask(t, d, "main", map[string]any{"question": "evidence ready to merge #42 (main · HEAD 0123456)", "kind": "approve",
		"options": []string{"merge", "wait", "send back"}})
	g := call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": own.ID, "rev": inboxGet(t, d, own.ID).Rev, "choice": 0})
	if g.Answer.Merge != nil {
		t.Fatalf("merging on an agent's item: %+v", g.Answer)
	}
	// pekoed's own items of another kind (the .git watch) neither
	if mergeAsked(&state.InboxItem{From: callerPekoed, Kind: "decision", Options: []string{"merge", "x"}}, intp(0)) {
		t.Fatal("decision item asks for a merge")
	}
	// an item from before pekoed merged: option 0 is "merged" (recorded only)
	if mergeAsked(&state.InboxItem{From: callerPekoed, Kind: "approve", Options: []string{"merged", "wait", "send back"}}, intp(0)) {
		t.Fatal("old merged answer asks for a merge")
	}
	// tapi's items
	tapi := callAs[state.InboxItem](t, d, agentAs(TapiSlug), "inbox.post", map[string]any{"question": "merge #42?", "kind": "approve",
		"options": []string{"merge", "wait"}, "about": "main"})
	g = call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": tapi.ID, "rev": inboxGet(t, d, tapi.ID).Rev, "choice": 0})
	if g.Answer.Merge != nil {
		t.Fatalf("merging on tapi's item: %+v", g.Answer)
	}

	// a session named pekoed would post as pekoed: not given that name, and
	// one that has it (made before) cannot post
	if slug := d.uniqueSlug("pekoed", "x", "app"); slug == callerPekoed {
		t.Fatal("slug pekoed given")
	}
	if err := callAsErr(d, agentAs(callerPekoed), "inbox.post", map[string]any{"question": "evidence ready to merge #42", "kind": "approve",
		"options": []string{"merge", "wait", "send back"}}); err == nil || !strings.Contains(err.Error(), "named pekoed") {
		t.Fatalf("post as pekoed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if ms := ghMerges(ghd); len(ms) != 0 {
		t.Fatalf("merged: %q", ms)
	}
}

// pekoed's item that is not its latest for the session (a record that does
// not name it) merges nothing.
func TestMergeRunNotLatest(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	d.DB.Set(mergeKey("main"), `{"pr":42,"head":"`+mergeHead+`","id":999}`)
	answerMerge(t, d, it.ID)
	run := waitMergeEnd(t, d, it.ID)
	if run.State != state.MergeFailed || !strings.Contains(run.Detail, "not pekoed's latest merge evidence") {
		t.Fatalf("run: %+v", run)
	}
	if ms := ghMerges(ghd); len(ms) != 0 {
		t.Fatalf("merged: %q", ms)
	}
}

// wait and send back merge nothing.
func TestMergeRunOtherAnswers(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	g := call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "choice": mergeWait})
	if g.Answer.Merge != nil {
		t.Fatalf("wait: %+v", g.Answer)
	}
	time.Sleep(100 * time.Millisecond)
	if ms := ghMerges(ghd); len(ms) != 0 {
		t.Fatalf("merged: %q", ms)
	}
}

// A merge still running when pekoed stopped is marked failed on start.
func TestMergeRunStaleOnStart(t *testing.T) {
	d, _, _, it := mergeRunSetup(t)
	cur, _ := d.DB.InboxGet(it.ID)
	cur.Status = state.InboxAnswered
	cur.Answer = &state.InboxAnswer{Choice: intp(mergeMerge), By: callerHuman, Merge: &state.MergeRun{State: state.MergeRunning, PR: 42, Head: mergeHead}}
	d.DB.SaveInbox(cur)
	d.failStaleMerges()
	g := inboxGet(t, d, it.ID)
	if g.Answer.Merge.State != state.MergeFailed || !strings.Contains(g.Answer.Merge.Detail, "pekoed stopped during the merge") || g.Rev <= cur.Rev {
		t.Fatalf("after start: %+v", g.Answer.Merge)
	}
}

// Completion criterion: answered from the Web UI (iPhone) it merges the
// same way, and the inbox tab keeps the merge in sight (merges) after the
// item left the open list; the overview's merge step says it.
func TestMergeRunFromWeb(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	h := testWebHandler(d)
	w := doWeb(t, h, inboxAnswerReq(ipPhone, it.ID, map[string]any{"choice": mergeMerge, "rev": inboxGet(t, d, it.ID).Rev}))
	if w.Code != 200 {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	run := waitMergeEnd(t, d, it.ID)
	if run.State != state.MergeMerged || len(ghMerges(ghd)) != 1 {
		t.Fatalf("run %+v gh %q", run, ghMerges(ghd))
	}
	if g := inboxGet(t, d, it.ID); g.Answer.Via != "web:iphone" {
		t.Fatalf("via: %+v", g.Answer)
	}
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	if len(rs) == 0 || rs[0].Kind != "merge" || rs[0].Via != "web:iphone" {
		t.Fatalf("send log: %+v", rs)
	}
	w = doWeb(t, h, webReq{path: "/api/inbox", from: ipPhone})
	var res struct {
		Items  []state.InboxItem
		Merges []state.InboxItem
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || len(res.Merges) != 1 || res.Merges[0].ID != it.ID || res.Merges[0].Answer.Merge.State != state.MergeMerged {
		t.Fatalf("inbox: %s", w.Body)
	}
	w = doWeb(t, h, webReq{path: "/api/overview", from: ipPhone})
	if !strings.Contains(w.Body.String(), "merged by pekoed: landing") {
		t.Fatalf("overview: %s", w.Body)
	}
}

// gh pr merge that does not end in time: stopped, and the item says the
// outcome is unknown (GitHub may have merged it), not "not merged". The
// send log says so too. A child of gh that still holds its output does not
// keep the merge from ending.
func TestMergeRunTimeout(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	oldRun, oldWait := mergeRunTimeout, mergeWaitDelay
	mergeRunTimeout, mergeWaitDelay = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { mergeRunTimeout, mergeWaitDelay = oldRun, oldWait })
	os.WriteFile(filepath.Join(ghd, "merge-hang"), nil, 0o644)
	t.Cleanup(func() { // the children outlive gh: stop them
		b, _ := os.ReadFile(filepath.Join(ghd, "hang.pids"))
		for _, f := range strings.Fields(string(b)) {
			if pid, err := strconv.Atoi(f); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	start := time.Now()
	answerMerge(t, d, it.ID)
	run := waitMergeEnd(t, d, it.ID)
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("merge ended after %s: waited on gh's children", took)
	}
	if b, _ := os.ReadFile(filepath.Join(ghd, "hang.pids")); len(strings.Fields(string(b))) != 2 {
		t.Fatalf("fake gh did not leave its children: %q", b)
	}
	want := "unknown: gh pr merge took over 300ms and was stopped; GitHub may have merged it"
	if run.State != state.MergeFailed || !strings.HasPrefix(run.Detail, want) {
		t.Fatalf("run: %+v", run)
	}
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	if len(rs) == 0 || rs[0].Kind != "merge" || rs[0].OK || !strings.HasPrefix(rs[0].Error, want) {
		t.Fatalf("send log: %+v", rs)
	}
}

// gh pr merge runs in the main checkout of the repository, not in the
// session's worktree.
func TestMergeRunInMainCheckout(t *testing.T) {
	d, _, ghd, it := mergeRunSetup(t)
	other, _ := d.snapshot("feat-a")
	d.update("main", func(x *state.Session) { x.Path = other.Path })
	s, _ := d.snapshot("main")
	if s.Path == s.RepoDir {
		t.Fatalf("worktree is the main checkout: %s", s.Path)
	}
	answerMerge(t, d, it.ID)
	if run := waitMergeEnd(t, d, it.ID); run.State != state.MergeMerged {
		t.Fatalf("run: %+v", run)
	}
	b, _ := os.ReadFile(filepath.Join(ghd, "merge.cwd"))
	want, _ := filepath.EvalSymlinks(s.RepoDir)
	if got := strings.TrimSpace(string(b)); got != want {
		t.Fatalf("gh pr merge ran in %q, want %q (worktree %s)", got, want, s.Path)
	}
}

// An origin whose owner or name gh would read as a flag or an @file is no
// GitHub repository: nothing is read or merged.
func TestMergeRunOddOrigin(t *testing.T) {
	for _, origin := range []string{
		"git@github.com:@etc/passwd",
		"https://github.com/../app.git",
		"https://github.com/./app.git",
		"https://github.com/me/...git",
		"https://github.com/me/..git",
		"git@github.com:../...git",
	} {
		t.Run(origin, func(t *testing.T) {
			d, _, ghd, it := mergeRunSetup(t)
			s, _ := d.snapshot("main")
			run(t, s.RepoDir, "git", "remote", "set-url", "origin", origin)
			d.webBases.Delete(s.RepoDir)
			n := len(ghLog(ghd))
			answerMerge(t, d, it.ID)
			r := waitMergeEnd(t, d, it.ID)
			if r.State != state.MergeFailed || !strings.Contains(r.Detail, "no GitHub repository in origin") {
				t.Fatalf("run: %+v (repo %q)", r, d.ghRepo(s))
			}
			if l := ghLog(ghd); len(l) != n {
				t.Fatalf("gh ran: %q", l[n:])
			}
		})
	}
}
