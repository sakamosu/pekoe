package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func testFS(text string) fstest.MapFS {
	return fstest.MapFS{
		"skills/pekoe/SKILL.md":      {Data: []byte("pekoe " + text)},
		"skills/pekoe-work/SKILL.md": {Data: []byte("work " + text)},
		"skills/README.md":           {Data: []byte("not a skill")},
		"skills/empty/notes.txt":     {Data: []byte("no SKILL.md: not a skill")},
	}
}

func testDirs(t *testing.T) Dirs {
	t.Helper()
	base := t.TempDir()
	return Dirs{Links: filepath.Join(base, "claude", "skills"), Copies: filepath.Join(base, "data", "skills")}
}

func states(t *testing.T, fsys fs.FS, d Dirs) map[string]State {
	t.Helper()
	items, err := Check(fsys, d)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]State{}
	for _, it := range items {
		m[it.Name] = it.State
	}
	return m
}

func TestNames(t *testing.T) {
	got := Names(testFS("v1"))
	if len(got) != 2 || got[0] != "pekoe" || got[1] != "pekoe-work" {
		t.Fatalf("Names = %v", got)
	}
}

func TestInstallStatusUninstall(t *testing.T) {
	d := testDirs(t)
	fsys := testFS("v1")
	items, _ := Check(fsys, d)
	for _, it := range items {
		if it.State != Missing {
			t.Fatalf("%s: %s before install", it.Name, it.State)
		}
	}
	if skipped, err := Install(fsys, d, items, false); err != nil || len(skipped) > 0 {
		t.Fatalf("install: %v skipped=%v", err, skipped)
	}
	for name, st := range states(t, fsys, d) {
		if st != Installed {
			t.Errorf("%s: %s after install", name, st)
		}
	}
	b, err := os.ReadFile(filepath.Join(d.Links, "pekoe-work", "SKILL.md"))
	if err != nil || string(b) != "work v1" {
		t.Fatalf("through the link: %q %v", b, err)
	}

	// a newer binary: the copy is stale until Refresh (pekoed's start)
	fsys2 := testFS("v2")
	if st := states(t, fsys2, d)["pekoe"]; st != Stale {
		t.Fatalf("after the text changed: %s", st)
	}
	if err := Refresh(fsys2, d); err != nil {
		t.Fatal(err)
	}
	if st := states(t, fsys2, d)["pekoe"]; st != Installed {
		t.Fatalf("after Refresh: %s", st)
	}
	b, _ = os.ReadFile(filepath.Join(d.Links, "pekoe", "SKILL.md"))
	if string(b) != "pekoe v2" {
		t.Fatalf("Refresh did not rewrite: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(d.Copies, "*", "*.pekoe-tmp")); len(left) > 0 {
		t.Fatalf("tmp left behind: %v", left)
	}

	items, _ = Check(fsys2, d)
	if err := Uninstall(fsys2, d, items); err != nil {
		t.Fatal(err)
	}
	for name, st := range states(t, fsys2, d) {
		if st != Missing {
			t.Errorf("%s: %s after uninstall", name, st)
		}
	}
	if _, err := os.Stat(d.Copies); !os.IsNotExist(err) {
		t.Fatalf("copies left: %v", err)
	}
}

// Refresh only touches skills that were installed.
func TestRefreshDoesNotInstall(t *testing.T) {
	d := testDirs(t)
	if err := Refresh(testFS("v1"), d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Copies); !os.IsNotExist(err) {
		t.Fatalf("Refresh created copies: %v", err)
	}
}

// A link made by hand (ln -s <repo>/skills/pekoe ~/.claude/skills/pekoe)
// is Elsewhere: left alone unless replace, and replacing it keeps what it
// pointed at. A link to the same place (relative, or through a symlinked
// directory) counts as ours.
func TestLinkMadeByHand(t *testing.T) {
	d := testDirs(t)
	fsys := testFS("v1")
	repo := filepath.Join(t.TempDir(), "repo", "skills", "pekoe")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "SKILL.md"), []byte("by hand"), 0o644)
	os.MkdirAll(d.Links, 0o755)
	if err := os.Symlink(repo, filepath.Join(d.Links, "pekoe")); err != nil {
		t.Fatal(err)
	}
	items, _ := Check(fsys, d)
	if items[0].State != Elsewhere || items[0].Points != repo {
		t.Fatalf("hand-made link: %+v", items[0])
	}
	skipped, err := Install(fsys, d, items, false)
	if err != nil || len(skipped) != 1 || skipped[0] != "pekoe" {
		t.Fatalf("install without replace: %v skipped=%v", err, skipped)
	}
	if to, _ := os.Readlink(filepath.Join(d.Links, "pekoe")); to != repo {
		t.Fatalf("replaced without asking: %s", to)
	}
	if st := states(t, fsys, d)["pekoe-work"]; st != Installed {
		t.Fatalf("the other skill was not installed: %s", st)
	}

	items, _ = Check(fsys, d)
	if _, err := Install(fsys, d, items, true); err != nil {
		t.Fatal(err)
	}
	if st := states(t, fsys, d)["pekoe"]; st != Installed {
		t.Fatalf("after replace: %s", st)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "SKILL.md")); string(b) != "by hand" {
		t.Fatalf("replacing the link touched its target: %q", b)
	}

	// uninstall leaves a hand-made link alone
	os.Remove(filepath.Join(d.Links, "pekoe"))
	os.Symlink(repo, filepath.Join(d.Links, "pekoe"))
	items, _ = Check(fsys, d)
	if err := Uninstall(fsys, d, items); err != nil {
		t.Fatal(err)
	}
	if to, _ := os.Readlink(filepath.Join(d.Links, "pekoe")); to != repo {
		t.Fatalf("uninstall removed a hand-made link: %q", to)
	}
}

func TestSameTargetCountsAsOurs(t *testing.T) {
	d := testDirs(t)
	fsys := testFS("v1")
	items, _ := Check(fsys, d)
	Install(fsys, d, items, false)
	// the same copy, linked relatively and through a symlinked directory
	link := filepath.Join(d.Links, "pekoe")
	os.Remove(link)
	rel, _ := filepath.Rel(d.Links, filepath.Join(d.Copies, "pekoe"))
	os.Symlink(rel, link)
	if st := states(t, fsys, d)["pekoe"]; st != Installed {
		t.Fatalf("relative link to the copy: %s", st)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	os.Symlink(d.Copies, alias)
	os.Remove(link)
	os.Symlink(filepath.Join(alias, "pekoe"), link)
	if st := states(t, fsys, d)["pekoe"]; st != Installed {
		t.Fatalf("link through a symlinked directory: %s", st)
	}
}

// A real directory (a skill copied by hand, or someone else's) is never
// moved or replaced.
func TestDirectoryIsBlocked(t *testing.T) {
	d := testDirs(t)
	fsys := testFS("v1")
	own := filepath.Join(d.Links, "pekoe")
	os.MkdirAll(own, 0o755)
	os.WriteFile(filepath.Join(own, "SKILL.md"), []byte("mine"), 0o644)
	items, _ := Check(fsys, d)
	if items[0].State != Blocked {
		t.Fatalf("directory: %s", items[0].State)
	}
	skipped, err := Install(fsys, d, items, true)
	if err != nil || len(skipped) != 1 {
		t.Fatalf("install: %v skipped=%v", err, skipped)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "SKILL.md")); string(b) != "mine" {
		t.Fatalf("directory touched: %q", b)
	}
	items, _ = Check(fsys, d)
	Uninstall(fsys, d, items)
	if b, _ := os.ReadFile(filepath.Join(own, "SKILL.md")); string(b) != "mine" {
		t.Fatalf("uninstall touched the directory: %q", b)
	}
}
