package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pekoe/internal/state"
)

// Two finishLanding of one session (watchMerges every 2 minutes while a
// teardown is still running) run the teardown once.
func TestFinishLandingRunsOnce(t *testing.T) {
	d, _, repo := setup(t)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	count := filepath.Join(t.TempDir(), "count")
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	cfg := "[hooks]\nteardown = [\"echo x >> " + count + "; sleep 0.5\"]\n"
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte(cfg), 0o644)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	for i := 0; i < 100; i++ {
		if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"}); s.State != state.Creating {
			break
		}
		sleep()
	}
	d.update("feat-x", func(x *state.Session) { x.Landing, x.PR = true, 1 })
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.finishLanding("feat-x") }()
	}
	wg.Wait()
	d.finishLanding("feat-x") // a watchMerges that listed it before it landed
	b, _ := os.ReadFile(count)
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Fatalf("teardown ran %d times", n)
	}
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"}); s.State != state.Landed {
		t.Fatalf("not landed: %s", s.State)
	}
}
