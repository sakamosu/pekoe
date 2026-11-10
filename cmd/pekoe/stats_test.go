package main

import (
	"strings"
	"testing"
	"time"

	"pekoe/internal/daemon"
	"pekoe/internal/state"
)

func TestStatsFlags(t *testing.T) {
	for _, args := range [][]string{{"--days", "0"}, {"--days", "x"}, {"extra"}} {
		if _, ok := cmdStats(args).(*usageError); !ok {
			t.Errorf("%v: want a usage error before connecting", args)
		}
	}
	if f, _, err := parseCmd("stats", []string{"--days", "30", "--repo", "app"}); err != nil || f["days"] != "30" || f["repo"] != "app" {
		t.Fatalf("flags: %v %v", f, err)
	}
}

// One line per day and repository (tapi chat named), the merges after.
func TestPrintStats(t *testing.T) {
	st := daemon.Stats{From: "2026-11-05", To: "2026-11-11", Merged: 1,
		Tokens: state.Usage{Input: 10, Output: 2500, CacheCreation: 40000, CacheRead: 1_200_000},
		Rows: []daemon.StatsRow{
			{Day: "2026-11-11", Repo: "app", Merged: 1, Tokens: state.Usage{Input: 10, Output: 2500, CacheCreation: 40000, CacheRead: 1_000_000}},
			{Day: "2026-11-11", Repo: "", Tokens: state.Usage{CacheRead: 200_000}},
		},
		Merges: []daemon.StatsMerge{{Day: "2026-11-11", Slug: "fix-csv", Repo: "app", PR: 42, MergedAt: time.Now(), Tokens: state.Usage{Output: 999}}},
	}
	out, _ := capture(t, func() error { printStats(st); return nil })
	for _, want := range []string{"merged 1", "1.24M", "app", "(tapi chat)", "app/fix-csv #42", "999 tokens"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	out, _ = capture(t, func() error { printStats(daemon.Stats{From: "a", To: "b"}); return nil })
	if !strings.Contains(out, "nothing recorded") {
		t.Errorf("empty: %s", out)
	}
}
