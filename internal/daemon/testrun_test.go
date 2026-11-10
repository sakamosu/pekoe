package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pekoe/internal/state"
)

// A slow test run from an earlier Stop must not overwrite the result of the
// run a later Stop started (it tested older code).
func TestOlderTestRunDoesNotOverwrite(t *testing.T) {
	d, _, repo := setup(t)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	counter := filepath.Join(t.TempDir(), "n")
	// the first run is slow and fails, the second is quick and passes
	test := `n=$(cat ` + counter + ` 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + counter + `; if [ $n = 1 ]; then sleep 1; exit 1; fi; exit 0`
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte("[hooks]\ntest = '"+test+"'\n"), 0o644)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	done := make(chan struct{})
	go func() { d.runTest("main"); close(done) }()
	for i := 0; i < 100; i++ { // the first run has started
		if b, _ := os.ReadFile(counter); len(b) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.runTest("main")
	<-done
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if s.Test.Status != "pass" {
		t.Fatalf("the older run's result won: %+v", s.Test)
	}
}
