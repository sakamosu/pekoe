package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"pekoe/internal/paths"
)

// DefaultPolicy is what pekoed denies the commands it runs from a
// worktree. webStateDir is [web] state_dir (the tsnet node's keys).
//
// Read: the secrets — the human token, the Chrome extension's HTTP token,
// the tsnet state, and config.toml ([web] auth_key) — and state.db (with
// its -wal and -shm: other sessions' prompts, review comments, reports and
// the inbox, which agents read only through pekoed's API, filtered by
// caller; the agents' own sandbox denies it too).
//
// Write: pekoe's data and config directories (else the command could turn
// the sandbox off, change the hooks, or replace the token), and places
// whose contents run later outside any sandbox: shell startup files, git's
// and Claude Code's settings (~/.claude.json's MCP servers, skills, hooks,
// plugins, agents, commands and CLAUDE.md as well, in ~/.claude and in
// $CLAUDE_CONFIG_DIR), launch agents, editor and tmux config, and the
// user-writable directories on $PATH (pekoed runs git, gh and tmux from
// there). This list cannot be complete: the sandbox is aimed at the
// secrets, not at everything a command running as the user could do. The
// repositories' .git/hooks and .git/config stay writable (husky and
// pre-commit install themselves there); pekoed watches them instead
// (internal/gitwatch).
//
// Connect: the user's tmux servers (pekoe's among them): through one, a
// command could open a window that runs outside the sandbox; and the
// sockets of the review surface's nvim (NvimDir). Other unix sockets
// (Docker, ssh-agent …) stay reachable: builds and tests use them.
func DefaultPolicy(webStateDir string) Policy {
	p := Policy{
		DenyRead: []string{
			paths.HumanTokenFile(),
			paths.TokenFile(),
			paths.WebStateDir(),
			paths.GlobalConfig(),
			paths.StateDB(), paths.StateDB() + "-wal", paths.StateDB() + "-shm",
		},
		DenyWrite: []string{paths.DataDir(), paths.ConfigDir()},
	}
	if webStateDir != "" && webStateDir != paths.WebStateDir() {
		p.DenyRead = append(p.DenyRead, webStateDir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, rel := range homeAutorun {
			p.DenyWrite = append(p.DenyWrite, filepath.Join(home, rel))
		}
		for _, rel := range homeAutorunDirs {
			p.Stubs = append(p.Stubs, Stub{Path: filepath.Join(home, rel), Dir: true})
		}
		p.DenyWrite = append(p.DenyWrite, claudeAutorun(filepath.Join(home, ".claude"))...)
		p.Stubs = append(p.Stubs, claudeStubs(filepath.Join(home, ".claude"))...)
		p.DenyWrite = append(p.DenyWrite, filepath.Join(home, ".claude.json"))
	}
	if c := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(c) {
		p.DenyWrite = append(p.DenyWrite, claudeAutorun(c)...)
		p.Stubs = append(p.Stubs, claudeStubs(c)...)
		p.DenyWrite = append(p.DenyWrite, filepath.Join(c, ".claude.json"))
	}
	if z := os.Getenv("ZDOTDIR"); filepath.IsAbs(z) {
		p.DenyWrite = append(p.DenyWrite, z)
	}
	p.DenyWrite = append(p.DenyWrite, writablePathDirs(os.Getenv("PATH"))...)
	p.DenyConnect = append(p.DenyConnect, tmuxDir(), paths.NvimDir())
	return p
}

// claudeAutorun are what Claude Code reads from its config directory dir
// (~/.claude or $CLAUDE_CONFIG_DIR) that run commands or instruct the next
// agent it starts. projects/ (transcripts, memory) and the caches stay
// writable: a `claude -p` gatekeeper writes them.
func claudeAutorun(dir string) []string {
	var out []string
	for _, rel := range []string{"settings.json", "settings.local.json", "skills", "hooks", "plugins", "agents", "commands", "rules", "output-styles", "CLAUDE.md"} {
		out = append(out, filepath.Join(dir, rel))
	}
	return out
}

// claudeStubs are claudeAutorun(dir) made empty on Linux when missing
// (Policy.Stubs): the directories, an empty CLAUDE.md and settings files
// of {}. Were they left missing, bwrap could only cover them by making
// all of dir read-only (bwrapArgs), and `claude -p` creates new entries
// there (projects/, todos/ …).
func claudeStubs(dir string) []Stub {
	var out []Stub
	for _, rel := range []string{"skills", "hooks", "plugins", "agents", "commands", "rules", "output-styles"} {
		out = append(out, Stub{Path: filepath.Join(dir, rel), Dir: true})
	}
	for _, rel := range []string{"settings.json", "settings.local.json"} {
		out = append(out, Stub{Path: filepath.Join(dir, rel), Content: "{}\n"})
	}
	return append(out, Stub{Path: filepath.Join(dir, "CLAUDE.md")})
}

// homeAutorunDirs are the directories of homeAutorun below ~/.config that
// are made empty on Linux when missing (Policy.Stubs), so that ~/.config
// itself need not be read-only. An empty one changes nothing. Shell
// startup files are not: an empty ~/.bash_profile would hide ~/.profile.
var homeAutorunDirs = []string{
	".config/fish", ".config/zsh", ".config/git", ".config/tmux", ".config/nvim",
	".config/systemd/user", ".config/autostart",
}

// homeAutorun are files and directories below $HOME whose contents get run
// (or decide what gets run) later, outside the sandbox.
var homeAutorun = []string{
	".zshrc", ".zshenv", ".zprofile", ".zlogin", ".zlogout",
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile",
	".config/fish", ".config/zsh",
	".gitconfig", ".config/git",
	".ssh",
	".tmux.conf", ".config/tmux",
	".vimrc", ".config/nvim",
	"Library/LaunchAgents",
	".config/systemd/user", ".config/autostart",
}

// writablePathDirs are the absolute directories on path that this user can
// write to (~/.local/bin, ~/go/bin, /opt/homebrew/bin …).
func writablePathDirs(path string) []string {
	var out []string
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			continue
		}
		if unix.Access(d, unix.W_OK) == nil {
			out = append(out, d)
		}
	}
	return out
}

// tmuxDir is where tmux keeps this user's server sockets.
func tmuxDir() string {
	base := os.Getenv("TMUX_TMPDIR")
	if base == "" {
		base = "/tmp"
	}
	return filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()))
}
