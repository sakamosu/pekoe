package daemon

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

// ---- input ----

type sendP struct {
	Slug   string `json:"slug"`
	Text   string `json:"text"`
	Submit bool   `json:"submit"`
	Window string `json:"window"`
}

func win(w string) string {
	if w == "" {
		return engine.WinAgent
	}
	return w
}

func (d *Daemon) hSend(p sendP) (any, error) { return d.send(p, false) }

// session.send without the human token (hSendAs, after authorizeSend)
// is send(p, true). The prompt check is repeated right before
// typing, under the session's send lock: authorizeSend's check ran before
// the request was dispatched, and an agent can time its own permission
// dialog to show up in between, where the text's first character would
// pick a choice ("2" is "Yes, and don't ask again").

func (d *Daemon) send(p sendP, agent bool) (any, error) {
	if _, err := d.get(p.Slug); err != nil {
		return nil, err
	}
	// One send per session at a time, so each text is typed right after
	// its own check, not after a check that a parallel send's wait made
	// stale.
	mu, _ := d.sendLocks.LoadOrStore(p.Slug, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	// An instruction (submit) never goes into a permission prompt: its
	// first character could pick a choice, its Enter the highlighted "Yes".
	// Checked before typing and again right before Enter. A plain paste
	// (no submit: the TUI forwarding the user's own typing) is not held,
	// unless it comes without the human token (agent).
	toAgent := win(p.Window) == engine.WinAgent
	guard := p.Submit && toAgent
	if guard || (agent && toAgent) {
		why := errPromptUp
		if agent {
			why = errPromptUpTUI
		}
		if err := d.sendGuard(p.Slug, why); err != nil {
			return nil, err
		}
	}
	if err := d.Eng.SendText(p.Slug, win(p.Window), p.Text); err != nil {
		return nil, err
	}
	if p.Submit {
		// give the agent's input box a moment to take the paste
		time.Sleep(150 * time.Millisecond)
		if guard {
			if err := d.sendGuard(p.Slug, errPromptUpTyped); err != nil {
				return nil, err
			}
		}
		if err := d.Eng.SendKeys(p.Slug, win(p.Window), "Enter"); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// sendGuard refuses with why (wrapped, see promptUpErr) while slug's agent
// shows a permission prompt.
func (d *Daemon) sendGuard(slug string, why error) error {
	s, err := d.snapshot(slug)
	if err != nil {
		return err
	}
	if d.promptUp(slug, s.State, s.WaitKind) {
		return promptUpErr(slug, why)
	}
	return nil
}

// A send held back by a permission prompt says which session waits and
// what happened to the text; one wording for every caller (human.go's
// authorizeSend too). errors.Is finds the reason inside.
var (
	errPromptUp      = errors.New("answer it first, then send (nothing was sent)")
	errPromptUpTUI   = errors.New("answer it in the pekoe TUI first, then send (nothing was sent)")
	errPromptUpTyped = errors.New("the text was typed but not submitted (Enter would have answered it)")
)

func promptUpErr(slug string, why error) error {
	return fmt.Errorf("%s is waiting for a permission answer: %w", slug, why)
}

var (
	dialogAskRe = regexp.MustCompile(`(?i)\bdo you want to\b`)
	dialogYesRe = regexp.MustCompile(`(?m)^[\s│|]*(?:[❯›>]\s*)?1[.)]\s+Yes(?:[\s│|]*$|,)`)
	dialogYNRe  = regexp.MustCompile(`(?i)(\[y/n\]|\(y/n\))\s*$`)
)

// promptUp reports whether Enter sent to slug's agent window now would
// answer a permission prompt: one the hooks reported (a question is fine:
// it is answered with words), or one already on screen that the hook has
// not reported yet (the agent asks between send's state check and its
// Enter). The screen check is the bottom of the screen: Claude Code's
// "Do you want to …" with a "1. Yes" choice, or a "[y/n]" last line.
func (d *Daemon) promptUp(slug string, st state.State, waitKind string) bool {
	if st == state.Waiting && waitKind != "question" {
		return true
	}
	sc, err := d.Eng.Capture(slug, engine.WinAgent)
	return err == nil && permissionOnScreen(sc.Lines)
}

func permissionOnScreen(lines []string) bool {
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return false
	}
	if dialogYNRe.MatchString(stripANSI(lines[len(lines)-1])) {
		return true
	}
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	text := stripANSI(strings.Join(lines, "\n"))
	return dialogAskRe.MatchString(text) && dialogYesRe.MatchString(text)
}

type keysP struct {
	Slug    string   `json:"slug"`
	Window  string   `json:"window"`
	Keys    []string `json:"keys"`
	Literal string   `json:"literal"`
	Hex     []string `json:"hex"`
}

func (d *Daemon) hKeys(p keysP) (any, error) {
	if p.Slug == TapiSlug { // tapi chat has its own methods (tapi.keys)
		return nil, fmt.Errorf("no such session: %s", p.Slug)
	}
	return d.sendKeys(p)
}

func (d *Daemon) sendKeys(p keysP) (any, error) {
	w := win(p.Window)
	if p.Literal != "" {
		if err := d.Eng.SendLiteral(p.Slug, w, p.Literal); err != nil {
			return nil, err
		}
	}
	if len(p.Hex) > 0 {
		if err := d.Eng.SendHex(p.Slug, w, p.Hex); err != nil {
			return nil, err
		}
	}
	if len(p.Keys) > 0 {
		if err := d.Eng.SendKeys(p.Slug, w, p.Keys...); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type answerP struct {
	Slug   string `json:"slug"`
	Answer string `json:"answer"` // allow | deny | always | y | n
	// Expect, when given, is the prompt the user was shown: the answer is
	// refused (errPromptChanged) unless the session still waits on that
	// tool. The web UI always sends it (web.go).
	Expect *answerExpect `json:"expect,omitempty"`
}

type answerExpect struct {
	Tool *state.Tool `json:"tool"`
}

var errPromptChanged = errors.New("the prompt changed; look again")

func (d *Daemon) sendKeySpec(slug, spec string) error {
	if spec == "" {
		return nil
	}
	if len([]rune(spec)) == 1 {
		return d.Eng.SendLiteral(slug, engine.WinAgent, spec)
	}
	return d.Eng.SendKeys(slug, engine.WinAgent, spec)
}

func (d *Daemon) hAnswer(p answerP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.State != state.Waiting {
		// never type a stray "1" into the agent's prompt
		return nil, fmt.Errorf("%s is not waiting (state: %s)", p.Slug, s.State)
	}
	if p.Expect != nil && !sameTool(s.PendingTool, p.Expect.Tool) {
		return nil, errPromptChanged
	}
	ag := d.cfg().Agents[s.Agent]
	var spec string
	switch p.Answer {
	case "allow", "y", "yes":
		spec = ag.Allow
	case "always", "a", "A":
		spec = ag.Always
	case "deny", "n", "no":
		spec = ag.Deny
	default:
		return nil, fmt.Errorf("answer must be allow, deny or always")
	}
	if s.PendingTool != nil && s.PendingTool.Name == "Trust" {
		// folder trust menu: the cursor starts on "No, exit"
		switch spec {
		case ag.Allow, ag.Always:
			err = d.Eng.SendKeys(p.Slug, engine.WinAgent, "Down", "Enter")
		default:
			err = d.Eng.SendKeys(p.Slug, engine.WinAgent, "Escape")
		}
	} else {
		err = d.sendKeySpec(p.Slug, spec)
	}
	if err != nil {
		return nil, err
	}
	next := state.Running
	if p.Answer == "deny" || p.Answer == "n" || p.Answer == "no" {
		next = state.Idle
	}
	if s.PendingTool != nil && s.PendingTool.Name == "Trust" {
		next = state.Idle // UserPromptSubmit moves it to running if a prompt follows
	}
	return d.update(p.Slug, func(x *state.Session) {
		if x.State == state.Waiting {
			x.State = next
			x.WaitKind = ""
			x.PendingTool = nil
		}
	})
}

type screenP struct {
	Slug   string `json:"slug"`
	Window string `json:"window"`
}

func (d *Daemon) hScreen(p screenP) (any, error) {
	if _, err := d.get(p.Slug); err != nil {
		return nil, err
	}
	return d.Eng.Capture(p.Slug, win(p.Window))
}
