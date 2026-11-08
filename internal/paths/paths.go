// Package paths resolves every location pekoe reads or writes.
// Nothing here points inside a target repository (design: リポジトリを汚さない).
package paths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return h
}

// root returns $PEKOE_HOME when set (used by tests and sandboxes).
func root() string { return os.Getenv("PEKOE_HOME") }

// ConfigDir is ~/.config/pekoe.
func ConfigDir() string {
	if r := root(); r != "" {
		return filepath.Join(r, "config")
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "pekoe")
	}
	return filepath.Join(home(), ".config", "pekoe")
}

// DataDir is ~/.local/share/pekoe.
func DataDir() string {
	if r := root(); r != "" {
		return filepath.Join(r, "share")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "pekoe")
	}
	return filepath.Join(home(), ".local", "share", "pekoe")
}

// WorktreesRoot is ~/worktrees. pekoe only ever deletes below this directory.
func WorktreesRoot() string {
	if w := os.Getenv("PEKOE_WORKTREES"); w != "" {
		return w
	}
	if r := root(); r != "" {
		return filepath.Join(r, "worktrees")
	}
	return filepath.Join(home(), "worktrees")
}

func Socket() string {
	if s := os.Getenv("PEKOE_SOCK"); s != "" {
		return s
	}
	return filepath.Join(DataDir(), "pekoed.sock")
}

func StateDB() string   { return filepath.Join(DataDir(), "state.db") }
func TokenFile() string { return filepath.Join(DataDir(), "http-token") }
func DaemonLog() string { return filepath.Join(DataDir(), "pekoed.log") }
func PidFile() string   { return filepath.Join(DataDir(), "pekoed.pid") }

// LayoutFile keeps the TUI's panel widths (<prefix> z < > =).
func LayoutFile() string { return filepath.Join(DataDir(), "layout.json") }

// WebStateDir holds the tsnet node state of the web UI ([web], docs/web.md).
func WebStateDir() string { return filepath.Join(DataDir(), "tsnet") }

// TapiDir is ~/.local/share/pekoe/tapi: the working directory of the tapi
// chat agent (docs/tapi-chat.md). Not $HOME: hooks are matched to it by cwd.
func TapiDir() string { return filepath.Join(DataDir(), "tapi") }

// LockFile is held (flock) by the one pekoed serving this data directory.
func LockFile() string { return filepath.Join(DataDir(), "pekoed.lock") }

// TmuxSocketName is the private tmux server's -L name ($PEKOE_TMUX_SOCKET,
// default "pekoe").
func TmuxSocketName() string {
	if s := os.Getenv("PEKOE_TMUX_SOCKET"); s != "" {
		return s
	}
	return "pekoe"
}

// TmuxPidFile records the pid of the tmux server named socket, so it can be
// stopped even when its socket is unreachable.
func TmuxPidFile(socket string) string {
	return filepath.Join(DataDir(), "tmux-"+Slug(socket)+".pid")
}

func GlobalConfig() string { return filepath.Join(ConfigDir(), "config.toml") }
func ReposConfigDir() string {
	return filepath.Join(ConfigDir(), "repos")
}
func DefaultsDir() string { return filepath.Join(ConfigDir(), "defaults") }

// RepoConfig is ~/.config/pekoe/repos/<repo-id>.toml.
func RepoConfig(repoID string) string {
	return filepath.Join(ReposConfigDir(), repoID+".toml")
}

// PicksDir is ~/.local/share/pekoe/<repo-id>/picks.
func PicksDir(repoID string) string {
	return filepath.Join(DataDir(), repoID, "picks")
}

// PreviewDir is ~/.local/share/pekoe/<repo-id>/preview/<slug>: the in-terminal
// browser preview's screenshots and its remembered path.
func PreviewDir(repoID, slug string) string {
	return filepath.Join(DataDir(), repoID, "preview", slug)
}

// ErrorsDir is ~/.local/share/pekoe/<repo-id>/errors (errors sent to agents).
func ErrorsDir(repoID string) string {
	return filepath.Join(DataDir(), repoID, "errors")
}

// Worktree is ~/worktrees/<repo>/<slug>.
func Worktree(repo, slug string) string {
	return filepath.Join(WorktreesRoot(), repo, slug)
}

// UnderWorktrees reports whether p is strictly inside WorktreesRoot.
func UnderWorktrees(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	rootDir := WorktreesRoot()
	if r, err := filepath.EvalSymlinks(rootDir); err == nil {
		rootDir = r
	}
	rel, err := filepath.Rel(rootDir, abs)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && strings.Count(rel, string(filepath.Separator)) >= 1
}

// Slug turns a branch name into a filesystem/ref-safe slug: feat/csv -> feat-csv.
// A '.' becomes '-' too (release/1.2 -> release-1-2): the slug names tmux
// windows, and tmux reads what follows a '.' in a target as a pane.
func Slug(branch string) string {
	var b strings.Builder
	for _, r := range branch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if s == "" {
		s = "session"
	}
	return s
}

// DirPerm and FilePerm are the modes of pekoe's own directories and files
// below DataDir and ConfigDir: they hold prompts, session env, page captures
// and logs, so only the owner may read them.
const (
	DirPerm  fs.FileMode = 0o700
	FilePerm fs.FileMode = 0o600
)

// Ensure creates dirs (and missing parents) with DirPerm.
func Ensure(dirs ...string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(d, DirPerm); err != nil {
			return err
		}
	}
	return nil
}

// Tighten removes group and other access from root and everything below it,
// fixing files written by older versions with 0644 / 0755. Symlinks are not
// followed (neither a symlinked root nor links inside it), so files that live
// elsewhere — a dotfiles repo, say — are left alone. A missing root is fine.
// It keeps going past entries it cannot fix and returns the first error.
func Tighten(root string) error {
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			keep(err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			keep(err)
			return nil
		}
		if m := info.Mode().Perm(); m&0o077 != 0 {
			keep(os.Chmod(p, m&^0o077))
		}
		return nil
	})
	keep(err)
	return first
}
