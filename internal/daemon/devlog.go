package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// The dev hook ([hooks] dev) runs in a tmux window of its own. What it
// prints also goes to a file, without the terminal's escape sequences, so
// an agent can read its own dev server's output (`pekoe dev --log`) and
// what pekoe did with it: started, ended (how), stopped, [hooks] dev_stop
// and its output. pekoed writes the file from the window's output stream:
// the hook still runs on a terminal (colours, Next.js's own output), and
// nothing in its sandbox has to write outside the worktree.
//
// Output printed while pekoed is down (between a stop and the next start)
// is not in the file; the window still has it.

// devLogMax: past this size the file moves to <file>.1 (one old file kept).
const devLogMax = 2 << 20

// devStopTimeout bounds [hooks] dev_stop (make down, docker compose down).
var devStopTimeout = 5 * time.Minute

// devLogs strips escape sequences from each session's dev output, keeping
// a sequence cut between two chunks for the next one.
type devLogs struct {
	mu sync.Mutex
	st map[string]*ansiStrip
}

func devLogPath(slug string) string {
	return filepath.Join(paths.DataDir(), "devlogs", slug+".log")
}

// writeDevLog appends the dev window's output (output loop).
func (d *Daemon) writeDevLog(slug string, data []byte) {
	d.devlog.mu.Lock()
	defer d.devlog.mu.Unlock()
	if d.devlog.st == nil {
		d.devlog.st = map[string]*ansiStrip{}
	}
	a := d.devlog.st[slug]
	if a == nil {
		a = &ansiStrip{}
		d.devlog.st[slug] = a
	}
	if b := a.feed(data); len(b) > 0 {
		appendDevLog(slug, b)
	}
}

// devLogNote appends one line of pekoe's own about the dev hook.
func (d *Daemon) devLogNote(slug, format string, args ...any) {
	line := fmt.Sprintf("[pekoe %s] ", time.Now().Format("2006-01-02 15:04:05")) + fmt.Sprintf(format, args...)
	d.devlog.mu.Lock()
	defer d.devlog.mu.Unlock()
	pre := ""
	if a := d.devlog.st[slug]; a != nil && a.midLine {
		pre = "\n" // the hook's last line had no newline yet
		a.midLine = false
	}
	appendDevLog(slug, []byte(pre+strings.TrimRight(line, "\n")+"\n"))
}

// forgetDevLog drops the session's log (rm).
func (d *Daemon) forgetDevLog(slug string) {
	d.devlog.mu.Lock()
	delete(d.devlog.st, slug)
	d.devlog.mu.Unlock()
	os.Remove(devLogPath(slug))
	os.Remove(devLogPath(slug) + ".1")
}

// appendDevLog writes b at the end of slug's log. Callers hold devlog.mu.
func appendDevLog(slug string, b []byte) {
	p := devLogPath(slug)
	if err := paths.Ensure(filepath.Dir(p)); err != nil {
		return
	}
	if fi, err := os.Stat(p); err == nil && fi.Size()+int64(len(b)) > devLogMax {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		logOnce("devlog:"+slug, "dev log %s: %v", p, err)
		return
	}
	_, _ = f.Write(b)
	f.Close()
}

// ansiStrip turns terminal output into plain lines: escape sequences
// (CSI, OSC, DCS …) dropped, \r\n and a lone \r (a progress line drawn
// over) made \n, other control characters dropped. Its state carries over
// between chunks.
type ansiStrip struct {
	st      int
	cr      bool
	midLine bool // the last byte written was not \n
}

const (
	asText = iota
	asEsc
	asCSI
	asStr    // OSC, DCS, APC, PM, SOS: up to BEL or ESC \
	asStrEsc // ESC inside a string
	asCharset
)

func (a *ansiStrip) feed(in []byte) []byte {
	var out bytes.Buffer
	emit := func(b byte) {
		out.WriteByte(b)
		a.midLine = b != '\n'
	}
	for _, b := range in {
		switch a.st {
		case asText:
			if a.cr {
				a.cr = false
				emit('\n')
				if b == '\n' {
					continue
				}
			}
			switch {
			case b == 0x1b:
				a.st = asEsc
			case b == '\r':
				a.cr = true
			case b == '\n' || b == '\t' || (b >= 0x20 && b != 0x7f):
				emit(b)
			}
		case asEsc:
			switch b {
			case '[':
				a.st = asCSI
			case ']', 'P', '_', '^', 'X':
				a.st = asStr
			case '(', ')', '*', '+', '#', '%':
				a.st = asCharset
			default:
				a.st = asText
			}
		case asCSI:
			if b >= 0x40 && b <= 0x7e {
				a.st = asText
			}
		case asStr:
			switch b {
			case 0x07:
				a.st = asText
			case 0x1b:
				a.st = asStrEsc
			}
		case asStrEsc:
			if b == '\\' {
				a.st = asText
			} else {
				a.st = asStr
			}
		case asCharset:
			a.st = asText
		}
	}
	return out.Bytes()
}

// ---- pekoe dev --log ----

type devLogP struct {
	Slug  string `json:"slug"`
	Lines int    `json:"lines"` // from the end; 0: devLogDefault
}

const (
	devLogDefault  = 100
	devLogMaxLines = 5000
)

// DevLog is what dev.log answers: the end of the dev hook's log and
// whether the hook is running or how it ended.
type DevLog struct {
	Slug    string         `json:"slug"`
	Path    string         `json:"path"`
	Running bool           `json:"running"`
	Exit    *state.DevExit `json:"exit,omitempty"`
	Lines   []string       `json:"lines"`
}

func (d *Daemon) hDevLog(p devLogP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	n := p.Lines
	if n <= 0 {
		n = devLogDefault
	}
	if n > devLogMaxLines {
		n = devLogMaxLines
	}
	path := devLogPath(s.Slug)
	res := DevLog{Slug: s.Slug, Path: path, Running: s.Dev, Exit: s.DevExit, Lines: []string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil // never started (or not since this version)
	}
	if err != nil {
		return nil, err
	}
	res.Lines = lastLines(string(b), n)
	return res, nil
}

// hAgentDevLog is dev.log without the human token: the caller's own
// session only, as dev.toggle (hAgentDevToggle). The log is the output of
// a command its own worktree runs; another session's may hold that
// session's secrets ([env], .env printed at start).
func (d *Daemon) hAgentDevLog(who *Caller, p devLogP) (any, error) {
	pane := who.Pane()
	switch {
	case pane == "":
		return nil, errUnknownCaller("dev.log")
	case pane == TapiSlug:
		return nil, fmt.Errorf("dev.log: tapi chat has no dev server")
	case p.Slug != "" && p.Slug != pane:
		return nil, fmt.Errorf("dev.log: a session reads only its own dev server's log (%s, not %s): %w", pane, p.Slug, ErrHumanOnly)
	}
	p.Slug = pane
	return d.hDevLog(p)
}

func lastLines(s string, n int) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return []string{}
	}
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return ls
}

// ---- the dev hook ending ----

// checkDevExits (tickLoop): a dev hook that ended by itself turns Dev
// false, with its exit status and the time pekoed saw it end. The window
// stays (remain-on-exit) so its last screen can be read.
func (d *Daemon) checkDevExits() {
	// a dev.toggle or rm holding it (dev_stop may take minutes) decides
	// itself; the next tick looks again instead of piling up behind it
	if !d.devMu.TryLock() {
		return
	}
	defer d.devMu.Unlock()
	d.mu.Lock()
	var devs []string
	for slug, s := range d.sessions {
		if s.Dev {
			devs = append(devs, slug)
		}
	}
	d.mu.Unlock()
	for _, slug := range devs {
		if d.Eng.Alive(slug, engine.WinDev) {
			continue
		}
		ex := d.deadDevExit(slug)
		d.devLogNote(slug, "dev hook ended by itself: %s", describeDevExit(ex))
		d.endDevWatch(slug)
		d.update(slug, func(x *state.Session) { x.Dev, x.DevExit = false, ex })
	}
}

// deadDevExit is how slug's dev hook, no longer running, ended: the dead
// window's exit status, unknown when the window is gone.
func (d *Daemon) deadDevExit(slug string) *state.DevExit {
	ex := &state.DevExit{Status: -1, At: time.Now()}
	if sc, err := d.Eng.Capture(slug, engine.WinDev); err == nil && sc.Dead {
		ex.Status, ex.Signal = sc.ExitStatus, sc.Signal
	}
	return ex
}

// exitStatusOf is a command's exit status from its error (-1: it did not
// exit by itself: not started, timed out, killed).
func exitStatusOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return ee.ExitCode()
	}
	return -1
}

// devWindowClosed: the dev window is gone (closed, or the session's
// windows killed), so the TUI no longer offers the last screen of a hook
// that ended.
func devWindowClosed(x *state.Session) {
	if x.DevExit != nil && !x.DevExit.Closed {
		c := *x.DevExit
		c.Closed = true
		x.DevExit = &c
	}
}

func describeDevExit(ex *state.DevExit) string {
	switch {
	case ex.Stopped:
		return "stopped by pekoe"
	case ex.Signal != "":
		return "killed by " + ex.Signal
	case ex.Status >= 0:
		return fmt.Sprintf("exit status %d", ex.Status)
	}
	return "exit status unknown (its window is gone)"
}

// ---- stopping the dev hook ----

// stopDev stops slug's dev hook if it is running and then runs [hooks]
// dev_stop. final (rm, a landed session's teardown, pekoed stopping its
// agents): dev_stop also runs for a hook that ended by itself and has had
// no dev_stop since, since what it started (compose's containers) may
// still be up. Callers hold devMu. It answers the dev_stop error, if any
// (logged in the dev log too).
func (d *Daemon) stopDev(slug string, final bool) error {
	s, err := d.snapshot(slug)
	if err != nil {
		return err
	}
	alive := d.Eng.Alive(slug, engine.WinDev)
	if alive {
		_ = d.Eng.Kill(slug, engine.WinDev)
		d.endDevWatch(slug)
		d.devLogNote(slug, "dev hook stopped by pekoe")
	}
	ex := s.DevExit
	switch {
	case alive:
		ex = &state.DevExit{Status: -1, Stopped: true, At: time.Now()}
	case s.Dev: // ended by itself, before checkDevExits saw it
		ex = d.deadDevExit(slug)
	}
	due := alive || s.Dev || (final && ex != nil && ex.DevStop == nil)
	var runErr error
	if due {
		if run := d.runDevStop(s); run != nil {
			c := *ex
			c.DevStop = run
			ex = &c
			if run.Error != "" {
				runErr = fmt.Errorf("[hooks] dev_stop %q: %s (log: %s)", run.Command, run.Error, devLogPath(slug))
			}
		}
	}
	if alive || s.Dev || ex != s.DevExit {
		d.update(slug, func(x *state.Session) { x.Dev, x.DevExit = false, ex })
	}
	return runErr
}

// runDevStop runs [hooks] dev_stop for s as the dev hook runs: in its
// worktree, with its environment, in pekoed's hooks sandbox. nil when
// there is none to run (no key, the worktree gone).
func (d *Daemon) runDevStop(s *state.Session) *state.HookRun {
	cfg := d.repoConfig(s)
	if strings.TrimSpace(cfg.Hooks.DevStop) == "" {
		return nil
	}
	if fi, err := os.Stat(s.Path); err != nil || !fi.IsDir() {
		d.devLogNote(s.Slug, "[hooks] dev_stop not run: the worktree %s is gone", s.Path)
		return nil
	}
	cmd := d.vars(s).ExpandShell(cfg.Hooks.DevStop)
	d.devLogNote(s.Slug, "[hooks] dev_stop: %s", cmd)
	env := append(append([]string{}, s.Env...), "PEKOE_SESSION="+s.Slug)
	watched := d.watchGit(s, "dev_stop")
	t0 := time.Now()
	out, err := d.shell(context.Background(), s.Path, env, cmd, devStopTimeout)
	watched()
	run := &state.HookRun{Command: cmd, Status: 0, At: time.Now()}
	if out != "" {
		d.devlog.mu.Lock()
		appendDevLog(s.Slug, []byte(strings.TrimRight(out, "\n")+"\n"))
		d.devlog.mu.Unlock()
	}
	if err != nil {
		run.Status, run.Error = exitStatusOf(err), err.Error()
		d.devLogNote(s.Slug, "[hooks] dev_stop failed after %s: %v", time.Since(t0).Round(100*time.Millisecond), err)
		log.Printf("dev_stop %s: %v", s.Slug, err)
	} else {
		d.devLogNote(s.Slug, "[hooks] dev_stop done (%s)", time.Since(t0).Round(100*time.Millisecond))
	}
	return run
}

// stopAllDevs is pekoed stopping its agents (daemon.stop kill_agents): each
// session's dev hook is stopped and its dev_stop run, all at once, before
// the tmux server goes.
func (d *Daemon) stopAllDevs() {
	d.devMu.Lock()
	defer d.devMu.Unlock()
	d.mu.Lock()
	var slugs []string
	for slug, s := range d.sessions {
		if s.State != state.Landed && (s.Dev || (s.DevExit != nil && s.DevExit.DevStop == nil)) {
			slugs = append(slugs, slug)
		}
	}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, slug := range slugs {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			if err := d.stopDev(slug, true); err != nil {
				log.Printf("stop %s: %v", slug, err)
			}
		}(slug)
	}
	wg.Wait()
}
