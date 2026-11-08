package migrate

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	home, repo, sha, settings string
	l                         Layout
}

// newFixture builds a boba installation under a temporary HOME: data and
// config directories, a state.db that points at them and at refs/boba/,
// a repository with those refs, and settings.json with boba's hooks.
// Nothing outside t.TempDir() is read or written.
func newFixture(t *testing.T) *fixture {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PEKOE_HOME", "")
	t.Setenv("PEKOE_SOCK", "")
	t.Setenv("BOBA_SOCK", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	f := &fixture{home: home, l: DefaultLayout()}
	if f.l.OldData != filepath.Join(home, ".local/share/boba") || f.l.NewData != filepath.Join(home, ".local/share/pekoe") ||
		f.l.OldConfig != filepath.Join(home, ".config/boba") || f.l.NewConfig != filepath.Join(home, ".config/pekoe") {
		t.Fatalf("layout outside the temp HOME: %+v", f.l)
	}

	f.repo = filepath.Join(home, "src", "app")
	os.MkdirAll(f.repo, 0o755)
	git(t, f.repo, "init", "-q", "-b", "main")
	git(t, f.repo, "commit", "-q", "--allow-empty", "-m", "init")
	f.sha = git(t, f.repo, "rev-parse", "HEAD")
	git(t, f.repo, "update-ref", "refs/boba/s/prompt-1", f.sha)
	git(t, f.repo, "update-ref", "refs/boba/s/review-1", f.sha)

	os.MkdirAll(filepath.Join(f.l.OldConfig, "repos"), 0o755)
	os.WriteFile(filepath.Join(f.l.OldConfig, "config.toml"), []byte("# boba config\n[web]\nhostname = \"boba\"\n"), 0o644)
	os.MkdirAll(f.l.OldData, 0o755)
	os.WriteFile(filepath.Join(f.l.OldData, "bobad.log"), []byte("log\n"), 0o644)
	os.WriteFile(filepath.Join(f.l.OldData, "bobad.pid"), []byte("1\n"), 0o644)
	os.WriteFile(filepath.Join(f.l.OldData, "bobad.lock"), nil, 0o644)
	os.Symlink(f.repo, filepath.Join(f.l.OldData, "nvim"))

	db, err := state.Open(filepath.Join(f.l.OldData, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSession(&state.Session{Slug: "s", Repo: "app", RepoDir: f.repo, Path: f.repo, State: state.Idle}); err != nil {
		t.Fatal(err)
	}
	db.AddPrompt("s", 1, "see refs/boba/ in the docs", "refs/boba/s/prompt-1")
	db.AddReview("s", 1, "refs/boba/s/review-1")
	db.Set("errors", filepath.Join(f.l.OldData, "x", "errors"))
	db.Close()

	f.settings = filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(f.settings), 0o755)
	os.WriteFile(f.settings, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/u/.local/bin/boba event Stop","timeout":10}]}]}}`), 0o600)
	return f
}

func snapshot(t *testing.T, root string) string {
	var b strings.Builder
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			data := []byte{}
			if fi.Mode().IsRegular() {
				data, _ = os.ReadFile(p)
			}
			b.WriteString(p + " " + fi.Mode().String() + " " + string(data) + "\n")
		}
		return nil
	})
	return b.String()
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	// what `make install` put there before the migration
	os.MkdirAll(f.l.NewData, 0o755)
	os.Symlink(f.repo, filepath.Join(f.l.NewData, "nvim"))
	before := snapshot(t, f.home)
	refs := git(t, f.repo, "for-each-ref")
	var out bytes.Buffer
	if err := Run(f.l, Options{DryRun: true, Bin: "/u/.local/bin/pekoe", Out: &out}); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, f.home); after != before {
		b, a := strings.Split(before, "\n"), strings.Split(after, "\n")
		for i := 0; i < len(a) || i < len(b); i++ {
			var x, y string
			if i < len(b) {
				x = b[i]
			}
			if i < len(a) {
				y = a[i]
			}
			if x != y {
				t.Fatalf("dry-run changed files: line %d:\n%.200q\n%.200q", i, x, y)
			}
		}
	}
	if got := git(t, f.repo, "for-each-ref"); got != refs {
		t.Fatalf("dry-run changed refs: %s", got)
	}
	if strings.Contains(out.String(), "boba config") {
		t.Errorf("comments reported:\n%s", out.String())
	}
	for _, want := range []string{"move the contents of", "bobad.log → pekoed.log", "prompts.ref · 1 rows", "copy 2 refs", "boba event", "hostname = \"boba\"", "claude mcp add pekoe"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run output misses %q:\n%s", want, out.String())
		}
	}
}

func TestMigrate(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	if err := Run(f.l, Options{Bin: "/u/.local/bin/pekoe", Out: &out}); err != nil {
		t.Fatal(err)
	}
	t.Log(out.String())
	for _, gone := range []string{f.l.OldData, f.l.OldConfig} {
		if exists(gone) {
			t.Errorf("%s still exists", gone)
		}
	}
	for _, p := range []string{"pekoed.log", "state.db", "nvim"} {
		if !exists(filepath.Join(f.l.NewData, p)) {
			t.Errorf("%s missing", p)
		}
	}
	for _, p := range []string{"bobad.log", "bobad.pid", "bobad.lock", "pekoed.pid", "pekoed.lock"} {
		if exists(filepath.Join(f.l.NewData, p)) {
			t.Errorf("%s left", p)
		}
	}
	if !exists(filepath.Join(f.l.NewConfig, "config.toml")) {
		t.Error("config not moved")
	}

	db, err := state.Open(filepath.Join(f.l.NewData, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := db.Prompts("s")
	if len(ps) != 1 || ps[0].Ref != "refs/pekoe/s/prompt-1" || ps[0].Text != "see refs/boba/ in the docs" {
		t.Errorf("prompts: %+v", ps)
	}
	if ref, _ := db.ReviewRef("s", 1); ref != "refs/pekoe/s/review-1" {
		t.Errorf("review ref %q", ref)
	}
	if v := db.Get("errors"); v != filepath.Join(f.l.NewData, "x", "errors") {
		t.Errorf("kv path %q", v)
	}
	db.Close()

	refs := git(t, f.repo, "for-each-ref", "--format=%(refname)")
	for _, want := range []string{"refs/pekoe/s/prompt-1", "refs/pekoe/s/review-1", "refs/boba/s/prompt-1"} {
		if !strings.Contains(refs, want) {
			t.Errorf("refs miss %s:\n%s", want, refs)
		}
	}

	b, _ := os.ReadFile(f.settings)
	if strings.Contains(string(b), "boba event") || !strings.Contains(string(b), "/u/.local/bin/pekoe event Stop") {
		t.Errorf("settings.json: %s", b)
	}

	// again, with --prune-old: nothing to move, the old refs go
	out.Reset()
	if err := Run(f.l, Options{PruneOld: true, Bin: "/u/.local/bin/pekoe", Out: &out}); err != nil {
		t.Fatal(err)
	}
	refs = git(t, f.repo, "for-each-ref", "--format=%(refname)")
	if strings.Contains(refs, "refs/boba/") || !strings.Contains(refs, "refs/pekoe/s/prompt-1") {
		t.Errorf("after --prune-old:\n%s", refs)
	}
}

func TestRefusesWhenDestinationInUse(t *testing.T) {
	f := newFixture(t)
	os.MkdirAll(f.l.NewData, 0o755)
	os.WriteFile(filepath.Join(f.l.NewData, "state.db"), nil, 0o644)
	before := snapshot(t, f.home)
	err := Run(f.l, Options{Bin: "pekoe", Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("err = %v", err)
	}
	if snapshot(t, f.home) != before {
		t.Fatal("changed files before refusing")
	}
}

func TestRefusesWhileDaemonRuns(t *testing.T) {
	f := newFixture(t)
	// short path: unix socket paths must fit in ~104 bytes on macOS
	dir, err := os.MkdirTemp("/tmp", "pk-mig")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "bobad.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	f.l.Sockets = append(f.l.Sockets, sock)
	err = Run(f.l, Options{Bin: "pekoe", Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "daemon is running") {
		t.Fatalf("err = %v", err)
	}
	if !exists(f.l.OldData) {
		t.Fatal("moved while a daemon runs")
	}
}
