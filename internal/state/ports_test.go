package state

import (
	"path/filepath"
	"testing"
	"time"
)

// A held port is skipped by AllocPort until its time passes, then given
// out again; the session's own port and other ports are unaffected.
func TestAllocPortSkipsHeld(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := db.AllocPort("r", "a", 3001, nil)
	if err != nil || p != 3001 {
		t.Fatalf("a: %d %v", p, err)
	}
	if err := db.DeleteSession("a"); err != nil {
		t.Fatal(err)
	}
	if err := db.HoldPort(HeldPort{Port: 3001, RepoID: "r", Slug: "a", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if p, err := db.AllocPort("r", "b", 3001, nil); err != nil || p != 3002 {
		t.Fatalf("b got %d %v, want 3002 (3001 held)", p, err)
	}
	// a shorter hold does not shorten a longer one
	if err := db.HoldPort(HeldPort{Port: 3001, RepoID: "r", Slug: "a", Until: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if p, err := db.AllocPort("r", "c", 3001, nil); err != nil || p != 3003 {
		t.Fatalf("c got %d %v, want 3003 (3001 still held)", p, err)
	}
	// once the time has passed, the port is free again
	if _, err := db.db.Exec(`UPDATE held_ports SET until=? WHERE port=3001`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if p, err := db.AllocPort("r", "d", 3001, nil); err != nil || p != 3001 {
		t.Fatalf("d got %d %v, want 3001 (hold expired)", p, err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM held_ports`).Scan(&n)
	if n != 0 {
		t.Fatalf("expired hold kept: %d rows", n)
	}
}
