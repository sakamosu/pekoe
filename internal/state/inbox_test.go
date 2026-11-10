package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestInboxRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rec := 0
	now := time.Now()
	a := &InboxItem{Rev: 1, Kind: "decision", From: "feat-a", About: "feat-a", Question: "q?", Options: []string{"x", "y"}, Recommend: &rec,
		Facts: &InboxFacts{Slug: "feat-a", Head: "abc"}, Status: InboxOpen, Delivery: DeliveryNone, CreatedAt: now, UpdatedAt: now}
	b := &InboxItem{Rev: 1, Kind: "login", From: "+tapi", Question: "gh auth login", Status: InboxOpen, Delivery: DeliveryNone, CreatedAt: now, UpdatedAt: now}
	for _, it := range []*InboxItem{a, b} {
		if err := db.AddInbox(it); err != nil || it.ID == 0 {
			t.Fatal(it.ID, err)
		}
	}
	got, err := db.InboxGet(a.ID)
	if err != nil || got.Recommend == nil || *got.Recommend != 0 || got.Facts == nil || got.Facts.Head != "abc" || len(got.Options) != 2 || got.Answer != nil {
		t.Fatalf("get: %+v %v", got, err)
	}
	if g, _ := db.InboxGet(b.ID); g.Recommend != nil || g.Links == nil || g.Options == nil {
		t.Fatalf("no recommendation: %+v", g)
	}
	if _, err := db.InboxGet(999); err != ErrNoInboxItem {
		t.Fatalf("missing: %v", err)
	}
	if n, _ := db.InboxOpenCount("feat-a"); n != 1 {
		t.Fatalf("open count: %d", n)
	}

	one := 1
	got.Status, got.Rev, got.Delivery = InboxAnswered, 2, DeliveryPending
	got.Answer = &InboxAnswer{Choice: &one, Note: "n", By: "human", At: now}
	if err := db.SaveInbox(got); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.InboxOpenCount("feat-a"); n != 0 {
		t.Fatalf("open count after the answer: %d", n)
	}
	open, _ := db.InboxList(InboxFilter{Status: []string{InboxOpen}})
	if len(open) != 1 || open[0].ID != b.ID {
		t.Fatalf("open: %+v", open)
	}
	pend, _ := db.InboxList(InboxFilter{Delivery: DeliveryPending})
	if len(pend) != 1 || pend[0].Answer == nil || *pend[0].Answer.Choice != 1 {
		t.Fatalf("pending: %+v", pend)
	}
	if ok, _ := db.SetInboxDelivery(a.ID, DeliveryPending, DeliveryDelivered); !ok {
		t.Fatal("pending -> delivered")
	}
	if ok, _ := db.SetInboxDelivery(a.ID, DeliveryPending, DeliveryWaited); ok {
		t.Fatal("delivered twice")
	}
	if all, _ := db.InboxList(InboxFilter{}); len(all) != 2 || all[0].ID != b.ID {
		t.Fatalf("all, newest first: %+v", all)
	}
	// rm of the session keeps its questions (precedent)
	db.DeleteSession("feat-a")
	if _, err := db.InboxGet(a.ID); err != nil {
		t.Fatal(err)
	}
}
