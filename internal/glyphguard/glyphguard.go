// Package glyphguard checks that the default glyph tables for state display
// (icon sets, the unread mark, tapi's face …) stay narrow in every terminal
// and font (docs/ambiguous-width.md, issue #85). It is imported by tests
// only, so golang.org/x/text/width stays out of the binary.
//
// A glyph is bad when it is East Asian Ambiguous (the terminal may count it
// as two cells: path A) or listed in WideInFonts (Neutral, but a font we
// measured draws it wider than a cell: path B). Allowed regardless:
//   - ASCII
//   - box drawing U+2500–257F and block elements U+2580–259F: the major
//     terminals draw them themselves
//   - private use (nerd font icons): drawn using the space that follows
//
// Bad glyphs that are still in use sit in each package's known list with
// the stage of docs/ambiguous-width.md that removes them. A bad glyph not in
// the list fails, and so does a list entry nothing uses any more.
package glyphguard

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"golang.org/x/text/width"
)

// Use is one glyph (or a short string of them) a default table puts on
// screen, and where it comes from ("unicode.Run", "states.work.eyes").
type Use struct {
	Where, Glyph string
}

// Known is a bad glyph still in use at Where, removed in Stage.
type Known struct {
	Where string
	Rune  rune
	Stage string // "段階 2" …, with what replaces it when decided
}

// WideInFonts are Neutral glyphs measured wider than one cell in a font
// listed in docs/ambiguous-width.md「2. 代わりの文字の候補」(the right edge
// of the glyph, in cells; - is "not in the font"). EAW alone lets them
// through. Glyphs checked and allowed on purpose are in AllowedNarrow.
var WideInFonts = map[rune]string{
	'⚠': "JPDOC 1.6, HackGen35 Console 1.6",
	'▱': "JPDOC 1.5, Noto Sans Mono 1.8",
	'▰': "Noto Sans Mono 1.8 (JPDOC: -)",
	'⊚': "Noto Sans Mono 1.6",
	'✕': "HackGen35 Console 1.3",
	'✔': "HackGen35 Console 1.4–1.5",
	'✘': "HackGen35 Console 1.4–1.5",
}

// AllowedNarrow are Neutral glyphs that overflow in one measured font but
// are kept: no candidate fits more fonts, and they always stand before a
// space (theme.Prefix), which the overflow uses. Not consulted by Check;
// it records the decision so the two lists stay apart (TestLists).
var AllowedNarrow = map[rune]string{
	'✓': "HackGen35 Console 1.4; fits in the other 7 measured fonts that have it (Monaco, Noto: -)",
	'✗': "HackGen35 Console 1.4; fits in the other 6 measured fonts that have it (Monaco, Cascadia, Noto: -)",
}

// Bad reports why r must not be in a default state-display table, or "".
func Bad(r rune) string {
	switch {
	case r < 0x80:
		return ""
	case r >= 0x2500 && r <= 0x259F: // box drawing, block elements
		return ""
	case r >= 0xE000 && r <= 0xF8FF, r >= 0xF0000 && r <= 0xFFFFD, r >= 0x100000 && r <= 0x10FFFD:
		return "" // private use: nerd font
	}
	if why, ok := WideInFonts[r]; ok {
		return "wide in fonts: " + why
	}
	if width.LookupRune(r).Kind() == width.EastAsianAmbiguous {
		return "East Asian Ambiguous"
	}
	return ""
}

// Check fails t for each bad glyph in uses that known does not list, and
// for each known entry that uses no longer has.
func Check(t testing.TB, uses []Use, known []Known) {
	t.Helper()
	type key struct {
		where string
		r     rune
	}
	want := map[key]bool{}
	for _, k := range known {
		kk := key{k.Where, k.Rune}
		if want[kk] {
			t.Errorf("known: %s %q listed twice", k.Where, k.Rune)
		}
		if Bad(k.Rune) == "" {
			t.Errorf("known: %s %q (U+%04X) is not bad; drop it", k.Where, k.Rune, k.Rune)
		}
		if k.Stage == "" {
			t.Errorf("known: %s %q has no stage", k.Where, k.Rune)
		}
		want[kk] = true
	}
	seen := map[key]bool{}
	var unexpected []string
	for _, u := range uses {
		for _, r := range u.Glyph {
			why := Bad(r)
			if why == "" {
				continue
			}
			kk := key{u.Where, r}
			if seen[kk] {
				continue
			}
			seen[kk] = true
			if !want[kk] {
				unexpected = append(unexpected, fmt.Sprintf("%s: %q (U+%04X) %s", u.Where, r, r, why))
			}
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Errorf("glyphs that may draw two cells wide (docs/ambiguous-width.md); pick a Neutral glyph from its table:\n  %s",
			strings.Join(unexpected, "\n  "))
	}
	for _, k := range known {
		if !seen[key{k.Where, k.Rune}] {
			t.Errorf("known: %s %q (%s) is no longer used; remove it from the list", k.Where, k.Rune, k.Stage)
		}
	}
}
