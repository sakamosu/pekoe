package theme

// Confetti is the scatter of glyphs tapi throws over the toast when a PR
// is merged (docs/ui-guidelines.md「置字グラフィック」). Every glyph is one
// cell: nf-fa-star / nf-fa-star_o for nerd, neutral-width stars for
// unicode, and plain punctuation for ascii.
func Confetti(ic IconSet) []string {
	switch ic.Name {
	case "ascii":
		return []string{"*", "+", "'", "."}
	case "unicode":
		return []string{"✦", "✧", "⋆", "⁺"}
	}
	return []string{"", "", "✦", "⁺"}
}
