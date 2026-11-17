package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"pekoe/internal/client"
	"pekoe/internal/hooks"
	"pekoe/internal/paths"
	"pekoe/internal/tui"
)

// tuiReconnectWait is how long the TUI waits for pekoed to come back after
// the connection drops: `pekoe daemon restart` waits for the old pekoed to
// exit (up to 10 s) before it starts the new one.
const tuiReconnectWait = 20 * time.Second

func runTUI() error {
	cl := connect()
	if !hooks.Installed() {
		fmt.Fprintln(os.Stderr, "hint: Claude Code hooks are not installed; run `pekoe hooks install` so pekoe can see agent states.")
	}
	return tuiLoop(os.Stderr,
		func() error {
			defer cl.Close()
			return tui.Run(cl, cwd())
		},
		func() error { // only dial: a stopped pekoed is not started again
			c, err := client.WaitDial(tuiReconnectWait)
			if err == nil {
				cl = c
			}
			return err
		})
}

// tuiLoop runs the TUI. When the connection to pekoed ends (a restart or a
// stop), it says so on the terminal, waits for pekoed to answer again and
// opens the TUI anew; when pekoed does not come back, it ends with an error
// that says why.
func tuiLoop(stderr io.Writer, run, wait func() error) error {
	for {
		err := run()
		if !errors.Is(err, tui.ErrDisconnected) {
			return err
		}
		fmt.Fprintf(stderr, "pekoe: lost connection to pekoed (restarted or stopped); waiting up to %s for it to come back (Ctrl-C to quit)\n", tuiReconnectWait)
		if err := wait(); err != nil {
			return fmt.Errorf("lost connection to pekoed, and it did not come back within %s (start it with `pekoe daemon start`; log: %s)", tuiReconnectWait, paths.DaemonLog())
		}
		fmt.Fprintln(stderr, "pekoe: pekoed is back; reopening")
	}
}
