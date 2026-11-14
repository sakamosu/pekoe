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

// knownMarkViolations shrinks as docs/ambiguous-width.md「実装の段階」
// replaces them.
var knownMarkViolations = []glyphguard.Known{
	// • fits in every measured font (path B is fine), so 段階 2 may leave
	// it to 段階 4's wide table
	{Where: "unreadMark", Rune: '•', Stage: "段階 2 か 4"},
	{Where: "hunkOpenMark", Rune: '○', Stage: "段階 2"},
	{Where: "logoDot", Rune: '●', Stage: "段階 2"},
	// the work face's bubbles: not in 段階 3's list yet, decided there
	{Where: "workBubbles", Rune: '·', Stage: "段階 3"},
	{Where: "workBubbles", Rune: '°', Stage: "段階 3"},
}
