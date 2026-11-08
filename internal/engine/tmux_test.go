package engine

import (
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestTerminalFeatureIndexes(t *testing.T) {
	out := `terminal-features[0] xterm*:clipboard:ccolour:cstyle:focus:title
terminal-features[1] screen*:title
terminal-features[3] xterm-256color:RGB:hyperlinks
terminal-features[4] "xterm-256color:RGB:hyperlinks"
terminal-features[5] xterm-256color:RGB`
	got := terminalFeatureIndexes(out, "xterm-256color:RGB:hyperlinks")
	if want := []int{3, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := terminalFeatureIndexes("terminal-features[0] screen*:title", "x"); got != nil {
		t.Fatalf("got %v, want none", got)
	}
}

// Against a real private tmux server: repeated starts keep one entry, and
// copies left by older versions collapse to one.
func TestEnsureTerminalFeature(t *testing.T) {
	tm := &Tmux{Socket: "boba-test-features", Session: "t"}
	if _, err := tm.run("new-session", "-d", "-s", "t", "sleep 30"); err != nil {
		t.Skip("tmux unavailable:", err)
	}
	defer tm.KillServer()
	const f = "xterm-256color:RGB:hyperlinks"
	count := func() int {
		out, _ := tm.run("show-options", "-s", "terminal-features")
		return len(terminalFeatureIndexes(out, f))
	}
	tm.ensureTerminalFeature(f)
	tm.ensureTerminalFeature(f)
	if n := count(); n != 1 {
		t.Fatalf("after two starts: %d entries", n)
	}
	for i := 0; i < 3; i++ {
		tm.run("set-option", "-sa", "terminal-features", ","+f)
	}
	tm.ensureTerminalFeature(f)
	if n := count(); n != 1 {
		t.Fatalf("after dedupe: %d entries", n)
	}
}

// KillServer stops a server whose socket it cannot reach (started under a
// different TMUX_TMPDIR) through the pid recorded at start.
func TestKillServerFallsBackToPidFile(t *testing.T) {
	// short dirs: a unix socket path must fit in ~104 bytes
	shortDir := func() string {
		d, err := os.MkdirTemp("/tmp", "bt")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		return d
	}
	tmpA, tmpB := shortDir(), shortDir()
	tm := &Tmux{Socket: "boba-test-kill", Session: "t", PidFile: t.TempDir() + "/tmux.pid"}
	t.Setenv("TMUX_TMPDIR", tmpA)
	if _, err := tm.run("new-session", "-d", "-s", "t", "sleep 60"); err != nil {
		t.Skip("tmux unavailable:", err)
	}
	tm.recordPid()
	pid := tm.recordedPid()
	t.Cleanup(func() { killTmuxPid(pid, tm.Socket) }) // never leave the server behind
	if pid == 0 {
		tm.KillServer()
		t.Fatal("pid not recorded")
	}
	t.Setenv("TMUX_TMPDIR", tmpB) // kill-server now looks in the wrong place
	if err := tm.KillServer(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output(); err == nil && strings.Contains(string(out), "tmux") {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("tmux server %d still running", pid)
	}
	// stopping again (nothing running) is fine
	if err := tm.KillServer(); err != nil {
		t.Fatal(err)
	}
}

func TestIsTmuxServerCmd(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{"tmux -L boba-sb-x -f /dev/null new-session -d -s boba -n _keeper", true},
		{"/opt/homebrew/bin/tmux -L boba-sb-x new-session", true},
		{"tmux -L boba -f /dev/null new-session", false}, // another server
		{"tmux -L boba-sb-xy new-session", false},        // prefix only
		{"sleep 3600 -L boba-sb-x", false},               // not tmux
		{"", false},
	} {
		if got := isTmuxServerCmd(tc.cmd, "boba-sb-x"); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.cmd, got, tc.want)
		}
	}
}
