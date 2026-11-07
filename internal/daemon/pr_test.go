package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"boba/internal/state"
)

// prFixture builds the graphql answer the fake gh returns.
type prFixture struct {
	State, Merge, Decision string
	Checks                 []map[string]any
	Reviews                []map[string]any
	Threads                []map[string]any
	Comments               []map[string]any
}

func (f prFixture) write(t *testing.T, ghDir string) {
	t.Helper()
	if f.State == "" {
		f.State = "OPEN"
	}
	if f.Merge == "" {
		f.Merge = "BLOCKED"
	}
	nz := func(v []map[string]any) []map[string]any {
		if v == nil {
			return []map[string]any{}
		}
		return v
	}
	var decision any
	if f.Decision != "" {
		decision = f.Decision
	}
	var rollup any
	if f.Checks != nil {
		rollup = map[string]any{"contexts": map[string]any{"nodes": f.Checks}}
	}
	pr := map[string]any{
		"number": 42, "title": "Add CSV export", "url": "https://github.com/me/app/pull/42",
		"state": f.State, "isDraft": false, "mergeable": "MERGEABLE", "mergeStateStatus": f.Merge,
		"reviewDecision": decision, "baseRefName": "main", "headRefName": "feat/csv",
		"commits":       map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": rollup}}}},
		"reviews":       map[string]any{"nodes": nz(f.Reviews)},
		"reviewThreads": map[string]any{"nodes": nz(f.Threads)},
		"comments":      map[string]any{"nodes": nz(f.Comments)},
	}
	b, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
	if err := os.WriteFile(filepath.Join(ghDir, "pr.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func actionsCheck(name, status, conclusion string, run, job int64) map[string]any {
	c := map[string]any{"__typename": "CheckRun", "name": name, "status": status, "conclusion": nil,
		"detailsUrl": fmt.Sprintf("https://github.com/me/app/actions/runs/%d/job/%d", run, job),
		"startedAt":  "2026-11-08T10:00:00Z", "completedAt": nil, "databaseId": job,
		"checkSuite": map[string]any{"workflowRun": map[string]any{"databaseId": run, "workflow": map[string]any{"name": "CI"}}}}
	if conclusion != "" {
		c["conclusion"] = conclusion
		c["completedAt"] = "2026-11-08T10:03:12Z"
	}
	return c
}

func thread(path string, line int, resolved bool, at string, bodies ...string) map[string]any {
	var cs []any
	for _, b := range bodies {
		cs = append(cs, map[string]any{"author": map[string]any{"login": "alice"}, "body": b, "createdAt": at, "url": "https://github.com/me/app/pull/42#discussion_r1"})
	}
	return map[string]any{"isResolved": resolved, "isOutdated": false, "path": path, "line": line, "originalLine": line, "comments": map[string]any{"nodes": cs}}
}

// prSession opens a session on the repo and gives it PR #42.
func prSession(t *testing.T, d *Daemon, repo string) string {
	t.Helper()
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.update(s.Slug, func(x *state.Session) { x.PR = 42 })
	return s.Slug
}

// updates counts session.updated events for one slug.
func updates(d *Daemon, slug string) func() int {
	var mu sync.Mutex
	n := 0
	d.testHook = func(ev string, data any) {
		if s, ok := data.(*state.Session); ok && ev == "session.updated" && s.Slug == slug {
			mu.Lock()
			n++
			mu.Unlock()
		}
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// poll runs the background poller as if a minute had passed.
func poll(d *Daemon) {
	d.mu.Lock()
	d.prFetched = map[string]time.Time{}
	d.mu.Unlock()
	d.watchPRStatus()
}

func TestPRViewParses(t *testing.T) {
	d, _, repo := setup(t)
	ghd := fakeGH(t, "OPEN")
	slug := prSession(t, d, repo)
	prFixture{
		Merge: "BLOCKED", Decision: "CHANGES_REQUESTED",
		Checks: []map[string]any{
			actionsCheck("test (ubuntu)", "COMPLETED", "FAILURE", 100, 777),
			actionsCheck("lint", "COMPLETED", "SUCCESS", 100, 778),
			actionsCheck("build", "IN_PROGRESS", "", 101, 779),
			{"__typename": "StatusContext", "context": "vercel", "state": "PENDING", "targetUrl": "https://vercel.com/x", "createdAt": "2026-11-08T10:00:00Z"},
		},
		Reviews: []map[string]any{
			{"author": map[string]any{"login": "bob"}, "state": "CHANGES_REQUESTED", "body": "please stream", "submittedAt": "2026-11-08T11:00:00Z", "url": "https://github.com/me/app/pull/42#pullrequestreview-1"},
			{"author": nil, "state": "COMMENTED", "body": "", "submittedAt": "2026-11-08T09:00:00Z", "url": ""},
		},
		Threads: []map[string]any{
			thread("src/api.ts", 88, false, "2026-11-08T11:05:00Z", "why not stream?", "agreed"),
			thread("src/api.ts", 10, true, "2026-11-08T08:00:00Z", "typo"),
		},
		Comments: []map[string]any{{"author": map[string]any{"login": "carol"}, "body": "LGTM after fixes", "createdAt": "2026-11-08T12:00:00Z", "url": "https://github.com/me/app/pull/42#issuecomment-1"}},
	}.write(t, ghd)

	v := call[PRView](t, d, "pr.view", map[string]any{"slug": slug})
	if v.Number != 42 || v.State != "OPEN" || v.MergeState != "BLOCKED" || v.ReviewDecision != "CHANGES_REQUESTED" || v.Base != "main" || v.Head != "feat/csv" {
		t.Fatalf("header: %+v", v)
	}
	if len(v.Checks) != 4 {
		t.Fatalf("checks: %+v", v.Checks)
	}
	fail := v.Checks[0]
	if fail.Bucket != "fail" || fail.RunID != 100 || fail.JobID != 777 || fail.Workflow != "CI" || fail.Seconds != 192 {
		t.Fatalf("failed check: %+v", fail)
	}
	if v.Checks[1].Bucket != "pass" || v.Checks[2].Bucket != "pending" || !v.Checks[2].FinishedAt.IsZero() {
		t.Fatalf("pass / running: %+v", v.Checks[1:3])
	}
	if sc := v.Checks[3]; sc.Name != "vercel" || sc.Bucket != "pending" || sc.RunID != 0 || sc.URL == "" {
		t.Fatalf("status context: %+v", sc)
	}
	if len(v.Reviews) != 2 || v.Reviews[0].Author != "bob" || v.Reviews[1].Author != "ghost" {
		t.Fatalf("reviews: %+v", v.Reviews)
	}
	if len(v.Threads) != 2 || v.Threads[0].Path != "src/api.ts" || v.Threads[0].Line != 88 || len(v.Threads[0].Comments) != 2 || !v.Threads[1].Resolved {
		t.Fatalf("threads: %+v", v.Threads)
	}
	if len(v.Comments) != 1 || v.Comments[0].Author != "carol" {
		t.Fatalf("comments: %+v", v.Comments)
	}
	log, _ := os.ReadFile(filepath.Join(ghd, "gh.log"))
	if !strings.Contains(string(log), "api graphql -F owner={owner} -F name={repo} -F number=42") {
		t.Fatalf("gh calls: %s", log)
	}
	// the summary rides along on the session
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": slug})
	st := s.PRStatus
	if st == nil || st.Fail != 1 || st.Pass != 1 || st.Pending != 2 || st.Unresolved != 1 || st.ReviewDecision != "CHANGES_REQUESTED" {
		t.Fatalf("summary: %+v", st)
	}
	if st.LastComment.Format("15:04") != "12:00" || st.LastThread.Format("15:04") != "11:05" {
		t.Fatalf("comment times: %+v", st)
	}
}

func TestPRViewWithoutPR(t *testing.T) {
	d, _, repo := setup(t)
	fakeGH(t, "OPEN")
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	res, err := d.Call("pr.view", mustJSON(map[string]any{"slug": s.Slug}))
	if err != nil || res != nil {
		t.Fatalf("a session on its base branch has no PR: %v %v", res, err)
	}
}

func TestPRStatusAttention(t *testing.T) {
	d, _, repo := setup(t)
	ghd := fakeGH(t, "OPEN")
	slug := prSession(t, d, repo)
	n := updates(d, slug)
	get := func() *state.PRStatus {
		return call[state.Session](t, d, "session.get", map[string]any{"slug": slug}).PRStatus
	}
	next := func() string {
		s := call[*state.Session](t, d, "attention.next", map[string]any{"from": "elsewhere"})
		if s == nil {
			return ""
		}
		return s.Slug
	}

	// first look: CI running, nothing to see yet
	running := []map[string]any{actionsCheck("test", "IN_PROGRESS", "", 1, 2)}
	prFixture{Checks: running}.write(t, ghd)
	poll(d)
	if st := get(); st == nil || st.Pending != 1 || len(st.Attention) != 0 {
		t.Fatalf("first poll: %+v", st)
	}
	if next() != "" {
		t.Fatal("nothing should be in the attention queue yet")
	}
	before := n()
	poll(d)
	if n() != before {
		t.Fatal("an unchanged PR must not emit session.updated")
	}

	// CI turns red
	failed := []map[string]any{actionsCheck("test", "COMPLETED", "FAILURE", 1, 2)}
	prFixture{Checks: failed}.write(t, ghd)
	poll(d)
	if st := get(); st.Fail != 1 || strings.Join(st.Attention, ",") != "ci_failed" {
		t.Fatalf("ci failure: %+v", st)
	}
	if n() == before {
		t.Fatal("a change must emit session.updated")
	}
	if next() != slug {
		t.Fatal("a red CI puts the session in the attention queue")
	}

	// changes requested + a new review comment
	prFixture{Checks: failed, Decision: "CHANGES_REQUESTED", Threads: []map[string]any{thread("a.txt", 2, false, "2026-11-08T11:00:00Z", "why?")}}.write(t, ghd)
	poll(d)
	if st := get(); strings.Join(st.Attention, ",") != "ci_failed,changes_requested,new_comment" {
		t.Fatalf("review: %+v", st.Attention)
	}

	// opening the PR view clears it
	call[PRView](t, d, "pr.view", map[string]any{"slug": slug, "seen": true})
	if st := get(); len(st.Attention) != 0 {
		t.Fatalf("seen: %+v", st.Attention)
	}
	if next() != "" {
		t.Fatal("seen PR must leave the attention queue")
	}
	// the same state again is not news
	poll(d)
	if st := get(); len(st.Attention) != 0 {
		t.Fatalf("unchanged after seen: %+v", st.Attention)
	}

	// a reply on the open thread is news; so is becoming mergeable
	prFixture{Checks: []map[string]any{actionsCheck("test", "COMPLETED", "SUCCESS", 1, 3)}, Merge: "CLEAN", Decision: "APPROVED",
		Threads: []map[string]any{thread("a.txt", 2, false, "2026-11-08T12:00:00Z", "why?", "still?")}}.write(t, ghd)
	poll(d)
	if st := get(); strings.Join(st.Attention, ",") != "new_comment,mergeable" {
		t.Fatalf("reply + clean: %+v", st.Attention)
	}
	call[state.Session](t, d, "pr.seen", map[string]any{"slug": slug})
	if st := get(); len(st.Attention) != 0 {
		t.Fatalf("pr.seen: %+v", st.Attention)
	}
}

// Merges are noticed for every session, but only landing ones are torn down.
func TestPRMergeDetectedWithoutTeardown(t *testing.T) {
	d, _, repo := setup(t)
	ghd := fakeGH(t, "MERGED")
	slug := prSession(t, d, repo)
	prFixture{State: "MERGED", Merge: "UNKNOWN"}.write(t, ghd)
	poll(d)
	d.watchMerges()
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": slug})
	if s.PRStatus == nil || s.PRStatus.State != "MERGED" {
		t.Fatalf("merge not noticed: %+v", s.PRStatus)
	}
	if s.State == state.Landed || s.Stopped {
		t.Fatalf("a non-landing session must not be torn down: %+v", s)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatal("worktree removed")
	}
	// merged PRs are not polled any more
	os.Remove(filepath.Join(ghd, "gh.log"))
	poll(d)
	if b, _ := os.ReadFile(filepath.Join(ghd, "gh.log")); strings.Contains(string(b), "graphql") {
		t.Fatalf("merged PR polled again: %s", b)
	}
}

func TestPRPollingSkipsWithoutGH(t *testing.T) {
	d, _, repo := setup(t)
	ghd := fakeGH(t, "OPEN")
	slug := prSession(t, d, repo)
	prFixture{}.write(t, ghd)

	// gh not authenticated: quietly nothing
	os.WriteFile(filepath.Join(ghd, "noauth"), nil, 0o644)
	poll(d)
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": slug}); s.PRStatus != nil {
		t.Fatalf("unauthenticated gh produced a status: %+v", s.PRStatus)
	}
	if _, err := d.Call("pr.view", mustJSON(map[string]any{"slug": slug})); err == nil || !strings.Contains(err.Error(), "auth login") {
		t.Fatalf("pr.view should explain the gh failure: %v", err)
	}

	// no gh at all
	bin := t.TempDir()
	git, _ := exec.LookPath("git")
	os.Symlink(git, filepath.Join(bin, "git"))
	t.Setenv("PATH", bin)
	poll(d)
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": slug}); s.PRStatus != nil {
		t.Fatalf("no gh produced a status: %+v", s.PRStatus)
	}
}

func TestFailedLog(t *testing.T) {
	d, _, repo := setup(t)
	ghd := fakeGH(t, "OPEN")
	slug := prSession(t, d, repo)
	prFixture{Checks: []map[string]any{
		actionsCheck("lint", "COMPLETED", "SUCCESS", 100, 778),
		actionsCheck("test (ubuntu)", "COMPLETED", "FAILURE", 100, 777),
	}}.write(t, ghd)
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "test (ubuntu)\tRun tests\t2026-11-08T10:00:00Z \x1b[31mline %d\x1b[0m\r\n", i)
	}
	os.WriteFile(filepath.Join(ghd, "job.log"), []byte(b.String()), 0o644)

	fl := call[FailedLog](t, d, "pr.failed_log", map[string]any{"slug": slug, "check": "test (ubuntu)"})
	lines := strings.Split(fl.Log, "\n")
	if fl.RunID != 100 || fl.JobID != 777 || !fl.Truncated || len(lines) != 200 {
		t.Fatalf("failed log: run %d job %d truncated %v, %d lines", fl.RunID, fl.JobID, fl.Truncated, len(lines))
	}
	if !strings.HasSuffix(lines[199], "line 300") || !strings.HasSuffix(lines[0], "line 101") || strings.Contains(fl.Log, "\x1b") || strings.Contains(fl.Log, "\r") || strings.Contains(fl.Log, "Run tests") || strings.Contains(fl.Log, "2026-") {
		t.Fatalf("tail: %q … %q", lines[0], lines[199])
	}
	log, _ := os.ReadFile(filepath.Join(ghd, "gh.log"))
	if !strings.Contains(string(log), "run view --job 777 --log-failed") {
		t.Fatalf("gh calls: %s", log)
	}

	// no job log (older gh / log not per job): whole run
	os.WriteFile(filepath.Join(ghd, "job.log"), nil, 0o644)
	os.WriteFile(filepath.Join(ghd, "run.log"), []byte("boom\n"), 0o644)
	fl = call[FailedLog](t, d, "pr.failed_log", map[string]any{"slug": slug, "check": "test (ubuntu)", "run_id": 100, "job_id": 777})
	if fl.Log != "boom" || fl.Truncated {
		t.Fatalf("run fallback: %+v", fl)
	}
	if _, err := d.Call("pr.failed_log", mustJSON(map[string]any{"slug": slug, "check": "nope"})); err == nil {
		t.Fatal("unknown check must fail")
	}
}
