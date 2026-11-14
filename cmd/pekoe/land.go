package main

// land / dev / rm: commands on a session by its slug.

import (
	"fmt"
	"os"

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
	var res struct {
		PortHeld *state.HeldPort `json:"port_held"`
	}
	if err := cl.Call("worktree.rm", map[string]any{"slug": pos[0], "force": f["force"] != ""}, &res); err != nil {
		return err
	}
	if h := res.PortHeld; h != nil {
		fmt.Fprintf(os.Stderr, "%s: [hooks] dev_stop did not succeed or did not run; port %d is kept from new sessions until %s, as what dev started (containers) may still hold ports\n",
			pos[0], h.Port, h.Until.Local().Format("2006-01-02 15:04"))
	}
	return nil
}
