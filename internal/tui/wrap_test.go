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

// j/k in the Sessions panel wrap around at the ends.
func TestSessionsCursorWraps(t *testing.T) {
	m := wrapModel(t, "a", "b", "c")
	m.sel = 0
	for _, step := range []struct {
		key  string
		want string
	}{
		{"j", "b"}, {"down", "c"}, {"j", "a"}, // bottom → top
		{"k", "c"}, {"up", "b"}, {"k", "a"}, {"up", "c"}, // top → bottom
	} {
		m.keySessions(step.key)
		if s := m.current(); s == nil || s.Slug != step.want {
			t.Fatalf("after %q: sel %d, want %s", step.key, m.sel, step.want)
		}
	}
}

// Wrapping follows the filtered list, not the whole one.
func TestSessionsCursorWrapsFiltered(t *testing.T) {
	m := wrapModel(t, "api-1", "web-1", "api-2")
	m.filter = "api"
	m.sel = 1 // api-2
	m.keySessions("j")
	if s := m.current(); s == nil || s.Slug != "api-1" {
		t.Fatalf("sel %d, want api-1", m.sel)
	}
}

func TestSessionsCursorWrapsTiny(t *testing.T) {
	m := wrapModel(t)
	for _, k := range []string{"j", "k", "down", "up"} {
		m.keySessions(k)
		if m.sel != 0 || m.current() != nil {
			t.Fatalf("empty list after %q: sel %d", k, m.sel)
		}
	}
	m = wrapModel(t, "only")
	for _, k := range []string{"j", "k", "down", "up"} {
		m.keySessions(k)
		if s := m.current(); m.sel != 0 || s == nil || s.Slug != "only" {
			t.Fatalf("single session after %q: sel %d", k, m.sel)
		}
	}
}
