package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pekoe/internal/paths"
)

// lockEnv isolates a data directory and a socket (kept short: macOS caps
// unix socket paths at ~104 bytes, too short for t.TempDir()).
func lockEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PEKOE_HOME", t.TempDir())
	t.Setenv("PEKOE_WORKTREES", filepath.Join(os.Getenv("PEKOE_HOME"), "wt"))
	t.Setenv("PEKOE_TMUX_SOCKET", "pekoe-test-unused") // never started: fakeEngine
	dir, err := os.MkdirTemp("/tmp", "pekoelk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("PEKOE_SOCK", filepath.Join(dir, "s"))
	old := lockWait
	lockWait = 3 * time.Second
	t.Cleanup(func() { lockWait = old })
}

// newServing builds a daemon on the fake engine (HTTP on a free port).
func newServing(t *testing.T) (*Daemon, error) {
	t.Helper()
	d, err := New()
	if err != nil {
		return nil, err
	}
	d.Eng = newFake()
	d.Global.HTTP.Port = 0
	return d, nil
}

// runBG runs d and returns a channel closed with Run's result.
func runBG(d *Daemon) chan error {
	done := make(chan error, 1)
	go func() { done <- d.Run() }()
	return done
}

func waitServing(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if socketServes() {
			return
		}
	}
	t.Fatal("daemon never served its socket")
}

func stopAndWait(t *testing.T, d *Daemon, done chan error) {
	t.Helper()
	d.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after Stop")
	}
}

// A second pekoed on the same data directory must not start, whether the
// first is serving or not yet listening, and must not open state.db.
func TestSecondDaemonRefused(t *testing.T) {
	lockEnv(t)
	d1, err := newServing(t)
	if err != nil {
		t.Fatal(err)
	}
	// d1 holds the lock but is not serving yet: d2 waits, then gives up
	lockWait = 200 * time.Millisecond
	if _, err := New(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second New before Run: err = %v, want ErrAlreadyRunning", err)
	}
	lockWait = 3 * time.Second

	done := runBG(d1)
	waitServing(t)
	start := time.Now()
	if _, err := New(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second New while serving: err = %v, want ErrAlreadyRunning", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("refusal took %s; a serving daemon should be detected at once", time.Since(start))
	}
	stopAndWait(t, d1, done)

	// once the first has exited, a new one starts
	d3, err := newServing(t)
	if err != nil {
		t.Fatalf("New after stop: %v", err)
	}
	stopAndWait(t, d3, runBG(d3))
}

// The old daemon's late shutdown must leave alone a socket and pid file
// that are no longer its own (issue #42: it unlinked the new daemon's).
func TestShutdownKeepsNewerSocket(t *testing.T) {
	lockEnv(t)
	d, err := newServing(t)
	if err != nil {
		t.Fatal(err)
	}
	done := runBG(d)
	waitServing(t)

	// another daemon took over the path (e.g. one sharing $PEKOE_SOCK)
	sock := paths.Socket()
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
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
			c.Close()
		}
	}()
	want, _ := statID(sock)
	os.WriteFile(paths.PidFile(), []byte("424242"), 0o644)

	stopAndWait(t, d, done)

	if got, ok := statID(sock); !ok || got != want {
		t.Fatalf("the newer socket was removed or replaced (exists=%v)", ok)
	}
	if !socketServes() {
		t.Fatal("the newer socket no longer accepts connections")
	}
	if b, _ := os.ReadFile(paths.PidFile()); string(b) != "424242" {
		t.Fatalf("pid file = %q, want the newer daemon's", b)
	}
}

// Its own socket and pid file are removed on the way out.
func TestShutdownRemovesOwnSocket(t *testing.T) {
	lockEnv(t)
	d, err := newServing(t)
	if err != nil {
		t.Fatal(err)
	}
	done := runBG(d)
	waitServing(t)
	if b, _ := os.ReadFile(paths.PidFile()); string(b) != fmt.Sprint(os.Getpid()) {
		t.Fatalf("pid file = %q", b)
	}
	stopAndWait(t, d, done)
	if _, err := os.Lstat(paths.Socket()); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
	if _, err := os.Lstat(paths.PidFile()); !os.IsNotExist(err) {
		t.Fatalf("pid file left behind: %v", err)
	}
}

// slowClose delays the engine's Close, as a slow tmux would delay shutdown.
type slowClose struct {
	*fakeEngine
	delay time.Duration
}

func (s slowClose) Close() { time.Sleep(s.delay) }

// daemon.stop, then several autostarts (hooks, CLI) while the old daemon is
// still shutting down — its listener closed, its exit delayed: exactly one
// new daemon comes up, and the old one's late cleanup leaves its socket be.
func TestStopThenConcurrentStartsYieldOne(t *testing.T) {
	lockEnv(t)
	old, err := newServing(t)
	if err != nil {
		t.Fatal(err)
	}
	old.Eng = slowClose{newFake(), 700 * time.Millisecond}
	oldDone := runBG(old)
	waitServing(t)
	call[any](t, old, "daemon.stop", nil)
	// clients autostart once they can no longer connect
	for end := time.Now().Add(5 * time.Second); socketServes(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("old daemon kept serving after daemon.stop")
		}
	}

	var (
		wg      sync.WaitGroup
		started atomic.Int32
		mu      sync.Mutex
		winner  *Daemon
		winDone chan error
		errs    []error
	)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := newServing(t)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			started.Add(1)
			mu.Lock()
			winner, winDone = d, runBG(d)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if err := <-oldDone; err != nil {
		t.Fatalf("old Run: %v", err)
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("%d daemons started, want 1 (errors: %v)", n, errs)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Errorf("loser: %v, want ErrAlreadyRunning", err)
		}
	}
	waitServing(t)
	// old.sockID is safe to read: old's Run has returned (oldDone)
	if got, ok := statID(paths.Socket()); !ok || got == old.sockID {
		t.Fatalf("socket missing or still the old daemon's (exists=%v)", ok)
	}
	if b, _ := os.ReadFile(paths.PidFile()); string(b) != fmt.Sprint(os.Getpid()) {
		t.Fatalf("pid file = %q, want the new daemon's", b)
	}
	stopAndWait(t, winner, winDone)
}
