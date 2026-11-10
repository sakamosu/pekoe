package testenv

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// An agent's sandbox (Claude Code's, which pekoe starts agents in) refuses
// some of what the integration tests need: binding a unix socket or a TCP
// port, and so starting a tmux server. The tests that need the real thing
// ask with Need*, and are skipped — with the reason — only where the
// environment cannot do it; everything else they check still fails loudly.

// SkipNote ends every skip message for a missing capability, so it is clear
// where to run the test instead.
const SkipNote = "run make test outside the agent's sandbox to cover it"

var (
	unixOnce sync.Once
	unixErr  error
	tcpOnce  sync.Once
	tcpErr   error
)

// CanBindUnix reports whether a unix socket can be bound here (nil) or why
// not. The answer is probed once per process.
func CanBindUnix() error {
	unixOnce.Do(func() {
		dir, err := shortDir("pkcan")
		if err != nil {
			unixErr = err
			return
		}
		defer os.RemoveAll(dir)
		ln, err := net.Listen("unix", filepath.Join(dir, "s"))
		if err != nil {
			unixErr = err
			return
		}
		ln.Close()
	})
	return unixErr
}

// CanListenTCP reports whether a TCP port on the loopback can be bound here
// (nil) or why not. The answer is probed once per process.
func CanListenTCP() error {
	tcpOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			tcpErr = err
			return
		}
		ln.Close()
	})
	return tcpErr
}

// NeedUnixSocket skips the test where a unix socket cannot be bound.
func NeedUnixSocket(t testing.TB) {
	t.Helper()
	if err := CanBindUnix(); err != nil {
		t.Skipf("needs a real unix socket, which cannot be bound here (%v); %s", err, SkipNote)
	}
}

// NeedTCP skips the test where a TCP port cannot be bound.
func NeedTCP(t testing.TB) {
	t.Helper()
	if err := CanListenTCP(); err != nil {
		t.Skipf("needs a real TCP port, which cannot be bound here (%v); %s", err, SkipNote)
	}
}

// NeedTmux skips the test where tmux is not installed, or where its server
// could not start because a unix socket cannot be bound. Past this, a tmux
// that fails is the test's failure.
func NeedTmux(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed:", err)
	}
	if err := CanBindUnix(); err != nil {
		t.Skipf("needs a tmux server, whose unix socket cannot be bound here (%v); %s", err, SkipNote)
	}
}

// Missing lists the capabilities this environment lacks, for a summary at
// the end of make test (internal/testenv/probe).
func Missing() []string {
	var out []string
	if err := CanBindUnix(); err != nil {
		out = append(out, fmt.Sprintf("unix socket (%v)", err))
	}
	if err := CanListenTCP(); err != nil {
		out = append(out, fmt.Sprintf("TCP port (%v)", err))
	}
	return out
}
