package engine

import (
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"pekoe/internal/testenv"
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
	testenv.NeedTmux(t)
	t.Setenv("TMUX_TMPDIR", testenv.ShortDir(t, "bt"))
	tm := &Tmux{Socket: "pekoe-test-features", Session: "t"}
	if _, err := tm.run("new-session", "-d", "-s", "t", "sleep 30"); err != nil {
		t.Fatal(err)
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
	testenv.NeedTmux(t)
	// short dirs: a unix socket path must fit in ~104 bytes
	tmpA, tmpB := testenv.ShortDir(t, "bt"), testenv.ShortDir(t, "bt")
	tm := &Tmux{Socket: "pekoe-test-kill", Session: "t", PidFile: t.TempDir() + "/tmux.pid"}
	t.Setenv("TMUX_TMPDIR", tmpA)
	if _, err := tm.run("new-session", "-d", "-s", "t", "sleep 60"); err != nil {
		t.Fatal(err)
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
		{"tmux -L pekoe-sb-x -f /dev/null new-session -d -s pekoe -n _keeper", true},
		{"/opt/homebrew/bin/tmux -L pekoe-sb-x new-session", true},
		{"tmux -L pekoe -f /dev/null new-session", false}, // another server
		{"tmux -L pekoe-sb-xy new-session", false},        // prefix only
		{"sleep 3600 -L pekoe-sb-x", false},               // not tmux
		{"", false},
	} {
		if got := isTmuxServerCmd(tc.cmd, "pekoe-sb-x"); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// A session made when slugs could hold '.' (v1.2) is still reachable: tmux
// would read "=v1.2~agent" as window "v1", pane "2~agent".
func TestTargetWithDotInSlug(t *testing.T) {
	testenv.NeedTmux(t)
	d := testenv.ShortDir(t, "bt") // a unix socket path must be short
	t.Setenv("TMUX_TMPDIR", d)
	tm := &Tmux{Socket: "pekoe-test-dot", Session: "t", PidFile: t.TempDir() + "/tmux.pid"}
	if _, err := tm.run("new-session", "-d", "-s", "t", "-n", "_keeper", "sleep 60"); err != nil {
		t.Fatal(err)
	}
	tm.recordPid()
	pid := tm.recordedPid()
	t.Cleanup(func() { tm.KillServer(); killTmuxPid(pid, tm.Socket) })
	if _, err := tm.run("new-window", "-d", "-t", "=t:", "-n", "v1.2~agent", "cat"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tm.target("v1.2", "agent"), "@") {
		t.Fatalf("target %q is not a window id", tm.target("v1.2", "agent"))
	}
	if err := tm.SendText("v1.2", "agent", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := tm.Kill("v1.2", "agent"); err != nil {
		t.Fatal(err)
	}
	if tm.exists("v1.2", "agent") {
		t.Fatal("window with '.' in its name was not killed")
	}
	// plain names keep the name target (no extra lookup)
	if got := tm.target("feat-x", "agent"); got != "t:=feat-x~agent" {
		t.Fatalf("target %q", got)
	}
}

// Against a real private tmux server: SendText types control characters
// as nothing, on both paths (send-keys -l for one line, paste-buffer for
// several), so text cannot end a paste or press Shift-Tab / ^C.
func TestSendTextDropsControls(t *testing.T) {
	testenv.NeedTmux(t)
	d := testenv.ShortDir(t, "bt") // a unix socket path must be short
	t.Setenv("TMUX_TMPDIR", d)
	tm := &Tmux{Socket: "pekoe-test-sendtext", Session: "t", PidFile: t.TempDir() + "/tmux.pid"}
	out := d + "/out"
	// raw: ^C is a byte for cat, not a signal; cat writes what arrives
	if _, err := tm.run("new-session", "-d", "-s", "t", "-n", winName("s", "agent"), "stty raw -echo; exec cat > "+out); err != nil {
		t.Fatal(err)
	}
	tm.recordPid()
	pid := tm.recordedPid()
	t.Cleanup(func() { tm.KillServer(); killTmuxPid(pid, tm.Socket) })
	time.Sleep(200 * time.Millisecond)
	if err := tm.SendText("s", "agent", "A\x1b[ZB\x03C"); err != nil {
		t.Fatal(err)
	}
	if err := tm.SendText("s", "agent", "D\x1b[201~\x1b[ZE\nF\x03G"); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if got, _ = os.ReadFile(out); strings.Contains(string(got), "G") {
			break
		}
	}
	if s := string(got); !strings.Contains(s, "A[ZBC") || !strings.Contains(s, "FG") || strings.ContainsAny(s, "\x1b\x03") {
		t.Fatalf("pane got %q", s)
	}
}

// Against a real private tmux server: a ';' at the end of a literal (or a
// lone ';', as one keystroke from the TUI) reaches the pane, not tmux's
// command parser.
func TestSendLiteralSemicolon(t *testing.T) {
	testenv.NeedTmux(t)
	d := testenv.ShortDir(t, "bt") // a unix socket path must be short
	t.Setenv("TMUX_TMPDIR", d)
	tm := &Tmux{Socket: "pekoe-test-semicolon", Session: "t", PidFile: t.TempDir() + "/tmux.pid"}
	out := d + "/out"
	if _, err := tm.run("new-session", "-d", "-s", "t", "-n", winName("s", "agent"), "stty raw -echo; exec cat > "+out); err != nil {
		t.Fatal(err)
	}
	tm.recordPid()
	pid := tm.recordedPid()
	t.Cleanup(func() { tm.KillServer(); killTmuxPid(pid, tm.Socket) })
	time.Sleep(200 * time.Millisecond)
	for _, s := range []string{";", "b;", "c;d", ";e", `f\;`, "Z"} {
		if err := tm.SendLiteral("s", "agent", s); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
	}
	var got []byte
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if got, _ = os.ReadFile(out); strings.Contains(string(got), "Z") {
			break
		}
	}
	if want := `;b;c;d;ef\;Z`; string(got) != want {
		t.Fatalf("pane got %q, want %q", got, want)
	}
}
