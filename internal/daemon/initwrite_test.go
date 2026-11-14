package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"pekoe/internal/state"
)

// init.write says where the main checkout is (pekoe init opens the main
// session there), also when init ran in a subdirectory; the session opened
// there is the one whose path is the repository's.
func TestInitWriteReturnsRepoDir(t *testing.T) {
	d, _, repo := setup(t)
	sub := filepath.Join(repo, "sub")
	os.MkdirAll(sub, 0o755)
	res := call[map[string]any](t, d, "init.write", map[string]any{"path": sub, "toml": "[hooks]\n", "overwrite": true})
	dir, _ := res["repo_dir"].(string)
	if dir == "" {
		t.Fatalf("init.write = %v, want repo_dir", res)
	}
	s := call[state.Session](t, d, "session.open", map[string]any{"path": dir, "resume": "new"})
	if s.Path != dir || s.RepoDir != dir || s.Managed {
		t.Fatalf("session opened at repo_dir %s = %+v, want the main checkout's session", dir, s)
	}
}
