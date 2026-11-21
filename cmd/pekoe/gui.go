package main

// gui: the local GUI, the web UI in this Mac's own browser on 127.0.0.1
// (docs/gui.md). gui.open needs the human token: an agent cannot open it.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// guiOpenResult is gui.open's answer.
type guiOpenResult struct {
	Port    int       `json:"port"`
	File    string    `json:"file"`
	Expires time.Time `json:"expires"`
	Browser string    `json:"browser,omitempty"`
}

// guiStatus is gui.status's answer.
type guiStatus struct {
	Enabled   bool       `json:"enabled"`
	Listening bool       `json:"listening"`
	Port      int        `json:"port,omitempty"`
	Sessions  int        `json:"sessions"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
}

// openGUIPage hands the redirect page to the OS (open / xdg-open). The
// page's path is the only argument: the token stays inside the file, off
// every command line. A variable for the tests.
var openGUIPage = func(file, browser string) error {
	if runtime.GOOS == "darwin" {
		args := []string{}
		if browser != "" {
			args = append(args, "-a", browser)
		}
		// file is an absolute path (pekoed's data directory): never a flag
		return exec.Command("open", append(args, file)...).Run()
	}
	return exec.Command("xdg-open", file).Run()
}

// guiProfileNote is printed each time the GUI is opened: the GUI takes the
// browser tab for the person, so a browser an agent drives must not be the
// one it is opened in.
const guiProfileNote = "Open the GUI in a browser (or profile) that no agent drives: whatever can drive that tab (Claude in Chrome, Playwright, DevTools) can answer prompts as you."

func cmdGUI(args []string) error {
	f, pos, err := parseCmd("gui", args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return &usageError{use: specs["gui"].use, msg: "unexpected argument " + pos[0]}
	}
	n := 0
	for _, k := range []string{"status", "revoke", "stop", "no-open"} {
		if f[k] != "" {
			n++
		}
	}
	if n > 1 {
		return &usageError{use: specs["gui"].use, msg: "use one of --status, --revoke, --stop, --no-open"}
	}
	cl := connect()
	defer cl.Close()
	switch {
	case f["status"] != "":
		var st guiStatus
		if err := cl.Call("gui.status", nil, &st); err != nil {
			return err
		}
		if jsonOut {
			printJSON(st)
			return nil
		}
		printGUIStatus(os.Stdout, st)
		return nil
	case f["revoke"] != "":
		var res map[string]any
		if err := cl.Call("gui.revoke", nil, &res); err != nil {
			return err
		}
		if jsonOut {
			printJSON(res)
			return nil
		}
		fmt.Printf("signed out %v browser session(s); open tabs need `pekoe gui` again\n", res["revoked"])
		return nil
	case f["stop"] != "":
		var res map[string]any
		if err := cl.Call("gui.stop", nil, &res); err != nil {
			return err
		}
		if jsonOut {
			printJSON(res)
			return nil
		}
		if res["stopped"] == true {
			fmt.Println("the local GUI stopped listening; every browser session was signed out")
		} else {
			fmt.Println("the local GUI was not listening")
		}
		return nil
	}
	var res guiOpenResult
	if err := cl.Call("gui.open", nil, &res); err != nil {
		// e.g. the port is taken: no page was written, nothing is opened
		return err
	}
	if res.File == "" {
		return errors.New("gui.open returned no page to open")
	}
	if f["no-open"] != "" {
		if jsonOut {
			printJSON(res)
			return nil
		}
		fmt.Printf("open this file in your browser within %s (it works once):\n  %s\n%s\n", time.Until(res.Expires).Round(time.Second), res.File, guiProfileNote)
		return nil
	}
	if err := openGUIPage(res.File, res.Browser); err != nil {
		return fmt.Errorf("opening %s: %w (pekoe gui --no-open prints the file to open yourself)", res.File, err)
	}
	if jsonOut {
		printJSON(res)
		return nil
	}
	fmt.Printf("opened the local GUI on 127.0.0.1:%d (sign-in link valid once, for 60 s)\n%s\n", res.Port, guiProfileNote)
	return nil
}

func printGUIStatus(w io.Writer, st guiStatus) {
	switch {
	case !st.Enabled && !st.Listening:
		fmt.Fprintln(w, "the local GUI is off ([gui] enable = false)")
		return
	case !st.Listening:
		fmt.Fprintln(w, "not listening (pekoe gui starts it)")
		return
	case !st.Enabled:
		// enable = false refuses the next gui.open only: the tabs open
		// now keep working until --stop (docs/gui.md)
		fmt.Fprintln(w, "[gui] enable = false: no new sign-in links, but the open tabs still work (pekoe gui --stop stops it)")
	}
	fmt.Fprintf(w, "listening  127.0.0.1:%d\nsessions   %d\n", st.Port, st.Sessions)
	if st.LastUsed != nil {
		fmt.Fprintf(w, "last used  %s\n", st.LastUsed.Local().Format("2006-01-02 15:04:05"))
	}
}
