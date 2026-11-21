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
//
// With no Stop, the state would stay running until the next prompt: pekoed
// would hold its deliveries to tapi (deliverHeld: busy), and a session would
// keep its place under max_running and its Interrupt button. So after the
// Escape pekoed reads the screen for a while (settleInterrupt) and, once the
// turn is gone from it, moves running to idle (Claude Code's input box,
// "What should Claude do instead?"), as the idle_prompt Notification would.
// The Escape counts as a person's typing (noteTyped): pekoed's deliveries
// wait as long after it as after a key, not to start the agent again at once.

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
// it never lands between a send's paste and its Enter (session.send's and
// tapi.send's: tapiSend takes TapiSlug's).
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
	turn := d.interruptTurn(slug)
	if err := d.Eng.SendKeys(slug, engine.WinAgent, "Escape"); err != nil {
		return err
	}
	// a person's key, as the TUI's esc through tapi.keys / session.keys:
	// no delivery right into the input box they just stopped (inboxTypedQuiet)
	d.noteTyped(slug)
	go d.settleInterrupt(slug, turn)
	return nil
}

// How settleInterrupt reads the screen after an Escape: every
// interruptPoll, for interruptSettle at most (Claude Code redraws in a
// moment; an Escape it did not take leaves the spinner, and the state as it
// is).
const (
	interruptPoll   = 250 * time.Millisecond
	interruptSettle = 10 * time.Second
)

// interruptTurn: the count of slug's hooks that show it at work
// (outState.turn, tapiState.gen), to tell a hook that came after the
// Escape.
func (d *Daemon) interruptTurn(slug string) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if slug == TapiSlug {
		return d.tapi.gen
	}
	if st := d.out[slug]; st != nil {
		return st.turn
	}
	return 0
}

// settleInterrupt moves slug from running to idle once its screen shows
// the interrupted turn is over: no spinner line, no menu, no permission
// prompt (those are the hooks' to report). Only while the state is still
// the running the Escape left and no hook said the agent works again
// (turn: interruptTurn before the Escape); a Stop, a permission prompt or
// the next prompt win.
func (d *Daemon) settleInterrupt(slug string, turn uint64) {
	still := func(st state.State) bool { return st == state.Running && d.interruptTurn(slug) == turn }
	deadline := time.Now().Add(interruptSettle)
	for time.Now().Before(deadline) {
		select {
		case <-d.stop:
			return
		case <-time.After(interruptPoll):
		}
		var st state.State
		if slug == TapiSlug {
			st, _ = d.tapiNow()
		} else {
			s, err := d.snapshot(slug)
			if err != nil {
				return
			}
			st = s.State
		}
		if !still(st) || !d.Eng.Alive(slug, engine.WinAgent) {
			return
		}
		sc, err := d.Eng.Capture(slug, engine.WinAgent)
		if err != nil {
			return
		}
		if turnGoesOn(sc.Lines) {
			continue
		}
		if slug == TapiSlug {
			d.tapiUpdate(func(t *tapiState) {
				if t.state == state.Running && t.gen == turn {
					t.state, t.waitKind, t.pending = state.Idle, "", nil
				}
			})
			return
		}
		d.update(slug, func(x *state.Session) {
			o := d.out[slug] // update holds d.mu
			if x.State == state.Running && (o == nil || o.turn == turn) {
				x.State, x.WaitKind, x.PendingTool = state.Idle, "", nil
			}
		})
		return
	}
}

// turnGoesOn: the bottom of the screen still shows the turn, a menu or a
// permission prompt (anything but Claude Code's input box after a turn).
func turnGoesOn(lines []string) bool {
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	return busyRe.MatchString(stripANSI(strings.Join(lines, "\n"))) || permissionOnScreen(lines)
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
