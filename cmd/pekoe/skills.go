package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"pekoe"
	"pekoe/internal/paths"
	"pekoe/internal/skills"
)

func skillDirs() skills.Dirs {
	return skills.Dirs{Links: skills.LinksDir(), Copies: paths.SkillsDir()}
}

// cmdSkills installs the Claude Code skills built into the binary
// (internal/skills): status (default), install, uninstall. Like `pekoe hooks`
// it shows what it will do and asks first (--yes skips the question).
func cmdSkills(args []string) error {
	f, pos, err := parseCmd("skills", args)
	if err != nil {
		return err
	}
	sub := "status"
	if len(pos) > 0 {
		sub = pos[0]
	}
	if len(pos) > 1 {
		return &usageError{use: specs["skills"].use, msg: "unexpected argument " + pos[1]}
	}
	d := skillDirs()
	items, err := skills.Check(pekoe.Docs, d)
	if err != nil {
		return err
	}
	ask := func(q string) bool { return f["yes"] != "" || confirm(q) }
	switch sub {
	case "status":
		if jsonOut {
			printJSON(items)
			return nil
		}
		printSkillStatus(os.Stdout, items)
		return nil
	case "install":
		return skillsInstall(os.Stdout, d, items, ask)
	case "uninstall":
		return skillsUninstall(os.Stdout, d, items, ask)
	}
	return &usageError{use: specs["skills"].use, msg: "unknown subcommand " + sub}
}

func printSkillStatus(w io.Writer, items []skills.Item) {
	for _, it := range items {
		var s string
		switch it.State {
		case skills.Installed:
			s = "installed"
		case skills.Stale:
			s = "installed, older text · run `pekoe skills install` (or restart pekoed)"
		case skills.Missing:
			s = "not installed · run `pekoe skills install`"
		case skills.Elsewhere:
			s = "a link made elsewhere → " + it.Points + " · `pekoe skills install` offers to replace it"
		case skills.Blocked:
			s = "a directory pekoe did not make is there · move it away to install"
		}
		fmt.Fprintf(w, "%-18s %s\n", it.Name, s)
	}
	if len(items) > 0 {
		fmt.Fprintf(w, "links in %s, text in %s\n", filepath.Dir(items[0].Link), filepath.Dir(items[0].Copy))
	}
}

func skillsInstall(w io.Writer, d skills.Dirs, items []skills.Item, ask func(string) bool) error {
	var todo, elsewhere, blocked []skills.Item
	for _, it := range items {
		switch it.State {
		case skills.Missing, skills.Stale:
			todo = append(todo, it)
		case skills.Elsewhere:
			elsewhere = append(elsewhere, it)
		case skills.Blocked:
			blocked = append(blocked, it)
		}
	}
	for _, it := range blocked {
		fmt.Fprintf(w, "skip %s: %s is a directory or file pekoe did not make; move it away (out of %s) and run again\n", it.Name, it.Link, d.Links)
	}
	if len(todo) == 0 && len(elsewhere) == 0 {
		if len(blocked) == 0 {
			fmt.Fprintln(w, "already up to date:", d.Links)
			return nil
		}
		return fmt.Errorf("%d skill(s) not installed", len(blocked))
	}
	fmt.Fprintf(w, "the skills' text goes to %s, linked from %s:\n", d.Copies, d.Links)
	for _, it := range todo {
		what := "link"
		if it.State == skills.Stale {
			what = "update the text"
		}
		fmt.Fprintf(w, "  %-18s %s\n", it.Name, what)
	}
	for _, it := range elsewhere {
		fmt.Fprintf(w, "  %-18s replace the link to %s (what it points at is kept)\n", it.Name, it.Points)
	}
	if !ask("write them?") {
		return nil
	}
	skipped, err := skills.Install(pekoe.Docs, d, items, true)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "done: Claude Code reads them in its next session")
	if len(skipped) > 0 {
		return fmt.Errorf("not installed: %s", strings.Join(skipped, ", "))
	}
	return nil
}

func skillsUninstall(w io.Writer, d skills.Dirs, items []skills.Item, ask func(string) bool) error {
	var ours []skills.Item
	for _, it := range items {
		switch it.State {
		case skills.Installed, skills.Stale:
			ours = append(ours, it)
		case skills.Elsewhere:
			fmt.Fprintf(w, "leave %s: a link made elsewhere (→ %s)\n", it.Name, it.Points)
		case skills.Blocked:
			fmt.Fprintf(w, "leave %s: not pekoe's\n", it.Name)
		}
	}
	_, err := os.Stat(d.Copies)
	if len(ours) == 0 && os.IsNotExist(err) {
		fmt.Fprintln(w, "not installed")
		return nil
	}
	for _, it := range ours {
		fmt.Fprintf(w, "  remove %s\n", it.Link)
	}
	fmt.Fprintf(w, "  remove the skills' text in %s\n", d.Copies)
	if !ask("remove them?") {
		return nil
	}
	if err := skills.Uninstall(pekoe.Docs, d, items); err != nil {
		return err
	}
	fmt.Fprintln(w, "done")
	return nil
}
