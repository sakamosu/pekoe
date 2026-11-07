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
	if err := UpdateRef(dir, "refs/boba/s/prompt-1", sha); err != nil {
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
	if err := AddExclude(dir, ".boba/"); err != nil {
		t.Fatal(err)
	}
	AddExclude(dir, ".boba/")
	b, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if strings.Count(string(b), ".boba/") != 1 {
		t.Fatalf("exclude: %q", b)
	}
}
