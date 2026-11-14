package main

// web: the phone web UI's state.

import (
	"fmt"
	"io"
	"os"

	"pekoe/internal/paths"
)

// webStatus is web.status as `pekoe web` reads it (--json prints it as is).
type webStatus struct {
	Built   bool     `json:"built"`
	Enabled bool     `json:"enabled"`
	State   string   `json:"state"`
	AuthURL string   `json:"auth_url"`
	URLs    []string `json:"urls"`
	Error   string   `json:"error"`
	// Tailscale's backend state and its last error (a node stuck "starting")
	Backend      string `json:"backend,omitempty"`
	BackendError string `json:"backend_error,omitempty"`
}

// cmdWeb shows the phone web UI's state ([web], docs/web.md): its URL once
// the tsnet node is up, or the login URL for the first join.
func cmdWeb() error {
	cl := connect()
	defer cl.Close()
	var st webStatus
	if err := cl.Call("web.status", nil, &st); err != nil {
		return err
	}
	if jsonOut {
		printJSON(st)
		return nil
	}
	printWebStatus(os.Stdout, st, paths.GlobalConfig())
	return nil
}

// printWebStatus is the human view of `pekoe web`.
func printWebStatus(w io.Writer, st webStatus, globalConfig string) {
	if !st.Built {
		// --json still says so; here, say how to get it
		fmt.Fprintln(w, "this pekoe is built without the web UI (no Tailscale code in it).")
		if st.Enabled {
			fmt.Fprintln(w, "[web] enable = true in the config is ignored.")
		}
		fmt.Fprintln(w, "To use it, rebuild with: make install WEB=1 (docs/web.md).")
		return
	}
	if !st.Enabled {
		fmt.Fprintf(w, "web UI is off. To serve it on your tailnet, add to %s:\n\n  [web]\n  enable = true\n  allow_users = [\"you@github\"]   # your Tailscale login\n\nthen restart pekoed (pekoe daemon restart). See docs/web.md.\n", globalConfig)
		return
	}
	fmt.Fprintln(w, "state ", st.State)
	for _, u := range st.URLs {
		fmt.Fprintln(w, "url   ", u)
	}
	switch {
	case st.AuthURL != "":
		fmt.Fprintln(w, "login ", st.AuthURL)
		fmt.Fprintln(w, "\nOpen the login URL to add this machine to your tailnet (once), or restart pekoed with TS_AUTHKEY.")
	case st.State == "needs-login":
		// tsnet takes a few seconds after pekoed starts to get the URL
		// from the control server; until then there is nothing to open
		fmt.Fprintln(w, "login  (getting the login URL…)")
		fmt.Fprintln(w, "\nThe login URL is not ready yet. Run `pekoe web` again in a few seconds.")
		fmt.Fprintln(w, "If it never shows up, the machine may be waiting for approval in the Tailscale admin console (see pekoed.log).")
	}
	if st.State == "starting" && st.Backend != "" {
		fmt.Fprintln(w, "backend", st.Backend)
	}
	if st.BackendError != "" && st.State != "running" {
		fmt.Fprintln(w, "backend error", st.BackendError)
		fmt.Fprintln(w, "\nThe Tailscale backend keeps failing (see pekoed.log). If it says it cannot unmarshal or decode, this pekoe was likely built with a Go newer than its Tailscale library supports: rebuild from an up-to-date checkout (make install WEB=1).")
	}
	if st.Error != "" {
		fmt.Fprintln(w, "error ", st.Error)
	}
}
