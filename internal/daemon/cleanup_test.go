package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// setState puts s in st and dates the change ago.
func setState(t *testing.T, d *Daemon, slug string, st state.State, ago time.Duration, more ...func(*state.Session)) {
	t.Helper()
	if _, err := d.update(slug, func(x *state.Session) { x.State = st }); err != nil {
		t.Fatal(err)
	}
	d.update(slug, func(x *state.Session) {
		x.StateSince = time.Now().Add(-ago)
		for _, f := range more {
			f(x)
		}
	})
}

func cleanupItems(t *testing.T, d *Daemon, params map[string]any) map[string]CleanupItem {
	t.Helper()
	l := call[CleanupList](t, d, "cleanup.list", params)
	out := map[string]CleanupItem{}
	for _, it := range l.Items {
		out[it.Slug] = it
	}
	return out
}

// The three kinds of candidate, and the sessions that are not one.
func TestCleanupListKinds(t *testing.T) {
	d, _, repo := setup(t)
	landed := readySession(t, d, repo, "feat/landed")
	if err := d.finishLanding(landed.Slug); err != nil {
		t.Fatal(err)
	}
	merged := readySession(t, d, repo, "feat/merged")
	setState(t, d, merged.Slug, state.Done, time.Hour, func(x *state.Session) {
		x.PR = 7
		x.PRStatus = &state.PRStatus{State: "MERGED"}
	})
	stale := readySession(t, d, repo, "feat/stale")
	setState(t, d, stale.Slug, state.Done, 100*time.Hour)
	failed := readySession(t, d, repo, "feat/failed")
	setState(t, d, failed.Slug, state.Failed, 80*time.Hour)

	// not candidates
	fresh := readySession(t, d, repo, "feat/fresh")
	setState(t, d, fresh.Slug, state.Done, time.Hour)
	running := readySession(t, d, repo, "feat/running")
	setState(t, d, running.Slug, state.Running, 200*time.Hour)
	waitingMerge := readySession(t, d, repo, "feat/landing")
	setState(t, d, waitingMerge.Slug, state.Done, 200*time.Hour, func(x *state.Session) { x.Landing, x.PR = true, 9 })
	dev := readySession(t, d, repo, "feat/dev")
	setState(t, d, dev.Slug, state.Idle, 200*time.Hour, func(x *state.Session) { x.Dev = true })

	l := call[CleanupList](t, d, "cleanup.list", map[string]any{})
	if l.StaleAfter != "72h0m0s" {
		t.Errorf("stale_after %q, want the default 72h", l.StaleAfter)
	}
	var got []string
	for _, it := range l.Items {
		got = append(got, it.Kind+":"+it.Slug)
	}
	want := []string{"landed:" + landed.Slug, "merged:" + merged.Slug, "stale:" + stale.Slug, "stale:" + failed.Slug}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("candidates %v, want %v (landed, merged, then stale oldest first)", got, want)
	}

	items := cleanupItems(t, d, map[string]any{})
	if it := items[landed.Slug]; it.Worktree || it.HumanOnly || len(it.Warnings) != 0 {
		t.Errorf("landed: %+v, want no worktree, open to agents, no warning", it)
	}
	for _, slug := range []string{merged.Slug, stale.Slug, failed.Slug} {
		it := items[slug]
		if !it.Worktree || !it.DeletesWorktree || !it.HumanOnly {
			t.Errorf("%s: %+v, want its worktree there, deleted by rm, human only", slug, it)
		}
		if it.Dirty != 0 || it.Unpushed != 0 || len(it.Warnings) != 0 {
			t.Errorf("%s: clean worktree read as dirty %d unpushed %d %v", slug, it.Dirty, it.Unpushed, it.Warnings)
		}
	}

	// a shorter threshold takes the fresh one in; the repository filter
	items = cleanupItems(t, d, map[string]any{"stale_after": "30m"})
	if items[fresh.Slug].Kind != CleanupStale {
		t.Errorf("stale_after 30m: %s not listed", fresh.Slug)
	}
	if len(cleanupItems(t, d, map[string]any{"repo_id": "elsewhere"})) != 0 {
		t.Error("another repository's filter listed sessions")
	}
	b, _ := json.Marshal(map[string]any{"stale_after": "soon"})
	if _, err := d.Call("cleanup.list", b); err == nil {
		t.Error("stale_after soon accepted")
	}

	// [cleanup] stale_after
	d.Global.Cleanup.StaleAfter = "200h"
	if items := cleanupItems(t, d, map[string]any{}); items[stale.Slug].Kind != "" {
		t.Errorf("stale_after 200h in config: %s still listed", stale.Slug)
	}
}

// Uncommitted files and commits on no remote are counted and named, so a
// person sees what rm would lose.
func TestCleanupWarnsUncommittedAndUnpushed(t *testing.T) {
	d, _, repo := setup(t)
	s := readySession(t, d, repo, "feat/work")
	setState(t, d, s.Slug, state.Idle, 100*time.Hour, func(x *state.Session) { x.PR = 3 })

	os.WriteFile(filepath.Join(s.Path, "a.txt"), []byte("changed\n"), 0o644)
	os.WriteFile(filepath.Join(s.Path, "new.txt"), []byte("x\n"), 0o644)
	it := cleanupItems(t, d, nil)[s.Slug]
	if it.Dirty != 2 || it.Unpushed != 0 {
		t.Fatalf("dirty %d unpushed %d, want 2 and 0", it.Dirty, it.Unpushed)
	}
	if w := strings.Join(it.Warnings, "; "); !strings.Contains(w, "2 uncommitted files") || !strings.Contains(w, "PR #3 is still open") {
		t.Errorf("warnings %q", w)
	}

	run(t, s.Path, "git", "add", "a.txt")
	run(t, s.Path, "git", "commit", "-qm", "one")
	run(t, s.Path, "git", "add", "new.txt")
	run(t, s.Path, "git", "commit", "-qm", "two")
	it = cleanupItems(t, d, nil)[s.Slug]
	if it.Dirty != 0 || it.Unpushed != 2 {
		t.Fatalf("after commits: dirty %d unpushed %d, want 0 and 2", it.Dirty, it.Unpushed)
	}
	if w := strings.Join(it.Warnings, "; "); !strings.Contains(w, "2 unpushed commits") || strings.Contains(w, "uncommitted") {
		t.Errorf("warnings %q", w)
	}

	// pushed: the commits are on a remote-tracking branch
	remote := filepath.Join(t.TempDir(), "origin.git")
	run(t, repo, "git", "init", "-q", "--bare", remote)
	run(t, s.Path, "git", "remote", "add", "origin", remote)
	run(t, s.Path, "git", "push", "-q", "origin", "HEAD:refs/heads/feat/work")
	run(t, s.Path, "git", "fetch", "-q", "origin")
	if it := cleanupItems(t, d, nil)[s.Slug]; it.Unpushed != 0 {
		t.Errorf("after push: unpushed %d", it.Unpushed)
	}

	// merged on GitHub (squash) and the remote branch deleted: the PR's
	// head counts as pushed
	head := strings.TrimSpace(gitOut(t, s.Path, "rev-parse", "HEAD"))
	run(t, s.Path, "git", "push", "-q", "origin", ":refs/heads/feat/work")
	run(t, s.Path, "git", "fetch", "-q", "--prune", "origin")
	if it := cleanupItems(t, d, nil)[s.Slug]; it.Unpushed != 2 {
		t.Fatalf("remote branch gone: unpushed %d, want 2", it.Unpushed)
	}
	d.update(s.Slug, func(x *state.Session) { x.PRStatus = &state.PRStatus{State: "MERGED", HeadSHA: head} })
	it = cleanupItems(t, d, nil)[s.Slug]
	if it.Kind != CleanupMerged || it.Unpushed != 0 || len(it.Warnings) != 0 {
		t.Errorf("merged PR's head: %+v", it)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// Without the token the list is open, and rm removes only a landed
// candidate: a merged or stale one (its worktree and changes) takes a
// person.
func TestCleanupAgentRemovesOnlyLanded(t *testing.T) {
	d, _, repo := setup(t)
	c := dialPipe(t, d)
	landed := readySession(t, d, repo, "feat/landed")
	d.finishLanding(landed.Slug)
	merged := readySession(t, d, repo, "feat/merged")
	setState(t, d, merged.Slug, state.Done, time.Hour, func(x *state.Session) { x.PRStatus = &state.PRStatus{State: "MERGED"} })
	stale := readySession(t, d, repo, "feat/stale")
	setState(t, d, stale.Slug, state.Idle, 100*time.Hour)

	if e := c.call("cleanup.list", map[string]any{}, ""); e != "" {
		t.Fatalf("cleanup.list without the token: %s", e)
	}
	for _, slug := range []string{merged.Slug, stale.Slug} {
		if e := c.call("worktree.rm", map[string]any{"slug": slug}, ""); !strings.Contains(e, "only a person can do this") {
			t.Errorf("rm of the %s candidate without the token: error = %q", slug, e)
		}
		if _, err := d.get(slug); err != nil {
			t.Errorf("%s removed without the token", slug)
		}
	}
	if e := c.call("worktree.rm", map[string]any{"slug": landed.Slug}, ""); e != "" {
		t.Fatalf("rm of the landed candidate without the token: %s", e)
	}
	// with the token, a person removes the rest
	for _, slug := range []string{merged.Slug, stale.Slug} {
		if e := c.call("worktree.rm", map[string]any{"slug": slug}, d.humanToken); e != "" {
			t.Errorf("rm of %s with the token: %s", slug, e)
		}
	}
	if items := cleanupItems(t, d, nil); len(items) != 0 {
		t.Errorf("candidates left: %v", items)
	}
}

// An agent's repeated lists reuse a git reading younger than 10 s; a
// person's list reads git again.
func TestCleanupAgentReadsGitAtMostEvery10s(t *testing.T) {
	d, _, repo := setup(t)
	s := readySession(t, d, repo, "feat/x")
	setState(t, d, s.Slug, state.Done, 100*time.Hour)
	if it, _ := d.hAgentCleanup(cleanupP{}); it.(*CleanupList).Items[0].Dirty != 0 {
		t.Fatal("dirty before writing")
	}
	os.WriteFile(filepath.Join(s.Path, "n.txt"), []byte("x\n"), 0o644)
	if it, _ := d.hAgentCleanup(cleanupP{}); it.(*CleanupList).Items[0].Dirty != 0 {
		t.Error("agent's list read git again within 10 s")
	}
	if it, _ := d.hCleanup(cleanupP{}); it.(*CleanupList).Items[0].Dirty != 1 {
		t.Error("a person's list reused the old reading")
	}
}
