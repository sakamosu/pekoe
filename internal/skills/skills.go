// Package skills installs the Claude Code skills built into the binary
// (skills/<name>/SKILL.md in the repository, embedded with the docs) for
// the user: each one is written out to pekoe's data directory
// (paths.SkillsDir) and linked from ~/.claude/skills/<name>
// ($CLAUDE_CONFIG_DIR/skills when set), so every agent, in any repository
// or worktree, can read it (docs/orchestration.md「スキルの分け方と配り方」).
//
// The copies, not the repository's skills/, are what the links point at:
// the text then always matches the installed binary (a skill names
// commands and flags), whichever branch the checkout is on. A newer pekoe
// rewrites the copies when pekoed starts (Refresh), so `pekoe update`
// brings the new text without installing again.
package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Root is the directory of the skills inside the embedded file system.
const Root = "skills"

// Dirs are where the links and the copies go.
type Dirs struct {
	Links  string // ~/.claude/skills
	Copies string // <data>/skills
}

// LinksDir is the user's Claude Code skills directory.
func LinksDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "skills")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "skills")
}

// Names lists the skills in fsys: the directories under Root that hold a
// SKILL.md, sorted.
func Names(fsys fs.FS) []string {
	ents, err := fs.ReadDir(fsys, Root)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, err := fs.Stat(fsys, path.Join(Root, e.Name(), "SKILL.md")); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// State of one skill's link.
type State string

const (
	Missing   State = "missing"   // nothing at the link's place
	Installed State = "installed" // our link, and the copy matches this binary
	Stale     State = "stale"     // our link, but the copy is missing or older
	Elsewhere State = "elsewhere" // a symlink to somewhere else (one made by hand, say)
	Blocked   State = "blocked"   // a directory or file pekoe did not make
)

// Item is one skill and what is at its link's place.
type Item struct {
	Name   string `json:"name"`
	State  State  `json:"state"`
	Link   string `json:"link"`             // ~/.claude/skills/<name>
	Copy   string `json:"copy"`             // <data>/skills/<name>
	Points string `json:"points,omitempty"` // where a link made elsewhere points
}

// Check looks at every skill in fsys.
func Check(fsys fs.FS, d Dirs) ([]Item, error) {
	var items []Item
	for _, name := range Names(fsys) {
		it := Item{Name: name, Link: filepath.Join(d.Links, name), Copy: filepath.Join(d.Copies, name)}
		fi, err := os.Lstat(it.Link)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			it.State = Missing
		case err != nil:
			return nil, err
		case fi.Mode()&fs.ModeSymlink == 0:
			it.State = Blocked
		default:
			to, err := os.Readlink(it.Link)
			if err != nil {
				return nil, err
			}
			if !sameFile(it.Link, to, it.Copy) {
				it.State, it.Points = Elsewhere, to
				break
			}
			it.State = Installed
			if same, err := copyMatches(fsys, name, it.Copy); err != nil {
				return nil, err
			} else if !same {
				it.State = Stale
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// sameFile: the link at link, whose text is to, points at want.
func sameFile(link, to, want string) bool {
	if !filepath.IsAbs(to) {
		to = filepath.Join(filepath.Dir(link), to)
	}
	if filepath.Clean(to) == filepath.Clean(want) {
		return true
	}
	a, err1 := filepath.EvalSymlinks(to)
	b, err2 := filepath.EvalSymlinks(want)
	return err1 == nil && err2 == nil && a == b
}

// files lists a skill's files in fsys, relative to its directory.
func files(fsys fs.FS, name string) ([]string, error) {
	dir := path.Join(Root, name)
	var out []string
	err := fs.WalkDir(fsys, dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			out = append(out, strings.TrimPrefix(p, dir+"/"))
		}
		return nil
	})
	return out, err
}

// copyMatches: every file of the skill is in the copy with the same bytes.
func copyMatches(fsys fs.FS, name, copyDir string) (bool, error) {
	list, err := files(fsys, name)
	if err != nil {
		return false, err
	}
	for _, f := range list {
		want, err := fs.ReadFile(fsys, path.Join(Root, name, f))
		if err != nil {
			return false, err
		}
		have, err := os.ReadFile(filepath.Join(copyDir, filepath.FromSlash(f)))
		if err != nil || !bytes.Equal(have, want) {
			return false, nil
		}
	}
	return true, nil
}

// writeCopy writes the skill's files into copyDir (each through a
// temporary file and a rename), leaving files that already match alone.
func writeCopy(fsys fs.FS, name, copyDir string) error {
	list, err := files(fsys, name)
	if err != nil {
		return err
	}
	for _, f := range list {
		want, err := fs.ReadFile(fsys, path.Join(Root, name, f))
		if err != nil {
			return err
		}
		dst := filepath.Join(copyDir, filepath.FromSlash(f))
		if have, err := os.ReadFile(dst); err == nil && bytes.Equal(have, want) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		tmp := dst + ".pekoe-tmp"
		if err := os.WriteFile(tmp, want, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}

// Install writes the copies and links the items that need it: Missing and
// Stale ones, and Elsewhere ones when replace is set (the caller asked the
// user). Blocked ones are never touched: pekoe does not move a directory
// it did not make. It returns the names it skipped.
func Install(fsys fs.FS, d Dirs, items []Item, replace bool) (skipped []string, err error) {
	for _, it := range items {
		switch it.State {
		case Blocked:
			skipped = append(skipped, it.Name)
			continue
		case Elsewhere:
			if !replace {
				skipped = append(skipped, it.Name)
				continue
			}
		}
		if err := writeCopy(fsys, it.Name, it.Copy); err != nil {
			return skipped, err
		}
		if it.State == Installed || it.State == Stale {
			continue // the link is already ours
		}
		if err := os.MkdirAll(d.Links, 0o755); err != nil {
			return skipped, err
		}
		if it.State == Elsewhere {
			// a symlink (Check saw it): removing it leaves what it points at
			if err := removeLink(it.Link); err != nil {
				return skipped, err
			}
		}
		if err := os.Symlink(it.Copy, it.Link); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

// removeLink removes p only if it is (still) a symlink.
func removeLink(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("%s is no longer a symlink; left alone", p)
	}
	return os.Remove(p)
}

// Uninstall removes pekoe's links (Installed and Stale; links made by hand
// and directories are left alone) and the copies of every skill in fsys.
func Uninstall(fsys fs.FS, d Dirs, items []Item) error {
	for _, it := range items {
		if it.State == Installed || it.State == Stale {
			if err := removeLink(it.Link); err != nil {
				return err
			}
		}
	}
	for _, name := range Names(fsys) {
		if err := os.RemoveAll(filepath.Join(d.Copies, name)); err != nil {
			return err
		}
	}
	os.Remove(d.Copies) // only when empty
	return nil
}

// Refresh rewrites the copies of the skills that were installed (the copy
// directory exists) when this binary's text differs: pekoed calls it at
// start, so a new pekoe brings its skills along. It never creates links.
func Refresh(fsys fs.FS, d Dirs) error {
	var first error
	for _, name := range Names(fsys) {
		dir := filepath.Join(d.Copies, name)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := writeCopy(fsys, name, dir); err != nil && first == nil {
			first = err
		}
	}
	return first
}
