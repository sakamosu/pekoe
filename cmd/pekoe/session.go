package main

// new / open / adopt / ls: start, reopen, take over and list sessions.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/daemon"
	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/theme"
	"pekoe/internal/tui"
)

// agentPaneSize is the pty size to ask for when spawning an agent from the
// CLI: the agent pane of a TUI in this terminal, or 0x0 when stdout is not a
// terminal (run by an agent or a script). 0 lets the daemon use the running
// TUI's pane size instead of a guess like 120x40, whose pane width (67) the
// agent would wrap at.
func agentPaneSize() (int, int) {
	w, h, ok := tui.TermSizeOK()
	if !ok {
		return 0, 0
	}
	return tui.AgentWidth(w), tui.AgentHeight(h)
}

func cmdNew(args []string) error {
	f, pos, err := parseCmd("new", args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return &usageError{use: specs["new"].use, msg: "branch required"}
	}
	var request int64
	if r := f["request"]; r != "" {
		n, err := strconv.ParseInt(strings.TrimPrefix(r, "#"), 10, 64)
		if err != nil || n < 1 {
			return &usageError{use: specs["new"].use, msg: "--request is a ledger id (pekoe ledger)"}
		}
		request = n
	}
	cl := connect()
	defer cl.Close()
	dir, err := repoDir(cl, f)
	if err != nil {
		return err
	}
	cl.Subscribe("session.progress", "session.updated")
	w, h := agentPaneSize()
	var s state.Session
	prompt := f["prompt"]
	if len(pos) > 1 && prompt == "" {
		prompt = strings.Join(pos[1:], " ")
	}
	if err := cl.Call("session.new", map[string]any{
		"repo": dir, "branch": pos[0], "from": f["from"], "agent": f["agent"], "prompt": prompt,
		"base": f["base"], "after": multiVal(f, "after"), "width": w, "height": h, "request": request,
	}, &s); err != nil {
		return err
	}
	if jsonOut {
		printJSON(s)
		return nil
	}
	fmt.Printf("%s → %s :%d\n", s.Branch, s.Path, s.Port)
	// follow progress until the agent starts or creation fails
	icons := theme.Icons(theme.IconsFromEnv())
	printed := map[string]string{}
	timeout := time.After(60 * time.Minute)
	for {
		select {
		case ev, ok := <-cl.Events:
			if !ok {
				return nil
			}
			switch ev.Event {
			case "session.progress":
				var p struct {
					Slug  string        `json:"slug"`
					Steps []daemon.Step `json:"steps"`
				}
				json.Unmarshal(ev.Data, &p)
				if p.Slug != s.Slug {
					continue
				}
				for _, st := range p.Steps {
					if printed[st.Name] == st.Status || st.Status == "pending" || st.Status == "running" {
						continue
					}
					printed[st.Name] = st.Status
					mark := icons.Check
					if st.Status == "fail" {
						mark = icons.Fail
					}
					fmt.Printf("  %s %-9s %s %s\n", mark, st.Label, st.Detail, fmtMs(st.Elapsed))
				}
			case "session.updated":
				var u state.Session
				json.Unmarshal(ev.Data, &u)
				if u.Slug != s.Slug {
					continue
				}
				if u.State == state.Failed {
					return fmt.Errorf("%s", u.Error)
				}
				if u.State != state.Creating {
					fmt.Printf("%s started · open with `pekoe` · attach raw with `pekoe attach %s`\n", u.Agent, u.Slug)
					return nil
				}
			}
		case <-timeout:
			return fmt.Errorf("timed out waiting for setup")
		}
	}
}

func fmtMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func cmdOpen(args []string) error {
	f, pos, err := parseCmd("open", args)
	if err != nil {
		return err
	}
	dir := cwd()
	if len(pos) > 0 {
		dir, _ = filepath.Abs(pos[0])
	}
	resume := "ask"
	if f["resume"] != "" {
		resume = "resume"
	}
	if f["new"] != "" {
		resume = "new"
	}
	cl := connect()
	defer cl.Close()
	w, h := agentPaneSize()
	params := map[string]any{"path": dir, "resume": resume, "agent": f["agent"], "width": w, "height": h}
	var raw json.RawMessage
	if err := cl.Call("session.open", params, &raw); err != nil {
		return err
	}
	var choice struct {
		NeedsChoice bool `json:"needs_choice"`
		Past        []struct {
			ID       string    `json:"id"`
			Modified time.Time `json:"modified"`
		} `json:"past"`
	}
	json.Unmarshal(raw, &choice)
	if choice.NeedsChoice {
		if jsonOut || !stdinIsTerminal() {
			// nobody to ask (a script): resuming one on its behalf would
			// pick for it, and the question would break --json's stdout
			return fmt.Errorf("past Claude Code sessions in %s: pass --resume (the latest) or --new", dir)
		}
		fmt.Printf("past Claude Code sessions in %s:\n", dir)
		for i, p := range choice.Past {
			if i >= 5 {
				break
			}
			fmt.Printf("  %d) %s  %s\n", i+1, p.ID, p.Modified.Format("01-02 15:04"))
		}
		fmt.Print("resume which? (number, enter = 1, n = new): ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimSpace(line)
		switch {
		case line == "n" || line == "new":
			params["resume"] = "new"
		case line == "":
			params["resume"] = choice.Past[0].ID
		default:
			n, err := strconv.Atoi(line)
			if err != nil || n < 1 || n > len(choice.Past) {
				return fmt.Errorf("invalid choice")
			}
			params["resume"] = choice.Past[n-1].ID
		}
		if err := cl.Call("session.open", params, &raw); err != nil {
			return err
		}
	}
	var s state.Session
	json.Unmarshal(raw, &s)
	if jsonOut {
		printJSON(s)
		return nil
	}
	fmt.Printf("opened %s (%s) :%d\n", s.Slug, s.Path, s.Port)
	return nil
}

func cmdAdopt(args []string) error {
	f, pos, err := parseCmd("adopt", args)
	if err != nil {
		return err
	}
	path := ""
	if len(pos) > 0 {
		path, _ = filepath.Abs(pos[0])
	}
	cl := connect()
	defer cl.Close()
	type cand struct {
		PID       int    `json:"pid"`
		Cwd       string `json:"cwd"`
		Where     string `json:"where"`
		SessionID string `json:"session_id"`
		Skip      string `json:"skip"`
	}
	var res struct {
		Candidates []cand `json:"candidates"`
		Skipped    []cand `json:"skipped"`
	}
	if err := cl.Call("session.adopt", map[string]any{"path": path}, &res); err != nil {
		return err
	}
	if len(res.Skipped) > 0 {
		fmt.Println("left running (not adopted):")
		for _, c := range res.Skipped {
			fmt.Printf("  pid %-6d %-8s %s  %s\n", c.PID, c.Where, c.Cwd, c.Skip)
		}
	}
	if len(res.Candidates) == 0 {
		if len(res.Skipped) == 0 {
			fmt.Println("no unmanaged agents found")
		}
		return nil
	}
	fmt.Println("agents running outside pekoe:")
	for _, c := range res.Candidates {
		fmt.Printf("  pid %-6d %-8s %s  session %s\n", c.PID, c.Where, c.Cwd, c.SessionID)
	}
	fmt.Println("adopting stops each one (Ctrl-C twice) and resumes the same session inside pekoe.")
	if f["yes"] == "" && !confirm("adopt them?") {
		return nil
	}
	var adopted []state.Session
	if err := cl.Call("session.adopt", map[string]any{"path": path, "confirm": true}, &adopted); err != nil {
		return err
	}
	for _, s := range adopted {
		fmt.Printf("adopted %s (%s)\n", s.Slug, s.Path)
	}
	return nil
}

// cmdLs lists sessions. Inside a repository (or a worktree of one) it shows
// only that repository's sessions; --all shows every repository and
// --repo another one. Outside a repository it shows everything.
func cmdLs(args []string) error {
	f, _, err := parseCmd("ls", args)
	if err != nil {
		return err
	}
	cl := connect()
	defer cl.Close()
	scope, err := lsScope(f, func(q string) (*daemon.KnownRepo, error) { return resolveRepoFlag(cl, q) }, func() *daemon.KnownRepo { return hereRepo(cl) })
	if err != nil {
		return err
	}
	params := map[string]any{}
	if scope != nil {
		params["repo_id"] = scope.ID
	}
	var ss []*state.Session
	if err := cl.Call("session.list", params, &ss); err != nil {
		return err
	}
	if jsonOut {
		printJSON(ss)
		return nil
	}
	if len(ss) == 0 {
		if scope != nil {
			fmt.Printf("no sessions in %s · `pekoe ls --all` for every repository · start one with `pekoe new <branch>`\n", scope.Name)
			return nil
		}
		fmt.Println("no sessions · start one with `pekoe new <branch>`")
		return nil
	}
	ic := theme.Icons(theme.IconsFromEnv())
	th := theme.Load(nil)
	for _, l := range lsLines(th, ic, ss, scope == nil, time.Now()) {
		fmt.Println(l)
	}
	return nil
}

// lsScope is the repository `pekoe ls` is limited to, nil for every one:
// --repo wins, --all shows everything, else the current directory's
// repository (nil outside one).
func lsScope(f map[string]string, resolve func(string) (*daemon.KnownRepo, error), here func() *daemon.KnownRepo) (*daemon.KnownRepo, error) {
	switch {
	case f["repo"] != "":
		return resolve(f["repo"])
	case f["all"] != "":
		return nil, nil
	}
	return here(), nil
}

// lsLines renders `pekoe ls`: sessions in creation order, grouped under a
// heading per repository when grouped and there is more than one. A slug
// that is not just the branch (a collision put the repository name in
// front) is shown, since send / land / rm take the slug.
func lsLines(th *theme.Theme, ic theme.IconSet, ss []*state.Session, grouped bool, now time.Time) []string {
	ss = append([]*state.Session(nil), ss...)
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].CreatedAt.Before(ss[j].CreatedAt) })
	line := func(s *state.Session) string {
		l := theme.SessionLine(th, ic, s, now, 0)
		if s.Slug != paths.Slug(s.Branch) {
			l += "  " + th.FG(theme.Dim, "slug "+s.Slug)
		}
		return l
	}
	type group struct {
		id, name string
		ss       []*state.Session
	}
	var gs []*group
	by := map[string]*group{}
	for _, s := range ss {
		g := by[s.RepoID]
		if g == nil {
			g = &group{id: s.RepoID, name: s.Repo}
			by[s.RepoID] = g
			gs = append(gs, g)
		}
		g.ss = append(g.ss, s)
	}
	var out []string
	if !grouped || len(gs) < 2 {
		for _, s := range ss {
			out = append(out, line(s))
		}
		return out
	}
	sort.SliceStable(gs, func(i, j int) bool {
		if gs[i].name != gs[j].name {
			return gs[i].name < gs[j].name
		}
		return gs[i].id < gs[j].id
	})
	names := map[string]int{}
	for _, g := range gs {
		names[g.name]++
	}
	for i, g := range gs {
		if i > 0 {
			out = append(out, "")
		}
		h := th.Bold(theme.Primary, g.name)
		if names[g.name] > 1 || g.name == "" {
			h += "  " + th.FG(theme.Dim, g.id)
		}
		out = append(out, h)
		for _, s := range g.ss {
			out = append(out, line(s))
		}
	}
	return out
}
