package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"pekoe/internal/daemon"
	"pekoe/internal/theme"
)

var cleanupSample = daemon.CleanupList{StaleAfter: "72h0m0s", Items: []daemon.CleanupItem{
	{Kind: daemon.CleanupLanded, Slug: "feat-a", Branch: "feat/a", State: "landed"},
	{Kind: daemon.CleanupLanded, Slug: "feat-b", Branch: "feat/b", State: "landed"},
	{Kind: daemon.CleanupMerged, Slug: "feat-c", Branch: "feat/c", State: "done", PR: 7, Worktree: true, DeletesWorktree: true, HumanOnly: true},
	{Kind: daemon.CleanupStale, Slug: "feat-d", Branch: "feat/d", State: "idle", Worktree: true, DeletesWorktree: true, HumanOnly: true,
		Dirty: 2, Unpushed: 1, Warnings: []string{"2 uncommitted files", "1 unpushed commit"}},
}}

func TestCleanupPick(t *testing.T) {
	slugs := func(its []daemon.CleanupItem) []string {
		var out []string
		for _, it := range its {
			out = append(out, it.Slug)
		}
		return out
	}
	got, err := cleanupPick(cleanupSample, nil, true)
	if err != nil || !slices.Equal(slugs(got), []string{"feat-a", "feat-b"}) {
		t.Errorf("--landed: %v %v", slugs(got), err)
	}
	got, err = cleanupPick(cleanupSample, []string{"feat-d", "feat-a", "feat-d"}, true)
	if err != nil || !slices.Equal(slugs(got), []string{"feat-d", "feat-a", "feat-b"}) {
		t.Errorf("slugs and --landed: %v %v", slugs(got), err)
	}
	if _, err := cleanupPick(cleanupSample, []string{"feat-z"}, false); err == nil || !strings.Contains(err.Error(), "not a cleanup candidate") {
		t.Errorf("a slug that is not a candidate: %v", err)
	}
}

// The warnings and who may remove it are on the line.
func TestCleanupLines(t *testing.T) {
	th := theme.Load(nil)
	th.Mode = theme.None
	ls := cleanupLines(th, theme.Icons("none"), cleanupSample, false, time.Now())
	if len(ls) != 4 {
		t.Fatalf("%d lines", len(ls))
	}
	if strings.Contains(ls[0], "person only") || !strings.HasPrefix(ls[0], "landed") {
		t.Errorf("landed line %q", ls[0])
	}
	if !strings.Contains(ls[2], "PR #7") || !strings.Contains(ls[2], "person only") {
		t.Errorf("merged line %q", ls[2])
	}
	if !strings.Contains(ls[3], "2 uncommitted files") || !strings.Contains(ls[3], "1 unpushed commit") {
		t.Errorf("stale line %q", ls[3])
	}
	if shortDur("72h0m0s") != "72h" || shortDur("30m0s") != "30m" || shortDur("1h30m0s") != "90m" {
		t.Errorf("shortDur: %s %s %s", shortDur("72h0m0s"), shortDur("30m0s"), shortDur("1h30m0s"))
	}
}

// --rm removes each picked candidate with worktree.rm and goes on after a
// refusal (an agent's rm of anything but landed), failing at the end.
func TestCleanupRm(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"cleanup.list": cleanupSample}
	if err := dispatch("cleanup", "--rm", "--landed", "--all", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := fd.calls(); !slices.Equal(got, []string{"cleanup.list", "worktree.rm", "worktree.rm"}) {
		t.Errorf("--rm --landed: %v", got)
	}
	fd.reset()
	fd.errs = map[string]string{"worktree.rm": "worktree.rm: only a person can do this"}
	err := dispatch("cleanup", "--rm", "feat-c", "feat-d", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not removed: feat-c, feat-d") {
		t.Errorf("refused rm: %v", err)
	}
	if got := fd.calls(); !slices.Equal(got, []string{"cleanup.list", "worktree.rm", "worktree.rm"}) {
		t.Errorf("refused rm: %v", got)
	}

	// flags that only go with --rm, and --rm with nothing to pick, act on nothing
	fd.reset()
	for _, args := range [][]string{{"cleanup", "--yes"}, {"cleanup", "feat-a"}, {"cleanup", "--rm"}, {"cleanup", "--stale", "soon"}} {
		if err := dispatch(args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
	if got := fd.calls(); slices.Contains(got, "worktree.rm") {
		t.Errorf("bad arguments removed something: %v", got)
	}
}
