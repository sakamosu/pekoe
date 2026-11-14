package main

// issues: the repository's open GitHub issues.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/daemon"
	"pekoe/internal/theme"
)

func cmdIssues(args []string) error {
	f, pos, err := parseCmd("issues", args)
	if err != nil {
		return err
	}
	cl := connect()
	defer cl.Close()
	dir, err := repoDir(cl, f)
	if err != nil {
		return err
	}
	ic := theme.Icons(theme.IconsFromEnv())
	th := theme.Load(nil)
	if len(pos) > 0 {
		n, err := strconv.Atoi(strings.TrimPrefix(pos[0], "#"))
		if err != nil {
			return fmt.Errorf("usage: pekoe issues [<N>] [--repo R] [--all|--label L] [--refresh]")
		}
		var det daemon.IssueDetail
		if err := cl.Call("issues.view", map[string]any{"repo": dir, "number": n, "refresh": f["refresh"] == "true"}, &det); err != nil {
			return err
		}
		if jsonOut {
			printJSON(det)
			return nil
		}
		fmt.Printf("#%d %s\n", det.Number, det.Title)
		meta := []string{strings.ToLower(det.State), "@" + det.Author}
		if len(det.Labels) > 0 {
			meta = append(meta, strings.Join(det.Labels, ", "))
		}
		if det.Session != nil {
			meta = append(meta, "session "+det.Session.Slug)
		}
		fmt.Println(th.FG(theme.Dim, strings.Join(meta, " · ")+" · "+det.URL))
		fmt.Println()
		fmt.Println(strings.TrimSpace(det.Body))
		for _, c := range det.Thread {
			fmt.Println()
			fmt.Println(th.FG(theme.Dim, fmt.Sprintf("── @%s · %s ago", c.Author, theme.Ago(time.Since(c.CreatedAt)))))
			fmt.Println(strings.TrimSpace(c.Body))
		}
		return nil
	}
	filter := "mine"
	switch {
	case f["label"] != "" && f["label"] != "true":
		filter = "label"
	case f["all"] == "true":
		filter = "open"
	}
	var l daemon.IssueList
	if err := cl.Call("issues.list", map[string]any{"repo": dir, "filter": filter, "label": f["label"], "refresh": f["refresh"] == "true"}, &l); err != nil {
		return err
	}
	if jsonOut {
		printJSON(l)
		return nil
	}
	if l.Unavailable != "" {
		return errors.New(l.Unavailable)
	}
	if len(l.Issues) == 0 {
		fmt.Println("no open issues" + map[string]string{"mine": " assigned to you · --all for every open issue", "label": " labeled " + l.Label}[l.Filter])
		return nil
	}
	for _, is := range l.Issues {
		parts := []string{" ", fmt.Sprintf("#%-5d", is.Number), is.Title}
		if is.Session != nil {
			lk := theme.Look(ic, is.Session.State, 0, false)
			parts[0] = th.FG(lk.Role, lk.Icon)
		}
		if len(is.Labels) > 0 {
			parts = append(parts, th.FG(theme.Dim, strings.Join(is.Labels, ",")))
		}
		if is.Comments > 0 {
			parts = append(parts, th.FG(theme.Dim, fmt.Sprintf("%dc", is.Comments)))
		}
		parts = append(parts, th.FG(theme.Dim, theme.Ago(time.Since(is.UpdatedAt))))
		if is.Session != nil {
			parts = append(parts, th.FG(theme.Dim, "→ "+is.Session.Slug))
		}
		fmt.Println(strings.Join(parts, "  "))
	}
	return nil
}
