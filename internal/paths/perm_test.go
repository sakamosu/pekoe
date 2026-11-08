package paths

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestEnsureIsOwnerOnly(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	d := filepath.Join(t.TempDir(), "a", "b")
	if err := Ensure(d); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, d); m != 0o700 {
		t.Errorf("Ensure made %o, want 700", m)
	}
}

func TestTighten(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	base := t.TempDir()
	root := filepath.Join(base, "share")
	sub := filepath.Join(root, "repo", "picks")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(root, "state.db")
	pick := filepath.Join(sub, "0001.md")
	script := filepath.Join(root, "run.sh")
	for p, m := range map[string]os.FileMode{db: 0o644, pick: 0o666, script: 0o755} {
		if err := os.WriteFile(p, nil, m); err != nil {
			t.Fatal(err)
		}
	}
	// a file outside, reached through a symlink, must not change
	outside := filepath.Join(base, "dotfiles.toml")
	if err := os.WriteFile(outside, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.toml")); err != nil {
		t.Fatal(err)
	}

	if err := Tighten(root); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{
		root: 0o700, filepath.Join(root, "repo"): 0o700, sub: 0o700,
		db: 0o600, pick: 0o600, script: 0o700, outside: 0o644,
	} {
		if m := mode(t, p); m != want {
			t.Errorf("%s: %o, want %o", p, m, want)
		}
	}

	if err := Tighten(filepath.Join(base, "missing")); err != nil {
		t.Errorf("missing root: %v", err)
	}
}

// A symlinked root is not followed either.
func TestTightenSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Tighten(link); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, real); m != 0o755 {
		t.Errorf("followed the symlinked root: %o", m)
	}
}
