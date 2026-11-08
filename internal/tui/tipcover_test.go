package tui

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"pekoe/internal/config"
	"pekoe/internal/tips"
)

// helpKeys is every key the help can list (all panel lists, the global
// keys, the tip key) plus the built-in review surface's bar, normalized.
func helpKeys(t *testing.T) map[string]string {
	t.Helper()
	m := tapiModel(t, "none", config.Mascot{})
	have := map[string]string{}
	add := func(label, action, where string) {
		for _, k := range tips.Expand(label) {
			if have[k] == "" {
				have[k] = "help: " + where + ": " + action
			}
		}
	}
	sets := m.helpSets()
	for _, name := range slices.Sorted(maps.Keys(sets)) {
		for _, k := range sets[name] {
			add(k[0], k[1], name)
		}
	}
	for _, k := range m.globalHelpKeys() {
		add(k[0], k[1], "global")
	}
	add(tipHelpKey[0], tipHelpKey[1], "global")
	for _, k := range reviewBarKeys {
		add(k[0], k[1], "review surface")
	}
	return have
}

// Every key in the help has a tip or a reason not to (internal/tips
// NoTip), and every TUI key a tip names is in the help.
func TestTipsCoverHelpKeys(t *testing.T) {
	for _, e := range tips.Coverage(tips.KindKey, helpKeys(t)) {
		t.Error(e)
	}
}

// the coverage check catches a new key without a tip and a tip naming a
// key that is gone
func TestTipsCoverCatches(t *testing.T) {
	have := helpKeys(t)
	have["Z"] = "help: sessions: something new"
	errs := strings.Join(tips.Coverage(tips.KindKey, have), "\n")
	if !strings.Contains(errs, `key "Z" (help: sessions: something new) has no tip: add one`) {
		t.Errorf("a new key went unnoticed:\n%s", errs)
	}
	delete(have, "Z")
	delete(have, "!")
	errs = strings.Join(tips.Coverage(tips.KindKey, have), "\n")
	if !strings.Contains(errs, `tip run-window names key "!", which does not exist`) {
		t.Errorf("a removed key went unnoticed:\n%s", errs)
	}
}
