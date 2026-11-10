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

// A test that resolves the user's real locations must blow up instead of
// writing there (the TestMain isolation was forgotten or undone).
func TestRealLocationsRefusedInTests(t *testing.T) {
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: resolved a real location without panicking", name)
			}
		}()
		f()
	}
	for _, k := range []string{"PEKOE_HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "PEKOE_SOCK", "PEKOE_WORKTREES", "PEKOE_TMUX_SOCKET"} {
		t.Setenv(k, "")
	}
	mustPanic("DataDir", func() { DataDir() })
	mustPanic("ConfigDir", func() { ConfigDir() })
	mustPanic("WorktreesRoot", func() { WorktreesRoot() })
	mustPanic("Socket", func() { Socket() })
	mustPanic("TmuxSocketName", func() { TmuxSocketName() })
	mustPanic("StateDB", func() { StateDB() })

	// pointing straight at them is refused too
	t.Setenv("PEKOE_HOME", t.TempDir())
	t.Setenv("PEKOE_SOCK", filepath.Join(realDirs[1], "pekoed.sock"))
	mustPanic("PEKOE_SOCK", func() { Socket() })
	t.Setenv("PEKOE_WORKTREES", realDirs[2])
	mustPanic("PEKOE_WORKTREES", func() { WorktreesRoot() })
	t.Setenv("PEKOE_TMUX_SOCKET", "pekoe")
	mustPanic("PEKOE_TMUX_SOCKET", func() { TmuxSocketName() })
	t.Setenv("PEKOE_HOME", "")
	t.Setenv("XDG_DATA_HOME", filepath.Dir(realDirs[1]))
	mustPanic("XDG_DATA_HOME", func() { DataDir() })

	// the TestMain isolation itself passes
	t.Setenv("PEKOE_HOME", t.TempDir())
	t.Setenv("PEKOE_SOCK", "")
	t.Setenv("PEKOE_WORKTREES", "")
	t.Setenv("PEKOE_TMUX_SOCKET", "pekoe-test-x")
	DataDir()
	ConfigDir()
	WorktreesRoot()
	Socket()
	TmuxSocketName()
}
