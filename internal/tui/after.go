package tui

// Session.after (pekoe new --after, daemon/after.go): which sessions one
// comes after. Only shown, nothing waits on them: the row carries
// "after <slug>" while one of them is not through yet, and the footer of
// Sessions spells out each one's state for the selected session.

import (
	"fmt"
	"strings"

	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// afterThrough: a dependency no longer holds anything up when it landed or
// its PR was merged, or it is gone (removed with pekoe rm).
func afterThrough(d *state.Session) bool {
	return d == nil || d.State == state.Landed || (d.PRStatus != nil && d.PRStatus.State == "MERGED")
}

// afterPending are the dependencies of s that are not through yet.
func (m *model) afterPending(s *state.Session) []string {
	var out []string
	for _, slug := range s.After {
		if _, d := m.find(slug); !afterThrough(d) {
			out = append(out, slug)
		}
	}
	return out
}

// afterTag is the row's tag: the first dependency not through yet and how
// many more, or only "after" when that would leave the branch name fewer
// than 8 cells of room.
func (m *model) afterTag(s *state.Session, room int) string {
	p := m.afterPending(s)
	if len(p) == 0 {
		return ""
	}
	t := "after " + clip(p[0], 12)
	if len(p) > 1 {
		t += fmt.Sprintf(" +%d", len(p)-1)
	}
	if room-w(t)-1 < 8 {
		t = "after"
	}
	return m.th.FG(theme.Dim, t)
}

// afterFooter is the footer's part for the selected session: each
// dependency with its state (merged, gone).
func (m *model) afterFooter(s *state.Session) string {
	if s == nil || len(s.After) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.After))
	for _, slug := range s.After {
		_, d := m.find(slug)
		word := "gone"
		role := theme.Dim
		switch {
		case d == nil:
		case d.PRStatus != nil && d.PRStatus.State == "MERGED" && d.State != state.Landed:
			word, role = "merged", theme.Ok
		default:
			lk := theme.Look(m.ic, d.State, m.frame, m.g.AnimateOn())
			word, role = lk.Word, lk.Role
			if d.State == state.Landed {
				role = theme.Ok
			}
		}
		parts = append(parts, m.th.FG(theme.Text, slug)+" "+m.th.FG(role, word))
	}
	return m.th.FG(theme.Dim, "after ") + strings.Join(parts, m.th.FG(theme.Dim, " · "))
}
