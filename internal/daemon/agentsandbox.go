package daemon

// The sandbox settings pekoe hands Claude Code agents (--settings), so an
// agent cannot read the human token even when the user has not configured
// Claude Code's sandbox themselves (human.go: the token is what tells a
// person from an agent). Claude Code applies --settings above the user's
// and the project's settings files, so their sandbox.enabled cannot turn it
// off (nor can their filesystem.disabled, written here as false), and
// allowUnsandboxedCommands=false makes it ignore dangerouslyDisableSandbox
// and makes the sandbox "admin-required": settings that loosen it count
// only from --settings and ~/.claude/settings.json, not from a
// repository's .claude/settings*.json, which an agent can write. Lists (denyRead, allowWrite, allowedDomains,
// permissions.deny, …) are merged with those files: a user adds the
// directories and domains their builds need in ~/.claude/settings.json
// (README: "Agents run in Claude Code's sandbox").
//
// The sandbox covers Bash and its children only; the Read and Edit tools
// are kept off the same paths with permissions.deny. Hooks run outside it,
// so `pekoe event` is unaffected.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"pekoe/internal/gitwatch"
	"pekoe/internal/gitx"
	"pekoe/internal/hooks"
	"pekoe/internal/paths"
	"pekoe/internal/sandbox"
	"pekoe/internal/voice/backend"
)

type claudeSettings struct {
	Permissions struct {
		Allow []string `json:"allow,omitempty"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
	Sandbox struct {
		Enabled                  bool `json:"enabled"`
		AllowUnsandboxedCommands bool `json:"allowUnsandboxedCommands"`
		FailIfUnavailable        bool `json:"failIfUnavailable"`
		Filesystem               struct {
			// Disabled is always written (false): ~/.claude/settings.json
			// may set it, and --settings ranks above that file
			Disabled  bool     `json:"disabled"`
			DenyRead  []string `json:"denyRead"`
			DenyWrite []string `json:"denyWrite,omitempty"`
		} `json:"filesystem"`
		Network struct {
			AllowUnixSockets []string `json:"allowUnixSockets"`
		} `json:"network"`
	} `json:"sandbox"`
	Hooks claudeHooks `json:"hooks,omitempty"`
}

// claudeHooks is the "hooks" of Claude Code's settings: event → groups.
type claudeHooks map[string][]claudeHookGroup

type claudeHookGroup struct {
	Hooks []claudeHook `json:"hooks"`
}

type claudeHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// readHookTimeout is the MessageDisplay hook's timeout in seconds, below
// Claude Code's default (10): the hook holds the text back from the
// screen until it returns (docs/voice.md 3.1, 3.2).
const readHookTimeout = 2

// selfExe is pekoe's own executable, the one the hooks name (tests swap it).
var selfExe = backend.SelfExecutable

// readHooks are the hooks pekoe adds to a Claude Code agent's settings for
// voice's read (docs/voice.md 3.1): MessageDisplay, when [voice] is on and
// focus_mode or background_mode is read at the agent's start. Other
// settings add none (nil), as does a pekoe whose executable is not found.
// Hooks given with --settings run as well as those in
// ~/.claude/settings.json (they are merged), which this leaves alone.
func (d *Daemon) readHooks() claudeHooks {
	v := d.cfg().Voice
	if !v.Enable || (v.Focus() != "read" && v.Background() != "read") {
		return nil
	}
	exe := selfExe()
	if exe == "" {
		return nil
	}
	h := claudeHook{Type: "command", Command: hooks.Command(exe, "MessageDisplay"), Timeout: readHookTimeout}
	return claudeHooks{"MessageDisplay": {{Hooks: []claudeHook{h}}}}
}

// agentSecrets: what an agent must not read (nor change with the Edit
// tool): the human token, the Chrome extension's token, state.db (with its
// -wal and -shm: prompts, review comments, reports and the send log, which
// agents read only through pekoed's API, which filters by caller), the Web
// UI's tsnet state (also where [web] state_dir moved it), the local GUI's
// one-time sign-in pages (gui.go) and pekoe's config directory. dir
// reports a directory.
//
// And the browsers' profiles (sandbox.BrowserProfiles), as the hooks'
// sandbox denies them: the local GUI's session token may be written
// there, and an agent that read it could hand it to its own dev server
// or tests, which reach 127.0.0.1. Bash's sandbox and the Read tool only:
// browser automation runs in MCP servers and in browsers started through
// LaunchServices, outside both. On Linux only those there now (the
// settings are written at every start): Claude Code's sandbox is bwrap
// there too, which can only cover a path that exists.
func (d *Daemon) agentSecrets() (files, dirs []string) {
	db := paths.StateDB()
	files = []string{paths.HumanTokenFile(), paths.TokenFile(), db, db + "-wal", db + "-shm"}
	dirs = []string{paths.WebStateDir(), paths.GUIDir(), paths.ConfigDir()}
	if sd := d.cfg().Web.StateDir; sd != "" {
		dirs = append(dirs, sd)
	}
	for _, b := range sandbox.BrowserProfiles() {
		if runtime.GOOS == "linux" {
			if _, err := os.Stat(b); err != nil {
				continue
			}
		}
		dirs = append(dirs, b)
	}
	return files, dirs
}

// gitProtected are the files of the repositories' git directories that
// name commands git runs (config, config.worktree, hooks), and the same in
// the git directories of the submodules already there (modules/…: a `git
// status` in the main checkout runs git in each submodule): an agent that
// commits writes the git directory, but must not change these, which the
// git you run yourself would follow. A session's own worktree keeps its
// submodules in worktrees/<id>/modules, which stays writable.
func gitProtected(repoDirs []string) (files, dirs []string) {
	for _, r := range repoDirs {
		if r == "" {
			continue
		}
		cd, err := gitx.CommonDir(r)
		if err != nil || cd == "" {
			cd = filepath.Join(r, ".git")
		}
		for _, g := range append([]string{cd}, gitwatch.ModuleDirs(cd)...) {
			files = append(files, filepath.Join(g, "config"), filepath.Join(g, "config.worktree"))
			dirs = append(dirs, filepath.Join(g, "hooks"))
		}
	}
	return files, dirs
}

// withReal adds each path's symlink-resolved form (macOS: /tmp is
// /private/tmp), sorted and without repeats.
func withReal(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range list {
		if p == "" {
			continue
		}
		for _, v := range []string{filepath.Clean(p), realpath(p)} {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out
}

// permPath writes an absolute path the way permission rules want it
// ("//abs"; "/x" would be relative to the settings file).
func permPath(p string) string { return "/" + p }

// claudeSandboxSettings is the JSON passed with --settings to a Claude Code
// agent working on the repositories repoDirs. readOnly are directories the
// agent reads but must not write, by Bash (denyWrite, which wins over the
// directories Claude Code lets it write, --add-dir included) nor by the
// Edit and Write tools (permissions.deny): tapi chat's repositories and
// worktrees (docs/orchestration.md「自分で作業しすぎないようにする」).
// Sessions get none. slug's directory of images sent from the web UI
// (webimage.go) is readable with the Read tool without asking: the agent is
// told to open them, and they are outside its worktree.
func (d *Daemon) claudeSandboxSettings(slug string, repoDirs, readOnly []string) string {
	b, _ := json.Marshal(d.sandboxSettings(slug, repoDirs, readOnly))
	return string(b)
}

func (d *Daemon) sandboxSettings(slug string, repoDirs, readOnly []string) claudeSettings {
	sf, sd := d.agentSecrets()
	gf, gd := gitProtected(repoDirs)
	sf, sd, gf, gd = withReal(sf), withReal(sd), withReal(gf), withReal(gd)
	ro := withReal(readOnly)

	var c claudeSettings
	c.Sandbox.Enabled = true
	c.Sandbox.AllowUnsandboxedCommands = false
	c.Sandbox.FailIfUnavailable = true
	c.Sandbox.Filesystem.DenyRead = append(append([]string{}, sf...), sd...)
	c.Sandbox.Filesystem.DenyWrite = append(append(append([]string{}, gf...), gd...), ro...)
	c.Sandbox.Network.AllowUnixSockets = withReal([]string{paths.Socket()})
	deny := []string{}
	for _, tool := range []string{"Read", "Edit"} {
		for _, f := range sf {
			deny = append(deny, tool+"("+permPath(f)+")")
		}
		for _, dir := range sd {
			deny = append(deny, tool+"("+permPath(dir)+"/**)")
		}
	}
	// the settings themselves and the skills' text (pekoe skills install;
	// Claude Code follows it in every session): Bash cannot write pekoe's
	// data directory, the Edit tool is kept off them here
	for _, dir := range withReal([]string{filepath.Dir(agentSettingsFile("x")), paths.SkillsDir()}) {
		deny = append(deny, "Edit("+permPath(dir)+"/**)")
	}
	for _, f := range gf {
		deny = append(deny, "Edit("+permPath(f)+")")
	}
	for _, dir := range gd {
		deny = append(deny, "Edit("+permPath(dir)+"/**)")
	}
	for _, dir := range ro {
		deny = append(deny, "Edit("+permPath(dir)+"/**)")
	}
	c.Permissions.Deny = deny
	if webImageDirOK(slug) {
		for _, dir := range withReal([]string{webImageDir(slug)}) {
			c.Permissions.Allow = append(c.Permissions.Allow, "Read("+permPath(dir)+"/**)")
		}
	}
	return c
}

// agentSettings is the --settings JSON of a Claude Code agent: the
// sandbox's (when sandbox) and read's hooks (readHooks), or "" when it
// gets neither. An agent without the sandbox gets a file with the hooks
// alone.
func (d *Daemon) agentSettings(slug string, sandbox bool, repoDirs, readOnly []string) string {
	h := d.readHooks()
	var v any
	switch {
	case sandbox:
		c := d.sandboxSettings(slug, repoDirs, readOnly)
		c.Hooks = h
		v = c
	case h != nil:
		v = struct {
			Hooks claudeHooks `json:"hooks"`
		}{h}
	default:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// agentSettingsFile is where a session's (or tapi's) settings are written
// for its next start. They are paths, not secrets; the file is read by
// Claude Code itself, before its sandbox applies.
func agentSettingsFile(slug string) string {
	name := paths.Slug(slug)
	if slug == TapiSlug {
		name = "tapi"
	}
	return filepath.Join(paths.DataDir(), "agent-settings", name+".json")
}

// withAgentSandbox appends --settings <file> to a Claude Code agent's
// command line unless [agents.<name>] sandbox = false and voice's read
// adds no hooks (agentSettings); other agents get nothing. readOnly: see
// claudeSandboxSettings. The settings go through a file: written inline they take a few
// KB of tmux's command line, which the prompt needs (promptArgMax).
func (d *Daemon) withAgentSandbox(slug, cmd string, sandbox bool, repoDirs, readOnly []string) (string, error) {
	if !isClaude(cmd) {
		return cmd, nil
	}
	settings := d.agentSettings(slug, sandbox, repoDirs, readOnly)
	if settings == "" {
		return cmd, nil
	}
	f := agentSettingsFile(slug)
	if err := paths.Ensure(filepath.Dir(f)); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), ".settings-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.WriteString(settings)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), f)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("writing the agent's settings: %w", err)
	}
	return cmd + " --settings " + shellQuote(f), nil
}
