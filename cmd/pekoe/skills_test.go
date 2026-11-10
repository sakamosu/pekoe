package main

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"pekoe"
	"pekoe/internal/skills"
)

// The skills built into the binary are the repository's skills/, each
// with a frontmatter whose name is its directory (Claude Code finds a skill
// by that name) and a description.
func TestBuiltInSkills(t *testing.T) {
	names := skills.Names(pekoe.Docs)
	for _, want := range []string{"pekoe", "pekoe-orchestrate", "pekoe-review", "pekoe-work"} {
		if !strings.Contains(" "+strings.Join(names, " ")+" ", " "+want+" ") {
			t.Errorf("skill %s is not built in (have %v)", want, names)
		}
	}
	for _, n := range names {
		b, err := fs.ReadFile(pekoe.Docs, path.Join(skills.Root, n, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		head, _, ok := strings.Cut(strings.TrimPrefix(string(b), "---\n"), "\n---\n")
		if !strings.HasPrefix(string(b), "---\n") || !ok {
			t.Errorf("%s: no frontmatter", n)
			continue
		}
		if !strings.Contains(head, "\nname: "+n+"\n") && !strings.HasPrefix(head, "name: "+n+"\n") {
			t.Errorf("%s: frontmatter name is not %q:\n%s", n, n, head)
		}
		if !strings.Contains(head, "\ndescription: ") {
			t.Errorf("%s: no description", n)
		}
	}
}

func TestSkillsInstallCommand(t *testing.T) {
	base := t.TempDir()
	d := skills.Dirs{Links: filepath.Join(base, "claude", "skills"), Copies: filepath.Join(base, "data", "skills")}
	// one link made by hand, as the README used to say
	repo := filepath.Join(base, "repo", "skills", "pekoe")
	os.MkdirAll(repo, 0o755)
	os.MkdirAll(d.Links, 0o755)
	os.Symlink(repo, filepath.Join(d.Links, "pekoe"))

	check := func() []skills.Item {
		items, err := skills.Check(pekoe.Docs, d)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	no := func(string) bool { return false }
	yes := func(string) bool { return true }

	var out bytes.Buffer
	if err := skillsInstall(&out, d, check(), no); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "replace the link to "+repo) || !strings.Contains(out.String(), "pekoe-work") {
		t.Fatalf("plan:\n%s", out.String())
	}
	if _, err := os.Stat(d.Copies); !os.IsNotExist(err) {
		t.Fatal("wrote although the answer was no")
	}

	out.Reset()
	if err := skillsInstall(&out, d, check(), yes); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, it := range check() {
		if it.State != skills.Installed {
			t.Errorf("%s: %s after install", it.Name, it.State)
		}
	}
	out.Reset()
	skillsInstall(&out, d, check(), no)
	if !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("second install:\n%s", out.String())
	}

	out.Reset()
	printSkillStatus(&out, check())
	if !strings.Contains(out.String(), "pekoe-orchestrate  installed") {
		t.Fatalf("status:\n%s", out.String())
	}

	out.Reset()
	if err := skillsUninstall(&out, d, check(), yes); err != nil {
		t.Fatal(err)
	}
	for _, it := range check() {
		if it.State != skills.Missing {
			t.Errorf("%s: %s after uninstall", it.Name, it.State)
		}
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("the hand-made link's target is gone: %v", err)
	}
}

// A directory in the way is reported and the command fails, but the other
// skills are installed.
func TestSkillsInstallBlocked(t *testing.T) {
	base := t.TempDir()
	d := skills.Dirs{Links: filepath.Join(base, "claude", "skills"), Copies: filepath.Join(base, "data", "skills")}
	os.MkdirAll(filepath.Join(d.Links, "pekoe-review"), 0o755)
	items, _ := skills.Check(pekoe.Docs, d)
	var out bytes.Buffer
	err := skillsInstall(&out, d, items, func(string) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "pekoe-review") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "skip pekoe-review") {
		t.Fatalf("output:\n%s", out.String())
	}
	items, _ = skills.Check(pekoe.Docs, d)
	for _, it := range items {
		if it.Name != "pekoe-review" && it.State != skills.Installed {
			t.Errorf("%s: %s", it.Name, it.State)
		}
	}
}
