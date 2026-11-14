package main

// conflicts: the conflict forecast between sessions.

import (
	"fmt"
	"strings"

	"pekoe/internal/daemon"
	"pekoe/internal/theme"
)

// cmdConflicts prints the conflict forecast: pairs of open sessions of the
// same repository that touch the same files, and whether their committed
// contents would conflict (git merge-tree) or merge automatically.
func cmdConflicts(args []string) error {
	f, _, err := parseCmd("conflicts", args)
	if err != nil {
		return err
	}
	cl := connect()
	defer cl.Close()
	id := ""
	if f["repo"] != "" {
		r, err := resolveRepoFlag(cl, f["repo"])
		if err != nil {
			return err
		}
		id = r.ID
	}
	var ps []daemon.ConflictPair
	if err := cl.Call("conflicts.list", map[string]any{"repo_id": id}, &ps); err != nil {
		return err
	}
	if jsonOut {
		printJSON(ps)
		return nil
	}
	if len(ps) == 0 {
		fmt.Println("no overlapping sessions")
		return nil
	}
	ic := theme.Icons(theme.IconsFromEnv())
	th := theme.Load(nil)
	repos := map[string]bool{}
	for _, p := range ps {
		repos[p.RepoID] = true
	}
	for _, p := range ps {
		role, word, files := theme.Warn, "overlap ", strings.Join(p.Files, ", ")+th.FG(theme.Dim, "  (auto-merges)")
		if p.Status == "conflict" {
			role, word, files = theme.Err, "conflict", strings.Join(p.ConflictFiles, ", ")
		}
		repo := ""
		if len(repos) > 1 {
			repo = th.FG(theme.Dim, p.RepoID) + "  "
		}
		fmt.Printf("%s %s  %s%s %s %s  %s\n", th.FG(role, ic.Warning), th.FG(role, word), repo, p.A, th.FG(theme.Dim, "↔"), p.B, files)
	}
	return nil
}
