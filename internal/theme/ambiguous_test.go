package theme

import (
	"reflect"
	"testing"

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
	glyphguard.Check(t, uses, knownIconViolations)
}

// knownIconViolations shrinks as docs/ambiguous-width.md「実装の段階」
// replaces them. Candidates are in its table 2 (the replacement each stage
// picks is decided there, not here).
var knownIconViolations = []glyphguard.Known{
	// 段階 2: 状態アイコンの置き換え
	{Where: "unicode.Sessions", Rune: '◆', Stage: "段階 2"},
	{Where: "unicode.Changes", Rune: '≡', Stage: "段階 2"},
	{Where: "unicode.Diff", Rune: '±', Stage: "段階 2"},
	{Where: "unicode.Browser", Rune: '◎', Stage: "段階 2"},
	{Where: "unicode.Run", Rune: '●', Stage: "段階 2"},
	{Where: "unicode.Wait", Rune: '●', Stage: "段階 2"},
	{Where: "unicode.Idle", Rune: '○', Stage: "段階 2"},
	{Where: "unicode.Warning", Rune: '⚠', Stage: "段階 2"},
	{Where: "unicode.Here", Rune: '▶', Stage: "段階 2"},
	{Where: "unicode.Unsent", Rune: '●', Stage: "段階 2"},
	{Where: "unicode.Pending", Rune: '…', Stage: "段階 2"},
	{Where: "unicode.Issues", Rune: '⊙', Stage: "段階 2"},
	{Where: "nerd.Here", Rune: '▶', Stage: "段階 2"},
	{Where: "nerd.Unsent", Rune: '●', Stage: "段階 2"},
}
