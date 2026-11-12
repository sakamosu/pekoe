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
	"sort"

	"pekoe/internal/gitwatch"
	"pekoe/internal/gitx"
	"pekoe/internal/paths"
)

type claudeSettings struct {
	Permissions struct {
		Deny []string `json:"deny"`
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
}

// agentSecrets: what an agent must not read (nor change with the Edit
// tool): the human token, the Chrome extension's token, state.db (with its
// -wal and -shm: prompts, review comments, reports and the send log, which
// agents read only through pekoed's API, which filters by caller), the Web
// UI's tsnet state (also where [web] state_dir moved it) and pekoe's
// config directory. dir reports a directory.
func (d *Daemon) agentSecrets() (files, dirs []string) {
	db := paths.StateDB()
	files = []string{paths.HumanTokenFile(), paths.TokenFile(), db, db + "-wal", db + "-shm"}
	dirs = []string{paths.WebStateDir(), paths.ConfigDir()}
	if sd := d.cfg().Web.StateDir; sd != "" {
		dirs = append(dirs, sd)
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
// Sessions get none.
func (d *Daemon) claudeSandboxSettings(repoDirs, readOnly []string) string {
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
	b, _ := json.Marshal(c)
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
// command line unless [agents.<name>] sandbox = false; other agents get
// nothing. readOnly: see claudeSandboxSettings. The settings go through a file: written inline they take a few
// KB of tmux's command line, which the prompt needs (promptArgMax).
func (d *Daemon) withAgentSandbox(slug, cmd string, sandbox bool, repoDirs, readOnly []string) (string, error) {
	if !sandbox || !isClaude(cmd) {
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
	_, err = tmp.WriteString(d.claudeSandboxSettings(repoDirs, readOnly))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), f)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("writing the agent's sandbox settings: %w", err)
	}
	return cmd + " --settings " + shellQuote(f), nil
}
