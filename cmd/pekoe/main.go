// pekoe — run coding agents in parallel, one per git worktree, from your terminal.
//
//	pekoe                      open the TUI (starts pekoed if needed)
//	pekoe init                 draft this repository's config
//	pekoe new <branch> [--repo R] [--from gh#N] [--agent codex] [--prompt text]
//	pekoe open <worktree> [--resume|--new]
//	pekoe adopt                bring agents started by hand under pekoe
//	pekoe ls [--all|--repo R]  sessions and states (default: this repository)
//	pekoe send <slug> <text>   write to the agent's pty (Enter appended)
//	pekoe event <Hook>         called by Claude Code hooks (stdin JSON)
//	pekoe review [<slug>|gh#N] open the review surface
//	pekoe land <slug> / pekoe rm <slug> [--force]
//	pekoe report <summary> --status done   a session reports its work
//	pekoe reports [<slug>]     the reports, newest first
//	pekoe wait [--mine] [--since CURSOR]  until something happens to sessions
//	pekoe dev <slug>           start/stop the dev server on the session port
//	pekoe undo <slug> [n]      throw away the changes of the last (or n-th) prompt
//	pekoe answer <slug> allow|deny|always
//	pekoe attach <slug>        attach the raw tmux window (debug)
//	pekoe hooks install|uninstall|status
//	pekoe skills install|uninstall|status
//	pekoe ext                  port and token for the Chrome extension
//	pekoe mcp                  MCP server (stdio) for agents
//	pekoe daemon start|stop|restart|status|run
//	pekoe tapi [reset]         tapi chat (TUI: t): state, or forget the conversation
//	pekoe update [--check] [--version vX.Y.Z]  replace pekoe with a GitHub release
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"pekoe"
	"pekoe/internal/client"
	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/hooks"
	"pekoe/internal/mascot"
	"pekoe/internal/mcp"
	"pekoe/internal/paths"
	"pekoe/internal/skills"
	"pekoe/internal/state"
	"pekoe/internal/theme"
	"pekoe/internal/tui"
)

var jsonOut bool

func main() {
	// global flags, before or after the subcommand
	args, global := splitGlobal(os.Args[1:])
	jsonOut = global
	if filepath.Base(os.Args[0]) == "pekoed" {
		args = append([]string{"daemon", "run"}, args...)
	}
	if len(args) == 0 {
		die(runTUI())
		return
	}
	cmd, args := args[0], args[1:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "new":
		err = cmdNew(args)
	case "open":
		err = cmdOpen(args)
	case "adopt":
		err = cmdAdopt(args)
	case "ls", "list":
		err = cmdLs(args)
	case "issues":
		err = cmdIssues(args)
	case "conflicts":
		err = cmdConflicts(args)
	case "send":
		err = cmdSend(args)
	case "event":
		if _, _, err := parseCmd("event", args); err != nil && helpRequested(err) {
			exitUsage(err)
		}
		cmdEvent(args) // never fails the hook (unknown flags included)
		return
	case "review":
		err = cmdReview(args)
	case "land":
		err = cmdSimple("land", "land", args)
	case "rm":
		err = cmdRm(args)
	case "dev":
		err = cmdSimple("dev", "dev.toggle", args)
	case "undo":
		err = cmdUndo(args)
	case "answer":
		err = cmdAnswer(args)
	case "attach":
		err = cmdAttach(args)
	case "hooks":
		err = cmdHooks(args)
	case "skills":
		err = cmdSkills(args)
	case "ext":
		if _, _, err = parseCmd("ext", args); err == nil {
			err = cmdExt()
		}
	case "report":
		err = cmdReport(args)
	case "reports":
		err = cmdReports(args)
	case "wait":
		err = cmdWait(args)
	case "ask-human":
		err = cmdAskHuman(args)
	case "inbox":
		err = cmdInbox(args)
	case "ledger":
		err = cmdLedger(args)
	case "web":
		if _, _, err = parseCmd("web", args); err == nil {
			err = cmdWeb()
		}
	case "mcp":
		if _, _, err = parseCmd("mcp", args); err == nil {
			err = mcp.Serve(os.Stdin, os.Stdout)
		}
	case "daemon":
		err = cmdDaemon(args)
	case "update":
		err = cmdUpdate(args)
	case "tapi":
		err = cmdTapi(args)
	case "tip", "tips":
		err = cmdTip(args)
	case "help", "-h", "--help":
		usage()
	case "version", "--version":
		fmt.Println("pekoe", daemon.Version)
	default:
		usage()
		os.Exit(2)
	}
	exitUsage(err)
	die(err)
}

// helpTapi is tapi on top of `pekoe --help` (a still image).
func helpTapi() string {
	g, err := config.LoadGlobal()
	if err != nil {
		g = &config.Global{}
	}
	icons := theme.IconsFromEnv()
	r := mascot.New(mascot.Options{
		Shape:  mascot.DetectShape(g.Mascot.Shape),
		Glyphs: mascot.DetectGlyphs(g.Mascot.Glyphs, icons, nil),
		Light:  g.Mascot.LightPalette(),
		Colors: g.Mascot.Colors,
	})
	text := []string{"pekoe v" + daemon.Version, "agents in worktrees,", "reviewed hunk by hunk"}
	width := 0 // not a terminal: no limit
	if tw, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && tw > 1 {
		// left-aligned, so only the last column is kept free: a glyph the
		// font draws two cells wide must not run past the right edge
		width = tw - 1
	}
	return strings.Join(r.FitBanner(r.Eyes(mascot.Work), text, width, 0), "\n") + "\n"
}

func usage() {
	fmt.Print(helpTapi())
	fmt.Print(`

  pekoe                         open the TUI
  pekoe init                    draft ~/.config/pekoe/repos/<repo-id>.toml
  pekoe new <branch> [--repo R] [--from gh#N] [--agent codex] [--prompt TEXT] [--request ID]
  pekoe open <worktree> [--resume|--new]
  pekoe adopt [--yes]           take over agents started by hand
  pekoe ls [--all|--repo R] [--json]   sessions (default: this repository; outside one, all)
  pekoe issues [--repo R] [--all|--label L] [--refresh] [--json]   open GitHub issues (default: assigned to you)
  pekoe issues <N> [--repo R] [--json]  an issue's body and comments
  pekoe conflicts [--repo R] [--json]  sessions touching the same files (conflict forecast)
  pekoe send <slug> <text>      <slug> may also be <repo>/<slug>
  pekoe review [<slug> | gh#N [--repo R]]
  pekoe land <slug>             rebase → test → gh pr create (teardown after merge)
  pekoe rm <slug> [--force]     teardown → delete worktree
  pekoe report <summary> --status done|blocked|progress|review [--evidence E]...
                               a session reports its work (run by its agent)
  pekoe reports [<slug>] [--json]   the sessions' reports, newest first
  pekoe wait [--mine] [--slug S] [--on KINDS] [--since CURSOR] [--timeout 30m] [--json]
                               block until something happens to sessions; pass the
                               returned cursor to the next wait's --since
  pekoe ask-human <question> [--option O]... [--recommend N] [--kind K] [--wait]
                               ask a person (run by an agent); the answer comes back
  pekoe inbox [<id>] [--all]    the questions agents asked; pekoe inbox answer <id> <n>
  pekoe ledger [--all] [--json] what you asked tapi chat for; tapi closes them with
                               pekoe ledger done|drop|close|link, you reopen|rm
  pekoe dev <slug>              toggle the dev server on the session's port
  pekoe undo <slug> [n]         undo the last (or n-th) prompt's changes
  pekoe answer <slug> allow|deny|always
  pekoe hooks install|uninstall|status
  pekoe skills install|uninstall|status [--yes]
                               link pekoe's Claude Code skills from ~/.claude/skills
  pekoe ext                     port + token for the Chrome extension
  pekoe web                     phone web UI on the tailnet: state, URL, login link ([web])
  pekoe mcp                     MCP server for agents (claude mcp add pekoe -- pekoe mcp)
  pekoe tapi [reset [--yes]]    tapi chat (t in the TUI): state, or end it and forget
                               the conversation
  pekoe tip [<id>] [--list] [--doc PATH]
                               a tip about pekoe, one not shown yet ([tips])
  pekoe daemon start|stop|restart|status
                               restart: stop, wait, start the installed binary
                               (after make install; agents keep running)
  pekoe daemon stop --kill-agents [--yes]
                               also stop the tmux server ($PEKOE_TMUX_SOCKET) and
                               ALL its agents · for sandboxes; on the default
                               socket "pekoe" this kills every real agent
  pekoe update [--check] [--version vX.Y.Z] [--no-web] [--force] [--yes]
                               replace pekoe with the latest GitHub release
                               (--check: only tell whether there is a newer one)

  R is a repo-id, a short name (as in ~/worktrees/<name>) or a path.
`)
}

func die(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "pekoe:", err)
	os.Exit(1)
}

func connect() *client.Client {
	cl, err := client.Connect()
	die(err)
	return cl
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func cwd() string {
	d, _ := os.Getwd()
	return d
}

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

// ---- commands ----

func runTUI() error {
	cl := connect()
	defer cl.Close()
	if !hooks.Installed() {
		fmt.Fprintln(os.Stderr, "hint: Claude Code hooks are not installed; run `pekoe hooks install` so pekoe can see agent states.")
	}
	return tui.Run(cl, cwd())
}

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
	return cl.Call("init.write", map[string]any{"path": dir, "toml": toml, "overwrite": true}, nil)
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

func confirm(q string) bool {
	fmt.Printf("%s [y/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
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

// resolveRepoFlag asks pekoed which repository a --repo value (repo-id,
// short name or path) means.
func resolveRepoFlag(cl *client.Client, q string) (*daemon.KnownRepo, error) {
	if q == "true" { // --repo with no value
		return nil, fmt.Errorf("--repo needs a repo-id, a short name or a path")
	}
	var r daemon.KnownRepo
	if err := cl.Call("repo.resolve", map[string]any{"repo": q, "cwd": cwd()}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// hereRepo is the repository of the current directory, nil outside one.
func hereRepo(cl *client.Client) *daemon.KnownRepo {
	var r daemon.KnownRepo
	if err := cl.Call("repo.resolve", map[string]any{"repo": cwd(), "cwd": cwd()}, &r); err != nil {
		return nil
	}
	return &r
}

// repoDir is the repository a command acts on: --repo when given, else the
// current directory. pekoed resolves worktrees to their main repository.
func repoDir(cl *client.Client, f map[string]string) (string, error) {
	if f["repo"] == "" {
		return cwd(), nil
	}
	r, err := resolveRepoFlag(cl, f["repo"])
	if err != nil {
		return "", err
	}
	if r.Dir == "" {
		return "", fmt.Errorf("%s is known only by its config (%s); pass its path to --repo", r.Name, r.ID)
	}
	return r.Dir, nil
}

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

// cmdConflicts prints the conflict forecast: pairs of open sessions of the
// same repository that touch the same files, and whether their committed
// contents would conflict (git merge-tree) or merge automatically.
func cmdConflicts(args []string) error {
	f, _, err := parseCmd("conflicts", args)
	if err != nil {
		return err
	}
	cl := connect()
	defer cl.Close()
	id := ""
	if f["repo"] != "" {
		r, err := resolveRepoFlag(cl, f["repo"])
		if err != nil {
			return err
		}
		id = r.ID
	}
	var ps []daemon.ConflictPair
	if err := cl.Call("conflicts.list", map[string]any{"repo_id": id}, &ps); err != nil {
		return err
	}
	if jsonOut {
		printJSON(ps)
		return nil
	}
	if len(ps) == 0 {
		fmt.Println("no overlapping sessions")
		return nil
	}
	ic := theme.Icons(theme.IconsFromEnv())
	th := theme.Load(nil)
	repos := map[string]bool{}
	for _, p := range ps {
		repos[p.RepoID] = true
	}
	for _, p := range ps {
		role, word, files := theme.Warn, "overlap ", strings.Join(p.Files, ", ")+th.FG(theme.Dim, "  (auto-merges)")
		if p.Status == "conflict" {
			role, word, files = theme.Err, "conflict", strings.Join(p.ConflictFiles, ", ")
		}
		repo := ""
		if len(repos) > 1 {
			repo = th.FG(theme.Dim, p.RepoID) + "  "
		}
		fmt.Printf("%s %s  %s%s %s %s  %s\n", th.FG(role, ic.Warning), th.FG(role, word), repo, p.A, th.FG(theme.Dim, "↔"), p.B, files)
	}
	return nil
}

func cmdSend(args []string) error {
	f, pos, err := parseCmd("send", args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: pekoe send <slug> <text>   (text '-' reads stdin)")
	}
	text := strings.Join(pos[1:], " ")
	if text == "-" {
		b, _ := io.ReadAll(os.Stdin)
		text = strings.TrimRight(string(b), "\n")
	}
	cl := connect()
	defer cl.Close()
	return cl.Call("session.send", map[string]any{"slug": pos[0], "text": text, "submit": f["no-enter"] == ""}, nil)
}

// cmdEvent forwards a hook's stdin JSON to pekoed. It must be fast and must
// never break Claude Code: errors are swallowed, exit code is always 0.
func cmdEvent(args []string) {
	hook := ""
	if len(args) > 0 {
		hook = args[0]
	}
	b, _ := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
	if len(b) == 0 {
		b = []byte("{}")
	}
	if !json.Valid(b) {
		return
	}
	cl, err := client.Dial()
	if err != nil {
		return
	}
	defer cl.Close()
	_ = cl.CallTimeout("session.event", map[string]any{
		"hook": hook, "session": os.Getenv("PEKOE_SESSION"), "payload": json.RawMessage(b),
	}, nil, 8*time.Second)
}

func cmdReview(args []string) error {
	f, pos, err := parseCmd("review", args)
	if err != nil {
		return err
	}
	cl := connect()
	defer cl.Close()
	if len(pos) > 0 && strings.HasPrefix(pos[0], "gh#") {
		n, err := strconv.Atoi(strings.TrimPrefix(pos[0], "gh#"))
		if err != nil {
			return fmt.Errorf("bad PR number: %s", pos[0])
		}
		dir, err := repoDir(cl, f)
		if err != nil {
			return err
		}
		var s state.Session
		if err := cl.Call("review.pr", map[string]any{"repo": dir, "number": n}, &s); err != nil {
			return err
		}
		return tui.RunReview(cl, s.Slug)
	}
	slug := ""
	if len(pos) > 0 {
		// <repo>/<slug> → the slug the review surface keys everything on
		var s state.Session
		if err := cl.Call("session.get", map[string]any{"slug": pos[0]}, &s); err != nil {
			return err
		}
		slug = s.Slug
	} else {
		var s *state.Session
		if err := cl.Call("review.next", nil, &s); err != nil {
			return err
		}
		if s == nil {
			return fmt.Errorf("nothing to review")
		}
		slug = s.Slug
	}
	return tui.RunReview(cl, slug)
}

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

func cmdUndo(args []string) error {
	_, args, err := parseCmd("undo", args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: pekoe undo <slug> [n]")
	}
	n := 0
	if len(args) > 1 {
		// a typo must not fall back to the last prompt's snapshot
		if n, err = strconv.Atoi(args[1]); err != nil || n < 1 {
			return &usageError{use: specs["undo"].use, msg: "n must be a number from 1: " + args[1]}
		}
	}
	cl := connect()
	defer cl.Close()
	var res map[string]any
	if err := cl.Call("session.undo", map[string]any{"slug": args[0], "n": n}, &res); err != nil {
		return err
	}
	fmt.Printf("restored to snapshot #%v (%v)\n", res["restored"], res["prompt"])
	return nil
}

func cmdAnswer(args []string) error {
	_, args, err := parseCmd("answer", args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: pekoe answer <slug> allow|deny|always")
	}
	cl := connect()
	defer cl.Close()
	return cl.Call("session.answer", map[string]any{"slug": args[0], "answer": args[1]}, nil)
}

func cmdAttach(args []string) error {
	_, args, err := parseCmd("attach", args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: pekoe attach <slug> [agent|review|dev]")
	}
	w := "agent"
	if len(args) > 1 {
		w = args[1]
	}
	sock := paths.TmuxSocketName()
	tm, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	return syscall.Exec(tm, []string{"tmux", "-L", sock, "attach-session", "-t", "pekoe:=" + args[0] + "~" + w}, os.Environ())
}

func cmdHooks(args []string) error {
	f, pos, err := parseCmd("hooks", args)
	if err != nil {
		return err
	}
	sub := "status"
	if len(pos) > 0 {
		sub = pos[0]
	}
	bin, _ := os.Executable()
	if b, err := filepath.EvalSymlinks(bin); err == nil {
		bin = b
	}
	switch sub {
	case "status":
		if hooks.Installed() {
			fmt.Println("installed in", hooks.SettingsPath())
		} else {
			fmt.Println("not installed · run `pekoe hooks install`")
		}
		return nil
	case "install", "uninstall":
		path, before, after, changed, err := hooks.Plan(bin, sub == "uninstall")
		if err != nil {
			return err
		}
		if !changed {
			fmt.Println("already up to date:", path)
			return nil
		}
		fmt.Printf("%s will be updated (%s pekoe hooks for %s).\n", path, map[bool]string{true: "remove", false: "add"}[sub == "uninstall"], strings.Join(hooks.Events, ", "))
		if f["yes"] == "" && !confirm("write it? (a backup is kept)") {
			return nil
		}
		backup, err := hooks.Apply(path, before, after)
		if err != nil {
			return err
		}
		if backup != "" {
			fmt.Println("backup:", backup)
		}
		fmt.Println("done:", path)
		return nil
	}
	return fmt.Errorf("usage: pekoe hooks install|uninstall|status")
}

func cmdExt() error {
	cl := connect()
	defer cl.Close()
	var info struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := cl.Call("ext.info", nil, &info); err != nil {
		return err
	}
	if jsonOut {
		printJSON(info)
		return nil
	}
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	extDir := filepath.Join(filepath.Dir(filepath.Dir(exe)), "extension")
	if alt := filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "pekoe", "extension"); hintDir(extDir) == "" && hintDir(alt) != "" {
		extDir = alt
	}
	fmt.Printf("port   %d\ntoken  %s\n\nChrome: chrome://extensions → Developer mode → Load unpacked\n        (choose extension/ in the pekoe repository%s) → enter port and token in the extension's options\n", info.Port, info.Token, hintDir(extDir))
	return nil
}

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

func hintDir(d string) string {
	if _, err := os.Stat(filepath.Join(d, "manifest.json")); err == nil {
		return ": " + d
	}
	return ""
}

// daemonStopTimeout bounds how long `pekoe daemon stop` waits for pekoed's
// process to exit (its shutdown closes HTTP within 1 s, then tmux and state.db).
var daemonStopTimeout = 10 * time.Second

func cmdDaemon(args []string) error {
	sub, name := "status", "daemon"
	if len(args) > 0 && !isFlag(args[0]) {
		sub, args = args[0], args[1:]
		name = "daemon " + sub
	}
	if _, ok := specs[name]; !ok {
		name = "daemon" // unknown subcommand: --help still answers
	}
	f, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return &usageError{use: specs[name].use, msg: "unexpected argument " + pos[0]}
	}
	switch sub {
	case "run":
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		d, err := daemon.New()
		if err != nil {
			return err
		}
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() { <-sig; d.Stop() }()
		signal.Ignore(syscall.SIGHUP)
		// a new pekoe brings its skills' text to an existing install
		if err := skills.Refresh(pekoe.Docs, skillDirs()); err != nil {
			log.Printf("skills: %v", err)
		}
		return d.Run()
	case "start":
		if cl, err := client.Dial(); err == nil {
			cl.Close()
			fmt.Println("pekoed is already running")
			return nil
		}
		cl := connect()
		cl.Close()
		fmt.Println("pekoed started · log:", paths.DaemonLog())
		return nil
	case "stop":
		if f["kill-agents"] != "" {
			return stopKillAgents(f["yes"] != "")
		}
		running, err := client.StopDaemon(nil, daemonStopTimeout)
		if err != nil {
			return err
		}
		if !running {
			fmt.Println("pekoed is not running")
			return nil
		}
		fmt.Println("pekoed stopped (agents keep running in tmux; they are picked up on next start)")
		return nil
	case "restart":
		return restartDaemon()
	case "status":
		cl, err := client.Dial()
		if err != nil {
			fmt.Println("pekoed is not running")
			return nil
		}
		defer cl.Close()
		var st map[string]any
		if err := cl.Call("daemon.status", nil, &st); err != nil {
			return err
		}
		if jsonOut {
			printJSON(st)
			return nil
		}
		fmt.Printf("pekoed %v · pid %v · %v sessions · socket %v · http :%v\n", st["version"], st["pid"], st["sessions"], st["socket"], st["http_port"])
		return nil
	}
	return &usageError{use: specs["daemon"].use, msg: "unknown subcommand " + sub}
}

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

// stopKillAgents stops pekoed and its tmux server ($PEKOE_TMUX_SOCKET), ending
// every agent on it. Meant for throwaway sandboxes; works when pekoed is
// already gone, too.
func stopKillAgents(yes bool) error {
	sock := paths.TmuxSocketName()
	if !yes {
		if !stdinIsTerminal() {
			return fmt.Errorf("--kill-agents ends every agent on tmux server %q; pass --yes to confirm", sock)
		}
		warn := ""
		if sock == "pekoe" {
			warn = " This is the default (real) server: ALL your agents will be killed."
		}
		if !confirm(fmt.Sprintf("stop pekoed and tmux server %q, killing its agents?%s", sock, warn)) {
			return nil
		}
	}
	// waits for pekoed to go, so it does not restart the server under us
	running, err := client.StopDaemon(map[string]any{"kill_agents": true}, daemonStopTimeout)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("pekoed stopped")
	} else {
		fmt.Println("pekoed is not running")
	}
	// pekoed killed the server on its way out; this catches what it could not
	// (pekoed already gone, or a server it never reached).
	tm := engine.NewTmux(sock)
	tm.PidFile = paths.TmuxPidFile(sock)
	if err := tm.KillServer(); err != nil {
		return err
	}
	fmt.Printf("tmux server %q stopped\n", sock)
	return nil
}

func stdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
