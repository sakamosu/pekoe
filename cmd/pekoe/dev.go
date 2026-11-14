package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"pekoe/internal/daemon"
	"pekoe/internal/state"
)

// cmdDev: `pekoe dev <slug>` starts or stops the session's dev server;
// `pekoe dev --log [<slug>]` prints the end of its log and whether it runs
// or how it ended. Without a slug the log is $PEKOE_SESSION's (an agent's
// own; pekoed decides by the caller's window anyway).
func cmdDev(args []string) error {
	f, pos, err := parseCmd("dev", args)
	if err != nil {
		return err
	}
	if f["log"] == "" {
		if f["lines"] != "" {
			return &usageError{use: specs["dev"].use, msg: "--lines goes with --log"}
		}
		if len(pos) < 1 {
			return &usageError{use: specs["dev"].use, msg: "slug required"}
		}
		cl := connect()
		defer cl.Close()
		var s state.Session
		if err := cl.Call("dev.toggle", map[string]any{"slug": pos[0]}, &s); err != nil {
			return err
		}
		if jsonOut {
			printJSON(s)
			return nil
		}
		if s.Dev {
			fmt.Printf("%s: dev server started on :%d · log: pekoe dev --log %s\n", s.Slug, s.Port, s.Slug)
		} else {
			fmt.Printf("%s: dev server stopped%s\n", s.Slug, devStopLine(s.DevExit))
		}
		return nil
	}
	n := 0
	if v := f["lines"]; v != "" {
		if n, err = strconv.Atoi(v); err != nil || n < 1 {
			return &usageError{use: specs["dev"].use, msg: "--lines is a positive number"}
		}
	}
	slug := os.Getenv("PEKOE_SESSION")
	if len(pos) > 0 {
		slug = pos[0]
	}
	cl := connect()
	defer cl.Close()
	var lg daemon.DevLog
	if err := cl.Call("dev.log", map[string]any{"slug": slug, "lines": n}, &lg); err != nil {
		return err
	}
	if jsonOut {
		printJSON(lg)
		return nil
	}
	fmt.Printf("# %s · %s\n", lg.Path, devStatus(lg.Running, lg.Exit))
	for _, l := range lg.Lines {
		fmt.Println(l)
	}
	return nil
}

// devStatus: "running", "ended: exit status 1 at 15:04:05", … (one line).
func devStatus(running bool, ex *state.DevExit) string {
	if running {
		return "running"
	}
	if ex == nil {
		return "not started"
	}
	var how string
	switch {
	case ex.Stopped:
		how = "stopped by pekoe"
	case ex.Signal != "":
		how = "ended: killed by " + ex.Signal
	case ex.Status >= 0:
		how = fmt.Sprintf("ended: exit status %d", ex.Status)
	default:
		how = "ended: exit status unknown"
	}
	return how + " at " + ex.At.Local().Format(time.DateTime) + devStopLine(ex)
}

// devStopLine: " · dev_stop ok" / " · dev_stop failed (exit status 2)", or
// nothing when no dev_stop ran.
func devStopLine(ex *state.DevExit) string {
	if ex == nil || ex.DevStop == nil {
		return ""
	}
	if ex.DevStop.Error == "" {
		return " · dev_stop ok"
	}
	return " · dev_stop failed: " + ex.DevStop.Error
}
