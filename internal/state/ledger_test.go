package state

import (
	"database/sql"
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

// repeats and auto are kept, the newest entry is read back, and a
// state.db made before the columns existed gets them.
func TestLedgerRepeatsAuto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE ledger (id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, text TEXT NOT NULL, status TEXT NOT NULL, sessions TEXT NOT NULL DEFAULT '[]', evidence TEXT NOT NULL DEFAULT '[]', note TEXT NOT NULL DEFAULT '', closed_at TEXT NOT NULL DEFAULT '');
INSERT INTO ledger(at,text,status) VALUES('2026-01-02T03:04:05Z','前からある依頼','open');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for i := 0; i < 2; i++ { // opening twice adds the columns once
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e, err := db.LedgerLast()
	if err != nil || e == nil || e.ID != 1 || e.Text != "前からある依頼" || e.Repeats != 0 || e.Auto {
		t.Fatalf("an old entry: %+v %v", e, err)
	}
	now := time.Now()
	e.Repeats, e.Auto, e.Status, e.ClosedAt = 2, true, LedgerChat, &now
	if err := db.SaveLedger(e); err != nil {
		t.Fatal(err)
	}
	if err := db.AddLedger(&LedgerEntry{At: now, Text: "次", Status: LedgerOpen}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.LedgerGet(1); got.Repeats != 2 || !got.Auto {
		t.Fatalf("saved: %+v", got)
	}
	if last, _ := db.LedgerLast(); last == nil || last.ID != 2 || last.Repeats != 0 || last.Auto {
		t.Fatalf("last: %+v", last)
	}
}

func TestLedgerLastEmpty(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if e, err := db.LedgerLast(); e != nil || err != nil {
		t.Fatalf("empty ledger: %+v %v", e, err)
	}
}

// The open entries and, for a while, the ones pekoed closed itself: not
// the ones it closed long ago, nor what tapi closed.
func TestLedgerOpenAndAuto(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	for _, e := range []LedgerEntry{
		{Text: "open のまま", Status: LedgerOpen},
		{Text: "さっき自動で", Status: LedgerChat, Auto: true, ClosedAt: at(-time.Minute)},
		{Text: "23 時間前に自動で", Status: LedgerChat, Auto: true, ClosedAt: at(-23 * time.Hour)},
		{Text: "25 時間前に自動で", Status: LedgerChat, Auto: true, ClosedAt: at(-25 * time.Hour)},
		{Text: "tapi が chat で", Status: LedgerChat, ClosedAt: at(-time.Minute)},
		{Text: "済んだ", Status: LedgerDone, ClosedAt: at(-time.Minute)},
		{Text: "次の open", Status: LedgerOpen},
	} {
		e := e
		e.At = now
		if err := db.AddLedger(&e); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveLedger(&e); err != nil { // auto is not AddLedger's
			t.Fatal(err)
		}
	}
	ids := func(es []LedgerEntry) (out []int64) {
		for _, e := range es {
			out = append(out, e.ID)
		}
		return
	}
	es, err := db.LedgerOpenAndAuto(0, now.Add(-24*time.Hour))
	if got := ids(es); err != nil || len(got) != 4 || got[0] != 1 || got[1] != 2 || got[2] != 3 || got[3] != 7 {
		t.Fatalf("open and lately auto, oldest first: %v %v", got, err)
	}
	if !es[1].Auto || es[1].Status != LedgerChat {
		t.Fatalf("an auto one says so: %+v", es[1])
	}
	if got := ids(must(db.LedgerOpenAndAuto(2, now.Add(-24*time.Hour)))); len(got) != 2 || got[0] != 3 {
		t.Fatalf("since: %v", got)
	}
	if got := ids(must(db.LedgerList(false, 0))); len(got) != 2 {
		t.Fatalf("open alone is unchanged: %v", got)
	}
}

func must(es []LedgerEntry, err error) []LedgerEntry {
	if err != nil {
		panic(err)
	}
	return es
}
