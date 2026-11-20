// Package testenv isolates a whole test binary from the user's real pekoe:
// every location internal/paths resolves (data, config, worktrees, the daemon
// socket, the tmux server) points into one temporary directory. Call Main
// from each package's TestMain; tests that need their own place still
// t.Setenv over it.
package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Main points PEKOE_HOME, XDG_DATA_HOME, XDG_CONFIG_HOME, PEKOE_SOCK,
// PEKOE_TMUX_SOCKET and PEKOE_WORKTREES into a fresh temporary directory,
// and TMPDIR too (t.TempDir, os.MkdirTemp), runs the tests and removes the
// directory.
//
// Stopped by SIGINT or SIGTERM (make test interrupted), it removes the
// directory and the ShortDir ones before exiting, and says which tests were
// running: the testing package would leave all of them behind and print
// nothing.
func Main(m *testing.M) {
	// before there is a directory to leave behind; a signal waits here
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	// short: $PEKOE_SOCK is a unix socket, about 104 bytes at most on macOS
	dir, err := os.MkdirTemp("", "pk")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		os.Exit(1)
	}
	tmp := filepath.Join(dir, "t")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		os.Exit(1)
	}
	// ShortDir stays under the original one: this directory is longer
	shortBase = os.TempDir()
	for k, v := range map[string]string{
		"PEKOE_HOME":        filepath.Join(dir, "h"),
		"XDG_DATA_HOME":     filepath.Join(dir, "xd"),
		"XDG_CONFIG_HOME":   filepath.Join(dir, "xc"),
		"PEKOE_SOCK":        filepath.Join(dir, "s"),
		"PEKOE_TMUX_SOCKET": "pekoe-test-" + filepath.Base(dir),
		"PEKOE_WORKTREES":   filepath.Join(dir, "wt"),
		// hooks and skills install into ~/.claude unless this is set
		"CLAUDE_CONFIG_DIR": filepath.Join(dir, "claude"),
		"TMPDIR":            tmp,
	} {
		os.Setenv(k, v)
	}
	go func() {
		s := <-sig
		running := "between tests"
		if names := runningTests(); len(names) > 0 {
			running = "while running " + strings.Join(names, ", ")
		}
		fmt.Fprintf(os.Stderr, "testenv: %v %s; removing the temporary directories\n", s, running)
		// the tests go on meanwhile and fail as their files go: not news
		if null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			unix.Dup2(int(null.Fd()), 1)
			unix.Dup2(int(null.Fd()), 2)
		}
		// what the tests started (a headless Chrome, a hook's shell and
		// what it runs) would go on writing into the directories
		stopDescendants()
		removeAll(dir)
		os.Exit(1)
	}()
	code := m.Run()
	signal.Stop(sig)
	os.RemoveAll(dir)
	os.Exit(code)
}

// stopDescendants kills the processes this one started and theirs. It
// freezes them first (SIGSTOP), until no new one turns up: one still
// running could start another between the listing and the kill.
func stopDescendants() {
	seen := map[int]bool{}
	for range 50 {
		found := false
		for _, p := range descendants(os.Getpid()) {
			if !seen[p] {
				seen[p], found = true, true
				syscall.Kill(p, syscall.SIGSTOP)
			}
		}
		if !found {
			break
		}
	}
	for p := range seen {
		syscall.Kill(p, syscall.SIGKILL)
	}
}

// descendants lists pid's children, theirs and so on (pgrep never lists
// itself).
func descendants(pid int) []int {
	var all []int
	for queue := []int{pid}; len(queue) > 0; queue = queue[1:] {
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(queue[0])).Output()
		for _, f := range strings.Fields(string(out)) {
			if c, err := strconv.Atoi(f); err == nil {
				all = append(all, c)
				queue = append(queue, c)
			}
		}
	}
	return all
}

// shortBase is where ShortDir makes its directories: the TMPDIR from before
// Main ("" before Main: os.TempDir()).
var shortBase string

// shortDirs are the ShortDir directories not yet removed, for Main to
// remove when the binary is stopped.
var shortDirs = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// removeAll removes dir and the ShortDir directories. The tests go on
// meanwhile and can make the directories again (a database's journal, a
// directory made with its parents), so it removes them until they are gone,
// for a second at most.
func removeAll(dir string) {
	dirs := []string{dir}
	shortDirs.Lock()
	for d := range shortDirs.m {
		dirs = append(dirs, d)
	}
	shortDirs.Unlock()
	for range 20 {
		left := false
		for _, d := range dirs {
			os.RemoveAll(d)
			if _, err := os.Lstat(d); err == nil {
				left = true
			}
		}
		if !left {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// a top-level test function in a goroutine's stack (its subtests and
// goroutines show as TestX.func1 and so on)
var testFrame = regexp.MustCompile(`(?m)^\S+\.((?:Test|Example|Fuzz|Benchmark)[^.(\s]*)[.(]`)

// runningTests names the top-level tests in the stacks of all goroutines.
func runningTests() []string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var names []string
	for _, m := range testFrame.FindAllSubmatch(buf, -1) {
		if name := string(m[1]); name != "TestMain" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
