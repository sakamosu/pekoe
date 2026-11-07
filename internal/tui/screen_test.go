package tui

import (
	"strings"
	"testing"

	"boba/internal/engine"
	"boba/internal/state"
)

func screenModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("BOBA_COLOR", "truecolor")
	m := newModel(nil, "")
	m.width, m.height = 222, 60
	m.sessions = []*state.Session{{Slug: "a", Branch: "a", State: state.Running}}
	m.sel = 0
	return m
}

// Claude Code paints full-width backgrounds (header, prompt, headings) with
// "set bg + erase line"; capture-pane -e returns that as a trailing open SGR.
// It must not leak past the text into the padding and the right border.
func TestScreenRowsCloseTrailingSGR(t *testing.T) {
	m := screenModel(t)
	const bg = "\x1b[48;5;237m"
	for _, line := range []string{
		"  \x1b[1mHeading\x1b[0m" + bg,    // ASCII only
		"  \x1b[1m症状\x1b[0m" + bg,         // wide chars
		bg + "> prompt text that is long", // bg set at the start, never reset
	} {
		m.screen = &engine.Screen{Width: 161, Height: 3, Lines: []string{line}}
		rows := m.screenRows(161, 3)
		box := m.box(165, 5, boxOpt{rows: rows, bottom: true})
		var got string
		for _, l := range box {
			if strings.Contains(l, bg) {
				got = l
			}
		}
		if got == "" {
			t.Fatalf("%q: line not rendered", line)
		}
		tail := got[strings.LastIndex(got, bg)+len(bg):]
		border := strings.LastIndex(tail, "│")
		if border < 0 {
			t.Fatalf("%q: no right border in %q", line, got)
		}
		if !strings.Contains(tail[:border], "\x1b[0m") && !strings.Contains(tail[:border], "\x1b[49m") {
			t.Errorf("%q: background still open at the right border: %q", line, tail)
		}
	}
}
