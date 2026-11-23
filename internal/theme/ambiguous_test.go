package theme

import (
	"reflect"
	"testing"

	"pekoe/internal/ambiguous"
	"pekoe/internal/glyphguard"
)

// iconUses lists every glyph of an icon set as "<set>.<field>".
func iconUses(ic IconSet) []glyphguard.Use {
	var uses []glyphguard.Use
	v := reflect.ValueOf(ic)
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if f.Name == "Name" {
			continue
		}
		where := ic.Name + "." + f.Name
		switch x := v.Field(i).Interface().(type) {
		case string:
			uses = append(uses, glyphguard.Use{Where: where, Glyph: x})
		case []string:
			for _, g := range x {
				uses = append(uses, glyphguard.Use{Where: where, Glyph: g})
			}
		default:
			panic("IconSet." + f.Name + ": add its type to iconUses")
		}
	}
	return uses
}

// The icon sets are state display: no glyph may draw two cells wide
// (docs/ambiguous-width.md「要点」1, 3). The nerd set's private-use icons
// and the │ separator are allowed by glyphguard.
func TestIconsNotAmbiguous(t *testing.T) {
	var uses []glyphguard.Use
	for _, name := range []string{"nerd", "unicode", "ascii"} {
		uses = append(uses, iconUses(Icons(name))...)
	}
	uses = append(uses, glyphguard.Use{Where: "TestRunningMark", Glyph: TestRunningMark})
	glyphguard.Check(t, uses, knownIconViolations)
}

// knownIconViolations: 段階 2 of docs/ambiguous-width.md「実装の段階」
// replaced them all (the glyphs picked are in「段階 2 で決めたこと」). A new
// one goes here only with the stage that removes it.
var knownIconViolations = []glyphguard.Known{}

// With the wide table ([theme] ambiguous = "wide", or measured) every icon
// set falls to ascii, nerd included: its private-use icons and │ are
// ambiguous-width too (docs/ambiguous-width.md「決めること」(3) b).
func TestIconsWide(t *testing.T) {
	defer ambiguous.SetWide(ambiguous.IsWide())
	t.Setenv("PEKOE_ICONS", "nerd")
	ambiguous.SetWide(true)
	for _, cfg := range []string{"", "nerd", "unicode", "ascii"} {
		name := IconsName(cfg)
		if name != "ascii" {
			t.Errorf("IconsName(%q) = %q with the wide table, want ascii", cfg, name)
		}
		uses := append(iconUses(Icons(name)), glyphguard.Use{Where: "TestRunningMark", Glyph: TestRunningMark})
		glyphguard.CheckWide(t, uses)
	}
	ambiguous.SetWide(false)
	if got := IconsName("unicode"); got != "nerd" { // PEKOE_ICONS first
		t.Errorf("IconsName without the wide table = %q", got)
	}
}
