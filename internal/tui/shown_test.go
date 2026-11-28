package tui

import (
	"testing"

	"pekoe/internal/client"
)

type noopMsg struct{}

func outputEv(slug string) client.Event {
	return client.Event{Event: "session.output", Data: []byte(`{"slug":"` + slug + `","window":"agent"}`)}
}

// pump passes on session.output only for the session shown: every other
// session's would only make Bubble Tea draw the View again for nothing.
// Other events always go through.
func TestPumpDropsHiddenOutput(t *testing.T) {
	m := repoModel(t, "none", 140)
	m.sel = 0
	m.Update(noopMsg{})
	cur := m.current().Slug
	other := ""
	for _, s := range m.sessions {
		if s.Slug != cur {
			other = s.Slug
			break
		}
	}
	if got := m.shown.get(); got != cur {
		t.Fatalf("shown %q after Update, want %q", got, cur)
	}
	if m.shown.dropHidden(outputEv(cur)) {
		t.Fatal("the shown session's output was dropped")
	}
	if !m.shown.dropHidden(outputEv(other)) {
		t.Fatal("a hidden session's output went through")
	}
	for _, ev := range []client.Event{
		{Event: "session.updated", Data: []byte(`{"slug":"` + other + `"}`)},
		{Event: "tapi.output", Data: []byte(`{}`)},
		{Event: "session.output", Data: []byte(`not json`)}, // onEvent decides, as before
	} {
		if m.shown.dropHidden(ev) {
			t.Fatalf("%s %s was dropped", ev.Event, ev.Data)
		}
	}

	// moving to the other session lets its output through from this Update on
	m.sel = 1
	m.Update(noopMsg{})
	if now := m.current().Slug; m.shown.dropHidden(outputEv(now)) || !m.shown.dropHidden(outputEv(cur)) {
		t.Fatalf("after the move to %s: shown %q", now, m.shown.get())
	}

	// no session shown (tapi row, dashboard): nothing to fetch, all dropped
	m.sel = -1
	m.Update(noopMsg{})
	if !m.shown.dropHidden(outputEv(cur)) {
		t.Fatal("output went through with no session shown")
	}

	// the standalone review keeps no shownSlug: everything goes through
	var none *shownSlug
	if none.dropHidden(outputEv(cur)) {
		t.Fatal("nil shownSlug dropped an event")
	}
}
