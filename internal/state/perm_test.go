package state

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// state.db and its -wal / -shm are owner-only even under a permissive umask.
func TestOpenOwnerOnly(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	p := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.Exec(`INSERT INTO kv(k, v) VALUES ('x', 'y')`); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p, p + "-wal", p + "-shm"} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := fi.Mode().Perm(); m != 0o600 {
			t.Errorf("%s: %o, want 600", filepath.Base(f), m)
		}
	}
}
