// Package backend runs speech backends: the external programs that turn a
// sentence into a WAV file (docs/voice.md「10. speech backend の約束事 v1」).
//
// A backend is never looked up in PATH. pekoe runs only two kinds: the
// bundled say (pekoe's own executable, `pekoe voice-backend-say`) and the
// ones a person wrote into config.toml's [voice.backends] with an absolute
// path. Every call checks the path again, starts the program without a
// shell (argv), with the data directory as its working directory and a PATH
// of the system directories and its own, and stops its whole process group
// when it runs out of time.
//
// This package does not play anything and knows nothing of the daemon or of
// internal/voice: the daemon puts an Engine (engine.go) behind whatever
// interface the speech queue asks for.
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Protocol is the version of the backend protocol this package speaks: what
// `version` must print.
const Protocol = "1"

// Builtin is the name of the bundled backend. It always means pekoe's own
// `voice-backend-say`: never a [voice.backends] entry, never PATH.
const Builtin = "say"

// BuiltinCommand is the hidden subcommand of pekoe that is the bundled say.
const BuiltinCommand = "voice-backend-say"

// SystemPATH is the PATH a backend runs with, before its own directory.
const SystemPATH = "/usr/bin:/bin:/usr/sbin:/sbin"

// MaxText is the most bytes of text synth takes on stdin.
const MaxText = 2000

// maxStderr is how much of a backend's stderr is read for the reason.
const maxStderr = 1024

// maxStdout bounds what version / check / voices may print.
const maxStdout = 1 << 20

// Timeouts are how long each command may run (docs/voice.md 10.3), and how
// long a process group gets between SIGTERM and SIGKILL.
type Timeouts struct {
	Version, Check, Voices, Synth, Grace time.Duration
}

// DefaultTimeouts are the protocol's.
var DefaultTimeouts = Timeouts{
	Version: 2 * time.Second,
	Check:   5 * time.Second,
	Voices:  5 * time.Second,
	Synth:   3 * time.Second,
	Grace:   time.Second,
}

func (t Timeouts) orDefault() Timeouts {
	d := DefaultTimeouts
	for _, p := range []struct{ v, def *time.Duration }{{&t.Version, &d.Version}, {&t.Check, &d.Check}, {&t.Voices, &d.Voices}, {&t.Synth, &d.Synth}, {&t.Grace, &d.Grace}} {
		if *p.v <= 0 {
			*p.v = *p.def
		}
	}
	return t
}

// Backend is one backend ready to be called: the executable and the
// arguments before the protocol's (the bundled say: pekoe and
// "voice-backend-say").
type Backend struct {
	Name   string
	Path   string   // absolute (a ~/ path already expanded)
	Prefix []string // arguments before the subcommand
	// Dir is the working directory: pekoe's data directory.
	Dir      string
	Timeouts Timeouts
}

// Error is a backend that could not be used or a call that failed.
type Error struct {
	Backend string
	Op      string // "path", "version", "check", "voices", "synth"
	Reason  string // one line: the backend's stderr, or what pekoe saw
	Timeout bool
}

func (e *Error) Error() string {
	s := "voice backend " + e.Backend
	if e.Op != "" {
		s += " " + e.Op
	}
	return s + ": " + e.Reason
}

var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// ValidName reports whether name may be a [voice.backends] key: lower-case
// letters, digits and "-".
func ValidName(name string) bool { return nameRe.MatchString(name) }

// ExpandPath turns a [voice.backends] path into an absolute one: an absolute
// path stays (cleaned), "~/…" is under the home directory. Anything else (a
// relative path, "~user/…", "~", "") is refused.
func ExpandPath(p string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("the path is empty")
	case strings.HasPrefix(p, "~/"):
		h, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(h) {
			return "", errors.New("no home directory to expand ~/ in")
		}
		return filepath.Join(h, p[2:]), nil
	case strings.HasPrefix(p, "~"):
		return "", fmt.Errorf("%s: only ~/ is expanded (not ~user/)", p)
	case !filepath.IsAbs(p):
		return "", fmt.Errorf("%s: not an absolute path (PATH is not searched)", p)
	}
	return filepath.Clean(p), nil
}

// checkExecutable: path (or, for a symbolic link, what it points at) is a
// regular file someone may execute.
func checkExecutable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: no such file", path)
		}
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s: not executable", path)
	}
	return nil
}

// Check makes sure the backend's executable is still there and executable
// (it is checked again before every call).
func (b *Backend) Check() error {
	if !filepath.IsAbs(b.Path) {
		return &Error{Backend: b.Name, Op: "path", Reason: b.Path + ": not an absolute path"}
	}
	if err := checkExecutable(b.Path); err != nil {
		return &Error{Backend: b.Name, Op: "path", Reason: err.Error()}
	}
	return nil
}

// Env is the environment a backend runs with: base (pekoed's) without
// PATH and PEKOE_VOICE_PROTOCOL, then PEKOE_VOICE_PROTOCOL=1 and a PATH of
// the system directories and the backend's own directory only.
func Env(base []string, exe string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "PEKOE_VOICE_PROTOCOL" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PEKOE_VOICE_PROTOCOL="+Protocol, "PATH="+SystemPATH+":"+filepath.Dir(exe))
}

// run starts the backend with args, stdin as its input, and returns what it
// printed. A non-zero exit, a timeout or ctx ending is an *Error whose reason
// is the first line of stderr.
func (b *Backend) run(ctx context.Context, op string, timeout time.Duration, stdin io.Reader, args ...string) ([]byte, error) {
	if err := b.Check(); err != nil {
		e := err.(*Error)
		e.Op = op + " (path)"
		return nil, e
	}
	if b.Dir != "" {
		if err := os.MkdirAll(b.Dir, 0o700); err != nil {
			return nil, &Error{Backend: b.Name, Op: op, Reason: err.Error()}
		}
	}
	grace := b.Timeouts.orDefault().Grace
	cmd := exec.Command(b.Path, append(append([]string(nil), b.Prefix...), args...)...)
	cmd.Dir = b.Dir
	cmd.Env = Env(os.Environ(), b.Path)
	cmd.Stdin = stdin
	var stdout limitedBuffer
	stdout.max = maxStdout
	var stderr limitedBuffer
	stderr.max = maxStderr
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// its own process group, so a timeout stops what it started too
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// a grandchild that left the group and kept stdout open must not hold
	// Wait forever
	cmd.WaitDelay = grace
	if err := cmd.Start(); err != nil {
		return nil, &Error{Backend: b.Name, Op: op, Reason: err.Error()}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var werr error
	timedOut := false
	select {
	case werr = <-done:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
		timedOut = true
	}
	if timedOut {
		stopGroup(cmd.Process.Pid, done, grace)
		reason := fmt.Sprintf("timed out after %s", timeout)
		if ctx.Err() != nil {
			reason = ctx.Err().Error()
		}
		return nil, &Error{Backend: b.Name, Op: op, Reason: reason, Timeout: true}
	}
	if werr != nil {
		reason := firstLine(stderr.String())
		if reason == "" {
			reason = werr.Error()
		}
		return nil, &Error{Backend: b.Name, Op: op, Reason: reason}
	}
	if stdout.over {
		return nil, &Error{Backend: b.Name, Op: op, Reason: fmt.Sprintf("printed more than %d bytes", maxStdout)}
	}
	return stdout.Bytes(), nil
}

// stopGroup sends SIGTERM to the process group, and SIGKILL when it has not
// ended after grace; it returns once the leader has been waited for.
func stopGroup(pid int, done <-chan error, grace time.Duration) {
	syscall.Kill(-pid, syscall.SIGTERM)
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-done:
		// the leader is gone; the rest of the group may not be
		syscall.Kill(-pid, syscall.SIGKILL)
		return
	case <-t.C:
	}
	syscall.Kill(-pid, syscall.SIGKILL)
	<-done
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.ToValidUTF8(s, "?"))
	return s
}

// limitedBuffer keeps the first max bytes and drops the rest (the backend
// is not blocked on a full pipe).
type limitedBuffer struct {
	bytes.Buffer
	max  int
	over bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.Len(); room > 0 {
		if len(p) > room {
			l.Buffer.Write(p[:room])
			l.over = true
		} else {
			l.Buffer.Write(p)
		}
	} else if len(p) > 0 {
		l.over = true
	}
	return len(p), nil
}

// Version runs `version` and returns what it printed (trimmed). A backend
// that does not print Protocol is an error: pekoe does not use it.
func (b *Backend) Version(ctx context.Context) (string, error) {
	out, err := b.run(ctx, "version", b.Timeouts.orDefault().Version, nil, "version")
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(out))
	if v != Protocol {
		return v, &Error{Backend: b.Name, Op: "version", Reason: fmt.Sprintf("speaks protocol %q, pekoe speaks %q", firstLine(v), Protocol)}
	}
	return v, nil
}

// CheckUsable runs `check`: nil when the backend says it can be used.
func (b *Backend) CheckUsable(ctx context.Context) error {
	_, err := b.run(ctx, "check", b.Timeouts.orDefault().Check, nil, "check")
	return err
}

// Voice is one entry of `voices`.
type Voice struct {
	Name  string `json:"name"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
}

// Voices runs `voices` and reads its JSON.
func (b *Backend) Voices(ctx context.Context) ([]Voice, error) {
	out, err := b.run(ctx, "voices", b.Timeouts.orDefault().Voices, nil, "voices")
	if err != nil {
		return nil, err
	}
	var vs []Voice
	if err := json.Unmarshal(out, &vs); err != nil {
		return nil, &Error{Backend: b.Name, Op: "voices", Reason: "not a JSON list of voices: " + err.Error()}
	}
	return vs, nil
}

// Request is one sentence to synthesize.
type Request struct {
	Text  string
	Voice string // logical: "tapi" (narrate) or "reader" (read)
	Lang  string // "ja" or "en"
}

// CleanText makes text what synth takes on stdin: valid UTF-8, one line,
// at most MaxText bytes (cut on a rune boundary).
func CleanText(text string) string {
	text = strings.ToValidUTF8(text, "")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > MaxText {
		cut := MaxText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}

// Synth runs `synth` writing to out (a file that does not exist yet, in an
// empty directory), and checks what it wrote: there, not empty, a RIFF file.
func (b *Backend) Synth(ctx context.Context, r Request, out string) error {
	text := CleanText(r.Text)
	if text == "" {
		return &Error{Backend: b.Name, Op: "synth", Reason: "no text"}
	}
	if _, err := b.run(ctx, "synth", b.Timeouts.orDefault().Synth, strings.NewReader(text), "synth", "--voice", r.Voice, "--lang", r.Lang, "--out", out); err != nil {
		return err
	}
	return checkWAV(b.Name, out)
}

func checkWAV(name, path string) error {
	bad := func(why string) error { return &Error{Backend: name, Op: "synth", Reason: why} }
	fi, err := os.Lstat(path)
	if err != nil {
		return bad("exited 0 but wrote no file")
	}
	if !fi.Mode().IsRegular() {
		return bad("the output is not a regular file")
	}
	if fi.Size() == 0 {
		return bad("wrote an empty file")
	}
	f, err := os.Open(path)
	if err != nil {
		return bad(err.Error())
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil || string(head) != "RIFF" {
		return bad("the output is not a WAV (RIFF) file")
	}
	return nil
}
