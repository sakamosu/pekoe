// Package config loads ~/.config/pekoe/config.toml (global) and
// ~/.config/pekoe/repos/<repo-id>.toml (per repository).
// pekoe knows nothing about package managers at runtime: hooks are plain
// shell command lists with {{var}} substitution (design: ただのシェル).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"pekoe/internal/paths"
)

type Worktree struct {
	Copy  []string `toml:"copy"`
	Clone []string `toml:"clone"`
	Base  string   `toml:"base"` // base branch, default: origin HEAD or main
}

type Hooks struct {
	Setup    []string `toml:"setup"`
	Dev      string   `toml:"dev"`
	Test     string   `toml:"test"`
	Teardown []string `toml:"teardown"`
}

type Review struct {
	// Gatekeeper is a read-only agent command that receives the diff on stdin
	// and prints "path:line: text" lines. Empty disables it.
	Gatekeeper string `toml:"gatekeeper"`
}

type Repo struct {
	Worktree Worktree          `toml:"worktree"`
	Hooks    Hooks             `toml:"hooks"`
	Env      map[string]string `toml:"env"`
	Review   Review            `toml:"review"`
	Agent    string            `toml:"agent"`
}

type Agent struct {
	Command string `toml:"command"` // e.g. "claude"
	Resume  string `toml:"resume"`  // e.g. "claude --resume {{session_id}}"
	Prompt  string `toml:"prompt"`  // how to pass an initial prompt: "arg" | "paste"
	Allow   string `toml:"allow"`   // keys answering a permission prompt with yes
	Always  string `toml:"always"`
	Deny    string `toml:"deny"`
	Hooks   bool   `toml:"hooks"` // agent reports state through `pekoe event`
	// Sandbox: Claude Code agents get pekoe's sandbox settings (--settings:
	// the sandbox on, pekoe's tokens and config unreadable). Default true;
	// ignored for other agents.
	Sandbox *bool `toml:"sandbox"`
}

// SandboxOn: unset means on.
func (a Agent) SandboxOn() bool { return a.Sandbox == nil || *a.Sandbox }

type Theme struct {
	Mode   string            `toml:"mode"`  // truecolor | ansi | none | auto
	Icons  string            `toml:"icons"` // nerd | unicode | ascii
	Colors map[string]string `toml:"colors"`
	ANSI   map[string]int    `toml:"ansi"`
}

// Mascot is [mascot]: tapi, the pearl (docs/mascot.md).
type Mascot struct {
	Shape   string            `toml:"shape"`   // auto | round | block
	Glyphs  string            `toml:"glyphs"`  // auto | unicode | narrow | ascii
	Palette string            `toml:"palette"` // dark (D) | light (E)
	Colors  map[string]string `toml:"colors"`  // hi bd sh eye
	Splash  *bool             `toml:"splash"`
}

func (m Mascot) SplashOn() bool { return m.Splash == nil || *m.Splash }

// LightPalette: palette = "light" | "E".
func (m Mascot) LightPalette() bool {
	p := strings.ToLower(m.Palette)
	return p == "light" || p == "e"
}

// Web is [web]: the phone UI that pekoed serves on the tailnet through an
// embedded tsnet node (docs/web.md). Off by default; while off, tsnet is
// never started.
type Web struct {
	Enable   bool   `toml:"enable"`
	Hostname string `toml:"hostname"` // tailnet machine name, default "pekoe"
	// AllowUsers are Tailscale login names (WhoIs UserProfile.LoginName,
	// e.g. "you@github"). Empty refuses to start: nobody would get in.
	AllowUsers []string `toml:"allow_users"`
	// AllowDevices optionally narrows to these machine names (as in the
	// admin console, e.g. "iphone-15"). Empty: any device of AllowUsers.
	AllowDevices []string `toml:"allow_devices"`
	StateDir     string   `toml:"state_dir"` // tsnet state, default <data>/tsnet
	AuthKey      string   `toml:"auth_key"`  // first join only; TS_AUTHKEY works too
}

// Tapi is [tapi]: the tapi chat agent (docs/tapi-chat.md).
type Tapi struct {
	Agent string `toml:"agent"` // a name in [agents.*], default "claude"
}

// Orchestrator is [orchestrator]: tapi as the orchestrator
// (docs/orchestration.md). Notify is how the TUI tells you that tapi or an
// agent's question in the inbox waits for you: "escalate" (the default:
// the same steps as a session's wait — a toast, the bar's speech blinking
// after 30 s, an OS notification after 2 min while the terminal is not
// focused) or "quiet" (as before tapi orchestrated: tapi's wait only in the
// bar, below a session's, and one message line per new question). Any
// other value is "escalate".
type Orchestrator struct {
	Notify string `toml:"notify"`
}

// QuietNotify: notify = "quiet".
func (o Orchestrator) QuietNotify() bool { return strings.EqualFold(o.Notify, "quiet") }

// Cleanup is [cleanup]: the candidates of `pekoe cleanup` (daemon/cleanup.go).
// stale_after is how long a stopped session (done, idle, failed …) has not
// moved before it is listed as stale: a Go duration ("72h", "36h30m").
// Empty or unreadable: DefaultStaleAfter.
type Cleanup struct {
	StaleAfter string `toml:"stale_after"`
}

// DefaultStaleAfter: three days. A session left done on Friday evening and
// picked up on Monday morning (about 63 hours) is not listed; one nobody
// touched for a working week is.
const DefaultStaleAfter = 72 * time.Hour

// Stale is stale_after as a duration (DefaultStaleAfter when unset or not
// a positive duration).
func (c Cleanup) Stale() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(c.StaleAfter)); err == nil && d > 0 {
		return d
	}
	return DefaultStaleAfter
}

// Tips is [tips]: one feature at a time in the TUI's empty main pane and
// in tapi chat's greeting (internal/tips). On by default.
type Tips struct {
	Enable *bool `toml:"enable"`
}

// Sandbox is [sandbox]: pekoed runs the commands that come from a worktree
// (an agent can write them: [hooks] test / setup / dev / teardown and the
// gatekeeper) in a sandbox that cannot read pekoe's tokens
// (internal/sandbox), and every git pekoed runs (gitx.SetSandbox). On by
// default; where no sandbox works, those commands are not run (git runs
// without one). enable = false runs them as they are.
type Sandbox struct {
	Enable *bool `toml:"enable"`
}

func (s Sandbox) On() bool { return s.Enable == nil || *s.Enable }

type Global struct {
	HTTP struct {
		Port int `toml:"port"`
	} `toml:"http"`
	Web  Web `toml:"web"`
	Keys struct {
		Prefix string            `toml:"prefix"`
		Map    map[string]string `toml:"map"`
	} `toml:"keys"`
	Theme  Theme            `toml:"theme"`
	Mascot Mascot           `toml:"mascot"`
	Agents map[string]Agent `toml:"agents"`
	Review struct {
		Surface string `toml:"surface"` // nvim | tui
	} `toml:"review"`
	Ports struct {
		Base int `toml:"base"`
	} `toml:"ports"`
	Tapi    Tapi    `toml:"tapi"`
	Tips    Tips    `toml:"tips"`
	Sandbox Sandbox `toml:"sandbox"`
	Animate *bool   `toml:"animate"`
	Picks   struct {
		AutoSend *bool `toml:"auto_send"`
	} `toml:"picks"`

	Orchestrator Orchestrator `toml:"orchestrator"`
	Cleanup      Cleanup      `toml:"cleanup"`
}

func DefaultAgents() map[string]Agent {
	return map[string]Agent{
		"claude": {
			Command: "claude",
			Resume:  "claude --resume {{session_id}}",
			Prompt:  "arg",
			Allow:   "1", Always: "2", Deny: "Escape",
			Hooks: true,
		},
		"codex": {
			Command: "codex", Prompt: "arg",
			Allow: "y", Always: "a", Deny: "n",
		},
		"opencode": {
			Command: "opencode", Prompt: "paste",
			Allow: "y", Always: "a", Deny: "n",
		},
	}
}

func LoadGlobal() (*Global, error) {
	g := &Global{}
	if b, err := os.ReadFile(paths.GlobalConfig()); err == nil {
		if _, err := toml.Decode(string(b), g); err != nil {
			return nil, fmt.Errorf("%s: %w", paths.GlobalConfig(), err)
		}
	}
	if g.HTTP.Port == 0 {
		g.HTTP.Port = 47777
	}
	if g.Web.Hostname == "" {
		g.Web.Hostname = "pekoe"
	}
	if g.Web.StateDir == "" {
		g.Web.StateDir = paths.WebStateDir()
	} else if rest, ok := strings.CutPrefix(g.Web.StateDir, "~/"); ok {
		if h, err := os.UserHomeDir(); err == nil {
			g.Web.StateDir = filepath.Join(h, rest)
		}
	}
	if g.Ports.Base == 0 {
		g.Ports.Base = 3001
	}
	if g.Keys.Prefix == "" {
		g.Keys.Prefix = "ctrl+q" // reachable on JIS keyboards; unused by Claude Code
	}
	agents := DefaultAgents()
	for k, v := range g.Agents {
		d := agents[k]
		if v.Command != "" {
			d.Command = v.Command
		}
		if v.Resume != "" {
			d.Resume = v.Resume
		}
		if v.Prompt != "" {
			d.Prompt = v.Prompt
		}
		if v.Allow != "" {
			d.Allow = v.Allow
		}
		if v.Always != "" {
			d.Always = v.Always
		}
		if v.Deny != "" {
			d.Deny = v.Deny
		}
		d.Hooks = d.Hooks || v.Hooks
		if v.Sandbox != nil {
			d.Sandbox = v.Sandbox
		}
		agents[k] = d
	}
	g.Agents = agents
	if g.Tapi.Agent == "" {
		g.Tapi.Agent = "claude"
	}
	if g.Review.Surface == "" {
		g.Review.Surface = "nvim"
	}
	return g, nil
}

func (g *Global) AnimateOn() bool  { return g.Animate == nil || *g.Animate }
func (g *Global) AutoSendOn() bool { return g.Picks.AutoSend == nil || *g.Picks.AutoSend }

// Sections are the top-level names of config.toml ([web], [tips] …, and
// the bare key animate), as Global declares them.
func Sections() []string {
	t := reflect.TypeOf(Global{})
	out := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("toml"), ","); name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

func (g *Global) TipsOn() bool { return g.Tips.Enable == nil || *g.Tips.Enable }

// LoadRepo reads the per-repo config. A missing file is not an error.
func LoadRepo(repoID string) (*Repo, bool, error) {
	r := &Repo{}
	b, err := os.ReadFile(paths.RepoConfig(repoID))
	if os.IsNotExist(err) {
		return r, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := toml.Decode(string(b), r); err != nil {
		return nil, true, fmt.Errorf("%s: %w", paths.RepoConfig(repoID), err)
	}
	return r, true, nil
}

// Vars are the five template variables: slug, branch, port, path, repo.
type Vars struct {
	Slug, Branch, Path, Repo string
	Port                     int
	Extra                    map[string]string
}

// Expand fills in the values as they are: for [env] values and display.
// Commands that go to a shell use ExpandShell.
func (v Vars) Expand(s string) string { return v.expand(s, func(x string) string { return x }) }

// ExpandShell fills in the values for a command line run by sh -c. A value
// may hold characters the shell treats specially ($, `, ;, spaces …), so a
// value with any of them is put in single quotes; plain values (the usual
// feat/x, /Users/me/worktrees/app/feat-x) are left as they are.
//
// The placeholder must not be inside quotes of the command's own:
// "app_{{branch}}" would become "app_'x$(…)'", and the $(…) runs since the
// single quotes are literal inside double quotes. pekoe quotes when needed,
// so write app_{{branch}}. session.new also refuses branch names with shell
// characters (gitx.CheckBranchName); a path ({{path}}) may still have them.
func (v Vars) ExpandShell(s string) string { return v.expand(s, ShellQuoteIfNeeded) }

func (v Vars) expand(s string, q func(string) string) string {
	r := strings.NewReplacer(
		"{{slug}}", q(v.Slug),
		"{{branch}}", q(v.Branch),
		"{{port}}", fmt.Sprint(v.Port),
		"{{path}}", q(v.Path),
		"{{repo}}", q(v.Repo),
	)
	s = r.Replace(s)
	for k, val := range v.Extra {
		s = strings.ReplaceAll(s, "{{"+k+"}}", q(val))
	}
	return s
}

// ShellQuoteIfNeeded returns s unchanged when every byte is one the shell
// reads literally, and s in single quotes otherwise.
func ShellQuoteIfNeeded(s string) string {
	if s != "" && strings.Trim(s, shellSafe) == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

// EnvList expands [env] into KEY=VALUE pairs, sorted for determinism.
func (r *Repo) EnvList(v Vars) []string {
	keys := make([]string, 0, len(r.Env))
	for k := range r.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+v.Expand(r.Env[k]))
	}
	return out
}
