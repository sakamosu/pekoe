package state

import (
	"path/filepath"
	"testing"
	"time"
)

// A merge at midnight with a fraction of a second is in the range that
// starts at midnight (RFC3339Nano strings do not sort by time there).
func TestSessionRecordsFromMidnight(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mid := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	at := mid.Add(500 * time.Millisecond)
	if err := db.RecordSession(SessionRecord{Slug: "a", Repo: "app", CreatedAt: mid.Add(-time.Hour), MergedAt: &at, EndedAt: &at}); err != nil {
		t.Fatal(err)
	}
	if rs, _ := db.SessionRecords(mid); len(rs) != 1 || !rs[0].MergedAt.Equal(at) {
		t.Fatalf("%+v", rs)
	}
}

// ids counted before usage_ids knew their transcript (path "") stay while
// the life has a transcript left, and go with its last one.
func TestForgetTranscriptOldIDs(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.Exec(`INSERT INTO usage_ids(slug,born,id) VALUES('+tapi','','old1'),('+tapi','','old2'),('x','b','other')`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/t/a.jsonl", "/t/b.jsonl"} {
		if err := db.AddUsage(UsageCursor{Slug: "+tapi", Path: p, Offset: 1}, "", []UsageMsg{{ID: p, Day: "2026-11-18"}}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(slug, born string) int {
		n, err := db.UsageIDs(slug, born)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := db.ForgetTranscript("+tapi", "", "/t/a.jsonl"); err != nil {
		t.Fatal(err)
	}
	if n := count("+tapi", ""); n != 3 {
		t.Fatalf("ids = %d, want 3 (old1 old2 /t/b.jsonl)", n)
	}
	if err := db.ForgetTranscript("+tapi", "", "/t/b.jsonl"); err != nil {
		t.Fatal(err)
	}
	if n := count("+tapi", ""); n != 0 {
		t.Fatalf("ids = %d, want 0", n)
	}
	if n := count("x", "b"); n != 1 {
		t.Fatalf("another life's ids = %d, want 1", n)
	}
}

// a state.db from before usage_ids.path gets the column
func TestUsageIDsPathAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP INDEX usage_ids_path`, `DROP TABLE usage_ids`,
		`CREATE TABLE usage_ids (slug TEXT NOT NULL, born TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY(slug, born, id))`,
		`INSERT INTO usage_ids VALUES('+tapi','','m1')`} {
		if _, err := db.db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.AddUsage(UsageCursor{Slug: "+tapi", Path: "/t/a.jsonl"}, "", []UsageMsg{{ID: "m1"}, {ID: "m2"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.UsageIDs("+tapi", ""); n != 2 {
		t.Fatalf("ids = %d, want 2", n)
	}
}
