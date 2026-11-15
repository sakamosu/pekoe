package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// iconRunes are the glyphs of an icon set that must have a space after
// them (docs/ambiguous-width.md「決めること」(3) a): a nerd font icon's
// glyph is about 1.5 cells wide and draws into the cell that follows, and a
// unicode icon the terminal's font lacks comes from a fallback font that may
// be as wide. Left out: the separator and the powerline caps (meant to touch
// what is next to them), the spinner (braille, fits in every font) and
// ASCII.
func iconRunes(ic theme.IconSet) map[rune]string {
	out := map[rune]string{}
	v := reflect.ValueOf(ic)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		switch name {
		case "Name", "Sep", "CapL", "CapR", "Spinner":
			continue
		}
		s, ok := v.Field(i).Interface().(string)
		if !ok {
			continue
		}
		for _, r := range s {
			if r >= 0x80 {
				out[r] = name
			}
		}
	}
	return out
}

// spaceAfterIcons reports each icon in the rendered lines that is followed
// by anything but a space (the end of a line counts as a space: the
// terminal leaves the cell blank).
func spaceAfterIcons(t *testing.T, ic theme.IconSet, what string, lines []string) {
	t.Helper()
	icons := iconRunes(ic)
	for _, l := range lines {
		r := []rune(strings.TrimRight(ansi.Strip(l), " "))
		for i, c := range r {
			name, ok := icons[c]
			if !ok || i+1 == len(r) || r[i+1] == ' ' {
				continue
			}
			t.Errorf("%s/%s: %s %q is followed by %q, not a space: %q", ic.Name, what, name, c, r[i+1], string(r))
		}
	}
}

// Every state icon in the session list and the screens around it has a
// space after it (theme.Prefix), in both sets that draw icons.
func TestIconsHaveSpaceAfter(t *testing.T) {
	for _, set := range []string{"nerd", "unicode"} {
		m := forecastModel(t, "truecolor")
		m.ic = theme.Icons(set)
		now := time.Now()
		pr := func(st string, fail, pending int, decision string) *state.PRStatus {
			return &state.PRStatus{State: st, Fail: fail, Pending: pending, ReviewDecision: decision, At: now}
		}
		more := []*state.Session{
			{Slug: "mk", Branch: "mk", RepoID: "r", State: state.Creating, StateSince: now},
			{Slug: "fl", Branch: "fl", RepoID: "r", State: state.Failed, StateSince: now, Error: "boom"},
			{Slug: "t1", Branch: "t1", RepoID: "r", State: state.Idle, StateSince: now, Test: state.Test{Status: "pass"}, Diffstat: state.Stat{Added: 3, Deleted: 1}, Unread: 2},
			{Slug: "t2", Branch: "t2", RepoID: "r", State: state.Idle, StateSince: now, Test: state.Test{Status: "fail"}, Diffstat: state.Stat{Added: 3}},
			{Slug: "t3", Branch: "t3", RepoID: "r", State: state.Running, StateSince: now, Test: state.Test{Status: "running"}, Diffstat: state.Stat{Added: 3}},
			{Slug: "t4", Branch: "t4", RepoID: "r", State: state.Idle, StateSince: now, Test: state.Test{Status: "blocked"}, Diffstat: state.Stat{Added: 3}},
			{Slug: "p1", Branch: "p1", RepoID: "r", State: state.Idle, StateSince: now, PR: 1, PRStatus: pr("OPEN", 0, 2, "")},
			{Slug: "p2", Branch: "p2", RepoID: "r", State: state.Idle, StateSince: now, PR: 2, PRStatus: pr("OPEN", 1, 0, "")},
			{Slug: "p3", Branch: "p3", RepoID: "r", State: state.Idle, StateSince: now, PR: 3, PRStatus: pr("OPEN", 0, 0, "CHANGES_REQUESTED")},
			{Slug: "p4", Branch: "p4", RepoID: "r", State: state.Landed, StateSince: now, PR: 4, PRStatus: pr("MERGED", 0, 0, "")},
			{Slug: "p5", Branch: "p5", RepoID: "r", State: state.Idle, StateSince: now, PR: 5, PRStatus: pr("CLOSED", 0, 0, "")},
		}
		m.sessions = append(m.sessions, more...)
		for _, cw := range []int{60, 100} {
			var lines []string
			for _, s := range m.sessions {
				lines = append(lines, m.sessionLine(s, cw, now))
			}
			spaceAfterIcons(t, m.ic, "session list", lines)
		}
		for _, s := range m.sessions {
			spaceAfterIcons(t, m.ic, "list "+s.Slug, []string{theme.SessionLine(m.th, m.ic, s, now, 0)})
		}
		spaceAfterIcons(t, m.ic, "view", strings.Split(m.View(), "\n"))
	}
}
