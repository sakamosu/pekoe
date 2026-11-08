package theme

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/width"
)

// Every confetti glyph is one cell and never East Asian Ambiguous (fonts
// like Moralerspace JPDOC draw those wide and the border breaks); ascii
// stays ASCII.
func TestConfettiGlyphs(t *testing.T) {
	for _, name := range []string{"nerd", "unicode", "ascii"} {
		set := Confetti(Icons(name))
		if len(set) < 2 {
			t.Fatalf("%s: %v", name, set)
		}
		for _, ladder := range set {
			if len(ladder) < 2 {
				t.Errorf("%s: ladder %v has nothing to twinkle down to", name, ladder)
			}
			for _, g := range ladder {
				if ansi.StringWidth(g) != 1 || len([]rune(g)) != 1 {
					t.Errorf("%s: %q is not one cell", name, g)
				}
				for _, r := range g {
					switch width.LookupRune(r).Kind() {
					case width.Neutral, width.EastAsianNarrow:
					default:
						t.Errorf("%s: %q (U+%04X) is not neutral or narrow width", name, g, r)
					}
					if name == "ascii" && r > 127 {
						t.Errorf("ascii: %q", g)
					}
				}
			}
		}
	}
}
