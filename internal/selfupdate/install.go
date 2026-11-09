package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Dir is one of the ShareDirs where it is installed.
type Dir struct {
	Name   string // "nvim", "extension"
	Path   string // <prefix>/share/pekoe/<name>
	Exists bool
	LinkTo string // the link's target when Path is a symbolic link (make install links the repository's)
}

// Plan is what update replaces: the binary pekoe runs from (symbolic links
// resolved), the pekoed link next to it, and the ShareDirs under
// <prefix>/share/pekoe, where prefix is the binary's directory's parent.
type Plan struct {
	Exe      string
	ShareDir string // "" when <prefix>/share/pekoe does not exist (only the binary is replaced)
	Dirs     []Dir
}

// NewPlan looks at what is installed around exe (already resolved).
func NewPlan(exe string) (Plan, error) {
	if !filepath.IsAbs(exe) {
		return Plan{}, fmt.Errorf("%s: not an absolute path", exe)
	}
	p := Plan{Exe: exe}
	share := filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "pekoe")
	if fi, err := os.Stat(share); err == nil && fi.IsDir() {
		p.ShareDir = share
		for _, name := range ShareDirs {
			d := Dir{Name: name, Path: filepath.Join(share, name)}
			if fi, err := os.Lstat(d.Path); err == nil {
				d.Exists = true
				if fi.Mode()&os.ModeSymlink != 0 {
					d.LinkTo, _ = os.Readlink(d.Path)
					if d.LinkTo == "" {
						d.LinkTo = "?"
					}
				} else if !fi.IsDir() {
					return Plan{}, fmt.Errorf("%s is not a directory; move it away and run pekoe update again", d.Path)
				}
			}
			p.Dirs = append(p.Dirs, d)
		}
	}
	return p, nil
}

// Links are the ShareDirs that are symbolic links (to a source checkout).
func (p Plan) Links() []Dir {
	var out []Dir
	for _, d := range p.Dirs {
		if d.LinkTo != "" {
			out = append(out, d)
		}
	}
	return out
}

// ErrManaged: the binary belongs to a package manager, which should update it.
var ErrManaged = errors.New("installed by a package manager")

// Check refuses a binary that a package manager owns (Homebrew's Cellar
// is writable by the user, so writability alone does not tell) and places
// pekoe cannot write to, before anything is downloaded.
func (p Plan) Check() error {
	for _, m := range []string{"/Cellar/", "/homebrew/", "/nix/store/", "/snap/"} {
		if strings.Contains(p.Exe, m) {
			return fmt.Errorf("%s: %w; update it with that package manager (e.g. brew upgrade), not pekoe update", p.Exe, ErrManaged)
		}
	}
	dirs := []string{filepath.Dir(p.Exe)}
	if p.ShareDir != "" {
		dirs = append(dirs, p.ShareDir)
	}
	for _, d := range dirs {
		f, err := os.CreateTemp(d, ".pekoe-update-check-")
		if err != nil {
			return fmt.Errorf("cannot write to %s (%v): reinstall pekoe where you can write (README: From a release), or run pekoe update as the user that owns it", d, err)
		}
		f.Close()
		os.Remove(f.Name())
	}
	return nil
}

// Result tells what Install replaced.
type Result struct {
	Dirs      []string // ShareDirs replaced or created
	Extension bool     // the Chrome extension was replaced (reload it)
}

// beforeBinarySwap lets the tests fail Install between the directories and
// the binary.
var beforeBinarySwap = func() error { return nil }

// Install replaces the binary and the ShareDirs with what Extract put in
// staged. The binary is written next to the old one and renamed over it
// (a running binary is never written to: macOS may kill a process whose
// signed pages change). Directories and links are moved aside and the new
// ones renamed in; a link is replaced by a directory, so the repository it
// points at is not written to. When a step fails, the steps done are
// undone. Nothing else under ShareDir is touched.
func Install(p Plan, staged string) (res Result, err error) {
	binDir := filepath.Dir(p.Exe)
	// undo runs (newest first) when a step fails; cleanup always runs after
	// it, so what was moved aside is put back before it would be removed
	var undo []func() error
	var cleanup []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				if uerr := undo[i](); uerr != nil {
					err = fmt.Errorf("%w; undoing failed too: %v", err, uerr)
				}
			}
		}
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}()

	// the new binary, next to the old one
	newBin, err := os.CreateTemp(binDir, ".pekoe-update-")
	if err != nil {
		return res, err
	}
	newBinPath := newBin.Name()
	cleanup = append(cleanup, func() { os.Remove(newBinPath) }) // gone once renamed
	if err := copyInto(newBin, filepath.Join(staged, "pekoe")); err != nil {
		return res, err
	}
	if err := os.Chmod(newBinPath, 0o755); err != nil {
		return res, err
	}

	// the new directories, next to the old ones
	var work string
	if p.ShareDir != "" {
		work, err = os.MkdirTemp(p.ShareDir, ".pekoe-update-")
		if err != nil {
			return res, err
		}
		// holds only what update made and what it moved aside: the old
		// directories, or the links to the repository (removing a link
		// does not touch what it points at)
		cleanup = append(cleanup, func() { os.RemoveAll(work) })
		for _, d := range p.Dirs {
			src := filepath.Join(staged, d.Name)
			if _, err := os.Stat(src); err != nil {
				continue // the release does not ship it: leave what is there
			}
			if err := copyTree(src, filepath.Join(work, "new-"+d.Name)); err != nil {
				return res, err
			}
		}
	}

	// keep the old binary until the end, to put it back
	oldBin := filepath.Join(binDir, fmt.Sprintf(".pekoe-old-%d", os.Getpid()))
	_ = os.Remove(oldBin)
	if err := os.Link(p.Exe, oldBin); err != nil {
		if err := copyFile(p.Exe, oldBin, 0o755); err != nil {
			return res, fmt.Errorf("keeping the old binary: %w", err)
		}
	}
	cleanup = append(cleanup, func() { os.Remove(oldBin) })

	// swap the directories
	for _, d := range p.Dirs {
		newDir := filepath.Join(work, "new-"+d.Name)
		if _, err := os.Lstat(newDir); err != nil {
			continue
		}
		aside := filepath.Join(work, "old-"+d.Name)
		if d.Exists {
			if err := os.Rename(d.Path, aside); err != nil {
				return res, err
			}
			undo = append(undo, func() error { return os.Rename(aside, d.Path) })
		}
		if err := os.Rename(newDir, d.Path); err != nil {
			return res, err
		}
		undo = append(undo, func() error { return os.Rename(d.Path, newDir) })
		res.Dirs = append(res.Dirs, d.Name)
		if d.Name == "extension" {
			res.Extension = true
		}
	}

	if err := beforeBinarySwap(); err != nil {
		return res, err
	}
	// swap the binary (rename is atomic: pekoe is always there)
	if err := os.Rename(newBinPath, p.Exe); err != nil {
		return res, err
	}
	undo = append(undo, func() error { return os.Rename(oldBin, p.Exe) })

	// pekoed → pekoe next to it, as in the archive and make install
	if err := linkPekoed(binDir, filepath.Base(p.Exe)); err != nil {
		return res, err
	}
	return res, nil
}

// linkPekoed makes <binDir>/pekoed run the new binary. A link is left as it
// is (it reaches the new binary through the same name); a copy of an old
// binary is replaced by a link, and a missing one is created.
func linkPekoed(binDir, exeName string) error {
	pekoed := filepath.Join(binDir, "pekoed")
	if exeName == "pekoed" {
		return nil
	}
	fi, err := os.Lstat(pekoed)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a file or a link; leaving it", pekoed)
	}
	tmp := filepath.Join(binDir, fmt.Sprintf(".pekoed-update-%d", os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(exeName, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, pekoed); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyInto(dst *os.File, src string) error {
	in, err := os.Open(src)
	if err != nil {
		dst.Close()
		return err
	}
	defer in.Close()
	if _, err := io.Copy(dst, in); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

func copyFile(src, dst string, mode os.FileMode) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	return copyInto(out, src)
}

// copyTree copies the regular files and directories Extract made.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", path)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, fi.Mode().Perm())
	})
}
