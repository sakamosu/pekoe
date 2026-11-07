// boba — Orca-style multi-agent workflow inside Ghostty.
//
//	boba                      open the TUI (starts bobad if needed)
//	boba init                 draft this repository's config
//	boba new <branch> [--from gh#N] [--agent codex] [--prompt text]
//	boba open <worktree> [--resume|--new]
//	boba adopt                bring agents started by hand under boba
//	boba ls                   sessions and states
//	boba send <slug> <text>   write to the agent's pty (Enter appended)
//	boba event <Hook>         called by Claude Code hooks (stdin JSON)
//	boba review [<slug>|gh#N] open the review surface
//	boba land <slug> / boba rm <slug> [--force]
//	boba dev <slug>           start/stop the dev server on the session port
//	boba undo <slug> [n]      throw away the changes of the last (or n-th) prompt
//	boba answer <slug> allow|deny|always
//	boba attach <slug>        attach the raw tmux window (debug)
//	boba hooks install|uninstall|status
//	boba ext                  port and token for the Chrome extension
//	boba mcp                  MCP server (stdio) for agents
//	boba daemon start|stop|status|run
package main

import (
	"bufio"
	"encoding/json"
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

	"boba/internal/client"
	"boba/internal/config"
	"boba/internal/daemon"
	"boba/internal/hooks"
	"boba/internal/mascot"
	"boba/internal/mcp"
	"boba/internal/paths"
	"boba/internal/state"
	"boba/internal/theme"
	"boba/internal/tui"
)

var jsonOut bool

func main() {
	args := os.Args[1:]
	// global flags
	var rest []string
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
			continue
		}
		rest = append(rest, a)
	}
	args = rest
	if filepath.Base(os.Args[0]) == "bobad" {
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
		err = cmdLs()
	case "conflicts":
		err = cmdConflicts(args)
	case "send":
		err = cmdSend(args)
	case "event":
		cmdEvent(args) // never fails the hook
		return
	case "review":
		err = cmdReview(args)
	case "land":
		err = cmdSimple("land", args, "land <slug>")
	case "rm":
		err = cmdRm(args)
	case "dev":
		err = cmdSimple("dev.toggle", args, "dev <slug>")
	case "undo":
		err = cmdUndo(args)
	case "answer":
		err = cmdAnswer(args)
	case "attach":
		err = cmdAttach(args)
	case "hooks":
		err = cmdHooks(args)
	case "ext":
		err = cmdExt()
	case "mcp":
		err = mcp.Serve(os.Stdin, os.Stdout)
	case "daemon":
		err = cmdDaemon(args)
	case "help", "-h", "--help":
		usage()
	case "version", "--version":
		fmt.Println("boba", daemon.Version)
	default:
		usage()
		os.Exit(2)
	}
	die(err)
}

// helpTapi is tapi on top of `boba --help` (a still image).
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
	text := []string{"boba v" + daemon.Version, "agents in worktrees,", "reviewed hunk by hunk"}
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

  boba                         open the TUI
  boba init                    draft ~/.config/boba/repos/<repo-id>.toml
  boba new <branch> [--from gh#N] [--agent codex] [--prompt TEXT]
  boba open <worktree> [--resume|--new]
  boba adopt [--yes]           take over agents started by hand
  boba ls [--json]
  boba conflicts [--repo ID] [--json]  sessions touching the same files (conflict forecast)
  boba send <slug> <text>
  boba review [<slug> | gh#N]
  boba land <slug>             rebase → test → gh pr create (teardown after merge)
  boba rm <slug> [--force]     teardown → delete worktree
  boba dev <slug>              toggle the dev server on the session's port
  boba undo <slug> [n]         undo the last (or n-th) prompt's changes
  boba answer <slug> allow|deny|always
  boba hooks install|uninstall|status
  boba ext                     port + token for the Chrome extension
  boba mcp                     MCP server for agents (claude mcp add boba -- boba mcp)
  boba daemon start|stop|status
`)
}

func die(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "boba:", err)
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

// flag parsing: --name value / --name=value / --bool
func flags(args []string, bools ...string) (map[string]string, []string) {
	isBool := map[string]bool{}
	for _, b := range bools {
		isBool[b] = true
	}
	f := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || a == "--" {
			pos = append(pos, a)
			continue
		}
		k := strings.TrimPrefix(a, "--")
		if key, v, ok := strings.Cut(k, "="); ok {
			f[key] = v
			continue
		}
		if isBool[k] || i+1 >= len(args) {
			f[k] = "true"
			continue
		}
		f[k] = args[i+1]
		i++
	}
	return f, pos
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
		fmt.Fprintln(os.Stderr, "hint: Claude Code hooks are not installed; run `boba hooks install` so boba can see agent states.")
	}
	return tui.Run(cl, cwd())
}

func cmdInit(args []string) error {
	f, pos := flags(args, "yes", "overwrite", "print")
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
	f, pos := flags(args, "wait")
	if len(pos) < 1 {
		return fmt.Errorf("usage: boba new <branch> [--from gh#N] [--agent codex] [--prompt TEXT]")
	}
	cl := connect()
	defer cl.Close()
	cl.Subscribe("session.progress", "session.updated")
	w, h := agentPaneSize()
	var s state.Session
	prompt := f["prompt"]
	if len(pos) > 1 && prompt == "" {
		prompt = strings.Join(pos[1:], " ")
	}
	if err := cl.Call("session.new", map[string]any{
		"repo": cwd(), "branch": pos[0], "from": f["from"], "agent": f["agent"], "prompt": prompt,
		"base": f["base"], "width": w, "height": h,
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
					fmt.Printf("%s started · open with `boba` · attach raw with `boba attach %s`\n", u.Agent, u.Slug)
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
	f, pos := flags(args, "resume", "new")
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
	f, pos := flags(args, "yes")
	path := ""
	if len(pos) > 0 {
		path, _ = filepath.Abs(pos[0])
	}
	cl := connect()
	defer cl.Close()
	var res struct {
		Candidates []struct {
			PID       int    `json:"pid"`
			Cwd       string `json:"cwd"`
			Where     string `json:"where"`
			SessionID string `json:"session_id"`
		} `json:"candidates"`
	}
	if err := cl.Call("session.adopt", map[string]any{"path": path}, &res); err != nil {
		return err
	}
	if len(res.Candidates) == 0 {
		fmt.Println("no unmanaged agents found")
		return nil
	}
	fmt.Println("agents running outside boba:")
	for _, c := range res.Candidates {
		fmt.Printf("  pid %-6d %-8s %s  session %s\n", c.PID, c.Where, c.Cwd, c.SessionID)
	}
	fmt.Println("adopting stops each one (Ctrl-C twice) and resumes the same session inside boba.")
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

func cmdLs() error {
	cl := connect()
	defer cl.Close()
	var ss []*state.Session
	if err := cl.Call("session.list", nil, &ss); err != nil {
		return err
	}
	if jsonOut {
		printJSON(ss)
		return nil
	}
	if len(ss) == 0 {
		fmt.Println("no sessions · start one with `boba new <branch>`")
		return nil
	}
	ic := theme.Icons(theme.IconsFromEnv())
	th := theme.Load(nil)
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].CreatedAt.Before(ss[j].CreatedAt) })
	for _, s := range ss {
		fmt.Println(theme.SessionLine(th, ic, s, time.Now(), 0))
	}
	return nil
}

// cmdConflicts prints the conflict forecast: pairs of open sessions of the
// same repository that touch the same files, and whether their committed
// contents would conflict (git merge-tree) or merge automatically.
func cmdConflicts(args []string) error {
	f, _ := flags(args)
	cl := connect()
	defer cl.Close()
	var ps []daemon.ConflictPair
	if err := cl.Call("conflicts.list", map[string]any{"repo_id": f["repo"]}, &ps); err != nil {
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
	f, pos := flags(args, "no-enter")
	if len(pos) < 2 {
		return fmt.Errorf("usage: boba send <slug> <text>   (text '-' reads stdin)")
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

// cmdEvent forwards a hook's stdin JSON to bobad. It must be fast and must
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
		"hook": hook, "session": os.Getenv("BOBA_SESSION"), "payload": json.RawMessage(b),
	}, nil, 8*time.Second)
}

func cmdReview(args []string) error {
	cl := connect()
	defer cl.Close()
	if len(args) > 0 && strings.HasPrefix(args[0], "gh#") {
		n, err := strconv.Atoi(strings.TrimPrefix(args[0], "gh#"))
		if err != nil {
			return fmt.Errorf("bad PR number: %s", args[0])
		}
		var s state.Session
		if err := cl.Call("review.pr", map[string]any{"repo": cwd(), "number": n}, &s); err != nil {
			return err
		}
		return tui.RunReview(cl, s.Slug)
	}
	slug := ""
	if len(args) > 0 {
		slug = args[0]
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

func cmdSimple(method string, args []string, use string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: boba %s", use)
	}
	cl := connect()
	defer cl.Close()
	var res any
	if err := cl.Call(method, map[string]any{"slug": args[0]}, &res); err != nil {
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
	f, pos := flags(args, "force", "yes")
	if len(pos) < 1 {
		return fmt.Errorf("usage: boba rm <slug> [--force]")
	}
	cl := connect()
	defer cl.Close()
	var s state.Session
	if err := cl.Call("session.get", map[string]any{"slug": pos[0]}, &s); err != nil {
		return err
	}
	if f["yes"] == "" {
		what := "forget the session (worktree outside ~/worktrees is kept)"
		if s.Managed {
			what = "run teardown and delete " + s.Path
		}
		if !confirm(fmt.Sprintf("%s: %s?", s.Slug, what)) {
			return nil
		}
	}
	return cl.Call("worktree.rm", map[string]any{"slug": pos[0], "force": f["force"] != ""}, nil)
}

func cmdUndo(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: boba undo <slug> [n]")
	}
	n := 0
	if len(args) > 1 {
		n, _ = strconv.Atoi(args[1])
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
	if len(args) < 2 {
		return fmt.Errorf("usage: boba answer <slug> allow|deny|always")
	}
	cl := connect()
	defer cl.Close()
	return cl.Call("session.answer", map[string]any{"slug": args[0], "answer": args[1]}, nil)
}

func cmdAttach(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: boba attach <slug> [agent|review|dev]")
	}
	w := "agent"
	if len(args) > 1 {
		w = args[1]
	}
	sock := os.Getenv("BOBA_TMUX_SOCKET")
	if sock == "" {
		sock = "boba"
	}
	tm, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	return syscall.Exec(tm, []string{"tmux", "-L", sock, "attach-session", "-t", "boba:=" + args[0] + "~" + w}, os.Environ())
}

func cmdHooks(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	f, _ := flags(args, "yes")
	bin, _ := os.Executable()
	if b, err := filepath.EvalSymlinks(bin); err == nil {
		bin = b
	}
	switch sub {
	case "status":
		if hooks.Installed() {
			fmt.Println("installed in", hooks.SettingsPath())
		} else {
			fmt.Println("not installed · run `boba hooks install`")
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
		fmt.Printf("%s will be updated (%s boba hooks for %s).\n", path, map[bool]string{true: "remove", false: "add"}[sub == "uninstall"], strings.Join(hooks.Events, ", "))
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
	return fmt.Errorf("usage: boba hooks install|uninstall|status")
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
	if alt := filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "boba", "extension"); hintDir(extDir) == "" && hintDir(alt) != "" {
		extDir = alt
	}
	fmt.Printf("port   %d\ntoken  %s\n\nChrome: chrome://extensions → デベロッパーモード → パッケージ化されていない拡張機能を読み込む\n        (boba リポジトリの extension/ を選ぶ%s) → 拡張のオプションに port と token を入力\n", info.Port, info.Token, hintDir(extDir))
	return nil
}

func hintDir(d string) string {
	if _, err := os.Stat(filepath.Join(d, "manifest.json")); err == nil {
		return ": " + d
	}
	return ""
}

func cmdDaemon(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
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
		return d.Run()
	case "start":
		if cl, err := client.Dial(); err == nil {
			cl.Close()
			fmt.Println("bobad is already running")
			return nil
		}
		cl := connect()
		cl.Close()
		fmt.Println("bobad started · log:", paths.DaemonLog())
		return nil
	case "stop":
		cl, err := client.Dial()
		if err != nil {
			fmt.Println("bobad is not running")
			return nil
		}
		defer cl.Close()
		if err := cl.Call("daemon.stop", nil, nil); err != nil {
			return err
		}
		fmt.Println("bobad stopped (agents keep running in tmux; they are picked up on next start)")
		return nil
	case "status":
		cl, err := client.Dial()
		if err != nil {
			fmt.Println("bobad is not running")
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
		fmt.Printf("bobad %v · pid %v · %v sessions · socket %v · http :%v\n", st["version"], st["pid"], st["sessions"], st["socket"], st["http_port"])
		return nil
	}
	return fmt.Errorf("usage: boba daemon start|stop|status")
}
