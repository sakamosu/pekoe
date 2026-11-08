package theme

// Confetti is what tapi throws over the toast when a PR is merged
// (docs/ui-guidelines.md「置字グラフィック」): a few kinds of sparkle, each
// a ladder from its brightest glyph to its faintest. A sparkle twinkles by
// stepping down its ladder, going dark, and flashing back to the top.
//
// Every glyph is one cell and never East Asian Ambiguous: fonts that draw
// ambiguous glyphs wide (Moralerspace JPDOC) would push the border out of
// line. That rules out ☆ ★ · ˚ ° and the Nerd Font stars (private use is
// ambiguous too), so nerd shares the unicode set. All of these are in
// Moralerspace JPDOC and Menlo, so no fallback font draws them at another
// size. ascii stays ASCII.
func Confetti(ic IconSet) [][]string {
	if ic.Name == "ascii" {
		return [][]string{
			{"*", "+", "."},
			{"+", "'", "."},
		}
	}
	return [][]string{
		{"✦", "✧", "⋆", "⋅"}, // four-pointed sparkle
		{"*", "∗", "⁺", "⋅"}, // asterisk
		{"⁺", "∙", "⋅"},      // a glint
	}
}
