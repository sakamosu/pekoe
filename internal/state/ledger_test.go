package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLedger(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, text := range []string{"CSV を直して", "ありがと", "README を英訳して"} {
		e := &LedgerEntry{At: time.Now(), Text: text, Status: LedgerOpen}
		if err := db.AddLedger(e); err != nil || e.ID == 0 {
			t.Fatal(e.ID, err)
		}
	}
	e, err := db.LedgerGet(2)
	if err != nil || e.Text != "ありがと" || len(e.Sessions) != 0 || e.ClosedAt != nil {
		t.Fatalf("get: %+v %v", e, err)
	}
	now := time.Now()
	e.Status, e.ClosedAt = LedgerChat, &now
	if err := db.SaveLedger(e); err != nil {
		t.Fatal(err)
	}
	e1, _ := db.LedgerGet(1)
	e1.Sessions, e1.Evidence = []string{"fix-csv"}, []string{"https://x/pull/1"}
	if err := db.SaveLedger(e1); err != nil {
		t.Fatal(err)
	}
	open, _ := db.LedgerList(false, 0)
	if len(open) != 2 || open[0].ID != 1 || open[0].Sessions[0] != "fix-csv" || open[1].ID != 3 {
		t.Fatalf("open, oldest first: %+v", open)
	}
	if all, _ := db.LedgerList(true, 1); len(all) != 2 || all[0].Status != LedgerChat || all[0].ClosedAt == nil {
		t.Fatalf("all since 1: %+v", all)
	}
	if n, _ := db.LedgerOpenCount(); n != 2 {
		t.Fatalf("open count %d", n)
	}
	if err := db.DeleteLedger(3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LedgerGet(3); !errors.Is(err, ErrNoLedgerEntry) {
		t.Fatalf("deleted: %v", err)
	}
	if err := db.DeleteLedger(3); !errors.Is(err, ErrNoLedgerEntry) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := db.SaveLedger(&LedgerEntry{ID: 99}); !errors.Is(err, ErrNoLedgerEntry) {
		t.Fatalf("save unknown: %v", err)
	}
}
