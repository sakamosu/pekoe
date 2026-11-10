package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"feat/csv": "feat-csv", "fix//login": "fix-login", "/x/": "x", "日本": "session", "a.b_c": "a-b_c", "release/1.2": "release-1-2", ".hidden.": "hidden"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q)=%q want %q", in, got, want)
		}
	}
}

func TestUnderWorktrees(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PEKOE_WORKTREES", root)
	inside := filepath.Join(root, "app", "feat-x")
	os.MkdirAll(inside, 0o755)
	if !UnderWorktrees(inside) {
		t.Fatal("worktree under root not recognized")
	}
	// a worktree about to be made (root is under /var → /private/var on macOS)
	if !UnderWorktrees(filepath.Join(root, "other", "feat-y")) {
		t.Fatal("a path that does not exist yet is not recognized")
	}
	if UnderWorktrees(filepath.Join(root, "..", "evil")) || UnderWorktrees(filepath.Join(root, "app", "..", "..", "x", "y")) {
		t.Fatal("a path climbing out of root is accepted")
	}
	for _, p := range []string{root, filepath.Join(root, "app"), t.TempDir(), filepath.Join(root, "..")} {
		if UnderWorktrees(p) {
			t.Errorf("%s must not be deletable", p)
		}
	}
}
