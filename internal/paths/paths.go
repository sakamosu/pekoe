// Package paths resolves every location pekoe reads or writes.
// Nothing here points inside a target repository (design: リポジトリを汚さない).
package paths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// realDirs are the user's real pekoe locations, taken when the process
// starts (before a test can t.Setenv HOME or XDG_*). A test binary must never
// resolve a path inside them: see notInTest.
var realDirs = func() []string {
	h := home()
	ds := []string{
		filepath.Join(h, ".config", "pekoe"),
		filepath.Join(h, ".local", "share", "pekoe"),
		filepath.Join(h, "worktrees"),
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		ds = append(ds, filepath.Join(x, "pekoe"))
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		ds = append(ds, filepath.Join(x, "pekoe"))
	}
	return ds
}()

// notInTest returns p, panicking when a test binary resolves p inside the
// user's real pekoe locations (realDirs): a test that forgot to isolate
// itself must fail, not write into ~/.local/share/pekoe or ~/.config/pekoe
// or work under ~/worktrees. Tests isolate the whole package with
// internal/testenv's Main.
func notInTest(p string) string {
	if !testing.Testing() {
		return p
	}
	c := filepath.Clean(p)
	for _, d := range realDirs {
		d = filepath.Clean(d)
		if c == d || strings.HasPrefix(c, d+string(filepath.Separator)) {
			panic("paths: a test resolved the real " + c + " (call testenv.Main in TestMain, or t.Setenv PEKOE_HOME)")
		}
	}
	return p
}

// ConfigDir is ~/.config/pekoe.
func ConfigDir() string {
	if r := root(); r != "" {
		return notInTest(filepath.Join(r, "config"))
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return notInTest(filepath.Join(x, "pekoe"))
	}
	return notInTest(filepath.Join(home(), ".config", "pekoe"))
}

// DataDir is ~/.local/share/pekoe.
func DataDir() string {
	if r := root(); r != "" {
		return notInTest(filepath.Join(r, "share"))
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return notInTest(filepath.Join(x, "pekoe"))
	}
	return notInTest(filepath.Join(home(), ".local", "share", "pekoe"))
}

// WorktreesRoot is ~/worktrees. pekoe only ever deletes below this directory.
func WorktreesRoot() string {
	if w := os.Getenv("PEKOE_WORKTREES"); w != "" {
		return notInTest(w)
	}
	if r := root(); r != "" {
		return notInTest(filepath.Join(r, "worktrees"))
	}
	return notInTest(filepath.Join(home(), "worktrees"))
}

func Socket() string {
	if s := os.Getenv("PEKOE_SOCK"); s != "" {
		return notInTest(s)
	}
	return filepath.Join(DataDir(), "pekoed.sock")
}

func StateDB() string   { return filepath.Join(DataDir(), "state.db") }
func TokenFile() string { return filepath.Join(DataDir(), "http-token") }
func DaemonLog() string { return filepath.Join(DataDir(), "pekoed.log") }
func PidFile() string   { return filepath.Join(DataDir(), "pekoed.pid") }

// HumanTokenFile holds the token that unlocks the human-only API methods
// (answering permission prompts, pressing keys, running commands). The TUI,
// the CLI and Neovim read it; agents must not (docs/web.md の脅威モデル).
func HumanTokenFile() string { return filepath.Join(DataDir(), "human-token") }

// LayoutFile keeps the TUI's panel widths (<prefix> z < > =).
func LayoutFile() string { return filepath.Join(DataDir(), "layout.json") }

// TipsFile records which tips were shown (internal/tips), shared by the TUI
// and `pekoe tip`.
func TipsFile() string { return filepath.Join(DataDir(), "tips.json") }

// TutorialFile records that the TUI's tutorial was seen (or skipped), so it
// starts by itself only the first time.
func TutorialFile() string { return filepath.Join(DataDir(), "tutorial.json") }

// WebStateDir holds the tsnet node state of the web UI ([web], docs/web.md).
func WebStateDir() string { return filepath.Join(DataDir(), "tsnet") }

// TapiDir is ~/.local/share/pekoe/tapi: the working directory of the tapi
// chat agent (docs/tapi-chat.md). Not $HOME: hooks are matched to it by cwd.
func TapiDir() string { return filepath.Join(DataDir(), "tapi") }

// SkillsDir holds the copies of the Claude Code skills built into the
// binary (internal/skills); `pekoe skills install` links them from
// ~/.claude/skills.
func SkillsDir() string { return filepath.Join(DataDir(), "skills") }

// LockFile is held (flock) by the one pekoed serving this data directory.
func LockFile() string { return filepath.Join(DataDir(), "pekoed.lock") }

// TmuxSocketName is the private tmux server's -L name ($PEKOE_TMUX_SOCKET,
// default "pekoe").
func TmuxSocketName() string {
	if s := os.Getenv("PEKOE_TMUX_SOCKET"); s != "" {
		if s == "pekoe" && testing.Testing() {
			panic("paths: a test named the real tmux server -L pekoe")
		}
		return s
	}
	if testing.Testing() {
		panic("paths: a test reached the real tmux server -L pekoe (call testenv.Main in TestMain, or t.Setenv PEKOE_TMUX_SOCKET)")
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

// UnderWorktrees reports whether p is strictly inside WorktreesRoot, at
// least two levels down (<repo>/<slug>). p need not exist yet: symlinks are
// resolved on its deepest existing ancestor, so a worktree about to be made
// can be checked before git creates it.
func UnderWorktrees(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	abs = resolveExisting(abs)
	rootDir := resolveExisting(filepath.Clean(WorktreesRoot()))
	rel, err := filepath.Rel(rootDir, abs)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		strings.Count(rel, string(filepath.Separator)) >= 1
}

// Real is p (made absolute) with the symlinks of its deepest existing
// ancestor resolved: the path the kernel sees, which is what a sandbox
// profile has to name (on macOS /tmp is /private/tmp). p need not exist.
func Real(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return resolveExisting(abs)
}

// resolveExisting evaluates the symlinks of the deepest existing ancestor of
// the clean absolute path p and appends the rest unchanged.
func resolveExisting(p string) string {
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
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
