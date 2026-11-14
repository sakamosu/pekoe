package main

// land / dev / rm: commands on a session by its slug.

import (
	"fmt"

	"pekoe/internal/state"
)

func cmdSimple(name, method string, args []string) error {
	_, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: %s", specs[name].use)
	}
	cl := connect()
	defer cl.Close()
	var res any
	if err := cl.Call(method, map[string]any{"slug": pos[0]}, &res); err != nil {
		return err
	}
	if jsonOut {
		printJSON(res)
	} else if m, ok := res.(map[string]any); ok {
		if steps, ok := m["steps"].([]any); ok {
			for _, s := range steps {
				fmt.Println(" ", s)
			}
		}
	}
	return nil
}

func cmdRm(args []string) error {
	f, pos, err := parseCmd("rm", args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pekoe rm <slug> [--force]")
	}
	cl := connect()
	defer cl.Close()
	var s state.Session
	if err := cl.Call("session.get", map[string]any{"slug": pos[0]}, &s); err != nil {
		return err
	}
	if f["yes"] == "" {
		what := "forget the session (worktree outside ~/worktrees is kept)"
		if s.State == state.Landed {
			what = "remove it from the list (landed: its worktree is already gone)"
		} else if s.Managed {
			what = "run teardown and delete " + s.Path
		}
		if !confirm(fmt.Sprintf("%s: %s?", s.Slug, what)) {
			return nil
		}
	}
	return cl.Call("worktree.rm", map[string]any{"slug": pos[0], "force": f["force"] != ""}, nil)
}
