package tui

import (
	"strings"
	"testing"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

func screenModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("PEKOE_COLOR", "truecolor")
	m := newModel(nil, "")
	m.width, m.height = 222, 60
	m.sessions = []*state.Session{{Slug: "a", Branch: "a", State: state.Running}}
	m.sel = 0
	return m
}

// A window spawned at a size the pane doesn't have (`pekoe new` from an agent,
// no terminal → 67x37) is resized to the pane once it is shown, once.
func TestFitScreenResizesMismatchedWindow(t *testing.T) {
	m := screenModel(t)
	aw, ah := m.agentSize()
	if aw != 161 || ah != 57 {
		t.Fatalf("agentSize = %dx%d, want 161x57", aw, ah)
	}
	small := &engine.Screen{Width: 67, Height: 37}
	if m.fitScreen("a", engine.WinAgent, small) == nil {
		t.Fatal("67x37 window in a 161x57 pane should be resized")
	}
	if m.fitScreen("a", engine.WinAgent, small) != nil {
		t.Fatal("the same mismatch must not be re-requested every frame")
	}
	if m.fitScreen("a", engine.WinAgent, &engine.Screen{Width: aw, Height: ah}) != nil {
		t.Fatal("a window that already fits needs no resize")
	}
	if m.fitScreen("a", engine.WinAgent, &engine.Screen{Width: 67, Height: 37, Dead: true}) != nil {
		t.Fatal("a dead pane is left alone")
	}
	m.width = 180 // the terminal changed: the new pane size is asked for again
	if m.fitScreen("a", engine.WinAgent, small) == nil {
		t.Fatal("a new pane size should be requested")
	}
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
