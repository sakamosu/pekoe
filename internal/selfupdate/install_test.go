package selfupdate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installed is a prefix as `make install` leaves it, with the data
// directory sharing <prefix>/share/pekoe: bin/pekoe, bin/pekoed → pekoe,
// share/pekoe/{nvim,extension} → a source checkout, and pekoe's data.
type installed struct {
	prefix, exe, share, repo string
}

var dataFiles = map[string]string{
	"state.db":       "db",
	"human-token":    "secret",
	"tips.json":      "{}",
	"layout.json":    "{}",
	"pekoed.log":     "log",
	"tsnet/a.state":  "node",
	"tapi/CLAUDE.md": "tapi",
}

func makeInstalled(t *testing.T, links bool) installed {
	t.Helper()
	root := t.TempDir()
	in := installed{prefix: filepath.Join(root, "prefix"), repo: filepath.Join(root, "repo")}
	in.exe = filepath.Join(in.prefix, "bin", "pekoe")
	in.share = filepath.Join(in.prefix, "share", "pekoe")
	write := func(p, s string, mode os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(in.exe, "old binary", 0o755)
	if err := os.Symlink("pekoe", filepath.Join(in.prefix, "bin", "pekoed")); err != nil {
		t.Fatal(err)
	}
	for f, s := range dataFiles {
		write(filepath.Join(in.share, f), s, 0o600)
	}
	write(filepath.Join(in.repo, "nvim", "plugin", "pekoe.lua"), "-- repo plugin", 0o644)
	write(filepath.Join(in.repo, "extension", "manifest.json"), `{"repo":true}`, 0o644)
	for _, d := range ShareDirs {
		if links {
			if err := os.Symlink(filepath.Join(in.repo, d), filepath.Join(in.share, d)); err != nil {
				t.Fatal(err)
			}
		} else {
			write(filepath.Join(in.share, d, "old.txt"), "old", 0o644)
		}
	}
	return in
}

func staged(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "staged")
	if err := Extract(bytes.NewReader(releaseArchive(t, "pekoe_0.2.0_darwin_arm64", "new binary")), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkUntouched: the data directory's other files and the checkout the
// links pointed at are as they were, and nothing of update's is left.
func checkUntouched(t *testing.T, in installed) {
	t.Helper()
	for f, s := range dataFiles {
		if got := readFile(t, filepath.Join(in.share, f)); got != s {
			t.Errorf("%s changed: %q", f, got)
		}
	}
	if got := readFile(t, filepath.Join(in.repo, "nvim", "plugin", "pekoe.lua")); got != "-- repo plugin" {
		t.Errorf("repo nvim changed: %q", got)
	}
	if got := readFile(t, filepath.Join(in.repo, "extension", "manifest.json")); got != `{"repo":true}` {
		t.Errorf("repo extension changed: %q", got)
	}
	if ents, _ := os.ReadDir(in.repo); len(ents) != 2 {
		t.Errorf("repo has %v", ents)
	}
	for _, dir := range []string{in.share, filepath.Dir(in.exe)} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".pekoe") {
				t.Errorf("left %s in %s", e.Name(), dir)
			}
		}
	}
}

func TestPlanSeesLinks(t *testing.T) {
	in := makeInstalled(t, true)
	p, err := NewPlan(in.exe)
	if err != nil {
		t.Fatal(err)
	}
	if p.ShareDir != in.share || len(p.Links()) != 2 || p.Links()[0].LinkTo != filepath.Join(in.repo, "nvim") {
		t.Fatalf("plan %+v", p)
	}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallReplacesLinks(t *testing.T) {
	in := makeInstalled(t, true)
	p, _ := NewPlan(in.exe)
	res, err := Install(p, staged(t))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extension || len(res.Dirs) != 2 {
		t.Errorf("result %+v", res)
	}
	if got := readFile(t, in.exe); got != "new binary" {
		t.Errorf("binary %q", got)
	}
	if fi, _ := os.Stat(in.exe); fi.Mode().Perm() != 0o755 {
		t.Errorf("binary mode %v", fi.Mode())
	}
	if l, _ := os.Readlink(filepath.Join(in.prefix, "bin", "pekoed")); l != "pekoe" {
		t.Errorf("pekoed → %q", l)
	}
	for _, d := range ShareDirs {
		fi, err := os.Lstat(filepath.Join(in.share, d))
		if err != nil || !fi.IsDir() {
			t.Errorf("%s is not a directory now: %v %v", d, fi, err)
		}
	}
	if got := readFile(t, filepath.Join(in.share, "nvim", "plugin", "pekoe.lua")); got != "-- new plugin" {
		t.Errorf("nvim %q", got)
	}
	checkUntouched(t, in)
}

func TestInstallReplacesDirs(t *testing.T) {
	in := makeInstalled(t, false)
	p, _ := NewPlan(in.exe)
	if len(p.Links()) != 0 {
		t.Fatalf("links %v", p.Links())
	}
	if _, err := Install(p, staged(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(in.share, "nvim", "old.txt")); !os.IsNotExist(err) {
		t.Errorf("old nvim file still there: %v", err)
	}
	if got := readFile(t, filepath.Join(in.share, "extension", "manifest.json")); got != `{"new":true}` {
		t.Errorf("extension %q", got)
	}
	checkUntouched(t, in)
}

func TestInstallUndoes(t *testing.T) {
	in := makeInstalled(t, true)
	p, _ := NewPlan(in.exe)
	beforeBinarySwap = func() error { return errors.New("boom") }
	defer func() { beforeBinarySwap = func() error { return nil } }()
	if _, err := Install(p, staged(t)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	if got := readFile(t, in.exe); got != "old binary" {
		t.Errorf("binary %q", got)
	}
	for _, d := range ShareDirs {
		if l, err := os.Readlink(filepath.Join(in.share, d)); err != nil || l != filepath.Join(in.repo, d) {
			t.Errorf("%s not put back: %q %v", d, l, err)
		}
	}
	checkUntouched(t, in)
}

func TestInstallWithoutShareDir(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "bin", "pekoe")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("old"), 0o755)
	p, err := NewPlan(exe)
	if err != nil || p.ShareDir != "" || len(p.Dirs) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := Install(p, staged(t)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, exe); got != "new binary" {
		t.Errorf("binary %q", got)
	}
	// a pekoed is made next to it
	if l, _ := os.Readlink(filepath.Join(root, "bin", "pekoed")); l != "pekoe" {
		t.Errorf("pekoed → %q", l)
	}
	if _, err := os.Stat(filepath.Join(root, "share")); !os.IsNotExist(err) {
		t.Errorf("made share/: %v", err)
	}
}

func TestCheckRefuses(t *testing.T) {
	for _, exe := range []string{
		"/opt/homebrew/Cellar/pekoe/0.1.0/bin/pekoe",
		// the cask (brew install sakamosu/tap/pekoe), Apple Silicon and Intel
		"/opt/homebrew/Caskroom/pekoe/0.4.0/bin/pekoe",
		"/usr/local/Caskroom/pekoe/0.4.0/bin/pekoe",
		"/home/linuxbrew/.linuxbrew/Caskroom/pekoe/0.4.0/bin/pekoe",
	} {
		if err := (Plan{Exe: exe}).Check(); !errors.Is(err, ErrManaged) {
			t.Errorf("homebrew %s: %v", exe, err)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "pekoe"), []byte("x"), 0o755)
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	p := Plan{Exe: filepath.Join(dir, "pekoe")}
	if err := p.Check(); err == nil || !strings.Contains(err.Error(), "cannot write") {
		t.Errorf("read-only: %v", err)
	}
}
