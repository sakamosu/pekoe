package main

// stats (docs/orchestration.md "4. PR の集計"): PRs merged and tokens used,
// by local date and repository, from what pekoed kept (sessions removed
// since included) and the live sessions' transcripts so far.

import (
	"fmt"
	"strconv"

	"pekoe/internal/daemon"
	"pekoe/internal/state"
)

func cmdStats(args []string) error {
	f, pos, err := parseCmd("stats", args)
	if err != nil {
		return err
	}
	use := specs["stats"].use
	if len(pos) > 0 {
		return &usageError{use: use, msg: "unexpected argument " + pos[0]}
	}
	days := 7
	if v := f["days"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return &usageError{use: use, msg: fmt.Sprintf("bad --days %q (1 or more)", v)}
		}
		days = n
	}
	cl := connect()
	defer cl.Close()
	repo := f["repo"]
	if repo != "" {
		// a repository pekoe knows: by its name; else the name as given
		// (one whose sessions are all gone may still be in the records)
		if r, err := resolveRepoFlag(cl, repo); err == nil {
			repo = r.Name
		} else if repo == "true" {
			return err
		}
	}
	var st daemon.Stats
	if err := cl.Call("stats", map[string]any{"days": days, "repo": repo}, &st); err != nil {
		return err
	}
	if jsonOut {
		printJSON(st)
		return nil
	}
	printStats(st)
	return nil
}

func printStats(st daemon.Stats) {
	fmt.Printf("%s … %s   merged %d · tokens %s\n", st.From, st.To, st.Merged, tokenLine(st.Tokens))
	if len(st.Rows) == 0 {
		fmt.Println("nothing recorded in these days")
		return
	}
	fmt.Println()
	fmt.Printf("%-10s  %-20s  %6s  %8s  %8s  %8s  %8s  %8s\n", "day", "repo", "merged", "total", "input", "output", "cache_w", "cache_r")
	for _, r := range st.Rows {
		name := r.Repo
		if name == "" {
			name = "(tapi chat)"
		}
		t := r.Tokens
		fmt.Printf("%-10s  %-20s  %6d  %8s  %8s  %8s  %8s  %8s\n", r.Day, name, r.Merged,
			ktok(t.Total()), ktok(t.Input), ktok(t.Output), ktok(t.CacheCreation), ktok(t.CacheRead))
	}
	if len(st.Merges) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("merged:")
	for _, m := range st.Merges {
		pr := ""
		if m.PR > 0 {
			pr = fmt.Sprintf(" #%d", m.PR)
		}
		fmt.Printf("  %s  %s/%s%s   %s tokens\n", m.MergedAt.Local().Format("2006-01-02 15:04"), m.Repo, m.Slug, pr, ktok(m.Tokens.Total()))
	}
}

func tokenLine(u state.Usage) string {
	return fmt.Sprintf("%s (in %s · out %s · cache write %s · cache read %s)",
		ktok(u.Total()), ktok(u.Input), ktok(u.Output), ktok(u.CacheCreation), ktok(u.CacheRead))
}

// ktok shortens a token count: 950, 12.3k, 4.56M, 1.23G.
func ktok(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%.2fG", float64(n)/1e9)
}
