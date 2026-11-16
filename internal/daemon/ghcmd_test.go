package daemon

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ghExecAllowed are where the module may start gh itself: ghCommand, and
// two that run no git (gh auth token, gh attestation verify).
var ghExecAllowed = map[string]bool{
	"internal/daemon/pr.go:ghCommand":     true,
	"internal/selfupdate/github.go:Token": true, // gh auth token
	"cmd/pekoe/update.go:newUpdater":      true, // gh attestation verify
}

// Every gh pekoed starts goes through ghCommand: an exec.Command* of "gh"
// anywhere else would start it with a cwd and environment of its own.
func TestGHStartedOnlyByGHCommand(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && (e.Name() == "node_modules" || strings.HasPrefix(e.Name(), ".")) && p != root {
			return filepath.SkipDir
		}
		if e.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(sel.Sel.Name, "Command") {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "exec" {
					return true
				}
				for _, a := range call.Args {
					if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, _ := strconv.Unquote(lit.Value); v == "gh" {
							found = append(found, rel+":"+fn.Name.Name)
						}
					}
				}
				return true
			})
		}
		return nil
	})
	if !slices.Contains(found, "internal/daemon/pr.go:ghCommand") {
		t.Fatalf("ghCommand not found (the scan is broken?): %q", found)
	}
	for _, f := range found {
		if !ghExecAllowed[f] {
			t.Errorf("%s starts gh itself: go through ghCommand (cwd outside every repository, --repo)", f)
		}
	}
}

// ghCommand's cwd is in no repository and its environment names none,
// even when pekoe's own directories sit in a git repository and pekoed
// was started with GIT_DIR / GIT_WORK_TREE.
func TestGHCommandOutsideEveryRepository(t *testing.T) {
	outer := t.TempDir()
	run(t, outer, "git", "init", "-q")
	t.Setenv("PEKOE_HOME", filepath.Join(outer, "home"))
	t.Setenv("GIT_DIR", filepath.Join(outer, ".git"))
	t.Setenv("GIT_WORK_TREE", outer)
	t.Setenv("GH_TOKEN", "x")

	c, done, err := ghCommand(context.Background(), "github.com/me/app", "pr", "view")
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(c.Dir); err != nil || len(entries) != 0 {
		t.Fatalf("cwd %s: %v %v", c.Dir, entries, err)
	}
	for _, e := range c.Env {
		k, _, _ := strings.Cut(e, "=")
		if k == "GIT_DIR" || k == "GIT_WORK_TREE" {
			t.Errorf("gh gets %s", e)
		}
	}
	if !slices.Contains(c.Env, "GH_TOKEN=x") {
		t.Errorf("GH_* dropped: %q", c.Env)
	}
	git := exec.Command("git", "rev-parse", "--absolute-git-dir")
	git.Dir, git.Env = c.Dir, c.Env
	if out, err := git.CombinedOutput(); err == nil {
		t.Fatalf("git finds a repository from gh's cwd %s: %s", c.Dir, out)
	}
	done()
	if _, err := os.Stat(c.Dir); !os.IsNotExist(err) {
		t.Fatalf("cwd left behind: %v", err)
	}

	if c, _, err := ghCommand(context.Background(), "", "pr", "view"); err == nil || c != nil {
		t.Fatalf("no repository: %v %v", c, err)
	}
}

func TestGHAPIRepo(t *testing.T) {
	for _, c := range []struct{ repo, host, owner, name string }{
		{"github.com/me/app", "", "me", "app"},
		{"ghe.example.com/team/svc", "--hostname ghe.example.com", "team", "svc"},
	} {
		host, owner, name := ghAPIRepo(c.repo)
		if strings.Join(host, " ") != c.host || owner != c.owner || name != c.name {
			t.Errorf("%s: %q %q %q", c.repo, host, owner, name)
		}
	}
}

func TestGHRepoOfOrigins(t *testing.T) {
	for origin, want := range map[string]string{
		"git@github.com:me/app.git":          "github.com/me/app",
		"https://github.com/Me/App":          "github.com/me/app",
		"ssh://git@ghe.example.com/team/svc": "ghe.example.com/team/svc",
		"":                                   "",
		"/srv/git/app.git":                   "",
		"https://github.com/me":              "",
		"https://github.com/me/app/extra":    "",
		"https://github.com/../app.git":      "",
		"https://github.com/me/..":           "",
		"https://github.com/-me/app.git":     "",
		"https://github.com/me/-app.git":     "",
		"git@github.com:me/--upload-pack=x":  "",
		"https://github.com/me/@etc/passwd":  "",
		"https://github.com/me/a%20b":        "",
		"https://-github.com/me/app":         "",
		"https://github.com/me/app name.git": "",
	} {
		if got := ghRepoFrom(webBaseOf(origin)); got != want {
			t.Errorf("%q: %q, want %q", origin, got, want)
		}
	}
}

func TestGHRepoOfRepository(t *testing.T) {
	d, _, repo := setup(t)
	if r := d.ghRepoOf(repo); r != "" {
		t.Fatalf("no origin: %q", r)
	}
	run(t, repo, "git", "remote", "add", "origin", "git@github.com:me/../app.git")
	d.webBases.Delete(repo)
	if r := d.ghRepoOf(repo); r != "" {
		t.Fatalf("..: %q", r)
	}
	run(t, repo, "git", "remote", "set-url", "origin", "git@github.com:me/app.git")
	d.webBases.Delete(repo)
	if r := d.ghRepoOf(repo); r != "github.com/me/app" {
		t.Fatalf("github: %q", r)
	}
}

func TestPRText(t *testing.T) {
	for _, c := range []struct {
		name, log   string
		title, body string
	}{
		{"one commit", "fix(tui): keep the icon\x00Why: it moved.\n\nMore.\n\x1e\n", "fix(tui): keep the icon", "Why: it moved.\n\nMore."},
		{"one commit without body", "add b\x00\x1e\n", "add b", ""},
		{"several", "first\x00body 1\n\x1e\nsecond\x00\x1e\nthird\x00x\n\x1e\n", "feat/x y", "- first\n- second\n- third\n"},
		{"none", "", "feat/x y", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			title, body := prText("feat/x-y", parseCommitMsgs(c.log))
			if title != c.title || body != c.body {
				t.Fatalf("title %q body %q", title, body)
			}
		})
	}
}

// What land reads for pr create is what git log gives for the branch.
func TestPRTextFromGitLog(t *testing.T) {
	_, _, repo := setup(t)
	run(t, repo, "git", "checkout", "-q", "-b", "feat/two")
	for _, m := range []string{"one", "two"} {
		os.WriteFile(filepath.Join(repo, m+".txt"), []byte(m), 0o644)
		run(t, repo, "git", "add", ".")
		run(t, repo, "git", "commit", "-qm", m, "-m", "body of "+m)
	}
	out, err := exec.Command("git", "-C", repo, "log", "--reverse", "--format=%s%x00%b%x1e", "main..HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	title, body := prText("feat/two", parseCommitMsgs(strings.TrimRight(string(out), "\n")))
	if title != "feat/two" || body != "- one\n- two\n" {
		t.Fatalf("title %q body %q", title, body)
	}
}

// gh keeps what it needs to find its token (the Secret Service over the
// session D-Bus on Linux) and nothing that points git anywhere.
func TestGHEnv(t *testing.T) {
	keep := map[string]string{
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1/bus",
		"XDG_RUNTIME_DIR":          "/run/user/1",
		"XDG_CONFIG_HOME":          "/c",
		"GH_HOST":                  "ghe.example.com",
		"GITHUB_TOKEN":             "t",
	}
	drop := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_GLOBAL", "GIT_EXEC_PATH"}
	for k, v := range keep {
		t.Setenv(k, v)
	}
	for _, k := range drop {
		t.Setenv(k, "x")
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", "/elsewhere")
	ceiling := t.TempDir()
	env := ghEnv(ceiling)
	for k, v := range keep {
		if !slices.Contains(env, k+"="+v) {
			t.Errorf("%s dropped", k)
		}
	}
	var ceilings []string
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if slices.Contains(drop, k) {
			t.Errorf("gh gets %s", e)
		}
		if k == "GIT_CEILING_DIRECTORIES" {
			ceilings = append(ceilings, v)
		}
	}
	if len(ceilings) != 1 || ceilings[0] != evalSymlinks(t, ceiling) {
		t.Errorf("GIT_CEILING_DIRECTORIES: %q", ceilings)
	}
}
