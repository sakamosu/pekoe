package main

// init: draft this repository's config.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"pekoe/internal/client"
	"pekoe/internal/state"
)

func cmdInit(args []string) error {
	f, pos, err := parseCmd("init", args)
	if err != nil {
		return err
	}
	dir := cwd()
	if len(pos) > 0 {
		dir = pos[0]
	}
	cl := connect()
	defer cl.Close()
	var res struct {
		RepoID     string   `json:"repo_id"`
		ConfigPath string   `json:"config_path"`
		Exists     bool     `json:"exists"`
		Matched    []string `json:"matched"`
		Ignored    []string `json:"ignored"`
		Selected   []string `json:"selected"`
		TOML       string   `json:"toml"`
	}
	if err := cl.Call("init.detect", map[string]any{"path": dir}, &res); err != nil {
		return err
	}
	if jsonOut {
		printJSON(res)
		return nil
	}
	fmt.Printf("repo-id   %s\nconfig    %s\ndetected  %s\n", res.RepoID, res.ConfigPath, strings.Join(res.Matched, ", "))
	selected := res.Selected
	if len(res.Ignored) > 0 && f["yes"] == "" {
		fmt.Println("\nignored files that exist here (copied into new worktrees when selected):")
		sel := map[string]bool{}
		for _, s := range res.Selected {
			sel[s] = true
		}
		for i, p := range res.Ignored {
			mark := " "
			if sel[p] {
				mark = "x"
			}
			fmt.Printf("  [%s] %2d %s\n", mark, i+1, p)
		}
		fmt.Print("toggle numbers (e.g. 1 3), enter to accept: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		for _, tok := range strings.Fields(line) {
			n, err := strconv.Atoi(tok)
			if err == nil && n >= 1 && n <= len(res.Ignored) {
				p := res.Ignored[n-1]
				sel[p] = !sel[p]
			}
		}
		selected = nil
		for _, p := range res.Ignored {
			if sel[p] {
				selected = append(selected, p)
			}
		}
	}
	toml := replaceCopy(res.TOML, selected)
	if f["print"] != "" {
		fmt.Print(toml)
		return nil
	}
	fmt.Printf("\n%s\n", toml)
	if res.Exists && f["overwrite"] == "" {
		if !confirm(fmt.Sprintf("%s exists. overwrite?", res.ConfigPath)) {
			return nil
		}
	} else if f["yes"] == "" && !confirm("write this config?") {
		return nil
	}
	var wrote struct {
		RepoDir string `json:"repo_dir"`
	}
	if err := cl.Call("init.write", map[string]any{"path": dir, "toml": toml, "overwrite": true}, &wrote); err != nil {
		return err
	}
	if f["no-open"] == "" {
		openMainSession(cl, res.RepoID, wrote.RepoDir, dir)
	}
	return nil
}

// openMainSession starts the agent in the repository's main checkout (as
// `pekoe open <repo> --new` does), so the repository shows up in the TUI
// right after init. One that is already there is left alone (init can run
// again), and a failure does not fail init: the config is written by then.
func openMainSession(cl *client.Client, repoID, repoDir, dir string) {
	fail := func(err error) {
		at := repoDir
		if at == "" {
			at = dir
		}
		fmt.Fprintf(os.Stderr, "the main session was not started (%v); start it with `pekoe open %s --new`\n", err, at)
	}
	if repoDir == "" { // a pekoed from before init.write said where
		fail(fmt.Errorf("pekoed did not say where the repository is"))
		return
	}
	var ss []*state.Session
	if err := cl.Call("session.list", map[string]any{"repo_id": repoID}, &ss); err != nil {
		fail(err)
		return
	}
	for _, s := range ss {
		if s.Path == repoDir || s.Path == s.RepoDir {
			fmt.Printf("main session %s is already there\n", s.Slug)
			return
		}
	}
	w, h := agentPaneSize()
	var s state.Session
	if err := cl.Call("session.open", map[string]any{"path": repoDir, "resume": "new", "width": w, "height": h}, &s); err != nil {
		fail(err)
		return
	}
	fmt.Printf("opened %s (%s) :%d\n", s.Slug, s.Path, s.Port)
}

func replaceCopy(toml string, sel []string) string {
	q := make([]string, len(sel))
	for i, s := range sel {
		b, _ := json.Marshal(s)
		q[i] = string(b)
	}
	lines := strings.Split(toml, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "copy  = ") {
			lines[i] = "copy  = [" + strings.Join(q, ", ") + "]"
		}
	}
	return strings.Join(lines, "\n")
}
