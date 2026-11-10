package daemon

import (
	"testing"

	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// A session lands when its PR was merged, and PR polling stops there: the
// summary must say merged, not the CI state fetched just before the merge.
func TestLandedSessionShowsPRMerged(t *testing.T) {
	d, _, repo := setup(t)
	s := readySession(t, d, repo, "feat/merged")
	d.update(s.Slug, func(x *state.Session) {
		x.PR, x.Landing = 89, true
		x.PRStatus = &state.PRStatus{State: "OPEN", Pass: 3, Pending: 1, Attention: []string{"ci"}}
	})
	if err := d.finishLanding(s.Slug); err != nil {
		t.Fatal(err)
	}
	got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if got.State != state.Landed || got.PRStatus == nil || got.PRStatus.State != "MERGED" || len(got.PRStatus.Attention) != 0 {
		t.Fatalf("after landing: state %s, pr status %+v", got.State, got.PRStatus)
	}
	if lk, ok := theme.PRLook(theme.WebIcons(), got.PRStatus); !ok || lk.Word != "merged" {
		t.Fatalf("PR look: %+v %v", lk, ok)
	}

	// a fetch that started before the merge and lands after it
	d.applyPRStatus(s.Slug, &state.PRStatus{State: "OPEN", Pass: 3}, false)
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); got.PRStatus.State != "MERGED" {
		t.Fatalf("late fetch: %+v", got.PRStatus)
	}
}

// Sessions that landed before this fix keep the old summary in state.db:
// restore marks them merged; a landed session without a PR stays without.
func TestRestoreMarksLandedPRMerged(t *testing.T) {
	d, _, repo := setup(t)
	a := readySession(t, d, repo, "feat/old-land")
	b := readySession(t, d, repo, "feat/no-pr")
	d.update(a.Slug, func(x *state.Session) {
		x.State, x.PR = state.Landed, 90
		x.PRStatus = &state.PRStatus{State: "OPEN", Pending: 2}
	})
	d.update(b.Slug, func(x *state.Session) { x.State = state.Landed })
	d.DB.Close()
	d.releaseLock()
	d2, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d2.Eng = newFake()
	t.Cleanup(func() { d2.DB.Close(); d2.releaseLock() })
	d2.restore()
	got := call[state.Session](t, d2, "session.get", map[string]any{"slug": a.Slug})
	if got.PRStatus == nil || got.PRStatus.State != "MERGED" || got.PRStatus.Pending != 2 {
		t.Fatalf("restored landed PR: %+v", got.PRStatus)
	}
	l, _ := d2.DB.Sessions()
	for _, x := range l {
		if x.Slug == a.Slug && (x.PRStatus == nil || x.PRStatus.State != "MERGED") {
			t.Fatalf("not saved: %+v", x.PRStatus)
		}
	}
	if got := call[state.Session](t, d2, "session.get", map[string]any{"slug": b.Slug}); got.PRStatus != nil {
		t.Fatalf("landed without a PR got a summary: %+v", got.PRStatus)
	}
}
