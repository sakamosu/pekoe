package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/config"
	"pekoe/internal/state"
)

// The row says what a session still comes after; the footer spells out
// each dependency's state for the selected one.
func TestSessionAfterShown(t *testing.T) {
	t.Setenv("PEKOE_ICONS", "unicode")
	m := tapiModel(t, "none", config.Mascot{Glyphs: "ascii"})
	m.width, m.height = 120, 24
	now := time.Now()
	m.sessions = []*state.Session{
		{Slug: "feat-a", Branch: "feat/a", RepoID: "r", State: state.Landed, StateSince: now},
		{Slug: "feat-b", Branch: "feat/b", RepoID: "r", State: state.Running, StateSince: now},
		{Slug: "feat-m", Branch: "feat/m", RepoID: "r", State: state.Idle, StateSince: now, PRStatus: &state.PRStatus{State: "MERGED"}},
		{Slug: "feat-c", Branch: "feat/c", RepoID: "r", State: state.Idle, StateSince: now, After: []string{"feat-a", "feat-b", "feat-m", "feat-gone"}},
		{Slug: "feat-d", Branch: "feat/d", RepoID: "r", State: state.Idle, StateSince: now, After: []string{"feat-a", "feat-m"}},
	}
	cw := m.leftWidth() - 4
	if c := ansi.Strip(m.sessionLine(m.sessions[3], cw, now)); !strings.Contains(c, "feat/c after") || w(c) != cw {
		t.Fatalf("feat-c row: %q", c)
	}
	// the slug when there is room, never at the cost of the branch name
	if a := ansi.Strip(m.afterTag(m.sessions[3], 40)); a != "after feat-b" {
		t.Fatalf("wide tag: %q", a)
	}
	if a := ansi.Strip(m.afterTag(m.sessions[3], 12)); a != "after" {
		t.Fatalf("narrow tag: %q", a)
	}
	if d := ansi.Strip(m.sessionLine(m.sessions[4], cw, now)); strings.Contains(d, "after") {
		t.Fatalf("feat-d comes after nothing pending: %q", d)
	}
	m.sel = 3
	_, footer := m.sessionRows(cw)
	if f := ansi.Strip(footer); !strings.Contains(f, "after feat-a landed · feat-b run · feat-m merged · feat-gone gone") {
		t.Fatalf("footer: %q", f)
	}
}

// A session whose base pekoed moved off a merged branch says so in the
// footer when selected, with or without --after.
func TestSessionBaseMovedShown(t *testing.T) {
	t.Setenv("PEKOE_ICONS", "unicode")
	m := tapiModel(t, "none", config.Mascot{Glyphs: "ascii"})
	m.width, m.height = 120, 24
	now := time.Now()
	m.sessions = []*state.Session{
		{Slug: "feat-p", Branch: "feat/p", RepoID: "r", State: state.Landed, StateSince: now},
		{Slug: "feat-c", Branch: "feat/c", RepoID: "r", State: state.Idle, StateSince: now, Base: "main", BaseFrom: "feat/p"},
		{Slug: "feat-d", Branch: "feat/d", RepoID: "r", State: state.Idle, StateSince: now, Base: "main", BaseFrom: "feat/p", After: []string{"feat-p"}},
		{Slug: "feat-e", Branch: "feat/e", RepoID: "r", State: state.Idle, StateSince: now, Base: "main"},
	}
	cw := m.leftWidth() - 4
	for i, want := range []string{"base main (was feat/p, merged)", "after feat-p landed    base main (was feat/p, merged)", ""} {
		m.sel = i + 1
		if cur := m.current(); cur == nil || cur != m.sessions[i+1] {
			t.Fatalf("sel %d is %v, not %s", m.sel, cur, m.sessions[i+1].Slug)
		}
		_, footer := m.sessionRows(cw)
		f := ansi.Strip(footer)
		if want == "" && strings.Contains(f, "was") || want != "" && !strings.Contains(f, want) {
			t.Fatalf("footer of %s: %q, want %q", m.sessions[i+1].Slug, f, want)
		}
	}
}
