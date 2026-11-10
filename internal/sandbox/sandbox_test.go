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

func TestBwrapArgs(t *testing.T) {
	kinds := map[string]bool{"/d/share": true, "/d/share/human-token": false, "/d/share/tsnet": true, "/tmp/tmux-1000": true, "/home/u/.zshrc": false}
	kind := func(p string) (bool, bool) { d, ok := kinds[p]; return d, ok }
	p := Policy{
		DenyRead:    []string{"/d/share/human-token", "/d/share/tsnet", "/d/share/missing-token"},
		DenyWrite:   []string{"/d/share", "/home/u/.zshrc", "/home/u/.bashrc"},
		DenyConnect: []string{"/tmp/tmux-1000"},
	}
	h := host{kind: kind, real: func(p string) string { return p }}
	got := strings.Join(bwrapArgs("/usr/bin/bwrap", p, []string{"/bin/bash", "-c", "npm test"}, h), " ")
	want := "/usr/bin/bwrap --dev-bind / / --die-with-parent --unshare-pid --proc /proc" +
		" --ro-bind /d/share /d/share --ro-bind /home/u/.zshrc /home/u/.zshrc" +
		" --ro-bind /dev/null /d/share/human-token --tmpfs /d/share/tsnet --remount-ro /d/share/tsnet" +
		" --tmpfs /tmp/tmux-1000 -- /bin/bash -c npm test"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
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
	p := DefaultPolicy(filepath.Join(home, "elsewhere", "tsnet"))
	for _, want := range []string{"share/human-token", "share/http-token", "share/tsnet", "config/config.toml", "elsewhere/tsnet"} {
		if !slices.Contains(p.DenyRead, filepath.Join(home, want)) {
			t.Fatalf("DenyRead lacks %s: %v", want, p.DenyRead)
		}
	}
	for _, want := range []string{"share", "config", ".zshrc", ".claude/settings.json", ".gitconfig", "bin"} {
		if !slices.Contains(p.DenyWrite, filepath.Join(home, want)) {
			t.Fatalf("DenyWrite lacks %s: %v", want, p.DenyWrite)
		}
	}
	if os.Getuid() != 0 && slices.Contains(p.DenyWrite, "/usr/bin") {
		t.Fatalf("an unwritable PATH directory: %v", p.DenyWrite)
	}
	if runtime.GOOS != "windows" && !slices.Contains(p.DenyConnect, filepath.Join("/tmp/tt", "tmux-"+strconv.Itoa(os.Getuid()))) {
		t.Fatalf("DenyConnect: %v", p.DenyConnect)
	}
}
