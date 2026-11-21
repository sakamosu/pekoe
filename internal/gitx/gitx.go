// Package gitx wraps the git CLI (no libgit2). Everything pekoe stores in git
// lives under refs/pekoe/, which is never pushed and never shows in git log.
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Unwrap() error { return e.Err }

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

// RunRaw runs git in dir (see guard.go for what keeps it from running
// commands an agent wrote) and returns its stdout.
func RunRaw(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	return run(ctx, dir, env, stdin, true, args...)
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
//
// path is looked at as git finds it (its .git file followed, a pin ignored):
// this is how a directory that is not ours yet, or no longer, is told apart.
func CheckWorktreeOf(repoDir, path string) error {
	unpinned := func(args ...string) (string, error) {
		out, err := run(context.Background(), path, nil, nil, false, args...)
		return strings.TrimRight(string(out), "\n"), err
	}
	top, err := unpinned("rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, path) {
		return ErrNotWorktree
	}
	mine, err := CommonDir(repoDir)
	if err != nil {
		return err
	}
	theirs, err := unpinned("rev-parse", "--path-format=absolute", "--git-common-dir")
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
// pass, and so do the ones pekoe suggests (feat/12-csv-export). It also
// applies git's own rules for a branch name (git check-ref-format --branch),
// so a name git would refuse ("feat/", "a..b", "x.lock") is turned down
// before a session, a port or a worktree is made.
func CheckBranchName(name string) error {
	if err := CheckRefArg("branch", name); err != nil {
		return err
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) || strings.ContainsRune(shellSpecial, r) || strings.ContainsRune(refSpecial, r) {
			return fmt.Errorf("invalid branch %q: %q is not allowed (use letters, digits, / - _ .)", name, r)
		}
	}
	if why := refFormat(name); why != "" {
		return fmt.Errorf("invalid branch %q: %s", name, why)
	}
	return nil
}

const (
	shellSpecial = "$`'\"\\;|&(){}<>*?[]~!#"
	// refSpecial: characters git never allows in a ref (on top of the
	// shell ones above, which already cover ~ ? * [ and the backslash)
	refSpecial = ":^"
)

// refFormat checks the structural rules of git check-ref-format --branch
// (the characters are checked by the caller). "" when name is fine.
func refFormat(name string) string {
	switch {
	case name == "HEAD" || name == "@":
		return "it names HEAD, not a branch"
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/"):
		return "must not start or end with '/'"
	case strings.Contains(name, "//"):
		return "must not contain '//'"
	case strings.Contains(name, ".."):
		return "must not contain '..'"
	case strings.Contains(name, "@{"):
		return "must not contain '@{'"
	case strings.HasSuffix(name, "."):
		return "must not end with '.'"
	}
	for _, c := range strings.Split(name, "/") {
		if strings.HasPrefix(c, ".") {
			return "no part between '/' may start with '.'"
		}
		if strings.HasSuffix(c, ".lock") {
			return "no part between '/' may end with '.lock'"
		}
	}
	return ""
}

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

// IsSSH tells whether remote URL u is one git reaches over ssh: ssh:// (or
// git+ssh://), or the scp-like host:path. Its host (NormalizeURL's first
// part) may then be a Host alias of ~/.ssh/config.
func IsSSH(u string) bool {
	u = strings.TrimSpace(u)
	if scheme, _, ok := strings.Cut(u, "://"); ok {
		scheme = strings.ToLower(scheme)
		return scheme == "ssh" || scheme == "git+ssh"
	}
	return scpLike.MatchString(u)
}

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

// AddWorktreeNoTrack is AddWorktree for a new branch started from a
// remote-tracking ref (origin/main): the branch does not take it as its
// upstream (a plain push must not go to the base).
func AddWorktreeNoTrack(repoDir, path, branch, start string) error {
	if BranchExists(repoDir, branch) {
		return AddWorktree(repoDir, path, branch, start)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := Run(repoDir, "worktree", "add", "--no-track", "-b", branch, "--end-of-options", path, start)
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

// SameChange: does head change the same as reviewed, each against its
// merge-base with base? It is what tells "the reviewed change, rebased
// onto a newer base" (pekoe land) from a head that changes more, less or
// elsewhere. reviewed's change is put onto head's merge-base the way a
// rebase would (a three-way merge from reviewed's merge-base, in memory:
// merge-tree writes no ref and touches no worktree), and the change is the
// same only when that gives head's tree exactly: every file, every byte,
// every place. A merge that conflicts is the same change only when every
// conflict is two additions at one place (the base side of the hunk is
// empty: both the new base and the reviewed change only added lines
// there) and head keeps both, whole and in one order (sameAfterAdditions);
// any other conflict (both sides changed the same lines, a file changed
// on one side and deleted on the other) is not. Commits git cannot find
// are an error (nothing to compare).
func SameChange(dir, reviewed, head, base string) (bool, error) {
	r, err := RevParse(dir, reviewed)
	if err != nil {
		return false, err
	}
	h, err := RevParse(dir, head)
	if err != nil {
		return false, err
	}
	mbR, err := MergeBase(dir, r, base)
	if err != nil {
		return false, err
	}
	mbH, err := MergeBase(dir, h, base)
	if err != nil {
		return false, err
	}
	if r == mbR {
		return false, nil // reviewed changes nothing: nothing was reviewed
	}
	want, err := Run(dir, "rev-parse", "--verify", "--quiet", "--end-of-options", h+"^{tree}")
	if err != nil {
		return false, err
	}
	// diff3: a conflict shows the base's lines too, so that two additions
	// at one place (an empty base side) can be told from two edits
	out, err := RunRaw(context.Background(), dir, nil, nil, "-c", "merge.conflictStyle=diff3", "merge-tree", "--write-tree", "-z", "--no-messages", "--merge-base", mbR, "--end-of-options", mbH, r)
	conflicts := false
	if err != nil {
		var ge *Error
		var ee *exec.ExitError
		if !errors.As(err, &ge) || !errors.As(ge.Err, &ee) || ee.ExitCode() != 1 {
			return false, err
		}
		conflicts = true
	}
	fields := strings.Split(string(out), "\x00")
	got := fields[0]
	if got == "" {
		return false, nil
	}
	if !conflicts {
		return got == want, nil
	}
	return sameAfterAdditions(dir, got, h, [3]string{mbH, mbR, r}, fields[1:])
}

// sameAfterAdditions: does head's tree hold the merge tree got (with
// conflict markers) resolved by keeping both sides of every conflict, when
// every conflict is two additions at one place? info is merge-tree -z's
// conflicted file info ("<mode> <oid> <stage>\t<path>", up to an empty
// field); labels are what merge-tree was given for ours, the base and
// theirs (its markers name them). Anything else is not the same change: a
// path not conflicted that differs from head, a conflict without both
// sides' content (one side deleted or renamed the file), a hunk whose base
// side has lines, a marker git did not write the usual way, and a file
// whose own lines (in any of its three versions) look like markers: those
// could not be told from git's.
func sameAfterAdditions(dir, got, head string, labels [3]string, info []string) (bool, error) {
	stages := map[string]map[string]bool{}
	var blobs []string
	for _, f := range info {
		if f == "" {
			break
		}
		meta, path, ok := strings.Cut(f, "\t")
		parts := strings.Fields(meta)
		if !ok || len(parts) != 3 {
			return false, nil
		}
		if stages[path] == nil {
			stages[path] = map[string]bool{}
		}
		stages[path][parts[2]] = true
		blobs = append(blobs, parts[1])
	}
	if len(stages) == 0 {
		return false, nil
	}
	diff, err := RunRaw(context.Background(), dir, nil, nil, "diff-tree", "-r", "-z", "--no-renames", "--name-only", "--end-of-options", got, head+"^{tree}")
	if err != nil {
		return false, err
	}
	for _, p := range strings.Split(string(diff), "\x00") {
		if p != "" && stages[p] == nil {
			return false, nil // a file the merge did not conflict on differs
		}
	}
	for _, oid := range blobs {
		b, err := RunRaw(context.Background(), dir, nil, nil, "cat-file", "blob", oid)
		if err != nil {
			return false, err
		}
		for _, l := range strings.Split(string(b), "\n") {
			if markerLike(strings.TrimSuffix(l, "\r")) {
				return false, nil
			}
		}
	}
	for path, st := range stages {
		if !st["2"] || !st["3"] {
			return false, nil // one side has no content for it (deleted, renamed)
		}
		// the same mode and type as head's (a symlink or a submodule is not text)
		gm, err := Run(dir, "ls-tree", "-z", "--format=%(objectmode)", "--end-of-options", got, "--", path)
		if err != nil {
			return false, err
		}
		hm, err := Run(dir, "ls-tree", "-z", "--format=%(objectmode)", "--end-of-options", head, "--", path)
		if err != nil {
			return false, err
		}
		if gm != "100644\x00" && gm != "100755\x00" || gm != hm {
			return false, nil
		}
		merged, err := RunRaw(context.Background(), dir, nil, nil, "cat-file", "blob", got+":"+path)
		if err != nil {
			return false, err
		}
		want, err := RunRaw(context.Background(), dir, nil, nil, "cat-file", "blob", head+":"+path)
		if err != nil {
			return false, err
		}
		ours, theirs, ok := keepBoth(string(merged), labels)
		if !ok || (ours != string(want) && theirs != string(want)) {
			return false, nil
		}
	}
	return true, nil
}

// markerLike: a line git could have written as a conflict marker (any
// label, any of the four kinds).
func markerLike(l string) bool {
	for _, m := range []string{"<<<<<<<", "|||||||", "=======", ">>>>>>>"} {
		if strings.HasPrefix(l, m) {
			return true
		}
	}
	return false
}

// keepBoth resolves the diff3 conflicts in text by keeping both sides,
// ours first (ours) and theirs first (theirs). ok is false when a hunk's
// base side has lines (both sides changed what was there: no order of the
// two keeps the change that was reviewed), when text has no conflict, or
// when a marker line shows up where git does not write one, or names
// other than labels (ours, base, theirs: what merge-tree was given). A
// file whose own lines look like markers is not told apart here: the
// caller turns those down first.
func keepBoth(text string, labels [3]string) (ours, theirs string, ok bool) {
	start, base, mid, end := "<<<<<<< "+labels[0], "||||||| "+labels[1], "=======", ">>>>>>> "+labels[2]
	marker := markerLike
	lines := strings.SplitAfter(text, "\n")
	var a, b strings.Builder
	hunks := 0
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSuffix(lines[i], "\n")
		if !marker(l) {
			a.WriteString(lines[i])
			b.WriteString(lines[i])
			continue
		}
		if l != start {
			return "", "", false
		}
		var o, t []string
		i++
		for ; i < len(lines) && !marker(strings.TrimSuffix(lines[i], "\n")); i++ {
			o = append(o, lines[i])
		}
		if i >= len(lines) || strings.TrimSuffix(lines[i], "\n") != base {
			return "", "", false
		}
		i++
		if i >= len(lines) || strings.TrimSuffix(lines[i], "\n") != mid {
			return "", "", false // the base side has lines
		}
		i++
		for ; i < len(lines) && !marker(strings.TrimSuffix(lines[i], "\n")); i++ {
			t = append(t, lines[i])
		}
		if i >= len(lines) || lines[i] != end+"\n" {
			return "", "", false
		}
		hunks++
		a.WriteString(strings.Join(o, "") + strings.Join(t, ""))
		b.WriteString(strings.Join(t, "") + strings.Join(o, ""))
	}
	if hunks == 0 {
		return "", "", false
	}
	return a.String(), b.String(), true
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
	Cut  bool   `json:"cut,omitempty"` // ReadDiff cut the text at its lineMax
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

// ParseDiff parses git diff's output (all of it in memory: ReadDiff reads
// a stream).
func ParseDiff(s string) []FileDiff {
	var p diffParser
	for _, ln := range strings.Split(s, "\n") {
		p.line(ln, false)
	}
	return p.finish()
}

// ReadDiff parses a diff as it is read, holding only what it keeps.
// lineMax > 0 cuts the text of a hunk's line at lineMax bytes (never half
// a character; Cut is set) without reading the rest into memory. keep,
// when set, is asked about each line of a hunk (its text already cut): a
// line it refuses is counted (the file's Added / Deleted, the hunk's
// numbering, skipped) but not kept, and a hunk left with no line is
// dropped. A last line without its "\n" (git ends every line with one, so
// the reader was stopped in the middle of it) is marked cut. The error is
// r's.
func ReadDiff(r io.Reader, lineMax int, keep func(Line) bool) (files []FileDiff, skipped int, err error) {
	p := diffParser{lineMax: lineMax, keep: keep}
	br := bufio.NewReaderSize(r, 64<<10)
	var long []byte // a line longer than the reader's buffer: its head
	for {
		b, rerr := br.ReadSlice('\n')
		if rerr == bufio.ErrBufferFull {
			// the head is enough for any header (a path is at most 4096
			// bytes) and for a hunk's line cut at lineMax; the rest is not kept
			if long == nil {
				long = append([]byte{}, b...)
			}
			continue
		}
		cut := long != nil
		if cut {
			b, long = long, nil
		}
		if rerr != nil && rerr != io.EOF {
			return p.finish(), p.skipped, rerr
		}
		ln := strings.TrimSuffix(string(b), "\n")
		if rerr == io.EOF {
			if ln != "" || cut {
				p.line(ln, true)
			}
			return p.finish(), p.skipped, nil
		}
		p.line(ln, cut)
	}
}

// diffParser reads a diff one line at a time (ParseDiff, ReadDiff).
type diffParser struct {
	lineMax int
	keep    func(Line) bool
	skipped int

	files            []FileDiff
	f                *FileDiff
	h                *Hunk
	oldN, newN       int
	oldLeft, newLeft int  // lines the hunk's header says are still to come
	dropped          bool // keep refused a line of h
}

func (p *diffParser) finishHunk() {
	if p.f != nil && p.h != nil && (len(p.h.Lines) > 0 || !p.dropped) {
		p.h.ID = HunkID(p.f.Path, *p.h)
		p.f.Hunks = append(p.f.Hunks, *p.h)
	}
	p.h, p.dropped = nil, false
}

// hunkLine adds a line of the current hunk (kind is " ", "+" or "-").
func (p *diffParser) hunkLine(kind, text string, cut bool) {
	l := Line{Kind: kind, Text: text, Cut: cut}
	switch kind {
	case "+":
		l.New = p.newN
		p.newN++
		p.newLeft--
		p.f.Added++
	case "-":
		l.Old = p.oldN
		p.oldN++
		p.oldLeft--
		p.f.Deleted++
	default:
		l.Old, l.New = p.oldN, p.newN
		p.oldN++
		p.newN++
		p.oldLeft--
		p.newLeft--
	}
	if p.lineMax > 0 && len(l.Text) > p.lineMax {
		l.Text, l.Cut = cutUTF8(l.Text, p.lineMax), true
	}
	if p.keep != nil && !p.keep(l) {
		p.skipped++
		p.dropped = true
		return
	}
	p.h.Lines = append(p.h.Lines, l)
}

// line takes one line of the diff (without its "\n"); cut: the rest of
// it was not read.
func (p *diffParser) line(ln string, cut bool) {
	switch {
	case strings.HasPrefix(ln, "diff --git "):
		p.finishHunk()
		p.files = append(p.files, FileDiff{Status: "M"})
		p.f = &p.files[len(p.files)-1]
		// diff --git a/x b/y (the only names a binary or mode-only
		// change gets)
		p.f.OldPath, p.f.Path = diffGitPaths(strings.TrimPrefix(ln, "diff --git "))
	case p.f == nil:
	case p.h == nil && strings.HasPrefix(ln, "new file mode"):
		p.f.Status = "A"
	case p.h == nil && strings.HasPrefix(ln, "deleted file mode"):
		p.f.Status = "D"
	case p.h == nil && strings.HasPrefix(ln, "rename from "):
		p.f.Status = "R"
		p.f.OldPath = unquotePath(strings.TrimPrefix(ln, "rename from "))
	case p.h == nil && strings.HasPrefix(ln, "rename to "):
		p.f.Path = unquotePath(strings.TrimPrefix(ln, "rename to "))
	case p.h == nil && strings.HasPrefix(ln, "Binary files"):
		p.f.Binary = true
	case p.h == nil && strings.HasPrefix(ln, "--- "):
	case p.h == nil && strings.HasPrefix(ln, "+++ "):
		if hp := headerPath(strings.TrimPrefix(ln, "+++ ")); hp != "/dev/null" {
			p.f.Path = strings.TrimPrefix(hp, "b/")
		}
	case strings.HasPrefix(ln, "@@"):
		p.finishHunk()
		m := hunkRe.FindStringSubmatch(ln)
		if m == nil {
			return
		}
		h := &Hunk{Header: ln, OldStart: atoi(m[1], 0), OldLines: atoi(m[2], 1), NewStart: atoi(m[3], 0), NewLines: atoi(m[4], 1)}
		p.h = h
		p.oldN, p.newN = h.OldStart, h.NewStart
		p.oldLeft, p.newLeft = h.OldLines, h.NewLines
	case p.h != nil && strings.HasPrefix(ln, "+"):
		p.hunkLine("+", ln[1:], cut)
	case p.h != nil && strings.HasPrefix(ln, "-"):
		p.hunkLine("-", ln[1:], cut)
	case p.h != nil && strings.HasPrefix(ln, " "):
		p.hunkLine(" ", ln[1:], cut)
	case p.h != nil && ln == "" && p.oldLeft > 0 && p.newLeft > 0:
		// a blank context line under diff.suppressBlankEmpty (git prints
		// no space before it); the end of the output is not one
		p.hunkLine(" ", "", false)
	case p.h != nil && strings.HasPrefix(ln, `\`):
		// "\ No newline at end of file"
	}
}

func (p *diffParser) finish() []FileDiff {
	p.finishHunk()
	if p.f != nil && p.f.Path == "" {
		p.f.Path = p.f.OldPath
	}
	return p.files
}

// cutUTF8 keeps at most n bytes of s, never half a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
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
