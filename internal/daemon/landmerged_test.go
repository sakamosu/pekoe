package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/state"
)

// landingSetup makes two worktree sessions with PR #42 — feat-x landing
// (pekoe land opened it), feat-y not — and a teardown hook that counts its
// runs and takes a while. The fake gh says the PR is merged.
func landingSetup(t *testing.T) (d *Daemon, count string) {
	t.Helper()
	d, _, repo := setup(t)
	onGitHub(t, d, repo)
	ghd := fakeGH(t, "MERGED")
	prFixture{State: "MERGED", Merge: "UNKNOWN"}.write(t, ghd)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	count = filepath.Join(t.TempDir(), "count")
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	cfg := "[hooks]\nteardown = [\"basename \\\"$PWD\\\" >> " + count + "; sleep 0.3\"]\n"
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte(cfg), 0o644)
	for _, b := range []string{"feat/x", "feat/y"} {
		s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": b})
		waitCreated(t, d, s.Slug)
	}
	d.update("feat-x", func(x *state.Session) { x.Landing, x.PR = true, 42 })
	d.update("feat-y", func(x *state.Session) { x.PR = 42 })
	return d, count
}

func waitLanded(t *testing.T, d *Daemon, slug string) state.Session {
	t.Helper()
	// a deadline in time, not in rounds: under load the teardown, git
	// worktree remove and the stats can take seconds
	for end := time.Now().Add(30 * time.Second); ; sleep10ms() {
		s := call[state.Session](t, d, "session.get", map[string]any{"slug": slug})
		// and finishLanding has returned (it records stats after landing):
		// nothing touches the DB after the test closes it
		_, busy := d.finishing.Load(slug)
		if s.State == state.Landed && !busy {
			return s
		}
		if time.Now().After(end) {
			t.Fatalf("%s not landed: %s (finishLanding running: %v)", slug, s.State, busy)
		}
	}
}

func teardowns(count string) []string {
	b, _ := os.ReadFile(count) // the worktree directory of each run
	return strings.Fields(string(b))
}

// The PR poller that sees a landing session's PR merged starts the landing
// right there, without waiting for watchMerges. watchMerges running at the
// same time does not tear it down a second time, and a session that did not
// go through pekoe land is only marked merged.
func TestPRPollerLandsMergedLanding(t *testing.T) {
	d, count := landingSetup(t)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); poll(d) }()
	go func() { defer wg.Done(); d.watchMerges() }()
	wg.Wait()
	x := waitLanded(t, d, "feat-x")
	if x.PRStatus == nil || x.PRStatus.State != "MERGED" || x.Landing {
		t.Fatalf("landed session: %+v", x)
	}
	if _, err := os.Stat(x.Path); !os.IsNotExist(err) {
		t.Fatal("worktree not removed after merge")
	}
	d.watchMerges() // a later round finds nothing to do
	poll(d)
	time.Sleep(400 * time.Millisecond) // a second teardown would have started by now
	if got := teardowns(count); len(got) != 1 || !strings.HasSuffix(got[0], "feat-x") {
		t.Fatalf("teardowns: %v", got)
	}
	y := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-y"})
	if y.PRStatus == nil || y.PRStatus.State != "MERGED" {
		t.Fatalf("merge of feat-y not noticed: %+v", y.PRStatus)
	}
	if y.State == state.Landed || y.Stopped {
		t.Fatalf("a non-landing session must not be torn down: %+v", y)
	}
	if _, err := os.Stat(y.Path); err != nil {
		t.Fatal("worktree of feat-y removed")
	}
}

// The poller does not wait for the teardown: it returns while the hook still
// runs. The hook waits for a file the test makes only after poll returned,
// so a poller that waited for it would never return.
func TestPRPollerDoesNotWaitForTeardown(t *testing.T) {
	d, count := landingSetup(t)
	rdir := t.TempDir()
	release := filepath.Join(rdir, "release")
	// the hook ends even when the test fails before releasing it, and
	// never outlives the test: it also stops once the directory is gone
	// (TempDir's cleanup can remove it before the hook sees release) or
	// after a minute (the test binary killed before its cleanups)
	t.Cleanup(func() { os.WriteFile(release, nil, 0o644) })
	cfgDir := filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos")
	ents, _ := os.ReadDir(cfgDir)
	os.WriteFile(filepath.Join(cfgDir, ents[0].Name()),
		[]byte("[hooks]\nteardown = [\"i=0; while [ ! -e "+release+" ] && [ -d "+rdir+" ] && [ $i -lt 1200 ]; do sleep 0.05; i=$((i+1)); done; basename \\\"$PWD\\\" >> "+count+"\"]\n"), 0o644)
	returned := make(chan struct{})
	go func() { poll(d); close(returned) }()
	select {
	case <-returned:
	case <-time.After(30 * time.Second): // only so a waiting poller fails the test
		t.Fatal("the poller waits for the teardown hook")
	}
	if got := teardowns(count); len(got) != 0 {
		t.Fatalf("the hook ran past its wait: %v", got)
	}
	os.WriteFile(release, nil, 0o644)
	x := waitLanded(t, d, "feat-x")
	if got := teardowns(count); len(got) != 1 || !strings.HasSuffix(got[0], "feat-x") {
		t.Fatalf("teardowns: %v (landed %+v)", got, x)
	}
}

// pekoed starting up looks for merged landing PRs at once (a merge that
// happened while it was down or restarting), not 2 minutes later.
func TestStartupWatchesMerges(t *testing.T) {
	d, count := landingSetup(t)
	go d.tickLoop()
	t.Cleanup(d.Stop)
	waitLanded(t, d, "feat-x")
	if got := teardowns(count); len(got) != 1 || !strings.HasSuffix(got[0], "feat-x") {
		t.Fatalf("teardowns: %v", got)
	}
	if y := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-y"}); y.State == state.Landed {
		t.Fatal("a non-landing session must not be torn down")
	}
}
