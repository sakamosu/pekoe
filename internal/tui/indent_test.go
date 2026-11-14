package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// col is the display column where sub starts in line, -1 when it is not there.
func col(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(line[:i])
}

// The rows under a heading (the tapi row, the session rows) sit rowIndent
// right of it: the row's icon lines up with the heading's name, so a
// repository reads as holding its sessions. The room comes out of the name
// column; the state and age stay, down to a narrow panel.
func TestSessionRowsIndentUnderHeadings(t *testing.T) {
	for _, cw := range []int{60, 40, 30, 26} {
		m := repoModel(t, "none", 180)
		m.sessions[0].Branch = "chore/very-long-branch-name"
		m.sessions[0].PR = 5
		rows, sels, _ := m.sessionRowsSel(cw)
		var dump []string
		for i, r := range rows {
			line := ansi.Strip(r.Text)
			dump = append(dump, line)
			if ww := ansi.StringWidth(r.Text); ww > cw {
				t.Errorf("cw %d: row %d wider than the panel (%d): %q", cw, i, ww, line)
			}
			switch sels[i] {
			case rowNone: // a heading: mark sp name, its name at column 2 as before
				if f := strings.Fields(line); col(line, f[len(f)-2]+" ") != 2 { // the name, before the rule
					t.Errorf("cw %d: heading %q does not have its name at 2", cw, line)
				}
			default: // the tapi row or a session row: indent icon sp name
				if !strings.HasPrefix(line, strings.Repeat(" ", rowIndent)) {
					t.Errorf("cw %d: row %q is not indented", cw, line)
				}
				if name := strings.Fields(line)[1]; col(line, name) != rowIndent+2 { // after the icon
					t.Errorf("cw %d: row %q has its name at %d, want %d", cw, line, col(line, name), rowIndent+2)
				}
			}
		}
		// headings and rows in the order the panel shows them
		want := []string{"orchestrator", "tapi", "api", "cho", "api-2", "web", "web-1"}
		if len(rows) != len(want) {
			t.Fatalf("cw %d: rows %q", cw, dump)
		}
		for i, sub := range want {
			if c := col(dump[i], sub); c < 0 {
				t.Errorf("cw %d: row %d %q has no %q", cw, i, dump[i], sub)
			}
		}
		// the session rows keep their state and age columns
		for i, word := range map[int]string{3: "run", 4: "idle", 6: "wait"} {
			if !strings.Contains(dump[i], word) {
				t.Errorf("cw %d: %q lost its state %q", cw, dump[i], word)
			}
		}
		if !strings.Contains(dump[3], "3m") || !strings.Contains(dump[6], "2m") {
			t.Errorf("cw %d: the age column went missing: %q / %q", cw, dump[3], dump[6])
		}
		if !strings.Contains(dump[3], "#5") {
			t.Errorf("cw %d: the PR tag went missing: %q", cw, dump[3])
		}
		t.Logf("cw %d:\n%s", cw, strings.Join(dump, "\n"))
	}
}

// In the whole screen the session rows of [1] start two columns right of
// their repository's heading name, and a click still selects the row.
func TestSessionRowsIndentOnScreen(t *testing.T) {
	m := repoModel(t, "none", 120)
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	lw := sessionsWidth(m)
	hi, ri := lineIndex(lines, "api ─", lw), lineIndex(lines, "api-1", lw)
	if hi < 0 || ri < 0 {
		t.Fatalf("no heading or row:\n%s", strings.Join(lines, "\n"))
	}
	hc := col(lines[hi], "api ─")
	rc := col(lines[ri], "api-1")
	// heading: mark sp name; row: indent icon sp name
	if rc-hc != rowIndent {
		t.Errorf("row name at %d, heading name at %d: want %d apart\n%s\n%s", rc, hc, rowIndent, lines[hi], lines[ri])
	}
	ic := col(lines[ri], "api-1") - 2 // the icon
	if ic != hc {
		t.Errorf("row icon at %d, heading name at %d", ic, hc)
	}
	if sel, ok := m.sessionRowAt(hc, ri); !ok || m.visible()[sel].Slug != "api-1" {
		t.Errorf("a click on the api-1 row selects %d %v", sel, ok)
	}
	if _, ok := m.sessionRowAt(hc, hi); ok {
		t.Errorf("a click on the heading selects a row")
	}
	t.Logf("\n%s", strings.Join(lines[:12], "\n"))
}
