package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"pekoe/internal/testenv"
)

func TestBornIn(t *testing.T) {
	const sock = "/tmp/tmux-501/pekoe"
	for _, tc := range []struct {
		name string
		env  []string
		want bool
	}{
		{"window of a", []string{"HOME=/h", "PEKOE_SESSION=a", "TMUX=/tmp/tmux-501/pekoe,123,0"}, true},
		{"another session", []string{"PEKOE_SESSION=b", "TMUX=/tmp/tmux-501/pekoe,123,0"}, false},
		{"another server (a sandbox's)", []string{"PEKOE_SESSION=a", "TMUX=/tmp/tmux-501/pekoe-sb-x,9,0"}, false},
		{"socket path prefix only", []string{"PEKOE_SESSION=a", "TMUX=/tmp/tmux-501/pekoe2,9,0"}, false},
		{"not in tmux (a person's nvim from the TUI)", []string{"PEKOE_SESSION=a"}, false},
		{"no session", []string{"TMUX=/tmp/tmux-501/pekoe,123,0"}, false},
		{"first value counts", []string{"PEKOE_SESSION=b", "PEKOE_SESSION=a", "TMUX=/tmp/tmux-501/pekoe,1,0"}, false},
		{"slug prefix only", []string{"PEKOE_SESSION=ab", "TMUX=/tmp/tmux-501/pekoe,1,0"}, false},
		{"unreadable", nil, false},
	} {
		if got := bornIn(tc.env, "a", sock); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if bornIn([]string{"PEKOE_SESSION=", "TMUX=,1,0"}, "", "") {
		t.Error("an empty slug and socket match nothing")
	}
}

// pick takes what a's windows left (by the environment, or by the place
// when it cannot be read) and what runs under it, and leaves pekoed's,
// the live panes', the server's, pekoed's parents', other sessions' and
// daemons', and a person's processes in the worktree.
func TestLeftoverPick(t *testing.T) {
	const sock = "/s/pekoe"
	since := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	before, after := since.Add(-time.Hour), since.Add(time.Minute)
	a := []string{"PEKOE_SESSION=a", "TMUX=/s/pekoe,10,0"}
	user := []string{"HOME=/h"}
	procs := map[int]proc{}
	env := map[int][]string{}
	cwd := map[int]string{}
	sid := map[int]int{}
	add := func(pid, ppid int, e []string, dir string, s int, start time.Time) {
		procs[pid] = proc{pid: pid, ppid: ppid, start: start, cmd: "p" + strconv.Itoa(pid)}
		env[pid], cwd[pid], sid[pid] = e, dir, s
	}
	add(1, 0, nil, "/", 1, before)
	add(5, 4, user, "/h", 5, before)    // a person's login shell
	add(10, 1, a, "/", 10, before)      // the tmux server (started from a's window)
	add(90, 1, a, "/wt", 90, after)     // pekoed's parent (a shell of a's)
	add(20, 90, a, "/", 20, after)      // pekoed (started from a's window)
	add(21, 20, a, "/wt", 21, after)    // a hook pekoed runs
	add(30, 10, a, "/wt", 30, after)    // a live pane (a new session a)
	add(31, 30, a, "/wt", 30, after)    // … and what runs in it
	add(110, 30, nil, "/wt", 99, after) // … a system binary in it, its own session gone
	add(40, 1, a, "/tmp", 40, after)    // a's agent left it: reparented to launchd
	add(41, 40, nil, "/tmp", 40, after) // … a system binary under it, elsewhere
	add(42, 10, a, "/wt", 42, after)    // a closed window's process that ignored SIGHUP
	add(50, 1, []string{"PEKOE_SESSION=b", "TMUX=/s/pekoe,10,0"}, "/wt", 50, after)
	add(60, 1, []string{"PEKOE_SESSION=a", "TMUX=/s/other,7,0"}, "/wt", 60, after)
	add(70, 1, []string{"PEKOE_SESSION=a"}, "/wt", 70, after)
	add(80, 1, user, "/wt", 99, after)     // readable, not a's: the place does not count
	add(100, 1, nil, "/wt/sub", 99, after) // a Bash tool call's `loop &`: its shell (99) gone
	add(101, 1, nil, "/wt", 101, after)    // a Bash tool call's shell left to launchd
	add(102, 101, nil, "/wt", 101, after)  // … and its job
	add(103, 5, nil, "/wt", 5, after)      // a person's job (their shell holds it)
	add(105, 1, user, "/", 105, after)     // an app launchd started (VS Code)
	add(104, 105, nil, "/wt", 105, after)  // … its task in the worktree
	add(107, 1, nil, "/", 107, after)      // a system app launchd started
	add(106, 107, nil, "/wt", 107, after)  // … its helper in the worktree
	add(108, 1, nil, "/wt", 99, before)    // started before the session was made
	add(109, 1, nil, "/wtx", 99, after)    // a worktree whose name starts the same
	add(111, 1, nil, "/wt", 0, after)      // no session id
	in := procInfo{
		env: func(pid int) []string { return env[pid] },
		cwds: func(pids []int) map[int]string {
			m := map[int]string{}
			for _, p := range pids {
				m[p] = cwd[p]
			}
			return m
		},
		sid:  func(pid int) int { return sid[pid] },
		gone: func(pid int) bool { _, ok := procs[pid]; return !ok },
	}
	pick := func(dir string) []int {
		r := leftoverRule{LeftoverScope: LeftoverScope{Slug: "a", Dir: dir, Since: since}, socket: sock, keepTree: []int{20, 30}, keepOnly: append([]int{10}, ancestors(procs, 20)...)}
		var got []int
		for _, p := range r.pick(procs, in) {
			got = append(got, p.pid)
		}
		sort.Ints(got)
		return got
	}
	if got, want := pick("/wt"), []int{40, 41, 42, 100, 101, 102}; !reflect.DeepEqual(got, want) {
		t.Errorf("worktree /wt: got %v, want %v", got, want)
	}
	// no worktree to go by: the environment only
	if got, want := pick(""), []int{40, 41, 42}; !reflect.DeepEqual(got, want) {
		t.Errorf("no worktree: got %v, want %v", got, want)
	}
}

// Another tmux server a's window started: the panes it opened as it
// started are a's, and go; a pane opened in it later (a person's `tmux -L
// x new`) stays, with what runs in it and the server.
func TestLeftoverPickOtherTmux(t *testing.T) {
	const sock = "/s/pekoe"
	since := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	at := since.Add(time.Minute)
	a := []string{"PEKOE_SESSION=a", "TMUX=/s/pekoe,10,0"}
	procs := map[int]proc{}
	env := map[int][]string{}
	add := func(pid, ppid int, e []string, start time.Time, cmd string) {
		procs[pid] = proc{pid: pid, ppid: ppid, start: start, cmd: cmd}
		env[pid] = e
	}
	add(1, 0, nil, since, "/sbin/launchd")
	add(200, 1, a, at, "/opt/homebrew/bin/tmux -L x new-session -d -s job sleep 120") // started from a's window
	add(201, 200, nil, at.Add(time.Second), "sleep 120")                              // the pane it opened as it started
	add(202, 201, nil, at.Add(time.Hour), "sleep 1")                                  // … and what runs in it, later
	// a person's pane, opened later (Linux shows its environment: the
	// server's, with the server's own $TMUX)
	add(203, 200, []string{"PEKOE_SESSION=a", "TMUX=/tmp/tmux-501/x,200,1"}, at.Add(2*time.Second), "-zsh")
	add(204, 203, nil, at.Add(time.Hour), "nvim")                     // … and what runs in it
	add(300, 1, a, at, "tmux -L y new-session -d sleep 120")          // another one, only its own panes
	add(301, 300, nil, at, "sleep 120")                               //
	add(400, 1, a, time.Time{}, "tmux -L z new-session -d sleep 120") // start unknown
	add(401, 400, nil, at, "sleep 120")                               //
	add(500, 1, a, at, "/usr/local/bin/mytmux -L w")                  // not tmux: all of it goes
	add(501, 500, []string{"HOME=/h"}, at.Add(time.Hour), "sh")       //
	in := procInfo{
		env:  func(pid int) []string { return env[pid] },
		cwds: func([]int) map[int]string { return nil },
		sid:  func(pid int) int { return pid },
		gone: func(pid int) bool { _, ok := procs[pid]; return !ok },
	}
	r := leftoverRule{LeftoverScope: LeftoverScope{Slug: "a", Since: since}, socket: sock}
	var got []int
	for _, p := range r.pick(procs, in) {
		got = append(got, p.pid)
	}
	sort.Ints(got)
	if want := []int{201, 202, 300, 301, 500, 501}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseProcs(t *testing.T) {
	out := "    1     0     0 Mon Nov 17 09:00:00 2026     /sbin/launchd\n" +
		"  412     1   501 Thu Nov  5 10:01:02 2026     /bin/zsh -c while :; do :; done\n" +
		"  413   412   502 Thu Nov 20 10:01:02 2026     other user\nbad line\n"
	got := parseProcs([]byte(out), 501)
	want := map[int]proc{412: {pid: 412, ppid: 1, start: time.Date(2026, 11, 5, 10, 1, 2, 0, time.Local), cmd: "/bin/zsh -c while :; do :; done"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}

func TestWithin(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{{"/w/x", true}, {"/w/x/a/b", true}, {"/w/xy", false}, {"/w", false}, {"", false}, {"/r/x/a", true}} {
		if got := within(tc.path, []string{"/w/x", "/r/x/"}); got != tc.want {
			t.Errorf("%q: got %v", tc.path, got)
		}
	}
}

// TestLeftoverHelper is a process whose environment can be read (this
// test binary: not a system one) for the tests below: it sleeps.
func TestLeftoverHelper(t *testing.T) {
	if os.Getenv("PEKOE_LEFTOVER_HELPER") == "" {
		t.Skip("run by TestProcEnv and TestKillLeftovers")
	}
	time.Sleep(2 * time.Minute)
}

func helperArgs() []string { return []string{os.Args[0], "-test.run=^TestLeftoverHelper$"} }

// procEnv reads the environment another process of ours started with,
// procCwds its working directory.
func TestProcEnv(t *testing.T) {
	dir := t.TempDir()
	c := exec.Command(helperArgs()[0], helperArgs()[1:]...)
	c.Env = []string{"PEKOE_LEFTOVER_HELPER=1", "PEKOE_SESSION=probe", "TMUX=/x,1,0"}
	c.Dir = dir
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	var env []string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if env = procEnv(c.Process.Pid); len(env) > 0 { // after exec
			break
		}
	}
	if !reflect.DeepEqual(env, c.Env) {
		t.Fatalf("env %q", env)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if got := procCwds([]int{c.Process.Pid})[c.Process.Pid]; got != real && got != dir {
		t.Fatalf("cwd %q, want %q", got, real)
	}
	if procSid(c.Process.Pid) <= 0 {
		t.Fatal("no session id")
	}
}

// alive reports whether pid runs (a process that is not our child: init
// reaps it once it is killed).
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// readPid waits for a window to write a pid to path.
func readPid(t *testing.T, path string) int {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		b, err := os.ReadFile(path)
		if pid, err2 := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && err2 == nil && pid > 1 {
			return pid
		}
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

// shq quotes args for sh.
func shq(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// Against a real private tmux server: closing a's windows and
// KillLeftovers stops what a's window sent to the background and left
// there: a busy loop in the window's session, one in a session of its own
// (setsid, as Claude Code's Bash tool runs its shell), both system
// binaries, and a process whose environment can be read that went
// elsewhere. It leaves another session's, a process of the same slug on
// another server or outside tmux, a person's job in the worktree and a new
// window a.
func TestKillLeftovers(t *testing.T) {
	testenv.NeedTmux(t)
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl (for setsid) is not installed:", err)
	}
	since := time.Now()
	d := testenv.ShortDir(t, "bt") // a unix socket path must be short
	t.Setenv("TMUX_TMPDIR", d)
	wt, other := filepath.Join(d, "wt"), filepath.Join(d, "other")
	for _, p := range []string{wt, other} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tm := &Tmux{Socket: "pekoe-test-left", Session: "t", PidFile: filepath.Join(t.TempDir(), "tmux.pid")}
	if _, err := tm.run("new-session", "-d", "-s", "t", "-n", "_keeper", "sleep 120"); err != nil {
		t.Fatal(err)
	}
	tm.recordPid()
	server := tm.recordedPid()
	var pids []int
	t.Cleanup(func() { // never leave a loop or the server behind
		for _, pid := range pids {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		tm.KillServer()
		killTmuxPid(server, tm.Socket)
	})
	// bg starts a command in the background from a shell that exits: the
	// job is reparented to init, as a Bash tool call's `cmd &` is. It
	// ignores SIGHUP, so closing the window does not end it.
	script := filepath.Join(d, "bg.sh")
	if err := os.WriteFile(script, []byte("( trap '' HUP; exec \"$@\" ) </dev/null >/dev/null 2>&1 &\necho $! > \"$PIDFILE\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pf := func(name string) string { return filepath.Join(d, name) }
	bg := func(name string, cmd ...string) string {
		return "PIDFILE=" + pf(name) + " sh " + shq(append([]string{script}, cmd...)...)
	}
	// a busy loop that ends with the test process: even a test killed
	// before its cleanups (-timeout) leaves none behind
	loop := []string{"sh", "-c", "while kill -0 " + strconv.Itoa(os.Getpid()) + " 2>/dev/null; do :; done"}
	setsid := []string{"perl", "-MPOSIX", "-e", "defined(POSIX::setsid()) or die; exec @ARGV", "sh", script}
	window := func(slug, dir, cmd string) {
		t.Helper()
		if _, err := tm.run("new-window", "-d", "-t", "=t:", "-n", slug+"~agent", "-c", dir, "-e", "PEKOE_SESSION="+slug, cmd); err != nil {
			t.Fatal(err)
		}
	}
	window("a", wt, bg("a1", loop...)+
		"; PIDFILE="+pf("a2")+" "+shq(append(append([]string{}, setsid...), loop...)...)+
		"; cd "+shq(other)+" && PEKOE_LEFTOVER_HELPER=1 "+bg("a3", helperArgs()...)+
		"; exec sleep 120")
	window("b", other, bg("b1", "sleep", "120")+"; PEKOE_LEFTOVER_HELPER=1 "+bg("b2", helperArgs()...)+"; exec sleep 120")
	// each pid goes to the cleanup as soon as it is read
	read := func(name string) int {
		t.Helper()
		pid := readPid(t, pf(name))
		pids = append(pids, pid)
		return pid
	}
	a1, a2, a3, b1, b2 := read("a1"), read("a2"), read("a3"), read("b1"), read("b2")
	if s1, s2 := procSid(a1), procSid(a2); s1 <= 0 || s2 <= 0 || s1 == s2 {
		t.Fatalf("loops in sessions %d and %d: setsid did not take", s1, s2)
	}
	// not born in a's windows, in a's worktree: another daemon's and a
	// person's of the same slug (environment readable), a person's job
	// (a system binary whose session the test's shell holds)
	outside := func(name string, env []string, cmd ...string) int {
		c := exec.Command("sh", append([]string{script}, cmd...)...)
		c.Dir = wt
		c.Env = []string{"PIDFILE=" + pf(name)}
		for _, kv := range os.Environ() { // the test's own (pekoe's, when run in a session) go
			if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "PEKOE_SESSION=") {
				c.Env = append(c.Env, kv)
			}
		}
		c.Env = append(c.Env, env...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return read(name)
	}
	o1 := outside("o1", []string{"PEKOE_LEFTOVER_HELPER=1", "PEKOE_SESSION=a", "TMUX=/elsewhere/pekoe,1,0"}, helperArgs()...)
	o2 := outside("o2", []string{"PEKOE_LEFTOVER_HELPER=1", "PEKOE_SESSION=a"}, helperArgs()...)
	o3 := outside("o3", nil, "sleep", "120")

	if err := tm.KillSession("a"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	for _, pid := range []int{a1, a2, a3} {
		if !alive(pid) {
			t.Fatalf("closing the window ended %d: the test shows nothing", pid)
		}
	}
	// a new session a, started after the old one went: its window stays
	window("a", wt, "sleep 120 & echo $! > "+pf("n1")+"; wait")
	n1 := read("n1")

	stopped, err := tm.KillLeftovers(LeftoverScope{Slug: "a", Dir: wt, Since: since})
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, l := range stopped {
		got = append(got, l.PID)
		if l.Command == "" {
			t.Errorf("stopped %d: no command", l.PID)
		}
	}
	sort.Ints(got)
	want := []int{a1, a2, a3}
	sort.Ints(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stopped %v, want %v (%v)", got, want, stopped)
	}
	for end := time.Now().Add(3 * time.Second); (alive(a1) || alive(a2) || alive(a3)) && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(a1) || alive(a2) || alive(a3) {
		t.Fatal("a's leftovers still run")
	}
	for name, pid := range map[string]int{"b's": b1, "b's helper": b2, "another server's": o1, "outside tmux": o2, "a person's job": o3, "the new a's": n1} {
		if !alive(pid) {
			t.Errorf("%s process %d was stopped", name, pid)
		}
	}
	// nothing left: a second call stops nothing
	if again, err := tm.KillLeftovers(LeftoverScope{Slug: "a", Dir: wt, Since: since}); err != nil || len(again) != 0 {
		t.Fatalf("again: %v %v", again, err)
	}
}

// Against real tmux servers: a's window starts another tmux server and
// leaves it; later a person (outside pekoe) opens a pane there. Closing
// a's windows and KillLeftovers stops the pane the server opened for a,
// and leaves the person's pane and the server.
func TestKillLeftoversOtherTmuxServer(t *testing.T) {
	testenv.NeedTmux(t)
	since := time.Now()
	d := testenv.ShortDir(t, "bx")
	t.Setenv("TMUX_TMPDIR", d)
	wt := filepath.Join(d, "wt")
	if err := os.Mkdir(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	tm := &Tmux{Socket: "pekoe-test-lx", Session: "t", PidFile: filepath.Join(t.TempDir(), "tmux.pid")}
	if _, err := tm.run("new-session", "-d", "-s", "t", "-n", "_keeper", "sleep 120"); err != nil {
		t.Fatal(err)
	}
	tm.recordPid()
	server := tm.recordedPid()
	const other = "pekoe-test-lxo"
	var pids []int
	t.Cleanup(func() { // never leave a server or a pane behind
		for _, pid := range pids {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		exec.Command("tmux", "-L", other, "kill-server").Run()
		tm.KillServer()
		killTmuxPid(server, tm.Socket)
	})
	panePid := func(sess string) int {
		t.Helper()
		out, err := exec.Command("tmux", "-L", other, "list-panes", "-t", "="+sess, "-F", "#{pane_pid}").Output()
		pid, err2 := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil || err2 != nil || pid <= 1 {
			t.Fatalf("pane of %s: %q %v", sess, out, err)
		}
		pids = append(pids, pid)
		return pid
	}
	if _, err := tm.run("new-window", "-d", "-t", "=t:", "-n", "a~agent", "-c", wt, "-e", "PEKOE_SESSION=a",
		"tmux -L "+other+" new-session -d -s job 'sleep 120'; exec sleep 120"); err != nil {
		t.Fatal(err)
	}
	var job int
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if exec.Command("tmux", "-L", other, "has-session", "-t", "=job").Run() == nil {
			job = panePid("job")
			break
		}
		if time.Now().After(end) {
			t.Fatal("the window did not start the other server")
		}
	}
	out, _ := exec.Command("tmux", "-L", other, "display-message", "-p", "#{pid}").Output()
	otherServer, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	pids = append(pids, otherServer)
	// later than the server's start by more than ps's whole seconds tell
	time.Sleep(2100 * time.Millisecond)
	c := exec.Command("tmux", "-L", other, "new-session", "-d", "-s", "person", "-c", d, "sleep 120")
	for _, kv := range os.Environ() { // a person's: not in pekoe's tmux
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "PEKOE_SESSION=") {
			c.Env = append(c.Env, kv)
		}
	}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	person := panePid("person")

	if err := tm.KillSession("a"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	stopped, err := tm.KillLeftovers(LeftoverScope{Slug: "a", Dir: wt, Since: since})
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, l := range stopped {
		got = append(got, l.PID)
	}
	if !reflect.DeepEqual(got, []int{job}) {
		t.Errorf("stopped %v, want only a's pane %d (%v)", got, job, stopped)
	}
	for end := time.Now().Add(3 * time.Second); alive(job) && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(job) {
		t.Error("a's pane in the other server still runs")
	}
	time.Sleep(300 * time.Millisecond)
	if !alive(person) {
		t.Errorf("the person's pane %d was stopped", person)
	}
	if !alive(otherServer) {
		t.Errorf("the other server %d (the person's pane is in it) was stopped", otherServer)
	}
}

// waitGroupGone returns once the group has exited, and SIGKILLs a group
// that outlives the grace (one that ignores SIGHUP, as an agent writing
// its transcript on the way out would).
func TestWaitGroupGone(t *testing.T) {
	start := func(script string) *exec.Cmd {
		c := exec.Command("sh", "-c", script)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Skipf("cannot start a process: %v", err)
		}
		go c.Wait() // reap: a zombie still counts in its group
		return c
	}
	gone := func(pgid int) bool { return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) }

	quick := start("sleep 0.2")
	at := time.Now()
	waitGroupGone(quick.Process.Pid, 5*time.Second)
	if !gone(quick.Process.Pid) || time.Since(at) > 3*time.Second {
		t.Fatalf("quick group: gone=%v after %v", gone(quick.Process.Pid), time.Since(at))
	}

	stubborn := start(`trap "" HUP TERM; sleep 30 & wait`)
	time.Sleep(100 * time.Millisecond) // the trap is set
	_ = syscall.Kill(-stubborn.Process.Pid, syscall.SIGHUP)
	time.Sleep(50 * time.Millisecond)
	if gone(stubborn.Process.Pid) {
		t.Fatal("the group did not outlive SIGHUP: nothing to test")
	}
	at = time.Now()
	waitGroupGone(stubborn.Process.Pid, 200*time.Millisecond)
	if !gone(stubborn.Process.Pid) {
		_ = syscall.Kill(-stubborn.Process.Pid, syscall.SIGKILL)
		t.Fatal("stubborn group still there")
	}
	if time.Since(at) > 3*time.Second {
		t.Fatalf("took %v", time.Since(at))
	}
}
