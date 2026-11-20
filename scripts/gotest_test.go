package scripts

// Tests of gotest.sh (make test's go test runs) with a fake go: this test
// binary run again with $FAKEGO set. Like cmd/go, the fake go test makes a
// work directory in $TMPDIR and runs a test binary (also this binary),
// keeping its output until it exits. Stopped by SIGINT, it waits for the
// binary, drops the output and removes the work directory; by SIGTERM, it
// exits at once and leaves the work directory behind.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("FAKEGO") {
	case "go":
		os.Exit(fakeGo(os.Args[1:]))
	case "binary":
		os.Exit(fakeBinary())
	}
	os.Exit(m.Run())
}

// event notes what the fakes did, one line each, in $FAKEGO_DIR/events.
func event(format string, a ...any) {
	f, err := os.OpenFile(filepath.Join(os.Getenv("FAKEGO_DIR"), "events"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, format+"\n", a...)
	f.Close()
}

// started notes a fake that now waits to be stopped.
func started() {
	pid := strconv.Itoa(os.Getpid())
	os.WriteFile(filepath.Join(os.Getenv("FAKEGO_DIR"), "started."+pid), nil, 0o644)
}

// $FAKEGO_BLOCK: which go test runs wait to be stopped. "background": the
// ones gotest.sh runs in the background (the daemon groups, the web run's
// ./cmd/...), in their test binary; "compile": the same ones, in go test
// itself (compiling: no binary yet); "all": every one, in the binary. Unset,
// none: every binary passes at once.
func fakeGo(args []string) int {
	if len(args) > 0 && args[0] == "list" {
		fmt.Println("pekoe/internal/other")
		fmt.Println("pekoe/internal/daemon")
		return 0
	}
	if len(args) == 0 || args[0] != "test" {
		return 2
	}
	if slices.Contains(args, "-list") {
		fmt.Print("TestA\nTestB\nTestC\nTestD\nok  \tpekoe/internal/daemon\t0.1s\n")
		return 0
	}
	id := strconv.Itoa(os.Getpid())
	event("go %s: test %s", id, strings.Join(args[1:], " "))
	work, err := os.MkdirTemp("", "go-build")
	if err != nil {
		fmt.Println(err)
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	mode := os.Getenv("FAKEGO_BLOCK")
	background := slices.ContainsFunc(args, func(a string) bool { return a == "pekoe/internal/daemon" || a == "./cmd/..." })
	block := mode == "all" || (mode == "background" || mode == "compile") && background

	if block && mode == "compile" {
		started()
		s := <-sig
		event("go %s: %v", id, s)
		if s == os.Interrupt {
			os.RemoveAll(work)
		}
		return 1
	}

	var out bytes.Buffer
	bin := exec.Command(os.Args[0])
	bin.Env = append(os.Environ(), "FAKEGO=binary", "FAKEGO_ID="+id, "FAKEGO_BLOCKS="+strconv.FormatBool(block))
	bin.Stdout, bin.Stderr = &out, &out
	if err := bin.Start(); err != nil {
		fmt.Println(err)
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- bin.Wait() }()
	select {
	case err := <-done:
		os.Stdout.Write(out.Bytes())
		os.RemoveAll(work)
		if err != nil {
			return 1
		}
		return 0
	case s := <-sig:
		event("go %s: %v", id, s)
		if s == syscall.SIGTERM {
			return 1 // the work directory stays
		}
		<-done // the binary's output is dropped
		os.RemoveAll(work)
		return 1
	}
}

func fakeBinary() int {
	id := os.Getenv("FAKEGO_ID")
	if os.Getenv("FAKEGO_BLOCKS") != "true" {
		fmt.Println("ok  \tfake")
		return 0
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	started()
	<-sig
	event("binary %s: terminated", id)
	fmt.Printf("binary of go %s stopped\n", id)
	return 1
}

// gotest makes the command running gotest.sh in the shell sh with the fake
// go and $TMPDIR in a directory of its own, returned with the fakes' one.
func gotest(t *testing.T, sh string, env ...string) (cmd *exec.Cmd, out *lockedBuf, fakeDir, tmp string) {
	t.Helper()
	fakeDir, tmp = t.TempDir(), t.TempDir()
	cmd = exec.Command(sh, "gotest.sh", os.Args[0])
	cmd.Env = append(os.Environ(), append([]string{"FAKEGO=go", "FAKEGO_DIR=" + fakeDir, "TMPDIR=" + tmp}, env...)...)
	out = &lockedBuf{}
	cmd.Stdout, cmd.Stderr = out, out
	return cmd, out, fakeDir, tmp
}

// lockedBuf is the script's output, read while it runs.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func readEvents(t *testing.T, fakeDir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fakeDir, "events"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// the daemon's go test runs (one per group) among the events
func groupRuns(events []string) int {
	n := 0
	for _, e := range events {
		if strings.Contains(e, ": test ") && strings.Contains(e, " -run ") {
			n++
		}
	}
	return n
}

// PEKOE_TEST_SHARDS is the number of groups per build, 1 included; what is
// not a whole number of at least 1 is refused before anything runs.
func TestGoTestShards(t *testing.T) {
	for _, n := range []int{1, 3} {
		cmd, out, fakeDir, _ := gotest(t, "sh", fmt.Sprintf("PEKOE_TEST_SHARDS=%d", n))
		if err := cmd.Run(); err != nil {
			t.Fatalf("PEKOE_TEST_SHARDS=%d: %v\n%s", n, err, out)
		}
		// two builds: the default and -tags web
		if got := groupRuns(readEvents(t, fakeDir)); got != 2*n {
			t.Errorf("PEKOE_TEST_SHARDS=%d: %d daemon runs, want %d\n%s", n, got, 2*n, out)
		}
		if want := fmt.Sprintf("(group %d/%d: ", n, n); !strings.Contains(out.String(), want) {
			t.Errorf("PEKOE_TEST_SHARDS=%d: no %q in\n%s", n, want, out)
		}
	}
	for _, v := range []string{"0", "abc", "-1", "02", "1.5"} {
		cmd, out, fakeDir, _ := gotest(t, "sh", "PEKOE_TEST_SHARDS="+v)
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(out.String(), "PEKOE_TEST_SHARDS="+v) {
			t.Errorf("PEKOE_TEST_SHARDS=%s: %v\n%s", v, err, out)
		}
		if ev := readEvents(t, fakeDir); ev[0] != "" {
			t.Errorf("PEKOE_TEST_SHARDS=%s ran go test: %v", v, ev)
		}
	}
}

// Stopped while its tests run, gotest.sh stops every go test so that it
// removes its work directory and prints its output, exits only after them,
// and prints what each background run had written. A go test of one package
// with a binary running: TERM to the binary only (INT to go test would drop
// the output; TERM to it would leave the work directory in $TMPDIR). One
// compiling: INT. The other packages' run (several packages): INT to go
// test, which would otherwise go on to the next package, and TERM to the
// binary. Ctrl-C (INT to the terminal's process group) reaches only the
// script, which stops the runs the same way.
func TestGoTestStopped(t *testing.T) {
	for _, tc := range []struct {
		name, block, shell string
		ctrlC              bool
		runs               int // fakes waiting to be stopped
		binaries           int // of them, test binaries
		shown              int // binaries whose output is shown
	}{
		// the other packages done: the web run's ./cmd/... and two daemon
		// groups per build
		{name: "background", block: "background", runs: 5, binaries: 5, shown: 5},
		// and the other packages still running
		{name: "all", block: "all", runs: 6, binaries: 6, shown: 5},
		{name: "compiling", block: "compile", runs: 5},
		// job control without a terminal (set -m): bash, not dash
		{name: "ctrl-c", block: "all", shell: "bash", ctrlC: true, runs: 6, binaries: 6, shown: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh := "sh"
			if tc.shell != "" {
				var err error
				if sh, err = exec.LookPath(tc.shell); err != nil {
					t.Skipf("no %s: %v", tc.shell, err)
				}
				if !jobControl(t, sh) {
					t.Skipf("%s puts no background job in a process group of its own without a terminal", sh)
				}
			}
			testStopped(t, sh, tc.block, tc.ctrlC, tc.runs, tc.binaries, tc.shown)
		})
	}
}

// jobControl: sh -c 'set -m' puts a background job in a process group of
// its own, as a shell with a terminal does.
func jobControl(t *testing.T, sh string) bool {
	t.Helper()
	out, err := exec.Command(sh, "-c", `set -m 2>/dev/null; sleep 1 & ps -o pid=,pgid= -p $!; kill $!`).Output()
	if err != nil {
		t.Fatalf("%s: %v", sh, err)
	}
	f := strings.Fields(string(out))
	return len(f) == 2 && f[0] == f[1]
}

func testStopped(t *testing.T, sh, block string, ctrlC bool, runs, binaries, shown int) {
	cmd, out, fakeDir, tmp := gotest(t, sh, "PEKOE_TEST_SHARDS=2", "FAKEGO_BLOCK="+block)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // a terminal's process group
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		// what is left when the test fails
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		pids, _ := filepath.Glob(filepath.Join(fakeDir, "started.*"))
		for _, e := range readEvents(t, fakeDir) {
			if f := strings.Fields(e); len(f) > 1 && f[0] == "go" {
				pids = append(pids, strings.TrimSuffix(f[1], ":"))
			}
		}
		for _, p := range pids {
			if pid, err := strconv.Atoi(strings.TrimPrefix(filepath.Ext(p), ".")); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			} else if pid, err := strconv.Atoi(p); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	for end := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		started, _ := filepath.Glob(filepath.Join(fakeDir, "started.*"))
		othersDone := block == "all" || strings.Contains(out.String(), "ok  \tfake")
		if len(started) == runs && othersDone {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("%d of %d fakes waiting\n%s", len(started), runs, out)
		}
	}
	if ctrlC {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	} else {
		cmd.Process.Signal(syscall.SIGTERM)
	}
	var err error
	select {
	case err = <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("gotest.sh did not exit when stopped\nevents: %v\n%s", readEvents(t, fakeDir), out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() == 0 {
		t.Errorf("exit: %v", err)
	}

	var goTERM, binTERM int
	for _, e := range readEvents(t, fakeDir) {
		switch {
		case strings.HasPrefix(e, "go ") && strings.HasSuffix(e, ": terminated"):
			goTERM++
		case strings.HasPrefix(e, "binary ") && strings.HasSuffix(e, ": terminated"):
			binTERM++
		}
	}
	if goTERM != 0 || binTERM != binaries {
		t.Errorf("go test terminated %d times, test binaries %d; want 0, %d\nevents: %v",
			goTERM, binTERM, binaries, readEvents(t, fakeDir))
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("left in $TMPDIR: %v", names)
	}
	// each background run's output, under its header
	o := out.String()
	for _, h := range []string{
		" test pekoe/internal/daemon (group 1/2: 2 of 4 tests) (stopped)",
		" test pekoe/internal/daemon (group 2/2: 2 of 4 tests) (stopped)",
		" test -tags web pekoe/internal/daemon (group 1/2: 2 of 4 tests) (stopped)",
		" test -tags web pekoe/internal/daemon (group 2/2: 2 of 4 tests) (stopped)",
		" test -tags web ./cmd/... (stopped)",
	} {
		if !strings.Contains(o, h) {
			t.Errorf("no header %q", h)
		}
	}
	if n := strings.Count(o, " stopped\n"); n != shown {
		t.Errorf("%d test binaries' output shown, want %d:\n%s", n, shown, o)
	}
}
