package daemon

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"pekoe/internal/paths"
)

// New fixes data and config written with 0644 / 0755 by older versions, and
// creates its own files owner-only.
func TestNewTightensDataDir(t *testing.T) {
	lockEnv(t)
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	picks := paths.PicksDir("repo")
	if err := os.MkdirAll(picks, 0o755); err != nil {
		t.Fatal(err)
	}
	pick := filepath.Join(picks, "0001.md")
	cfg := paths.RepoConfig("repo")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{pick, paths.DaemonLog(), cfg} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d.DB.Close(); d.releaseLock() }()
	for _, p := range []string{paths.DataDir(), filepath.Dir(picks), picks, paths.ConfigDir(), paths.ReposConfigDir()} {
		if m := statPerm(t, p); m != 0o700 {
			t.Errorf("%s: %o, want 700", p, m)
		}
	}
	for _, p := range []string{pick, paths.DaemonLog(), cfg, paths.StateDB()} {
		if m := statPerm(t, p); m != 0o600 {
			t.Errorf("%s: %o, want 600", p, m)
		}
	}
}

func statPerm(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}
