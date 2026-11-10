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
