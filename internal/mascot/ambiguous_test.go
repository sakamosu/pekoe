package mascot

import (
	"sort"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/glyphguard"
)

// tapi's face is state display: the eyes and the 1-cell symbol may not draw
// two cells wide (docs/ambiguous-width.md「要点」1). Covers tapi.json (the
// states, the reactions, the sprites) and the glyphs mascot.go and pr.go
// put in, as the default glyph setting (unicode) draws them. The outline
// is block elements and sextants, which glyphguard allows. The narrow
// setting's 🞄 is Neutral and goes away in 段階 3.
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

// knownFaceViolations shrinks in docs/ambiguous-width.md「実装の段階」3
// (マスコットの置き換え). ci_failed's ╥ is ambiguous too, but box drawing,
// which glyphguard allows like the rest of U+2500–257F.
var knownFaceViolations = []glyphguard.Known{
	{Where: "states.idle.eyes", Rune: '•', Stage: "段階 3"},
	{Where: "states.idle.cell", Rune: '•', Stage: "段階 3"},
	{Where: "states.work.eyes", Rune: '●', Stage: "段階 3"},
	{Where: "states.work.cell", Rune: '●', Stage: "段階 3"},
	{Where: "states.done.cell", Rune: '●', Stage: "段階 3"},
	{Where: "Peek", Rune: '˙', Stage: "段階 3"},
	{Where: "Pearl", Rune: '●', Stage: "段階 3"},
}
