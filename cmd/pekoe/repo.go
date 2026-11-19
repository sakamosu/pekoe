package main

// pekoe repo forget: a person makes pekoe stop knowing a repository
// (repo.forget, human only). The repository, its worktrees and branches stay
// as they are.

import (
	"fmt"
	"strings"

	"pekoe/internal/daemon"
)

func cmdRepo(args []string) error {
	sub, name := "", "repo"
	if len(args) > 0 && !isFlag(args[0]) {
		sub, args = args[0], args[1:]
		name = "repo " + sub
	}
	if _, ok := specs[name]; !ok {
		name = "repo" // unknown subcommand: --help still answers
	}
	f, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	switch sub {
	case "forget":
		return repoForget(specs[name].use, f, pos)
	case "":
		return &usageError{use: specs["repo"].use, msg: "subcommand required"}
	}
	return &usageError{use: specs["repo"].use, msg: "unknown subcommand " + sub}
}

func repoForget(use string, f map[string]string, pos []string) error {
	if len(pos) != 1 {
		if len(pos) > 1 {
			return &usageError{use: use, msg: "unexpected argument " + pos[1]}
		}
		return &usageError{use: use, msg: "which repository? a short name, a repo-id or a path"}
	}
	p := map[string]any{"repo": pos[0], "cwd": cwd(), "config": f["config"] != ""}
	cl := connect()
	defer cl.Close()
	if f["yes"] == "" {
		var plan daemon.RepoForget
		p["dry_run"] = true
		if err := cl.Call("repo.forget", p, &plan); err != nil {
			return err
		}
		delete(p, "dry_run")
		fmt.Println(forgetLines(&plan, "will be forgotten"))
		if !confirm(fmt.Sprintf("forget %s?", plan.Name)) {
			return nil
		}
	}
	var res daemon.RepoForget
	if err := cl.Call("repo.forget", p, &res); err != nil {
		return err
	}
	if jsonOut {
		printJSON(res)
		return nil
	}
	fmt.Println(forgetLines(&res, "forgotten"))
	return nil
}

// forgetLines says what repo.forget does: the repository, then one line per
// thing it drops.
func forgetLines(r *daemon.RepoForget, verb string) string {
	where := r.Dir
	if where == "" {
		where = "directory unknown"
	}
	lines := []string{fmt.Sprintf("%s (%s, %s) %s:", r.Name, r.ID, where, verb)}
	if r.Remembered {
		lines = append(lines, "  what pekoe remembered of it (state.db)")
	}
	if len(r.Landed) > 0 {
		lines = append(lines, "  landed sessions: "+strings.Join(r.Landed, ", "))
	}
	if r.ConfigFile != "" {
		state := "deleted"
		if !r.ConfigGone {
			state = "to delete"
		}
		lines = append(lines, "  config file ("+state+"): "+r.ConfigFile)
	}
	lines = append(lines, "  kept: the repository, its worktrees and branches, pekoe stats")
	return strings.Join(lines, "\n")
}
