package main

// tapi: tapi chat's state, or reset it.

import (
	"fmt"

	"pekoe/internal/daemon"
)

// cmdTapi shows tapi chat's state, or resets it (docs/tapi-chat.md).
func cmdTapi(args []string) error {
	sub, name := "status", "tapi"
	if len(args) > 0 && !isFlag(args[0]) {
		sub, args = args[0], args[1:]
		if sub == "reset" {
			name = "tapi reset"
		}
	}
	f, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return &usageError{use: specs[name].use, msg: "unexpected argument " + pos[0]}
	}
	switch sub {
	case "status":
		cl := connect()
		defer cl.Close()
		var v daemon.Tapi
		if err := cl.Call("tapi.get", nil, &v); err != nil {
			return err
		}
		if jsonOut {
			printJSON(v)
			return nil
		}
		switch {
		case !v.Started:
			fmt.Printf("tapi chat is not started (t in the TUI) · agent %s · %s\n", v.Agent, v.Dir)
		case !v.Alive:
			fmt.Printf("tapi chat: agent exited · agent %s · %s\n", v.Agent, v.Dir)
		default:
			st := string(v.State)
			if v.WaitKind != "" {
				st += " (" + v.WaitKind + ")"
			}
			fmt.Printf("tapi chat: %s · agent %s · %s\n", st, v.Agent, v.Dir)
		}
		fmt.Printf("ledger: %d open · inbox: %d open\n", v.LedgerOpen, v.InboxOpen)
		if o := v.Outside; o != nil {
			where := "outside every pekoe window (a Claude Code background session?)"
			if o.Window != "" {
				where = "in the window of " + o.Window
			}
			fmt.Printf("warning: tapi's conversation %s runs %s since %s: pekoe cannot tell it is tapi there.\n"+
				"  stop it in Claude Code, then quit Claude Code in tapi's window and restart it (↵), or pekoe tapi reset\n",
				o.ClaudeSessionID, where, o.Since.Local().Format("15:04"))
		}
		return nil
	case "reset":
		if f["yes"] == "" {
			if !stdinIsTerminal() {
				return fmt.Errorf("tapi reset ends tapi chat and deletes its conversation; pass --yes to confirm")
			}
			if !confirm("end tapi chat and forget its conversation?") {
				return nil
			}
		}
		cl := connect()
		defer cl.Close()
		if err := cl.Call("tapi.reset", nil, nil); err != nil {
			return err
		}
		fmt.Println("tapi chat reset")
		return nil
	}
	return &usageError{use: specs["tapi"].use, msg: "unknown subcommand " + sub}
}
