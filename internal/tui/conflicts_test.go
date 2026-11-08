package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/config"
	"pekoe/internal/state"
)

func forecastModel(t *testing.T, color string) *model {
	t.Helper()
	t.Setenv("PEKOE_ICONS", "unicode")
	m := tapiModel(t, color, config.Mascot{Glyphs: "ascii"})
	m.width, m.height = 120, 24
	now := time.Now()
	m.sessions = []*state.Session{
		{Slug: "feat-a", Branch: "feat/a-very-long-branch-name-that-must-give-way", RepoID: "r", State: state.Running, StateSince: now, PR: 42,
			Overlaps: []state.Overlap{
				{Slug: "feat-b", Files: []string{"src/api.ts", "README.md"}, Conflict: true, ConflictFiles: []string{"src/api.ts"}},
				{Slug: "feat-c", Files: []string{"README.md"}},
			}},
		{Slug: "feat-b", Branch: "feat/b", RepoID: "r", State: state.Waiting, WaitKind: "permission", StateSince: now,
			PendingTool: &state.Tool{Name: "Edit", Input: "src/api.ts", Touches: "src/api.ts", AlsoIn: []string{"feat-a"}},
			Overlaps:    []state.Overlap{{Slug: "feat-a", Files: []string{"src/api.ts"}, Conflict: true, ConflictFiles: []string{"src/api.ts"}}}},
		{Slug: "feat-c", Branch: "feat/c", RepoID: "r", State: state.Idle, StateSince: now,
			Overlaps: []state.Overlap{{Slug: "feat-a", Files: []string{"README.md"}}}},
		{Slug: "docs", Branch: "docs", RepoID: "r", State: state.Done, StateSince: now},
	}
	return m
}

func lineWith(t *testing.T, out, sub string) string {
	t.Helper()
	for _, l := range strings.Split(ansi.Strip(out), "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	t.Fatalf("no line with %q in:\n%s", sub, ansi.Strip(out))
	return ""
}

func TestSessionListConflictTag(t *testing.T) {
	m := forecastModel(t, "none")
	cw := leftWidth(m.width) - 4
	now := time.Now()
	a := ansi.Strip(m.sessionLine(m.sessions[0], cw, now))
	// the tags survive; the long branch name gives way
	if !strings.Contains(a, "#42 ⚠2") || !strings.Contains(a, "…") {
		t.Fatalf("feat-a row: %q", a)
	}
	if w(a) != cw {
		t.Fatalf("row width %d, want %d: %q", w(a), cw, a)
	}
	if c := ansi.Strip(m.sessionLine(m.sessions[2], cw, now)); !strings.Contains(c, "feat/c ⚠1") {
		t.Fatalf("feat-c row: %q", c)
	}
	if d := ansi.Strip(m.sessionLine(m.sessions[3], cw, now)); strings.Contains(d, "⚠") {
		t.Fatalf("no forecast, no tag: %q", d)
	}
	// colors: err when any peer conflicts, warn for overlap only
	mc := forecastModel(t, "truecolor")
	if got, want := mc.conflictTag(mc.sessions[0]), mc.th.FG("err", "⚠2"); got != want {
		t.Fatalf("conflict tag %q, want err %q", got, want)
	}
	if got, want := mc.conflictTag(mc.sessions[2]), mc.th.FG("warn", "⚠1"); got != want {
		t.Fatalf("overlap tag %q, want warn %q", got, want)
	}
	// ascii icons
	t.Setenv("PEKOE_ICONS", "ascii")
	ma := tapiModel(t, "none", config.Mascot{Glyphs: "ascii"})
	if got := ma.conflictTag(m.sessions[0]); got != "!2" {
		t.Fatalf("ascii tag %q", got)
	}
}

func TestPermissionModalAlsoChanged(t *testing.T) {
	m := forecastModel(t, "none")
	out := strings.Join(newPermissionModal(m, m.sessions[1]).view(), "\n")
	l := lineWith(t, out, "also changed in")
	if !strings.Contains(l, "⚠ also changed in feat-a") {
		t.Fatalf("warning line: %q", l)
	}
	// nothing to warn about: no line
	m.sessions[1].PendingTool.AlsoIn = nil
	if out := ansi.Strip(strings.Join(newPermissionModal(m, m.sessions[1]).view(), "\n")); strings.Contains(out, "also changed") {
		t.Fatalf("unexpected warning:\n%s", out)
	}
}

func TestChangesShowPeersAndLandForecast(t *testing.T) {
	for _, color := range []string{"truecolor", "ansi", "none"} {
		m := forecastModel(t, color)
		m.sel = 0
		m.changes = []change{{Status: "M", Path: "src/api.ts"}, {Status: "M", Path: "README.md"}, {Status: "A", Path: "src/new.ts"}}
		m.changesOf = "feat-a"
		m.focus = pThird
		out := m.View()
		if l := lineWith(t, out, "src/api.ts"); !strings.Contains(l, "⚠ feat-b") {
			t.Fatalf("%s: api.ts row: %q", color, l)
		}
		if l := lineWith(t, out, "README.md"); !strings.Contains(l, "⚠ feat-b,feat-c") {
			t.Fatalf("%s: README row: %q", color, l)
		}
		if l := lineWith(t, out, "src/new.ts"); strings.Contains(l, "⚠") {
			t.Fatalf("%s: new.ts row: %q", color, l)
		}
		if l := lineWith(t, out, "feat-b conflict"); !strings.Contains(l, "⚠ feat-c overlap") {
			t.Fatalf("%s: footer: %q", color, l)
		}

		// land: the forecast sits above pekoe's question
		m.sessionAction("l", m.sessions[0])
		out = m.View()
		lines := strings.Split(out, "\n")
		for i, l := range lines {
			if w(l) != m.width {
				t.Fatalf("%s: line %d width %d", color, i, w(l))
			}
		}
		ask := -1
		for i, l := range lines {
			if strings.Contains(ansi.Strip(l), "land feat/a") {
				ask = i
			}
		}
		if ask < 2 {
			t.Fatalf("%s: ask line missing:\n%s", color, ansi.Strip(out))
		}
		if l := ansi.Strip(lines[ask-2]); !strings.Contains(l, "⚠ feat-b conflict · src/api.ts") {
			t.Fatalf("%s: conflict note: %q", color, l)
		}
		if l := ansi.Strip(lines[ask-1]); !strings.Contains(l, "⚠ feat-c overlap, auto-merges · README.md") {
			t.Fatalf("%s: overlap note: %q", color, l)
		}
		m.modal = nil
	}
	// a clean land says so
	m := forecastModel(t, "none")
	notes := m.landNotes(m.sessions[3])
	if len(notes) != 1 || notes[0].detail != "no overlap with 3 open sessions" {
		t.Fatalf("clean notes: %+v", notes)
	}
}
