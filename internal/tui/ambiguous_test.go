package tui

import (
	"testing"

	"pekoe/internal/glyphguard"
)

// State display the TUI draws itself, outside the icon sets
// (internal/theme) and tapi's face (internal/mascot): no glyph may draw two
// cells wide (docs/ambiguous-width.md「要点」1). Body text (· … → in
// sentences, the markdown bullet) is not state display and is left to
// 段階 4's wide table.
func TestMarksNotAmbiguous(t *testing.T) {
	uses := []glyphguard.Use{
		{Where: "unreadMark", Glyph: unreadMark},
		{Where: "hunkOpenMark", Glyph: hunkOpenMark},
		{Where: "logoDot", Glyph: logoDot},
	}
	for _, g := range workBubbles {
		uses = append(uses, glyphguard.Use{Where: "workBubbles", Glyph: g})
	}
	glyphguard.Check(t, uses, knownMarkViolations)
}

// The dashboard's bars (the stage bar, the week's merges) draw every
// cell from barFull and barEmpty: neither may draw wider than a cell in
// any font, as ▰ ▱ did in Moralerspace JPDOC (▱ 1.5 cells, ▰ from a
// fallback font), which made the days' cells overlap.
func TestDashBarNotWide(t *testing.T) {
	glyphguard.Check(t, []glyphguard.Use{
		{Where: "barFull", Glyph: barFull},
		{Where: "barEmpty", Glyph: barEmpty},
	}, nil)
	if w(barFull) != 1 || w(barEmpty) != 1 || barFull == barEmpty {
		t.Errorf("bar cells %q %q: one cell each, and told apart without color", barFull, barEmpty)
	}
}

// knownMarkViolations shrinks as docs/ambiguous-width.md「実装の段階」
// replaces them.
var knownMarkViolations = []glyphguard.Known{
	// the work face's bubbles: not in 段階 3's list yet, decided there
	{Where: "workBubbles", Rune: '·', Stage: "段階 3"},
	{Where: "workBubbles", Rune: '°', Stage: "段階 3"},
}
