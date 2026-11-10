// Package gitx wraps the git CLI (no libgit2). Everything pekoe stores in git
// lives under refs/pekoe/, which is never pushed and never shows in git log.
package gitx

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

// Run executes git in dir and returns trimmed stdout.
func Run(dir string, args ...string) (string, error) {
	return RunEnv(dir, nil, args...)
}

func RunEnv(dir string, env []string, args ...string) (string, error) {
	out, err := RunRaw(context.Background(), dir, env, nil, args...)
	return strings.TrimRight(string(out), "\n"), err
}

func RunRaw(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	// paths in git's output as they are (core.quotePath defaults to true:
	// "\346\227\245..." for non-ASCII names), whatever the user's config
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotePath=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return out, nil
}

// Toplevel returns the worktree root containing dir.
func Toplevel(dir string) (string, error) { return Run(dir, "rev-parse", "--show-toplevel") }

// CommonDir returns the shared .git directory (same for all worktrees).
func CommonDir(dir string) (string, error) {
	d, err := Run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return d, nil
}

// ErrNotWorktree: the directory is not the top of a git worktree (it may sit
// inside one, or not be under git at all).
var ErrNotWorktree = errors.New("not a git worktree")

// ErrForeignWorktree: the directory is a worktree of another repository.
var ErrForeignWorktree = errors.New("a worktree of another repository")

// CheckWorktreeOf tells whether path is the top of a worktree of the
// repository whose main worktree (or any worktree) is repoDir, by comparing
// their common git directories. Repositories that share a name (forks, or
// clones without an origin) share ~/worktrees/<name>, so a directory found
// there is not necessarily ours. nil = ours; ErrNotWorktree; ErrForeignWorktree.
func CheckWorktreeOf(repoDir, path string) error {
	top, err := Toplevel(path)
	if err != nil || !samePath(top, path) {
		return ErrNotWorktree
	}
	mine, err := CommonDir(repoDir)
	if err != nil {
		return err
	}
	theirs, err := CommonDir(path)
	if err != nil {
		return ErrNotWorktree
	}
	if !samePath(mine, theirs) {
		return ErrForeignWorktree
	}
	return nil
}

func samePath(a, b string) bool {
	clean := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	return clean(a) == clean(b)
}

// CheckRefArg rejects a ref name that git would read as an option. Callers
// also put --end-of-options before such arguments; this catches the value
// early with a clear message.
func CheckRefArg(what, name string) error {
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid %s %q: must not start with '-'", what, name)
	}
	return nil
}

// CheckBranchName checks the name of a branch pekoe is asked to create. On
// top of CheckRefArg it refuses the characters a shell treats specially:
// the name goes into [hooks] commands as {{branch}}, which pekoed runs
// outside the agents' sandbox, and quoting cannot protect a {{branch}} the
// user put inside quotes of their own ("app_{{branch}}"). git itself allows
// most of them ($ ( ) | { } …). Ordinary names (letters, digits, / - _ .)
// pass, and so do the ones pekoe suggests (feat/12-csv-export).
func CheckBranchName(name string) error {
	if err := CheckRefArg("branch", name); err != nil {
		return err
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) || strings.ContainsRune(shellSpecial, r) {
			return fmt.Errorf("invalid branch %q: %q is not allowed (use letters, digits, / - _ .)", name, r)
		}
	}
	return nil
}

const shellSpecial = "$`'\"\\;|&(){}<>*?[]~!#"

// MainWorktree returns the primary worktree path of the repository.
func MainWorktree(dir string) (string, error) {
	wts, err := Worktrees(dir)
	if err != nil {
		return "", err
	}
	if len(wts) == 0 {
		return Toplevel(dir)
	}
	return wts[0].Path, nil
}

func Origin(dir string) string {
	u, _ := Run(dir, "remote", "get-url", "origin")
	return u
}

var scpLike = regexp.MustCompile(`^(?:[\w.-]+@)?([\w.-]+):(.+)$`)

// NormalizeURL turns git@github.com:me/app.git and https://github.com/me/app
// into github.com/me/app.
func NormalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if at := strings.LastIndex(strings.SplitN(u, "/", 2)[0], "@"); at >= 0 {
			u = u[at+1:]
		}
		// drop port
		host, rest, _ := strings.Cut(u, "/")
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		u = host + "/" + rest
	} else if m := scpLike.FindStringSubmatch(u); m != nil {
		u = m[1] + "/" + m[2]
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return strings.ToLower(u)
}

// RepoID is the normalized origin URL with / replaced by _, or the main
// worktree path when there is no origin (or the origin gives no usable id).
func RepoID(dir string) (string, error) {
	if n := NormalizeURL(Origin(dir)); n != "" {
		if id := strings.ReplaceAll(n, "/", "_"); pathElem(id) {
			return id, nil
		}
	}
	p, err := MainWorktree(dir)
	if err != nil {
		return "", err
	}
	p = strings.TrimPrefix(filepath.Clean(p), "/")
	return "path_" + strings.ReplaceAll(p, "/", "_"), nil
}

// RepoName is the short name used under ~/worktrees/<repo>. It is always a
// single path element: an origin of "x:.." or "https://host/a/.." would
// otherwise put worktrees outside ~/worktrees (filepath.Join cleans the "..").
func RepoName(dir string) string {
	if n := NormalizeURL(Origin(dir)); n != "" {
		if b := filepath.Base(n); pathElem(b) {
			return b
		}
	}
	if p, err := MainWorktree(dir); err == nil {
		if b := filepath.Base(p); pathElem(b) {
			return b
		}
	}
	return "repo"
}

// pathElem reports whether s can stand as one element of a path: not empty,
// not "." or "..", no separator, no NUL.
func pathElem(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

type WorktreeInfo struct {
	Path   string `json:"path"`
	Head   string `json:"head"`
	Branch string `json:"branch"`
	Bare   bool   `json:"bare"`
}

func Worktrees(dir string) ([]WorktreeInfo, error) {
	out, err := Run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var res []WorktreeInfo
	var cur *WorktreeInfo
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(ln, "worktree "):
			res = append(res, WorktreeInfo{Path: strings.TrimPrefix(ln, "worktree ")})
			cur = &res[len(res)-1]
		case cur == nil:
		case strings.HasPrefix(ln, "HEAD "):
			cur.Head = strings.TrimPrefix(ln, "HEAD ")
		case strings.HasPrefix(ln, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(ln, "branch "), "refs/heads/")
		case ln == "bare":
			cur.Bare = true
		}
	}
	return res, nil
}

func BranchExists(dir, branch string) bool {
	_, err := Run(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// DefaultBase guesses the base branch: origin/HEAD, then main, then master.
func DefaultBase(dir string) string {
	if r, err := Run(dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && r != "" {
		return strings.TrimPrefix(r, "origin/")
	}
	for _, b := range []string{"main", "master", "trunk", "develop"} {
		if BranchExists(dir, b) {
			return b
		}
	}
	return CurrentBranch(dir)
}

// CurrentBranch returns the checked-out branch name. symbolic-ref is tried
// first because it also works on an unborn branch (a repository with no
// commits yet), where `rev-parse --abbrev-ref HEAD` prints "HEAD" and fails.
// On a detached HEAD it falls back to rev-parse, which yields "HEAD".
func CurrentBranch(dir string) string {
	if b, err := Run(dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && b != "" {
		return b
	}
	h, _ := Run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	return h
}

// AddWorktree creates path on branch (creating the branch from base if needed).
func AddWorktree(repoDir, path, branch, base string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if BranchExists(repoDir, branch) {
		_, err := Run(repoDir, "worktree", "add", "--end-of-options", path, branch)
		return err
	}
	args := []string{"worktree", "add", "-b", branch, "--end-of-options", path}
	if base != "" {
		args = append(args, base)
	}
	_, err := Run(repoDir, args...)
	return err
}

func RemoveWorktree(repoDir, path string, force bool) error {
	args := []string{"worktree", "remove", "--end-of-options", path}
	if force {
		args = []string{"worktree", "remove", "--force", "--end-of-options", path}
	}
	_, err := Run(repoDir, args...)
	if err == nil {
		_, _ = Run(repoDir, "worktree", "prune")
	}
	return err
}

// IgnoredFiles lists ignored files that exist (git ls-files --others --ignored).
func IgnoredFiles(dir string) ([]string, error) {
	out, err := Run(dir, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ---- snapshots under refs/pekoe/<slug>/ ----

// SnapshotTree writes the full working tree (tracked + untracked, minus
// ignored) to a tree object without touching the real index.
func SnapshotTree(dir string) (string, error) {
	gitDir, err := Run(dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(gitDir, "pekoe-index-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	env := []string{"GIT_INDEX_FILE=" + tmp.Name()}
	if HasHead(dir) {
		if _, err := RunEnv(dir, env, "read-tree", "HEAD"); err != nil {
			return "", err
		}
	} else {
		os.Remove(tmp.Name())
	}
	if _, err := RunEnv(dir, env, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	return RunEnv(dir, env, "write-tree")
}

func HasHead(dir string) bool {
	_, err := Run(dir, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// SnapshotCommit records the working tree as a commit (parent HEAD) and
// returns its sha. It does not move any branch.
func SnapshotCommit(dir, message string) (string, error) {
	tree, err := SnapshotTree(dir)
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", message}
	if HasHead(dir) {
		args = []string{"commit-tree", tree, "-p", "HEAD", "-m", message}
	}
	env := []string{
		"GIT_AUTHOR_NAME=pekoe", "GIT_AUTHOR_EMAIL=pekoe@localhost",
		"GIT_COMMITTER_NAME=pekoe", "GIT_COMMITTER_EMAIL=pekoe@localhost",
	}
	return RunEnv(dir, env, args...)
}

func UpdateRef(dir, ref, sha string) error {
	_, err := Run(dir, "update-ref", ref, sha)
	return err
}

func DeleteRefs(dir, prefix string) {
	out, err := Run(dir, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil || out == "" {
		return
	}
	for _, r := range strings.Split(out, "\n") {
		_, _ = Run(dir, "update-ref", "-d", r)
	}
}

func RevParse(dir, rev string) (string, error) {
	return Run(dir, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
}

func MergeBase(dir, a, b string) (string, error) {
	return Run(dir, "merge-base", "--end-of-options", a, b)
}

// beforeRemove runs between RestoreSnapshot's diff and its removes (tests).
var beforeRemove func()

// RestoreSnapshot makes the working tree match commit (undo a prompt).
// Files that exist now but not in the snapshot are deleted.
func RestoreSnapshot(dir, commit string) error {
	cur, err := SnapshotTree(dir)
	if err != nil {
		return err
	}
	// -z: names unquoted even with tabs or newlines in them
	out, err := RunRaw(context.Background(), dir, nil, nil, "diff", "--name-status", "--no-renames", "-z", commit, cur)
	if err != nil {
		return err
	}
	f := strings.Split(string(out), "\x00")
	// Removed through an os.Root of dir: the agent can swap a directory on
	// the way for a symlink between the diff and the remove, and a plain
	// os.Remove would follow it out of the worktree (~/.zshrc for
	// a/.zshrc). The root refuses any path that leaves dir, race or not.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if beforeRemove != nil {
		beforeRemove()
	}
	for i := 0; i+1 < len(f); i += 2 {
		if f[i] == "A" { // added after the snapshot
			_ = root.Remove(f[i+1])
		}
	}
	_, err = Run(dir, "restore", "--source="+commit, "--worktree", "--", ":/")
	return err
}

// ---- diffs ----

type Line struct {
	Kind string `json:"kind"` // " ", "+", "-"
	Old  int    `json:"old"`
	New  int    `json:"new"`
	Text string `json:"text"`
}

type Hunk struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Lines    []Line `json:"lines"`
}

type FileDiff struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Status  string `json:"status"` // A M D R
	Binary  bool   `json:"binary,omitempty"`
	Hunks   []Hunk `json:"hunks"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// Diff returns the parsed diff between two tree-ish objects.
func Diff(dir, from, to string, paths ...string) ([]FileDiff, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff", "-U3", "--find-renames", from, to}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	out, err := RunRaw(context.Background(), dir, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	return ParseDiff(string(out)), nil
}

func atoi(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// unquotePath undoes git's C-style quoting of a path ("tab\tname",
// "\346\227\245..."), which applies to names with control characters, quotes
// or backslashes (and, without core.quotePath=false, non-ASCII).
func unquotePath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// headerPath reads the name on a ---/+++ line: git adds a tab after an
// unquoted name containing a space.
func headerPath(p string) string {
	if !strings.HasPrefix(p, `"`) {
		p = strings.TrimSuffix(p, "\t")
	}
	return unquotePath(p)
}

// diffGitPaths splits "a/x b/y" from a diff --git line, either side quoted.
func diffGitPaths(rest string) (oldPath, path string) {
	var a, b string
	if strings.HasPrefix(rest, `"`) {
		// the quoted name ends at the first unescaped quote
		end := -1
		for i := 1; i < len(rest); i++ {
			if rest[i] == '\\' {
				i++
			} else if rest[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 || end+1 >= len(rest) {
			return "", ""
		}
		a, b = rest[:end+1], strings.TrimPrefix(rest[end+1:], " ")
	} else if i := strings.Index(rest, ` "b/`); i >= 0 {
		a, b = rest[:i], rest[i+1:]
	} else if i := strings.Index(rest, " b/"); i >= 0 {
		a, b = rest[:i], rest[i+1:]
	} else {
		return "", ""
	}
	return strings.TrimPrefix(unquotePath(a), "a/"), strings.TrimPrefix(unquotePath(b), "b/")
}

func ParseDiff(s string) []FileDiff {
	var files []FileDiff
	var f *FileDiff
	var h *Hunk
	oldN, newN := 0, 0
	finishHunk := func() {
		if f != nil && h != nil {
			h.ID = HunkID(f.Path, *h)
			f.Hunks = append(f.Hunks, *h)
		}
		h = nil
	}
	for _, ln := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			finishHunk()
			files = append(files, FileDiff{Status: "M"})
			f = &files[len(files)-1]
			// diff --git a/x b/y (the only names a binary or mode-only
			// change gets)
			f.OldPath, f.Path = diffGitPaths(strings.TrimPrefix(ln, "diff --git "))
		case f == nil:
		case h == nil && strings.HasPrefix(ln, "new file mode"):
			f.Status = "A"
		case h == nil && strings.HasPrefix(ln, "deleted file mode"):
			f.Status = "D"
		case h == nil && strings.HasPrefix(ln, "rename from "):
			f.Status = "R"
			f.OldPath = unquotePath(strings.TrimPrefix(ln, "rename from "))
		case h == nil && strings.HasPrefix(ln, "rename to "):
			f.Path = unquotePath(strings.TrimPrefix(ln, "rename to "))
		case h == nil && strings.HasPrefix(ln, "Binary files"):
			f.Binary = true
		case h == nil && strings.HasPrefix(ln, "--- "):
		case h == nil && strings.HasPrefix(ln, "+++ "):
			if p := headerPath(strings.TrimPrefix(ln, "+++ ")); p != "/dev/null" {
				f.Path = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(ln, "@@"):
			finishHunk()
			m := hunkRe.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			h = &Hunk{Header: ln, OldStart: atoi(m[1], 0), OldLines: atoi(m[2], 1), NewStart: atoi(m[3], 0), NewLines: atoi(m[4], 1)}
			oldN, newN = h.OldStart, h.NewStart
		case h != nil && strings.HasPrefix(ln, "+"):
			h.Lines = append(h.Lines, Line{Kind: "+", New: newN, Text: ln[1:]})
			newN++
			f.Added++
		case h != nil && strings.HasPrefix(ln, "-"):
			h.Lines = append(h.Lines, Line{Kind: "-", Old: oldN, Text: ln[1:]})
			oldN++
			f.Deleted++
		case h != nil && strings.HasPrefix(ln, " "):
			h.Lines = append(h.Lines, Line{Kind: " ", Old: oldN, New: newN, Text: ln[1:]})
			oldN++
			newN++
		case h != nil && strings.HasPrefix(ln, `\`):
			// "\ No newline at end of file"
		}
	}
	finishHunk()
	if f != nil && f.Path == "" {
		f.Path = f.OldPath
	}
	return files
}

// HunkID identifies a hunk by file path + its changed content, so the id
// survives line-number shifts (design: パス + 変更後内容のハッシュ).
func HunkID(path string, h Hunk) string {
	s := sha1.New()
	s.Write([]byte(path))
	s.Write([]byte{0})
	for _, l := range h.Lines {
		if l.Kind == " " {
			continue
		}
		s.Write([]byte(l.Kind))
		s.Write([]byte(l.Text))
		s.Write([]byte{'\n'})
	}
	return hex.EncodeToString(s.Sum(nil))[:16]
}

type Stat struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
	Files   int `json:"files"`
}

func DiffStat(files []FileDiff) Stat {
	var st Stat
	for _, f := range files {
		st.Added += f.Added
		st.Deleted += f.Deleted
		st.Files++
	}
	return st
}

// ShowFile returns the content of path at rev ("" if missing).
func ShowFile(dir, rev, path string) string {
	out, err := RunRaw(context.Background(), dir, nil, nil, "show", rev+":"+path)
	if err != nil {
		return ""
	}
	return string(out)
}

// AddExclude appends a pattern to .git/info/exclude (local only, never committed).
func AddExclude(dir, pattern string) error {
	common, err := CommonDir(dir)
	if err != nil {
		return err
	}
	p := filepath.Join(common, "info", "exclude")
	b, _ := os.ReadFile(p)
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == pattern {
			return nil
		}
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		fh.WriteString("\n")
	}
	_, err = fh.WriteString(pattern + "\n")
	return err
}

// ---- conflict forecast ----

func splitZ(b []byte) []string {
	var out []string
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ChangedFiles lists every path that differs from base in the worktree dir:
// committed, staged, unstaged and untracked (ignored files excluded).
func ChangedFiles(dir, base string) ([]string, error) {
	tracked, err := RunRaw(context.Background(), dir, nil, nil, "diff", "--name-only", "--no-renames", "-z", base)
	if err != nil {
		return nil, err
	}
	untracked, err := RunRaw(context.Background(), dir, nil, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	return append(splitZ(tracked), splitZ(untracked)...), nil
}

// DiffNames lists the paths that differ between two commits.
func DiffNames(dir, from, to string) ([]string, error) {
	out, err := RunRaw(context.Background(), dir, nil, nil, "diff", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

// MergeTree tries to merge commits a and b without touching any worktree,
// index or ref (git merge-tree --write-tree, git >= 2.38). clean reports
// whether the merge applies automatically; conflicts lists the paths that
// would conflict.
func MergeTree(dir, a, b string) (clean bool, conflicts []string, err error) {
	out, err := RunRaw(context.Background(), dir, nil, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", a, b)
	if err == nil {
		return true, nil, nil
	}
	var ge *Error
	var ee *exec.ExitError
	if !errors.As(err, &ge) || !errors.As(ge.Err, &ee) || ee.ExitCode() != 1 {
		return false, nil, err
	}
	// exit 1: <tree>NUL then the conflicted paths, each NUL-terminated, then
	// an empty entry before the (suppressed) messages
	seen := map[string]bool{}
	for i, p := range strings.Split(string(out), "\x00") {
		if i == 0 {
			continue
		}
		if p == "" {
			break
		}
		if !seen[p] {
			seen[p] = true
			conflicts = append(conflicts, p)
		}
	}
	return false, conflicts, nil
}
