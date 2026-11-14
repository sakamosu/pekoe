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
//	pekoe config edit|check|show|path   config.toml
//	pekoe update [--check] [--version vX.Y.Z]  replace pekoe with a GitHub release
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"pekoe/internal/client"
	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/hooks"
	"pekoe/internal/mascot"
	"pekoe/internal/mcp"
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
	case "cleanup":
		err = cmdCleanup(args)
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
	case "stats":
		err = cmdStats(args)
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
	case "config":
		err = cmdConfig(args)
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
	}.WithTheme(theme.Load(&g.Theme)))
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
  pekoe cleanup [--all|--repo R] [--stale 72h]   landed, merged and long-stopped sessions, with uncommitted / unpushed work
  pekoe cleanup --rm <slug>... | --rm --landed [--force] [--yes]   remove the ones you pick (agents: landed only)
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
  pekoe stats [--days N] [--repo R] [--json]
                               PRs merged and tokens used, by day and repository
                               (removed sessions included; default: the last 7 days)
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
  pekoe config edit|check|show|path
                               config.toml: open it in $EDITOR (checked on save),
                               check it, the settings in use, its path
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

// ---- commands ----

func runTUI() error {
	cl := connect()
	defer cl.Close()
	if !hooks.Installed() {
		fmt.Fprintln(os.Stderr, "hint: Claude Code hooks are not installed; run `pekoe hooks install` so pekoe can see agent states.")
	}
	return tui.Run(cl, cwd())
}

func confirm(q string) bool {
	fmt.Printf("%s [y/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
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

func stdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
