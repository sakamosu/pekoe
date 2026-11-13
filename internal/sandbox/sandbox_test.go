package sandbox

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pekoe/internal/testenv"
)

func TestOffRunsAsIs(t *testing.T) {
	argv := []string{"/bin/sh", "-c", "echo hi"}
	for _, s := range []*Sandbox{nil, {}, New(Policy{}, false)} {
		got, err := s.Wrap(argv)
		if err != nil || !slices.Equal(got, argv) {
			t.Fatalf("off: %v %v", got, err)
		}
		if line, err := s.ShellLine("/bin/zsh", "make test"); err != nil || line != "make test" {
			t.Fatalf("off line: %q %v", line, err)
		}
	}
}

func fake(goos string, locErr, probeErr error) (*Sandbox, *int) {
	n := 0
	s := &Sandbox{Enabled: true, goos: goos,
		Policy: Policy{DenyRead: []string{"/x/human-token"}},
		locate: func() (string, error) { return "/usr/bin/tool", locErr },
		probe:  func([]string) error { n++; return probeErr },
	}
	return s, &n
}

func TestUnavailableRunsNothing(t *testing.T) {
	for _, tc := range []struct {
		name            string
		locErr, probeEr error
		want            string
	}{
		{"missing", errors.New("bubblewrap (bwrap) is not installed"), nil, "bubblewrap (bwrap) is not installed"},
		{"broken", nil, errors.New("exit status 71: sandbox_apply: Operation not permitted"), "tool does not work here: exit status 71"},
	} {
		s, _ := fake("linux", tc.locErr, tc.probeEr)
		got, err := s.Wrap([]string{"/bin/sh", "-c", "make"})
		if got != nil || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: %v %v", tc.name, got, err)
		}
		var ue *UnavailableError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "[sandbox] enable = false") {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if _, err := s.ShellLine("/bin/sh", "make"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s line: %v", tc.name, err)
		}
	}
}

// A probe that passed is not run again; one that failed is (bwrap may have
// been installed since).
func TestProbeKeptOnlyOnSuccess(t *testing.T) {
	s, n := fake("linux", nil, errors.New("no"))
	s.Check()
	s.Check()
	if *n != 2 {
		t.Fatalf("failed probe not retried: %d", *n)
	}
	s.probe = func([]string) error { *n++; return nil }
	s.Check()
	s.Check()
	if *n != 3 {
		t.Fatalf("passed probe re-run: %d", *n)
	}
}

func TestLocateTool(t *testing.T) {
	has := func(want ...string) func(string) bool {
		return func(p string) bool { return slices.Contains(want, p) }
	}
	if p, err := locateTool("darwin", has("/usr/bin/sandbox-exec")); p != "/usr/bin/sandbox-exec" || err != nil {
		t.Fatal(p, err)
	}
	if _, err := locateTool("darwin", has()); err == nil {
		t.Fatal("missing sandbox-exec")
	}
	if p, err := locateTool("linux", has("/usr/local/bin/bwrap", "/usr/bin/bwrap")); p != "/usr/bin/bwrap" || err != nil {
		t.Fatal(p, err)
	}
	// $PATH is not searched: ~/.local/bin/bwrap could be the agent's
	t.Setenv("PATH", t.TempDir())
	if _, err := locateTool("linux", has(filepath.Join(os.Getenv("PATH"), "bwrap"))); err == nil {
		t.Fatal("found bwrap outside the fixed paths")
	}
	if _, err := locateTool("freebsd", has("/usr/bin/bwrap")); err == nil {
		t.Fatal("freebsd")
	}
}

func TestSeatbeltArgs(t *testing.T) {
	dir := t.TempDir() // on macOS below /var → /private/var: both are named
	tok := filepath.Join(dir, "share", "human-token")
	p := Policy{
		DenyRead:    []string{tok, "relative/ignored"},
		DenyWrite:   []string{filepath.Join(dir, "share")},
		DenyConnect: []string{"/tmp/tmux-501"},
	}
	argv := []string{"/bin/zsh", "-c", "make test; echo '(deny default)'"}
	got := seatbeltArgs("/usr/bin/sandbox-exec", p, argv)
	if got[0] != "/usr/bin/sandbox-exec" || got[1] != "-p" {
		t.Fatalf("head: %q", got[:2])
	}
	if !slices.Equal(got[len(got)-3:], argv) {
		t.Fatalf("command not last, unchanged: %q", got)
	}
	prof := got[2]
	if strings.Contains(prof, dir) || strings.Contains(prof, "relative") {
		t.Fatalf("a path spliced into the profile:\n%s", prof)
	}
	for _, want := range []string{"(allow default)", "(deny file-read* (subpath (param ", "(deny file-write* (subpath (param ", "(deny file-write* (literal (param ", "(deny network-outbound (remote unix-socket (subpath (param ", "(deny lsopen)", "(deny appleevent-send)"} {
		if !strings.Contains(prof, want) {
			t.Fatalf("profile lacks %s:\n%s", want, prof)
		}
	}
	params := map[string]bool{}
	for i := 3; i < len(got)-3; i += 2 {
		if got[i] != "-D" {
			t.Fatalf("not a -D pair at %d: %q", i, got[i:])
		}
		_, v, _ := strings.Cut(got[i+1], "=")
		params[v] = true
	}
	for _, want := range []string{tok, filepath.Join(dir, "share"), "/tmp/tmux-501", dir, "/"} {
		if !params[want] {
			t.Fatalf("no parameter %s in %v", want, params)
		}
	}
	if real, _ := filepath.EvalSymlinks(dir); real != dir && !params[filepath.Join(real, "share", "human-token")] {
		t.Fatalf("resolved path of the token missing: %v", params)
	}
}

// fakeHost is a file system of kinds (true: directory) for bwrapArgs.
func fakeHost(kinds map[string]bool, made *[]Stub) host {
	return host{
		kind: func(p string) (bool, bool) { d, ok := kinds[p]; return d, ok },
		real: func(p string) string { return p },
		entries: func(dir string) []string {
			var out []string
			for p := range kinds {
				if filepath.Dir(p) == dir && p != dir {
					out = append(out, filepath.Base(p))
				}
			}
			slices.Sort(out)
			return out
		},
		create: func(st Stub) {
			kinds[st.Path] = st.Dir
			*made = append(*made, st)
		},
	}
}

func TestBwrapArgs(t *testing.T) {
	kinds := map[string]bool{"/": true, "/d": true, "/d/share": true, "/d/share/human-token": false, "/d/share/tsnet": true, "/tmp": true, "/tmp/tmux-1000": true,
		"/home": true, "/home/u": true, "/home/u/.zshrc": false, "/home/u/.bashrc": false}
	p := Policy{
		DenyRead:    []string{"/d/share/human-token", "/d/share/tsnet", "/d/share/missing-token"},
		DenyWrite:   []string{"/d/share", "/home/u/.zshrc", "/home/u/.bashrc"},
		DenyConnect: []string{"/tmp/tmux-1000"},
	}
	var made []Stub
	got := strings.Join(bwrapArgs("/usr/bin/bwrap", p, []string{"/bin/bash", "-c", "npm test"}, fakeHost(kinds, &made)), " ")
	want := "/usr/bin/bwrap --dev-bind / / --die-with-parent --unshare-pid --proc /proc" +
		" --ro-bind /d/share /d/share --ro-bind /home/u/.zshrc /home/u/.zshrc --ro-bind /home/u/.bashrc /home/u/.bashrc" +
		" --ro-bind /dev/null /d/share/human-token --tmpfs /d/share/tsnet --remount-ro /d/share/tsnet" +
		" --tmpfs /tmp/tmux-1000 -- /bin/bash -c npm test"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if len(made) != 0 {
		t.Fatalf("made %v", made)
	}
}

// A missing path cannot be bound over (bwrap would make it on the host):
// a stub is made when its parent exists; else the nearest existing
// ancestor becomes read-only with what is in it now bound back writable,
// so the missing path cannot be made (~/.claude/hooks, ~/.bash_profile).
func TestBwrapArgsMissing(t *testing.T) {
	kinds := map[string]bool{"/": true, "/h": true, "/h/u": true,
		"/h/u/.claude": true, "/h/u/.claude/projects": true, "/h/u/.claude/skills": true, "/h/u/.claude/history.jsonl": false,
		"/h/u/.zshrc": false, "/h/u/repo": true, "/h/u/.config": true, "/h/u/.config/gh": true,
		"/h/u/pk": true, "/h/u/pk/share": true, "/h/u/pk/share/state.db": false}
	p := Policy{
		DenyRead: []string{"/h/u/pk/share/state.db", "/h/u/pk/share/state.db-wal", "/srv/tsnet"},
		DenyWrite: []string{"/h/u/pk/share", "/h/u/.zshrc", "/h/u/.bash_profile",
			"/h/u/.claude/skills", "/h/u/.claude/hooks", "/h/u/.claude/CLAUDE.md", "/h/u/.claude/settings.json",
			"/h/u/.config/git", "/h/u/.config/systemd/user", "/nowhere/x"},
		Stubs: []Stub{
			{Path: "/h/u/.claude/hooks", Dir: true}, {Path: "/h/u/.claude/skills", Dir: true},
			{Path: "/h/u/.claude/settings.json", Content: "{}\n"},
			{Path: "/h/u/.config/git", Dir: true}, {Path: "/h/u/.config/systemd/user", Dir: true},
			{Path: "/h/u/missing/x", Dir: true},
		},
	}
	var made []Stub
	got := strings.Join(bwrapArgs("/usr/bin/bwrap", p, []string{"sh"}, fakeHost(kinds, &made)), " ")
	// the stubs whose parent exists are made; skills is there already
	want := []Stub{{Path: "/h/u/.claude/hooks", Dir: true}, {Path: "/h/u/.claude/settings.json", Content: "{}\n"}, {Path: "/h/u/.config/git", Dir: true}}
	if !slices.Equal(made, want) {
		t.Fatalf("made %v", made)
	}
	wantArgs := "/usr/bin/bwrap --dev-bind / / --die-with-parent --unshare-pid --proc /proc" +
		// ~/.bash_profile: ~ read-only, its entries writable again (not the
		// denied .zshrc, nor .claude and .config: read-only below)
		" --ro-bind /h/u /h/u --dev-bind /h/u/pk /h/u/pk --dev-bind /h/u/repo /h/u/repo" +
		// ~/.claude/CLAUDE.md had no stub: ~/.claude read-only
		" --ro-bind /h/u/.claude /h/u/.claude --dev-bind /h/u/.claude/history.jsonl /h/u/.claude/history.jsonl" +
		" --dev-bind /h/u/.claude/projects /h/u/.claude/projects" +
		// ~/.config/systemd/user: its parent is missing, so ~/.config
		" --ro-bind /h/u/.config /h/u/.config --dev-bind /h/u/.config/gh /h/u/.config/gh" +
		// state.db-wal: inside share, read-only already; /nowhere/x: not /
		// itself; /srv/tsnet: likewise
		" --ro-bind /h/u/pk/share /h/u/pk/share --ro-bind /h/u/.zshrc /h/u/.zshrc" +
		" --ro-bind /h/u/.claude/skills /h/u/.claude/skills --ro-bind /h/u/.claude/hooks /h/u/.claude/hooks" +
		" --ro-bind /h/u/.claude/settings.json /h/u/.claude/settings.json --ro-bind /h/u/.config/git /h/u/.config/git" +
		" --ro-bind /dev/null /h/u/pk/share/state.db -- sh"
	if got != wantArgs {
		t.Fatalf("got\n%s\nwant\n%s", got, wantArgs)
	}
}

func TestDefaultPolicyStubs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	p := DefaultPolicy("")
	for _, st := range p.Stubs {
		if !slices.Contains(p.DenyWrite, st.Path) {
			t.Fatalf("stub %s is not in DenyWrite", st.Path)
		}
		if !st.Dir && st.Content != "" && st.Content != "{}\n" {
			t.Fatalf("stub %s: %q", st.Path, st.Content)
		}
		// shell startup files are never stubbed (an empty ~/.bash_profile
		// hides ~/.profile)
		if filepath.Dir(st.Path) == home {
			t.Fatalf("stub in ~: %s", st.Path)
		}
	}
	for _, w := range []string{".claude/hooks", ".claude/settings.local.json", ".claude/CLAUDE.md", ".config/git"} {
		if !slices.ContainsFunc(p.Stubs, func(st Stub) bool { return st.Path == filepath.Join(home, w) }) {
			t.Fatalf("no stub for %s: %v", w, p.Stubs)
		}
	}
}

// The stubs on the real file system: made only when missing, never over
// what is there, and only in an existing directory.
func TestMakeStub(t *testing.T) {
	dir := t.TempDir()
	makeStub(Stub{Path: filepath.Join(dir, "d"), Dir: true})
	makeStub(Stub{Path: filepath.Join(dir, "s.json"), Content: "{}\n"})
	os.WriteFile(filepath.Join(dir, "keep"), []byte("mine\n"), 0o644)
	makeStub(Stub{Path: filepath.Join(dir, "keep"), Content: "{}\n"})
	makeStub(Stub{Path: filepath.Join(dir, "no", "x"), Dir: true})
	if fi, err := os.Stat(filepath.Join(dir, "d")); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatal(fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "s.json")); string(b) != "{}\n" {
		t.Fatalf("%q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "keep")); string(b) != "mine\n" {
		t.Fatalf("replaced: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "no")); err == nil {
		t.Fatal("made a parent")
	}
	got := dirEntries(dir)
	os.Symlink(dir, filepath.Join(dir, "ln"))
	if g := dirEntries(dir); !slices.Equal(g, got) || !slices.Equal(got, []string{"d", "keep", "s.json"}) {
		t.Fatal(got, g)
	}
}

func TestShellLineQuotes(t *testing.T) {
	s, _ := fake("linux", nil, nil)
	s.Policy = Policy{}
	line, err := s.ShellLine("/bin/zsh", `npm run dev; printf '\n[pekoe] exited (%s)\n' "$?"`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "exec ") {
		t.Fatal(line)
	}
	// the shell reads the line back into the same words
	out, err := exec.Command("/bin/sh", "-c", `printf '<%s>' `+strings.TrimPrefix(line, "exec ")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `<-c><npm run dev; printf '\n[pekoe] exited (%s)\n' "$?">`) {
		t.Fatalf("%s\n%s", line, out)
	}
}

func TestAncestors(t *testing.T) {
	got := ancestors([]string{"/a/b/c", "/a/b", "/x/y"})
	if !slices.Equal(got, []string{"/a", "/", "/x"}) {
		t.Fatal(got)
	}
}

// The real thing: on macOS with sandbox-exec, or Linux with a working bwrap.
// Skipped where neither runs (CI without bwrap, or these tests running in a
// sandbox already, where another one cannot start).
func realSandbox(t *testing.T, p Policy) *Sandbox {
	t.Helper()
	s := New(p, true)
	if err := s.Check(); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	return s
}

func TestRealSandboxDenies(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "share")
	os.MkdirAll(filepath.Join(data, "tsnet"), 0o700)
	tok := filepath.Join(data, "human-token")
	os.WriteFile(tok, []byte("SECRET-TOKEN\n"), 0o600)
	os.WriteFile(filepath.Join(data, "tsnet", "key"), []byte("NODE-KEY\n"), 0o600)
	wt := filepath.Join(dir, "wt")
	os.MkdirAll(wt, 0o755)
	// the socket directory goes in a short path: unix socket paths are
	// limited (~104 bytes on macOS)
	testenv.NeedUnixSocket(t)
	sockDir := testenv.ShortDir(t, "sbx")
	ln, err := net.Listen("unix", filepath.Join(sockDir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("CONNECTED\n"))
			c.Close()
		}
	}()
	s := realSandbox(t, Policy{
		DenyRead:    []string{tok, filepath.Join(data, "tsnet")},
		DenyWrite:   []string{data},
		DenyConnect: []string{sockDir},
	})
	// a Makefile in the worktree reads the token: make → sh → cat, and a
	// grandchild started in the background, all confined
	os.WriteFile(filepath.Join(wt, "Makefile"), []byte("test:\n\t-cat "+tok+"\n\t-sh -c 'cat "+tok+" & wait'\n\t-cat "+filepath.Join(data, "tsnet", "key")+"\n\t-echo pwned > "+filepath.Join(data, "x")+"\n\t-echo hi > "+filepath.Join(wt, "ok")+"\n\t@echo env=$$PEKOE_TEST_ENV pwd=$$(pwd)\n"), 0o644)
	script := "make test 2>&1; " + connectCmd(filepath.Join(sockDir, "s"))
	argv, err := s.Wrap([]string{"/bin/sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = wt
	c.Env = append(os.Environ(), "PEKOE_TEST_ENV=kept")
	out, _ := c.CombinedOutput()
	o := string(out)
	for _, leak := range []string{"SECRET-TOKEN", "NODE-KEY", "CONNECTED"} {
		if strings.Contains(o, leak) {
			t.Fatalf("%s got through:\n%s", leak, o)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "x")); err == nil {
		t.Fatalf("wrote into the data dir:\n%s", o)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "ok")); string(b) != "hi\n" {
		t.Fatalf("could not write the worktree:\n%s", o)
	}
	realWt, _ := filepath.EvalSymlinks(wt)
	if !strings.Contains(o, "env=kept") || !(strings.Contains(o, "pwd="+wt+"\n") || strings.Contains(o, "pwd="+realWt+"\n")) {
		t.Fatalf("environment or working directory lost:\n%s", o)
	}
	// outside the sandbox, the same socket answers (the test is real)
	if out, _ := exec.Command("/bin/sh", "-c", connectCmd(filepath.Join(sockDir, "s"))).CombinedOutput(); !strings.Contains(string(out), "CONNECTED") {
		t.Skipf("no way to connect to a unix socket from sh here: %s", out)
	}
}

// connectCmd connects to a unix socket from the shell, with whatever is
// installed (nc on macOS, python3 elsewhere).
func connectCmd(sock string) string {
	py := `import socket,sys; s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); print(s.recv(64).decode().strip())`
	return "python3 -c '" + py + "' " + sock + " 2>&1 || nc -U " + sock + " </dev/null 2>&1"
}

func TestDefaultPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PEKOE_HOME", home)
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"relative")
	t.Setenv("TMUX_TMPDIR", "/tmp/tt")
	ccd := filepath.Join(home, "ccd")
	t.Setenv("CLAUDE_CONFIG_DIR", ccd)
	p := DefaultPolicy(filepath.Join(home, "elsewhere", "tsnet"))
	for _, want := range []string{"share/human-token", "share/http-token", "share/tsnet", "config/config.toml", "elsewhere/tsnet",
		"share/state.db", "share/state.db-wal", "share/state.db-shm"} {
		if !slices.Contains(p.DenyRead, filepath.Join(home, want)) {
			t.Fatalf("DenyRead lacks %s: %v", want, p.DenyRead)
		}
	}
	// Claude Code's: what runs or instructs the next agent, in ~/.claude
	// and in $CLAUDE_CONFIG_DIR alike
	claude := []string{"settings.json", "settings.local.json", "skills", "hooks", "plugins", "agents", "commands", "rules", "output-styles", "CLAUDE.md"}
	want := []string{"share", "config", ".zshrc", ".gitconfig", "bin", ".claude.json", "ccd/.claude.json"}
	for _, c := range claude {
		want = append(want, filepath.Join(".claude", c), filepath.Join("ccd", c))
	}
	for _, w := range want {
		if !slices.Contains(p.DenyWrite, filepath.Join(home, w)) {
			t.Fatalf("DenyWrite lacks %s: %v", w, p.DenyWrite)
		}
	}
	// what a `claude -p` gatekeeper writes, and git's own directories
	// (husky), stay writable
	for _, ok := range []string{".claude/projects", ".claude", "repo/.git/hooks", "repo/.git/config"} {
		if slices.Contains(p.DenyWrite, filepath.Join(home, ok)) {
			t.Fatalf("DenyWrite has %s: %v", ok, p.DenyWrite)
		}
	}
	if !slices.Contains(p.DenyConnect, filepath.Join(home, "share", "nvim")) {
		t.Fatalf("DenyConnect lacks the review surface's nvim sockets: %v", p.DenyConnect)
	}
	if os.Getuid() != 0 && slices.Contains(p.DenyWrite, "/usr/bin") {
		t.Fatalf("an unwritable PATH directory: %v", p.DenyWrite)
	}
	if runtime.GOOS != "windows" && !slices.Contains(p.DenyConnect, filepath.Join("/tmp/tt", "tmux-"+strconv.Itoa(os.Getuid()))) {
		t.Fatalf("DenyConnect: %v", p.DenyConnect)
	}
}

// DefaultPolicy in the real sandbox: Claude Code's config that runs later
// and state.db are closed; what a build, `claude -p` or husky writes
// (~/.claude/projects, a repository's .git/hooks and .git/config) is not.
func TestRealDefaultPolicyClaudeAndGit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PEKOE_HOME", filepath.Join(home, "pk"))
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	data := filepath.Join(home, "pk", "share")
	os.MkdirAll(data, 0o700)
	os.WriteFile(filepath.Join(data, "state.db"), []byte("PROMPTS\n"), 0o600)
	os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}\n"), 0o600)
	git := filepath.Join(home, "repo", ".git")
	os.MkdirAll(filepath.Join(git, "hooks"), 0o755)
	os.WriteFile(filepath.Join(git, "config"), []byte("[core]\n"), 0o644)
	s := realSandbox(t, DefaultPolicy(""))
	script := `cat "$D/state.db"; echo x > "$H/.claude.json"; echo x > "$H/.claude/skills/evil"; mkdir -p "$H/.claude/hooks"; echo x > "$H/.claude/hooks/evil";
echo x > "$H/.claude/CLAUDE.md"; echo x > "$H/.bash_profile"; mkdir -p "$H/.config/systemd/user" && echo x > "$H/.config/systemd/user/evil.service";
mkdir -p "$H/.claude/projects" && echo ok > "$H/.claude/projects/t" && echo projects-ok;
echo '#!/bin/sh' > "$G/hooks/pre-commit" && echo hooks-ok; echo '[core] hooksPath = .husky' >> "$G/config" && echo config-ok`
	argv, err := s.Wrap([]string{"/bin/sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(os.Environ(), "D="+data, "H="+home, "G="+git)
	out, _ := c.CombinedOutput()
	o := string(out)
	if strings.Contains(o, "PROMPTS") {
		t.Fatalf("state.db readable:\n%s", o)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(b) != "{}\n" {
		t.Fatalf("~/.claude.json written:\n%s", o)
	}
	// none of these existed: on Linux bwrap cannot bind over a missing
	// path, so ~/.claude/hooks is made empty first (a stub) and ~ itself
	// is read-only for .bash_profile (bwrapArgs)
	for _, p := range []string{".claude/skills/evil", ".claude/hooks/evil", ".bash_profile", ".config"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			t.Fatalf("~/%s written:\n%s", p, o)
		}
	}
	if b, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md")); err == nil && len(b) != 0 {
		t.Fatalf("~/.claude/CLAUDE.md written:\n%s", o)
	}
	if runtime.GOOS == "darwin" {
		// Seatbelt denies by path: nothing is made
		if _, err := os.Stat(filepath.Join(home, ".claude", "hooks")); err == nil {
			t.Fatalf("~/.claude/hooks made:\n%s", o)
		}
	}
	for _, okay := range []string{"projects-ok", "hooks-ok", "config-ok"} {
		if !strings.Contains(o, okay) {
			t.Fatalf("%s missing: what builds and husky write is blocked:\n%s", okay, o)
		}
	}
}
