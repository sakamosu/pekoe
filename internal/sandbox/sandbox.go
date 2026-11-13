// Package sandbox runs the commands pekoed starts from code an agent can
// write — [hooks] test / setup / dev / teardown, the gatekeeper — so they
// cannot read pekoe's secrets (the human token above all) nor rewrite
// pekoe's own config.
//
// pekoed runs outside the agents' sandbox. Without this, an agent could put
// "cat ~/.local/share/pekoe/human-token" in a Makefile or a test, end its
// turn, and have pekoed run it with the user's full access.
//
// The profile allows everything but a deny list (the commands are builds,
// tests and dev servers, which write caches all over $HOME):
//
//   - macOS: sandbox-exec (Seatbelt, as Claude Code's own sandbox uses)
//   - Linux: bubblewrap (bwrap) with the host's / bound as it is and the
//     denied paths covered by read-only binds, /dev/null and empty tmpfs
//
// When neither works the commands are not run at all (Wrap returns an
// *UnavailableError): pekoe never falls back to running them bare, unless
// the user turned the sandbox off ([sandbox] enable = false).
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"pekoe/internal/config"
)

// ErrUnavailable is wrapped by the error Wrap returns when no sandbox can
// be set up on this machine.
var ErrUnavailable = errors.New("no sandbox available")

// UnavailableError says why the sandbox could not be set up.
type UnavailableError struct{ Reason string }

func (e *UnavailableError) Error() string {
	return "not run: pekoe runs commands from the worktree only in a sandbox, and " + e.Reason +
		" (to run them without one, set [sandbox] enable = false in config.toml)"
}

func (e *UnavailableError) Unwrap() error { return ErrUnavailable }

// Policy lists what the wrapped command must not reach. Paths are absolute;
// a path that does not exist (yet) is fine.
type Policy struct {
	// DenyRead: files and directories whose contents stay unreadable.
	DenyRead []string
	// DenyWrite: files and directories that cannot be written, created,
	// renamed or removed (anything below a directory included).
	DenyWrite []string
	// DenyConnect: directories whose unix sockets cannot be connected to
	// (tmux servers, which would run a command outside the sandbox).
	DenyConnect []string
	// Stubs: DenyWrite paths that may be made on the host, empty, when
	// they are missing and their parent directory exists, so that bwrap
	// has something to bind over (Linux only; Seatbelt denies by path).
	// Only what changes nothing by being there empty.
	Stubs []Stub
}

// Stub is a directory, or a file with Content, that pekoed makes before
// running a command in bwrap.
type Stub struct {
	Path    string
	Dir     bool
	Content string
}

// Sandbox wraps command lines in the platform's sandbox. The zero value
// and a nil *Sandbox run commands unwrapped (tests); daemons use New.
type Sandbox struct {
	Policy  Policy
	Enabled bool

	goos   string
	locate func() (string, error)
	probe  func(argv []string) error

	mu sync.Mutex
	ok string // the tool, once a probe passed
}

// New makes the daemon's sandbox. enabled is [sandbox] enable.
func New(p Policy, enabled bool) *Sandbox {
	s := &Sandbox{Policy: p, Enabled: enabled, goos: runtime.GOOS, probe: runProbe}
	s.locate = func() (string, error) { return locateTool(s.goos, fileExists) }
	return s
}

// Mechanism names what Wrap uses on this OS, for messages.
func (s *Sandbox) Mechanism() string {
	switch {
	case s == nil || !s.Enabled:
		return "none ([sandbox] enable = false)"
	case s.goos == "darwin":
		return "sandbox-exec"
	case s.goos == "linux":
		return "bwrap"
	}
	return "none"
}

// Check reports whether Wrap would work now (nil when the sandbox is off).
func (s *Sandbox) Check() error {
	_, err := s.Wrap([]string{"/bin/sh", "-c", ":"})
	return err
}

// Wrap returns argv run inside the sandbox: same environment, same working
// directory, stdin / stdout / stderr passed through, and every child process
// confined as well. Off: argv itself.
func (s *Sandbox) Wrap(argv []string) ([]string, error) {
	if s == nil || !s.Enabled {
		return argv, nil
	}
	tool, err := s.ready()
	if err != nil {
		return nil, err
	}
	return s.build(tool, argv), nil
}

// ShellLine is Wrap for a command line handed to a shell (tmux windows):
// "exec <sandbox …> sh -c 'cmd'". Off: cmd itself.
func (s *Sandbox) ShellLine(sh, cmd string) (string, error) {
	if s == nil || !s.Enabled {
		return cmd, nil
	}
	argv, err := s.Wrap([]string{sh, "-c", cmd})
	if err != nil {
		return "", err
	}
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = config.ShellQuoteIfNeeded(a)
	}
	return "exec " + strings.Join(q, " "), nil
}

func (s *Sandbox) build(tool string, argv []string) []string {
	if s.goos == "darwin" {
		return seatbeltArgs(tool, s.Policy, argv)
	}
	return bwrapArgs(tool, s.Policy, argv, hostFS)
}

// ready finds the tool and runs a trivial command through it once. A
// success is kept; a failure is tried again next time (bwrap installed
// meanwhile).
func (s *Sandbox) ready() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ok != "" {
		return s.ok, nil
	}
	tool, err := s.locate()
	if err != nil {
		return "", &UnavailableError{Reason: err.Error()}
	}
	if err := s.probe(s.build(tool, []string{"/bin/sh", "-c", ":"})); err != nil {
		return "", &UnavailableError{Reason: fmt.Sprintf("%s does not work here: %v", filepath.Base(tool), err)}
	}
	s.ok = tool
	return tool, nil
}

// bwrapPaths are where bubblewrap is looked for. Not $PATH: a directory
// on it may be one the sandboxed commands can write (~/.local/bin), and a
// fake bwrap there would run the next command bare.
var bwrapPaths = []string{"/usr/bin/bwrap", "/bin/bwrap", "/usr/local/bin/bwrap", "/run/current-system/sw/bin/bwrap"}

func locateTool(goos string, exists func(string) bool) (string, error) {
	switch goos {
	case "darwin":
		if p := "/usr/bin/sandbox-exec"; exists(p) {
			return p, nil
		}
		return "", errors.New("/usr/bin/sandbox-exec is missing")
	case "linux":
		for _, p := range bwrapPaths {
			if exists(p) {
				return p, nil
			}
		}
		return "", errors.New("bubblewrap (bwrap) is not installed")
	}
	return "", fmt.Errorf("pekoe has no sandbox for %s", goos)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func runProbe(argv []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%v: %s", err, firstLine(msg))
		}
		return err
	}
	return nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// Unavailable is an enabled sandbox that cannot be set up, for reason: what
// a machine without sandbox-exec or bwrap gets (tests use it).
func Unavailable(reason string) *Sandbox {
	return &Sandbox{Enabled: true, goos: runtime.GOOS,
		locate: func() (string, error) { return "", errors.New(reason) },
		probe:  func([]string) error { return nil },
	}
}
