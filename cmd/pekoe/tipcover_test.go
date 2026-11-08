package main

import (
	"strings"
	"testing"

	"pekoe/internal/tips"
)

// subcommands are the first words of the flag specs: every subcommand has
// one (TestEverySubcommandHasSpec)
func subcommands() map[string]string {
	have := map[string]string{}
	for name, sp := range specs {
		sub := "pekoe " + strings.Fields(name)[0]
		if have[sub] == "" || !strings.Contains(name, " ") {
			have[sub] = "usage: " + sp.use
		}
	}
	return have
}

// Every subcommand has a tip or a reason not to (internal/tips NoTip),
// and every subcommand and flag a tip names exists.
func TestTipsCoverSubcommands(t *testing.T) {
	for _, e := range tips.Coverage(tips.KindCommand, subcommands()) {
		t.Error(e)
	}
	flags := map[string]bool{}
	for _, sp := range specs {
		for _, f := range append(append([]string(nil), sp.bools...), sp.vals...) {
			flags["--"+f] = true
		}
	}
	for f, ids := range tips.Refs(tips.KindFlag) {
		if !flags[f] {
			t.Errorf("tip %s names flag %q, which no subcommand takes: fix its keys in internal/tips/tips.toml", strings.Join(ids, ", "), f)
		}
	}
}

func TestTipsCoverSubcommandsCatches(t *testing.T) {
	have := subcommands()
	have["pekoe frob"] = "usage: pekoe frob"
	delete(have, "pekoe land")
	errs := strings.Join(tips.Coverage(tips.KindCommand, have), "\n")
	for _, want := range []string{`command "pekoe frob" (usage: pekoe frob) has no tip`, `tip land names command "pekoe land", which does not exist`} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in\n%s", want, errs)
		}
	}
}
