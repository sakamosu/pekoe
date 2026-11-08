package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pekoe/internal/client"
)

// exitedPid is the pid of a process that has already exited, so StopDaemon's
// wait for it returns at once.
func exitedPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// stubStarts replaces both ways of starting pekoed: each notes its name on the
// fake daemon, then dials it (after `then`, which may change its answers).
func stubStarts(t *testing.T, fd *fakeDaemon, launchd int, then func()) {
	t.Helper()
	oldStart, oldLaunchd, oldPid := startDaemon, launchdStart, launchdPid
	t.Cleanup(func() { startDaemon, launchdStart, launchdPid = oldStart, oldLaunchd, oldPid })
	stub := func(name string) func() (*client.Client, error) {
		return func() (*client.Client, error) {
			fd.note(name)
			if then != nil {
				then()
			}
			return client.Dial()
		}
	}
	startDaemon, launchdStart = stub("start"), stub("launchd")
	launchdPid = func() int { return launchd }
}

func status(pid int, version string) map[string]any {
	return map[string]any{"pid": pid, "version": version}
}

// Running: status (for the version) → stop and wait → start → status.
func TestDaemonRestartStopsThenStarts(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"daemon.status": status(exitedPid(t), "0.1.0-16-gf2ba4e3")}
	stubStarts(t, fd, 0, func() {
		fd.mu.Lock()
		fd.results["daemon.status"] = status(os.Getpid(), "0.1.0-49-g633b8a2")
		fd.mu.Unlock()
	})
	out, err := capture(t, func() error { return dispatch("daemon", "restart") })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"daemon.status", "daemon.status", "daemon.stop", "start", "daemon.status"}
	if c := fd.calls(); !reflect.DeepEqual(c, want) {
		t.Fatalf("calls = %v, want %v", c, want)
	}
	if !strings.HasPrefix(out, "pekoed restarted · 0.1.0-16-gf2ba4e3 → 0.1.0-49-g633b8a2 · log: ") {
		t.Fatalf("output = %q", out)
	}
}

// The same version is shown once.
func TestDaemonRestartSameVersion(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"daemon.status": status(exitedPid(t), "0.1.0")}
	stubStarts(t, fd, 0, nil)
	out, err := capture(t, func() error { return dispatch("daemon", "restart") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "pekoed restarted · 0.1.0 · log: ") {
		t.Fatalf("output = %q", out)
	}
}

// Not running: just start, no error.
func TestDaemonRestartNotRunningStarts(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"daemon.status": status(os.Getpid(), "0.1.0")}
	sock := os.Getenv("PEKOE_SOCK")
	t.Setenv("PEKOE_SOCK", filepath.Join(filepath.Dir(sock), "none")) // nothing answers
	stubStarts(t, fd, 0, func() { os.Setenv("PEKOE_SOCK", sock) })
	out, err := capture(t, func() error { return dispatch("daemon", "restart") })
	if err != nil {
		t.Fatal(err)
	}
	if c := fd.calls(); !reflect.DeepEqual(c, []string{"start", "daemon.status"}) {
		t.Fatalf("calls = %v", c)
	}
	if !strings.HasPrefix(out, "pekoed was not running; started · 0.1.0 · log: ") {
		t.Fatalf("output = %q", out)
	}
}

// When stopping is refused (an agent, without the human token), nothing
// starts.
func TestDaemonRestartStopRefused(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"daemon.status": status(exitedPid(t), "0.1.0")}
	fd.errs = map[string]string{"daemon.stop": "only a person can do this"}
	stubStarts(t, fd, 0, nil)
	err := dispatch("daemon", "restart")
	if err == nil || !strings.Contains(err.Error(), "only a person") {
		t.Fatalf("err = %v", err)
	}
	if c := fd.calls(); !reflect.DeepEqual(c, []string{"daemon.status", "daemon.status", "daemon.stop"}) {
		t.Fatalf("calls = %v (must not start)", c)
	}
}

// When pekoed does not exit in time, nothing starts.
func TestDaemonRestartStopTimeout(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"daemon.status": status(os.Getpid(), "0.1.0")} // never exits
	stubStarts(t, fd, 0, nil)
	old := daemonStopTimeout
	daemonStopTimeout = 200 * time.Millisecond
	t.Cleanup(func() { daemonStopTimeout = old })
	err := dispatch("daemon", "restart")
	if err == nil || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range fd.calls() {
		if c == "start" || c == "launchd" {
			t.Fatalf("calls = %v (must not start)", fd.calls())
		}
	}
}

// A pekoed that launchd runs is started again through launchd; one launchd
// runs under another pid (another data directory) is not ours.
func TestDaemonRestartLaunchd(t *testing.T) {
	for _, c := range []struct {
		launchd func(pid int) int
		start   string
		out     string
	}{
		{func(pid int) int { return pid }, "launchd", "pekoed restarted (launchd) · 0.1.0 · log: "},
		{func(pid int) int { return pid + 1 }, "start", "pekoed restarted · 0.1.0 · log: "},
		{func(int) int { return 0 }, "start", "pekoed restarted · 0.1.0 · log: "},
	} {
		fd := startFakeDaemon(t)
		pid := exitedPid(t)
		fd.results = map[string]any{"daemon.status": status(pid, "0.1.0")}
		stubStarts(t, fd, c.launchd(pid), nil)
		out, err := capture(t, func() error { return dispatch("daemon", "restart") })
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"daemon.status", "daemon.status", "daemon.stop", c.start, "daemon.status"}
		if got := fd.calls(); !reflect.DeepEqual(got, want) {
			t.Errorf("calls = %v, want %v", got, want)
		}
		if !strings.HasPrefix(out, c.out) {
			t.Errorf("output = %q, want %q…", out, c.out)
		}
	}
}

func TestParseLaunchdPid(t *testing.T) {
	out := []byte("gui/501/dev.pekoe.pekoed = {\n\tactive count = 1\n\tpath = /Users/x/Library/LaunchAgents/dev.pekoe.pekoed.plist\n\tstate = running\n\n\tprogram = /Users/x/.local/bin/pekoe\n\tpid = 4242\n\tlast exit code = 0\n}\n")
	if n := parseLaunchdPid(out); n != 4242 {
		t.Fatalf("pid = %d", n)
	}
	if n := parseLaunchdPid([]byte("gui/501/dev.pekoe.pekoed = {\n\tstate = not running\n}\n")); n != 0 {
		t.Fatalf("not running: pid = %d", n)
	}
}
