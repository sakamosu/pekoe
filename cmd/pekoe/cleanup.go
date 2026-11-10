package main

// cleanup (docs/orchestration.md 段階 4 の G「後片付け」): the sessions that
// could go — landed, merged on GitHub, stopped for long — with what removing
// them would lose, and removing the ones a person (or, for landed ones, an
// agent) picks. Removing is worktree.rm, the same as `pekoe rm`: without the
// human token pekoed removes only landed sessions.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"pekoe/internal/daemon"
	"pekoe/internal/theme"
)

func cmdCleanup(args []string) error {
	f, pos, err := parseCmd("cleanup", args)
	if err != nil {
		return err
	}
	rm := f["rm"] != ""
	if !rm {
		for _, k := range []string{"force", "yes", "landed"} {
			if f[k] != "" {
				return &usageError{use: specs["cleanup"].use, msg: "--" + k + " goes with --rm"}
			}
		}
		if len(pos) > 0 {
			return &usageError{use: specs["cleanup"].use, msg: "unexpected argument " + pos[0] + " (to remove it: --rm)"}
		}
	} else if len(pos) == 0 && f["landed"] == "" {
		return &usageError{use: specs["cleanup"].use, msg: "--rm needs slugs, or --landed for every landed one"}
	}
	cl := connect()
	defer cl.Close()
	// slugs named on --rm are looked up in every repository
	scope := (*daemon.KnownRepo)(nil)
	if !rm || f["landed"] != "" {
		scope, err = lsScope(f, func(q string) (*daemon.KnownRepo, error) { return resolveRepoFlag(cl, q) }, func() *daemon.KnownRepo { return hereRepo(cl) })
		if err != nil {
			return err
		}
	}
	params := map[string]any{}
	if scope != nil {
		params["repo_id"] = scope.ID
	}
	if f["stale"] != "" {
		if d, err := time.ParseDuration(f["stale"]); err != nil || d <= 0 {
			return &usageError{use: specs["cleanup"].use, msg: "--stale takes a duration like 72h or 36h30m"}
		}
		params["stale_after"] = f["stale"]
	}
	var l daemon.CleanupList
	if err := cl.Call("cleanup.list", params, &l); err != nil {
		return err
	}
	th := theme.Load(nil)
	ic := theme.Icons(theme.IconsFromEnv())
	if !rm {
		if jsonOut {
			printJSON(l)
			return nil
		}
		if len(l.Items) == 0 {
			where := ""
			if scope != nil {
				where = " in " + scope.Name + " · `pekoe cleanup --all` for every repository"
			}
			fmt.Printf("nothing to clean up%s (stale: stopped for %s)\n", where, shortDur(l.StaleAfter))
			return nil
		}
		for _, line := range cleanupLines(th, ic, l, scope == nil, time.Now()) {
			fmt.Println(line)
		}
		fmt.Println(th.FG(theme.Dim, "remove: pekoe cleanup --rm <slug>… (--landed: every landed one) · agents may remove only landed ones"))
		return nil
	}

	picked, err := cleanupPick(l, pos, f["landed"] != "")
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		fmt.Println("no landed sessions to remove")
		return nil
	}
	if f["yes"] == "" {
		fmt.Println("remove:")
		for _, line := range cleanupLines(th, ic, daemon.CleanupList{StaleAfter: l.StaleAfter, Items: picked}, true, time.Now()) {
			fmt.Println(line)
		}
		q := fmt.Sprintf("remove %d session%s?", len(picked), plural(len(picked)))
		for _, it := range picked {
			if it.DeletesWorktree {
				q = fmt.Sprintf("remove %d session%s (run teardown and delete the worktrees shown)?", len(picked), plural(len(picked)))
				break
			}
		}
		if !confirm(q) {
			return nil
		}
	}
	var failed []string
	var res []map[string]any
	for _, it := range picked {
		err := cl.Call("worktree.rm", map[string]any{"slug": it.Slug, "force": f["force"] != ""}, nil)
		r := map[string]any{"slug": it.Slug, "removed": err == nil}
		if err != nil {
			r["error"] = err.Error()
			failed = append(failed, it.Slug)
			if !jsonOut {
				fmt.Fprintf(os.Stderr, "%s: %v\n", it.Slug, err)
			}
		} else if !jsonOut {
			fmt.Println("removed " + it.Slug)
		}
		res = append(res, r)
	}
	if jsonOut {
		printJSON(res)
	}
	if len(failed) > 0 {
		return fmt.Errorf("not removed: %s", strings.Join(failed, ", "))
	}
	return nil
}

// cleanupPick chooses the candidates to remove: the named slugs (each must
// be a candidate; for anything else there is `pekoe rm`), or every landed
// one.
func cleanupPick(l daemon.CleanupList, slugs []string, landed bool) ([]daemon.CleanupItem, error) {
	by := map[string]daemon.CleanupItem{}
	for _, it := range l.Items {
		by[it.Slug] = it
	}
	var out []daemon.CleanupItem
	seen := map[string]bool{}
	for _, s := range slugs {
		it, ok := by[s]
		if !ok {
			return nil, fmt.Errorf("%s is not a cleanup candidate (see pekoe cleanup; any session: pekoe rm)", s)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, it)
		}
	}
	if landed {
		for _, it := range l.Items {
			if it.Kind == daemon.CleanupLanded && !seen[it.Slug] {
				seen[it.Slug] = true
				out = append(out, it)
			}
		}
	}
	if len(slugs) > 0 && len(out) == 0 {
		return nil, errors.New("nothing picked")
	}
	return out, nil
}

// cleanupLines renders the candidates, one per line, the warnings after
// them:
//
//	landed  feat-x   feat/x   landed 3d
//	stale   feat-y   feat/y   idle   4d  ⚠ 2 uncommitted files · person only
func cleanupLines(th *theme.Theme, ic theme.IconSet, l daemon.CleanupList, showRepo bool, now time.Time) []string {
	slugW, branchW := 4, 6
	for _, it := range l.Items {
		slugW = max(slugW, len(it.Slug))
		branchW = max(branchW, len(it.Branch))
	}
	slugW, branchW = min(slugW, 32), min(branchW, 32)
	var out []string
	for _, it := range l.Items {
		role := theme.Dim
		switch it.Kind {
		case daemon.CleanupMerged:
			role = theme.Ok
		case daemon.CleanupStale:
			role = theme.Warn
		}
		parts := []string{th.FG(role, fmt.Sprintf("%-6s", it.Kind))}
		if showRepo {
			parts = append(parts, th.FG(theme.Dim, it.Repo))
		}
		parts = append(parts, fmt.Sprintf("%-*s", slugW, it.Slug), th.FG(theme.Dim, fmt.Sprintf("%-*s", branchW, it.Branch)),
			fmt.Sprintf("%-8s", it.State), th.FG(theme.Dim, fmt.Sprintf("%-4s", sinceAgo(it.Since, now))))
		if it.PR > 0 {
			parts = append(parts, th.FG(theme.Dim, fmt.Sprintf("PR #%d", it.PR)))
		}
		for _, w := range it.Warnings {
			parts = append(parts, th.FG(theme.Warn, ic.Warning+" "+w))
		}
		if it.HumanOnly {
			parts = append(parts, th.FG(theme.Dim, "person only"))
		}
		out = append(out, strings.Join(parts, "  "))
	}
	return out
}

// shortDur prints "72h0m0s" as "72h" and "30m0s" as "30m".
func shortDur(s string) string {
	d, err := time.ParseDuration(s)
	switch {
	case err != nil:
		return s
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return d.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sinceAgo is how long ago t was ("-" when unknown).
func sinceAgo(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return theme.Ago(now.Sub(t))
}
