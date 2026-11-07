// Package paths resolves every location boba reads or writes.
// Nothing here points inside a target repository (design: リポジトリを汚さない).
package paths

import (
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

// root returns $BOBA_HOME when set (used by tests and sandboxes).
func root() string { return os.Getenv("BOBA_HOME") }

// ConfigDir is ~/.config/boba.
func ConfigDir() string {
	if r := root(); r != "" {
		return filepath.Join(r, "config")
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "boba")
	}
	return filepath.Join(home(), ".config", "boba")
}

// DataDir is ~/.local/share/boba.
func DataDir() string {
	if r := root(); r != "" {
		return filepath.Join(r, "share")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "boba")
	}
	return filepath.Join(home(), ".local", "share", "boba")
}

// WorktreesRoot is ~/worktrees. boba only ever deletes below this directory.
func WorktreesRoot() string {
	if w := os.Getenv("BOBA_WORKTREES"); w != "" {
		return w
	}
	if r := root(); r != "" {
		return filepath.Join(r, "worktrees")
	}
	return filepath.Join(home(), "worktrees")
}

func Socket() string {
	if s := os.Getenv("BOBA_SOCK"); s != "" {
		return s
	}
	return filepath.Join(DataDir(), "bobad.sock")
}

func StateDB() string      { return filepath.Join(DataDir(), "state.db") }
func TokenFile() string    { return filepath.Join(DataDir(), "http-token") }
func DaemonLog() string    { return filepath.Join(DataDir(), "bobad.log") }
func PidFile() string      { return filepath.Join(DataDir(), "bobad.pid") }
func GlobalConfig() string { return filepath.Join(ConfigDir(), "config.toml") }
func ReposConfigDir() string {
	return filepath.Join(ConfigDir(), "repos")
}
func DefaultsDir() string { return filepath.Join(ConfigDir(), "defaults") }

// RepoConfig is ~/.config/boba/repos/<repo-id>.toml.
func RepoConfig(repoID string) string {
	return filepath.Join(ReposConfigDir(), repoID+".toml")
}

// PicksDir is ~/.local/share/boba/<repo-id>/picks.
func PicksDir(repoID string) string {
	return filepath.Join(DataDir(), repoID, "picks")
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
func Slug(branch string) string {
	var b strings.Builder
	for _, r := range branch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-.")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if s == "" {
		s = "session"
	}
	return s
}

func Ensure(dirs ...string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
