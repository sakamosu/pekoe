package daemon

// Interrupting an agent: one Escape into its agent window, the key Claude
// Code stops its turn on (the TUI forwards esc while typing into the pty,
// pty.go ptyTypingKey; in tapi chat too). The local GUI has no raw keys
// (docs/gui-design.md 決まったこと 5): it asks for this named operation
// instead, through a confirm sheet. Human only (humanMethods): an agent must
// not stop another one's turn.
//
// Only while the agent works — the hooks say running and its screen shows
// the turn's spinner line ("esc to interrupt", turnOnScreen) — so the key
// cannot land on anything that takes Escape as an answer. The screen is
// needed too: Claude Code sends no Stop for a turn interrupted with esc, so
// after one Interrupt the hooks' state stays running until the next prompt.
// What it would land on otherwise:
//   - a permission prompt (by the hooks, or one already on the screen that
//     the hook has not reported: promptUp, as send): Escape there is the
//     agent's "No" — that is session.answer's deny, which shows the person
//     what was asked
//   - a question's menu: Escape backs out of it (the keypad has its own)
//   - an idle input box: Escape clears what is typed, and two in a row
//     open Claude Code's rewind menu
// and at most once every interruptEvery per agent, so a double press does
// not become that second Escape after the first one stopped the turn.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// interruptEvery: the least time between two interrupts of one agent.
const interruptEvery = 2 * time.Second

// errInterruptState: the agent is not in a state an Escape may go to
// (webErr answers it 409).
var errInterruptState = errors.New("nothing was sent")

type interrupts struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// take reserves slug's interrupt now, or says how long ago the last was.
func (i *interrupts) take(slug string, now time.Time) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.last == nil {
		i.last = map[string]time.Time{}
	}
	if t, ok := i.last[slug]; ok && now.Sub(t) < interruptEvery {
		return fmt.Errorf("%s was interrupted %s ago; look at the screen first: %w", slug, now.Sub(t).Round(100*time.Millisecond), errInterruptState)
	}
	i.last[slug] = now
	return nil
}

// turnRe: Claude Code's spinner line while a turn goes on (output.go's
// busyRe without the menus: "esc to cancel" and "enter to select" are
// dialogs Escape would answer).
var turnRe = regexp.MustCompile(`(?i)\besc to interrupt\b`)

// turnOnScreen: the bottom of the screen shows a turn going on and no menu.
func turnOnScreen(lines []string) bool {
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	text := stripANSI(strings.Join(lines, "\n"))
	return turnRe.MatchString(text) && !menuRe.MatchString(text)
}

// interruptCheck: slug's agent runs (by the hooks and on its screen), shows
// no permission prompt, and its window is alive.
func (d *Daemon) interruptCheck(slug string, st state.State, waitKind string, stopped bool) error {
	if stopped {
		return fmt.Errorf("%s is stopped: %w", slug, errInterruptState)
	}
	if !d.Eng.Alive(slug, engine.WinAgent) {
		return fmt.Errorf("%s's agent is not running (its window has exited): %w", slug, errInterruptState)
	}
	if d.promptUp(slug, st, waitKind) {
		return fmt.Errorf("%s is waiting for a permission answer: deny it instead (Escape would answer it): %w", slug, errInterruptState)
	}
	if st != state.Running {
		return fmt.Errorf("%s is not working (state: %s), there is nothing to interrupt: %w", slug, st, errInterruptState)
	}
	sc, err := d.Eng.Capture(slug, engine.WinAgent)
	if err != nil || !turnOnScreen(sc.Lines) {
		return fmt.Errorf("%s's screen shows no turn going on (no \"esc to interrupt\"; it may have stopped already): %w", slug, errInterruptState)
	}
	return nil
}

// sendInterrupt checks and sends the one Escape, under slug's send lock so
// it never lands between a send's paste and its Enter.
func (d *Daemon) sendInterrupt(slug string, now func() (state.State, string, bool)) error {
	mu, _ := d.sendLocks.LoadOrStore(slug, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	st, wk, stopped := now()
	if err := d.interruptCheck(slug, st, wk, stopped); err != nil {
		return err
	}
	if err := d.interrupted.take(slug, time.Now()); err != nil {
		return err
	}
	return d.Eng.SendKeys(slug, engine.WinAgent, "Escape")
}

// hInterrupt is session.interrupt: {slug}.
func (d *Daemon) hInterrupt(p slugP) (any, error) {
	if p.Slug == TapiSlug { // tapi chat has its own (tapi.interrupt)
		return nil, fmt.Errorf("no such session: %s", p.Slug)
	}
	if _, err := d.snapshot(p.Slug); err != nil {
		return nil, err
	}
	err := d.sendInterrupt(p.Slug, func() (state.State, string, bool) {
		s, err := d.snapshot(p.Slug)
		if err != nil {
			return state.Idle, "", true
		}
		return s.State, s.WaitKind, s.Stopped
	})
	if err != nil {
		return nil, err
	}
	return d.snapshot(p.Slug)
}

// hTapiInterrupt is tapi.interrupt: the same for tapi chat (the TUI's esc
// while typing into it).
func (d *Daemon) hTapiInterrupt(struct{}) (any, error) {
	if err := d.tapiNeedsWindow(); err != nil {
		return nil, err
	}
	err := d.sendInterrupt(TapiSlug, func() (state.State, string, bool) {
		st, wk := d.tapiNow()
		return st, wk, false
	})
	if err != nil {
		return nil, err
	}
	return d.tapiView(), nil
}
