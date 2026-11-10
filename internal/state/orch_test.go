package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestReportsAndSends(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, slug := range []string{"a", "b", "a"} {
		r := &Report{Slug: slug, By: slug, Status: "done", Summary: "s" + string(rune('0'+i)), Evidence: []string{"x"}, At: time.Now()}
		if err := db.AddReport(r); err != nil || r.ID == 0 {
			t.Fatal(r.ID, err)
		}
	}
	rs, _ := db.Reports("a", 0, 10)
	if len(rs) != 2 || rs[0].Summary != "s2" || rs[0].Evidence[0] != "x" {
		t.Fatalf("reports of a, newest first: %+v", rs)
	}
	if rs, _ := db.Reports("", rs[1].ID, 10); len(rs) != 2 {
		t.Fatalf("since: %+v", rs)
	}
	// rm takes a session's reports with it
	if err := db.DeleteSession("a"); err != nil {
		t.Fatal(err)
	}
	if rs, _ := db.Reports("a", 0, 10); len(rs) != 0 {
		t.Fatalf("left after rm: %+v", rs)
	}

	for _, r := range []SendRecord{{From: "+tapi", To: "a", Kind: "new", Text: "do x", OK: true}, {From: "human", To: "b", Kind: "send", Text: "y", OK: true}, {From: "b", To: "+tapi", Kind: "send", Error: "e"}} {
		r.At = time.Now()
		if err := db.AddSend(&r); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := db.Sends(SendFilter{Party: "+tapi", Limit: 10}); len(got) != 2 || got[0].From != "b" || got[0].OK || got[0].Error != "e" {
		t.Fatalf("party: %+v", got)
	}
	if got, _ := db.Sends(SendFilter{To: "b", Limit: 10}); len(got) != 1 || got[0].Text != "y" {
		t.Fatalf("to: %+v", got)
	}
}
