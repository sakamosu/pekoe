package tui

import (
	"testing"

	"pekoe/internal/state"
)

func wrapModel(t *testing.T, slugs ...string) *model {
	t.Helper()
	t.Setenv("PEKOE_COLOR", "none")
	m := newModel(nil, "")
	m.width, m.height = 120, 40
	m.focus = pSessions
	m.sessions = nil
	for _, s := range slugs {
		m.sessions = append(m.sessions, &state.Session{Slug: s, Branch: s, State: state.Idle})
	}
	return m
}

// selName is the selected row: "tapi" or the session's slug ("" for none).
func selName(m *model) string {
	if m.onTapiRow() {
		return "tapi"
	}
	if s := m.current(); s != nil {
		return s.Slug
	}
	return ""
}

// j/k in the Sessions panel wrap around at the ends; the tapi row is the top.
func TestSessionsCursorWraps(t *testing.T) {
	m := wrapModel(t, "a", "b", "c")
	m.sel = 0
	for _, step := range []struct {
		key  string
		want string
	}{
		{"j", "b"}, {"down", "c"}, {"j", "tapi"}, {"j", "a"}, // bottom → top
		{"k", "tapi"}, {"up", "c"}, {"k", "b"}, {"up", "a"}, // top → bottom
		{"G", "c"}, {"g", "tapi"}, {"g", "tapi"}, {"G", "c"},
	} {
		m.keySessions(step.key)
		if got := selName(m); got != step.want {
			t.Fatalf("after %q: %q (sel %d), want %s", step.key, got, m.sel, step.want)
		}
	}
}

// Wrapping follows the filtered list, not the whole one.
func TestSessionsCursorWrapsFiltered(t *testing.T) {
	m := wrapModel(t, "api-1", "web-1", "api-2")
	m.filter = "api"
	m.sel = 1 // api-2
	m.keySessions("j")
	m.keySessions("j")
	if s := m.current(); s == nil || s.Slug != "api-1" {
		t.Fatalf("sel %d, want api-1", m.sel)
	}
}

func TestSessionsCursorWrapsTiny(t *testing.T) {
	m := wrapModel(t) // only the tapi row
	for _, k := range []string{"j", "k", "down", "up", "G", "g"} {
		m.keySessions(k)
		if !m.onTapiRow() || m.current() != nil {
			t.Fatalf("empty list after %q: sel %d", k, m.sel)
		}
	}
	m = wrapModel(t, "only")
	for i, k := range []string{"j", "k", "down", "up"} {
		m.keySessions(k)
		want := []string{"tapi", "only", "tapi", "only"}[i]
		if got := selName(m); got != want {
			t.Fatalf("single session after %q: %q, want %s", k, got, want)
		}
	}
}
