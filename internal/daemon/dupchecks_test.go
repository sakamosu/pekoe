package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// runCheck is a check run of a GitHub Actions workflow run, as the rollup
// gives it (the shape read from real PRs whose workflow runs on both push
// and pull_request, e.g. stretchr/testify #1990, charmbracelet/bubbletea
// #1882: the same name and workflow twice, each in its own run, started a
// few seconds or minutes apart).
func runCheck(name, workflow, event, conclusion string, run, job int64, started string) map[string]any {
	c := map[string]any{"__typename": "CheckRun", "name": name, "status": "IN_PROGRESS", "conclusion": nil,
		"detailsUrl": fmt.Sprintf("https://github.com/me/app/actions/runs/%d/job/%d", run, job),
		"startedAt":  started, "completedAt": nil, "databaseId": job,
		"checkSuite": map[string]any{"workflowRun": map[string]any{"databaseId": run, "event": event, "workflow": map[string]any{"name": workflow}}}}
	if conclusion != "" {
		c["status"], c["conclusion"] = "COMPLETED", conclusion
		at, _ := time.Parse(time.RFC3339, started)
		c["completedAt"] = at.Add(3 * time.Minute).Format(time.RFC3339)
	}
	return c
}

// The same check more than once in a head's rollup counts once: the one
// started last (issue #337). Before, an old failure next to a newer pass
// kept Fail > 0 and the ✗ after the PR number stayed.
func TestPRChecksDropDuplicates(t *testing.T) {
	d, _, repo := setup(t)
	ghd := fakeGH(t, "OPEN")
	slug := prSession(t, d, repo)
	prFixture{Checks: []map[string]any{
		// push's run failed; pull_request's run of the same workflow was re-run and passed
		runCheck("test (ubuntu-latest)", "test", "push", "FAILURE", 10, 100, "2026-10-21T06:57:08Z"),
		runCheck("test (ubuntu-latest)", "test", "pull_request", "SUCCESS", 11, 110, "2026-10-21T07:20:48Z"),
		// the other way round: the newer one is the failure, it stays
		runCheck("lint", "lint", "pull_request", "FAILURE", 13, 130, "2026-10-21T07:20:50Z"),
		runCheck("lint", "lint", "push", "SUCCESS", 12, 120, "2026-10-21T06:57:09Z"),
		// the same name in another workflow is another check
		runCheck("build", "release", "push", "SUCCESS", 14, 140, "2026-10-21T06:57:10Z"),
		runCheck("build", "test", "push", "SUCCESS", 10, 101, "2026-10-21T06:57:10Z"),
		// started at the same time: the finished one
		runCheck("vet", "test", "pull_request", "", 11, 112, "2026-10-21T07:20:48Z"),
		runCheck("vet", "test", "push", "SUCCESS", 10, 102, "2026-10-21T07:20:48Z"),
		// status contexts: per context, the one created last
		{"__typename": "StatusContext", "context": "vercel", "state": "FAILURE", "targetUrl": "https://vercel.com/1", "createdAt": "2026-10-21T06:00:00Z"},
		{"__typename": "StatusContext", "context": "vercel", "state": "SUCCESS", "targetUrl": "https://vercel.com/2", "createdAt": "2026-10-21T07:00:00Z"},
	}}.write(t, ghd)

	v := call[PRView](t, d, "pr.view", map[string]any{"slug": slug})
	var got []string
	for _, c := range v.Checks {
		got = append(got, fmt.Sprintf("%s/%s=%s#%d", c.Workflow, c.Name, c.Bucket, c.JobID))
	}
	want := "test/test (ubuntu-latest)=pass#110 lint/lint=fail#130 release/build=pass#140 test/build=pass#101 test/vet=pass#102 /vercel=pass#0"
	if strings.Join(got, " ") != want {
		t.Fatalf("checks:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	st := call[state.Session](t, d, "session.get", map[string]any{"slug": slug}).PRStatus
	if st.Pass != 5 || st.Fail != 1 || st.Pending != 0 || strings.Join(st.FailedChecks, ",") != "lint" || !strings.HasPrefix(st.CIResult, "red 5/1 ") {
		t.Fatalf("summary: %+v", st)
	}
}

// A red CI goes green on the same head (a re-run of the failed jobs, of the
// whole workflow, of one event's run) or on a new push, with an old failure
// of the same check still in the rollup: the ✗ after the PR number turns
// ✓, and wait's ci tells red, then green, once each (issue #337).
func TestPRCIGreenWithDuplicateChecks(t *testing.T) {
	const head1, head2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	lintOK := runCheck("lint", "test", "pull_request", "SUCCESS", 11, 111, "2026-10-21T07:00:00Z")
	pushRed := runCheck("test", "test", "push", "FAILURE", 10, 100, "2026-10-21T06:59:00Z")
	prRed := runCheck("test", "test", "pull_request", "FAILURE", 11, 110, "2026-10-21T07:00:00Z")
	red := []map[string]any{pushRed, prRed, lintOK}
	for _, tc := range []struct {
		name          string
		head          string
		running, done []map[string]any
	}{
		{"re-run of the failed jobs", head1,
			[]map[string]any{pushRed, prRed, runCheck("test", "test", "pull_request", "", 11, 112, "2026-10-21T07:30:00Z"), lintOK},
			[]map[string]any{pushRed, prRed, runCheck("test", "test", "pull_request", "SUCCESS", 11, 112, "2026-10-21T07:30:00Z"), lintOK}},
		{"re-run of the whole workflow, another check suite", head1,
			[]map[string]any{pushRed, prRed, lintOK, runCheck("test", "test", "pull_request", "", 21, 210, "2026-10-21T07:30:00Z"), runCheck("lint", "test", "pull_request", "", 21, 211, "2026-10-21T07:30:00Z")},
			[]map[string]any{pushRed, prRed, lintOK, runCheck("test", "test", "pull_request", "SUCCESS", 21, 210, "2026-10-21T07:30:00Z"), runCheck("lint", "test", "pull_request", "SUCCESS", 21, 211, "2026-10-21T07:30:00Z")}},
		{"re-run of the push and pull_request runs", head1,
			[]map[string]any{runCheck("test", "test", "push", "", 10, 101, "2026-10-21T07:30:00Z"), prRed, lintOK},
			[]map[string]any{runCheck("test", "test", "push", "SUCCESS", 10, 101, "2026-10-21T07:30:00Z"), runCheck("test", "test", "pull_request", "SUCCESS", 11, 113, "2026-10-21T07:31:00Z"), prRed, lintOK}},
		{"a fix pushed", head2,
			[]map[string]any{runCheck("test", "test", "push", "", 30, 300, "2026-10-21T08:00:00Z"), runCheck("test", "test", "pull_request", "", 31, 310, "2026-10-21T08:00:01Z")},
			[]map[string]any{runCheck("test", "test", "push", "SUCCESS", 30, 300, "2026-10-21T08:00:00Z"), runCheck("test", "test", "pull_request", "SUCCESS", 31, 310, "2026-10-21T08:00:01Z"), runCheck("lint", "test", "pull_request", "SUCCESS", 31, 311, "2026-10-21T08:00:01Z")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, repo := setup(t)
			ghd := fakeGH(t, "OPEN")
			slug := prSession(t, d, repo)
			step := func(what, head string, checks []map[string]any, icon string) *state.PRStatus {
				t.Helper()
				prFixture{Head: head, Checks: checks}.write(t, ghd)
				poll(d)
				st := call[state.Session](t, d, "session.get", map[string]any{"slug": slug}).PRStatus
				look, _ := theme.PRLook(theme.Icons("unicode"), st)
				if look.Word != icon {
					t.Fatalf("%s: PR mark %q, want %q (%+v)", what, look.Word, icon, st)
				}
				return st
			}
			step("red", head1, red, "ci fail")
			step("running", tc.head, tc.running, "ci run")
			st := step("green", tc.head, tc.done, "ci pass")
			if st.Fail != 0 || st.Pass != 2 || len(st.FailedChecks) != 0 || !strings.HasPrefix(st.CIResult, "green 2/0 ") {
				t.Fatalf("green counts: %+v", st)
			}
			poll(d) // nothing new: told once
			var ci []string
			d.changes.mu.Lock()
			for _, c := range d.changes.ring {
				if c.Kind == kindCI {
					ci = append(ci, c.Summary)
				}
			}
			d.changes.mu.Unlock()
			if len(ci) != 2 || !strings.Contains(ci[0], "ci red: 1 failed (test), 1 passed") || !strings.Contains(ci[1], "ci green: 2 passed") {
				t.Fatalf("wait's ci: red then green, once each: %q", ci)
			}
		})
	}
}
