package engine

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestSignalName(t *testing.T) {
	for in, want := range map[string]string{"": "", "0": "", "int": "SIGINT", "TERM": "SIGTERM", "2": "SIGINT", "9": "SIGKILL", "31": "signal 31"} {
		if got := SignalName(in); got != want {
			t.Errorf("SignalName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain テキスト", "plain テキスト"},
		{"a\nb\tc", "a\nb\tc"},
		{"a\r\nb\rc", "a\nb\nc"},
		{"AAA\x1b[201~\x1b[ZBBB\x03CCC", "AAA[201~[ZBBBCCC"},
		{"x\x00y\x7fz\u009b1~", "xyz1~"},
		{"\x1b\x03", ""},
	}
	for _, c := range cases {
		if got := CleanText(c.in); got != c.want {
			t.Errorf("CleanText(%q) = %q, want %q", c.in, got, c.want)
		}
		if HasControl(c.in) != (CleanText(c.in) != strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(c.in)) {
			t.Errorf("HasControl(%q) disagrees with CleanText", c.in)
		}
	}
}

func TestParsePanes(t *testing.T) {
	out := "_keeper\t100\t0\t9\nfeat-x~agent\t200\t0\t9\nfeat-x~dev\t201\t1\t9\n+tapi~agent\t300\t0\tx\nodd~agent\tx\t0\t9\nbad~agent\t1\t0\t9\nold~agent\t400\ngone~agent\t\t1\t9\nlost~agent\t\t0\t9\n"
	got := parsePanes(out)
	want := []Pane{
		{Slug: "feat-x", Window: "agent", PID: 200, Server: 9},
		{Slug: "feat-x", Window: "dev", PID: 201, Dead: true, Server: 9},
		{Slug: "+tapi", Window: "agent", PID: 300},
		{Slug: "old", Window: "agent", PID: 400},
		{Slug: "gone", Window: "agent", Dead: true, Server: 9}, // tmux 3.8: a dead pane has no pid
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

// KillWait waits for (and after the grace kills) only the group the
// pane's own process leads: not a dead pane's old pid, which the system
// may have given to anyone, nor a pid that is not the server's child.
func TestGroupToWait(t *testing.T) {
	start := func(own bool) int {
		c := exec.Command("sleep", "30")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: own}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Process.Kill(); c.Wait() })
		return c.Process.Pid
	}
	leader := start(true) // a group leader, the test process's child
	cases := []struct {
		name string
		p    Pane
		want int
	}{
		{"live pane", Pane{PID: leader, Server: os.Getpid()}, leader},
		{"dead pane", Pane{PID: leader, Server: os.Getpid(), Dead: true}, 0},
		{"pid not the server's child", Pane{PID: leader, Server: os.Getppid()}, 0},
		{"server not known", Pane{PID: leader}, 0},
		{"not a group leader", Pane{PID: start(false), Server: os.Getpid()}, 0},
		{"gone", Pane{PID: 1 << 30, Server: os.Getpid()}, 0},
	}
	for _, c := range cases {
		if got := groupToWait(c.p); got != c.want {
			t.Errorf("%s: groupToWait = %d, want %d", c.name, got, c.want)
		}
	}
}
