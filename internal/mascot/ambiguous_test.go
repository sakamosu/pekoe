package mascot

import (
	"sort"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/ambiguous"
	"pekoe/internal/glyphguard"
)

// tapi's face is state display: the eyes and the 1-cell symbol may not draw
// two cells wide (docs/ambiguous-width.md「要点」1). Covers tapi.json (the
// states, the reactions, the sprites) and the glyphs mascot.go and pr.go
// put in, as the default glyph setting (unicode) draws them. The outline
// is block elements and sextants, which glyphguard allows. The former
// narrow setting draws as unicode.
func TestFaceNotAmbiguous(t *testing.T) {
	var uses []glyphguard.Use
	add := func(where, g string) { uses = append(uses, glyphguard.Use{Where: where, Glyph: g}) }

	moods := make([]string, 0, len(spec.States))
	for m := range spec.States {
		moods = append(moods, string(m))
	}
	sort.Strings(moods)
	for _, m := range moods {
		d := spec.States[Mood(m)]
		add("states."+m+".eyes", d.Eyes)
		add("states."+m+".cell", d.Cell)
	}
	for _, e := range PREvents {
		d := spec.Reactions[e]
		add("reactions."+string(e)+".eyes", d.Eyes)
		add("reactions."+string(e)+".alt", d.Alt)
	}
	for name, sp := range spec.Sprites {
		for _, row := range sp {
			for _, c := range row {
				if c.G[0] != '$' {
					add("sprites."+name, c.G)
				}
			}
		}
	}

	// glyphs the code puts in (the default glyph setting is unicode), and
	// the outline of both shapes with the eyes left out
	for _, shape := range []string{"round", "block"} {
		r := New(Options{Shape: shape, Glyphs: "unicode"})
		for _, l := range append(r.Big(""), r.Line("")) {
			add("outline."+shape, ansi.Strip(l))
		}
	}
	r := New(Options{Glyphs: "unicode"})
	add("Peek", r.Peek())
	add("Blink", r.Blink())
	add("CloudyEyes", r.CloudyEyes())
	add("Pearl", r.Pearl())
	glyphguard.Check(t, uses, knownFaceViolations)
}

// knownFaceViolations is empty since docs/ambiguous-width.md「実装の段階」3
// (マスコットの置き換え): ● • ˙ became ◉ ∙ '. ci_failed's ╥ is ambiguous
// too, but box drawing, which glyphguard allows like the rest of
// U+2500–257F (terminals draw it themselves).
var knownFaceViolations []glyphguard.Known

// The wide table ([theme] ambiguous = "wide", or measured) draws tapi as
// ascii: nothing it puts on screen may be ambiguous-width, the outline's
// blocks and ci_failed's ╥ included (docs/ambiguous-width.md「段階 4 で決めたこと」).
func TestFaceWide(t *testing.T) {
	defer ambiguous.SetWide(ambiguous.IsWide())
	ambiguous.SetWide(true)
	glyphs := DetectGlyphs("auto", "nerd", true)
	if glyphs != "ascii" {
		t.Fatalf("auto with the wide table = %q", glyphs)
	}
	var uses []glyphguard.Use
	add := func(where, g string) { uses = append(uses, glyphguard.Use{Where: where, Glyph: ansi.Strip(g)}) }
	for _, shape := range []string{"round", "block"} {
		r := New(Options{Shape: shape, Glyphs: glyphs})
		for m := range spec.States {
			add("eyes."+string(m), r.Eyes(m))
			add("cell."+string(m), r.CellGlyph(m))
			for _, l := range r.Banner(r.Eyes(m), []string{"pekoe"}) {
				add("banner."+shape+"."+string(m), l)
			}
			add("line."+shape+"."+string(m), r.Line(r.Eyes(m)))
		}
		for _, e := range PREvents {
			add("reaction."+string(e), r.ReactionEyes(e, 0))
			add("reaction."+string(e)+".alt", r.ReactionEyes(e, 1))
		}
		add("Peek", r.Peek())
		add("Blink", r.Blink())
		add("CloudyEyes", r.CloudyEyes())
		add("Pearl", r.Pearl())
		for _, l := range r.FitBanner(r.Eyes(Work), []string{"a long line of text that gets clipped"}, 30, 0) {
			add("FitBanner", l)
		}
	}
	add("SummaryText", Summary{Mood: Idle, Total: 2, Cloudy: []string{"a", "b"}}.Speech())
	glyphguard.CheckWide(t, uses)
}
