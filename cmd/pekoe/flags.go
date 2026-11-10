package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// spec declares what a subcommand accepts. Anything else starting with "-"
// is refused before the command does anything (a typo or --help must never
// fall through to `daemon stop`, `rm`, `land` …).
type spec struct {
	use   string   // usage, without the leading "usage: "
	bools []string // --name
	vals  []string // --name VALUE / --name=VALUE
	multi []string // of vals: may be given more than once (multiVal)
}

// usageError ends a command before it acts: help (exit 0, usage on stdout)
// or a bad flag (exit 2, message and usage on stderr).
type usageError struct {
	use  string
	msg  string
	help bool
}

func (e *usageError) Error() string {
	if e.help {
		return "usage: " + e.use
	}
	return e.msg + "\nusage: " + e.use
}

// exitUsage reports a usageError and exits; other errors return false.
func exitUsage(err error) bool {
	var ue *usageError
	if !errors.As(err, &ue) {
		return false
	}
	if ue.help {
		fmt.Println("usage: " + ue.use)
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "pekoe: "+ue.msg)
	fmt.Fprintln(os.Stderr, "usage: "+ue.use)
	os.Exit(2)
	return true
}

// helpRequested: err asks for the usage (-h/--help).
func helpRequested(err error) bool {
	var ue *usageError
	return errors.As(err, &ue) && ue.help
}

func helpErr(use string) error { return &usageError{use: use, help: true} }

// isFlag: "--x" and "-x" are flags; "-" (stdin), "- text" and "-1" are not.
func isFlag(a string) bool {
	if strings.HasPrefix(a, "--") {
		return true
	}
	if len(a) < 2 || a[0] != '-' {
		return false
	}
	c := a[1]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// parse reads --name / --name VALUE / --name=VALUE as declared in sp, and
// returns the rest as positional arguments; "--" ends the flags. -h/--help
// anywhere before "--" asks for the usage.
func (sp spec) parse(args []string) (map[string]string, []string, error) {
	is := func(list []string, k string) bool {
		for _, x := range list {
			if x == k {
				return true
			}
		}
		return false
	}
	bad := func(format string, a ...any) error {
		return &usageError{use: sp.use, msg: fmt.Sprintf(format, a...)}
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-h" || a == "--help" {
			return nil, nil, helpErr(sp.use)
		}
	}
	f := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !isFlag(a) {
			pos = append(pos, a)
			continue
		}
		if !strings.HasPrefix(a, "--") {
			return nil, nil, bad("unknown flag %s", a)
		}
		k, v, hasV := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch {
		case is(sp.bools, k):
			if hasV {
				return nil, nil, bad("--%s takes no value", k)
			}
			f[k] = "true"
		case is(sp.vals, k):
			if !hasV {
				if i+1 >= len(args) {
					return nil, nil, bad("--%s needs a value", k)
				}
				i++
				v = args[i]
			}
			if v == "" {
				return nil, nil, bad("--%s needs a value", k)
			}
			if prev, ok := f[k]; ok && is(sp.multi, k) {
				v = prev + "\n" + v
			}
			f[k] = v
		default:
			return nil, nil, bad("unknown flag --%s", k)
		}
	}
	return f, pos, nil
}

// multiVal is every value of a flag declared in multi.
func multiVal(f map[string]string, k string) []string {
	if f[k] == "" {
		return nil
	}
	return strings.Split(f[k], "\n")
}

// Every subcommand's flags. `--json` is global (stripped in main, before or
// after the subcommand) and so is not listed here.
var specs = map[string]spec{
	"init":      {use: "pekoe init [<dir>] [--yes] [--overwrite] [--print]", bools: []string{"yes", "overwrite", "print"}},
	"new":       {use: "pekoe new <branch> [--repo R] [--from gh#N] [--agent codex] [--base REF] [--prompt TEXT]", bools: []string{"wait"}, vals: []string{"repo", "from", "agent", "prompt", "base"}},
	"open":      {use: "pekoe open [<worktree>] [--resume|--new] [--agent NAME]", bools: []string{"resume", "new"}, vals: []string{"agent"}},
	"adopt":     {use: "pekoe adopt [<path>] [--yes]", bools: []string{"yes"}},
	"ls":        {use: "pekoe ls [--all|--repo R] [--json]", bools: []string{"all"}, vals: []string{"repo"}},
	"issues":    {use: "pekoe issues [<N>] [--repo R] [--all|--label L] [--refresh] [--json]", bools: []string{"all", "mine", "refresh"}, vals: []string{"repo", "label"}},
	"conflicts": {use: "pekoe conflicts [--repo R] [--json]", vals: []string{"repo"}},
	"send":      {use: "pekoe send <slug> <text> [--no-enter]   (text '-' reads stdin; -- before text that starts with -)", bools: []string{"no-enter"}},
	"event":     {use: "pekoe event <Hook>   (called by Claude Code hooks, JSON on stdin)"},
	"review":    {use: "pekoe review [<slug> | gh#N [--repo R]]", vals: []string{"repo"}},
	"land":      {use: "pekoe land <slug>   rebase → test → gh pr create"},
	"rm":        {use: "pekoe rm <slug> [--force] [--yes]", bools: []string{"force", "yes"}},
	"dev":       {use: "pekoe dev <slug>   toggle the dev server on the session's port"},
	"undo":      {use: "pekoe undo <slug> [n]"},
	"answer":    {use: "pekoe answer <slug> allow|deny|always"},
	"attach":    {use: "pekoe attach <slug> [agent|review|dev]"},
	"hooks":     {use: "pekoe hooks install|uninstall|status [--yes]", bools: []string{"yes"}},
	"ext":       {use: "pekoe ext   port + token for the Chrome extension"},
	"mcp":       {use: "pekoe mcp   MCP server for agents (stdio)"},
	"web":       {use: "pekoe web [--json]   phone web UI on the tailnet: state, URL, login link"},
	"report":    {use: "pekoe report <summary> --status done|blocked|progress|review [--evidence URL|PATH]... [--verdict pass|fail --reviewed-sha SHA]   (summary '-' reads stdin; run by the session's agent)", vals: []string{"status", "evidence", "verdict", "reviewed-sha"}, multi: []string{"evidence"}},
	"reports":   {use: "pekoe reports [<slug>] [--limit N] [--json]   the sessions' reports, newest first", vals: []string{"limit"}},
	"wait":      {use: "pekoe wait [--mine] [--slug S,…] [--on report,done,…] [--since CURSOR] [--timeout 30m] [--json]   until something happens to sessions", bools: []string{"mine"}, vals: []string{"slug", "on", "since", "timeout"}},

	"daemon":         {use: "pekoe daemon start|stop|restart|status"},
	"daemon start":   {use: "pekoe daemon start"},
	"daemon restart": {use: "pekoe daemon restart   stop, wait, start again (e.g. after make install; agents keep running)"},
	"daemon status":  {use: "pekoe daemon status [--json]"},
	"daemon run":     {use: "pekoe daemon run   (foreground; normally started for you)"},
	"daemon stop":    {use: "pekoe daemon stop [--kill-agents [--yes]]", bools: []string{"kill-agents", "yes"}},

	"tapi":       {use: "pekoe tapi [status|reset] [--json]   tapi chat (TUI: t)"},
	"tapi reset": {use: "pekoe tapi reset [--yes]   end tapi chat and forget its conversation", bools: []string{"yes"}},

	"tip": {use: "pekoe tip [<id>] [--list] [--doc PATH] [--json]   a tip about pekoe (the next one not shown yet)", bools: []string{"list"}, vals: []string{"doc"}},

	"update": {use: "pekoe update [--check] [--version vX.Y.Z] [--no-web] [--force] [--yes]   replace pekoe with a GitHub release (--check: only look)", bools: []string{"check", "yes", "force", "no-web"}, vals: []string{"version"}},
}

// parseCmd parses args against the named spec.
func parseCmd(name string, args []string) (map[string]string, []string, error) {
	sp, ok := specs[name]
	if !ok {
		panic("no flag spec for " + name)
	}
	return sp.parse(args)
}

// splitGlobal takes the global flags (--json) out of the arguments, wherever
// they are, up to a "--".
func splitGlobal(args []string) (rest []string, json bool) {
	for i, a := range args {
		if a == "--" {
			return append(rest, args[i:]...), json
		}
		if a == "--json" {
			json = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, json
}
