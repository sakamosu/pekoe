package daemon

// The ledger (docs/orchestration.md "6. 依頼(ledger)"): every prompt a person
// types into tapi chat is recorded open, from the UserPromptSubmit hook of
// tapi's own window (never from what a caller claims to be: an entry is
// what tapi reads back as "the person asked for this"), and followed until
// tapi closes it — done with evidence, dropped with a reason, or chat for
// what was not a request. Sessions are tied to an entry with
// `pekoe new --request` or ledger.link. The ledger outlives tapi's
// conversation (tapi.reset leaves it), so a fresh tapi starts from what is
// still open. Without the human token only tapi chat reads and writes it,
// and only a person reopens or deletes an entry (tapi cannot erase what it
// let slip).
//
// pekoed tidies two things itself ("自動の整理"), by fixed rules and never
// by what the words say; whatever falls outside them stays open:
//   - the same text right after an open entry is folded into it
//     (recordRequest)
//   - when tapi's turn ends and it asked nobody for anything (and
//     started no wait), the turn's
//     input was no request: closed as chat, marked auto (tapiTurnEnd).
//     That is pekoed's guess, so tapi may undo it (ledger.keep, or tying
//     a session to the entry); what tapi or a person closed only a person
//     reopens.

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"pekoe/internal/state"
)

const (
	ledgerTextMax = 16000 // characters kept of a request
	ledgerNoteMax = 4000
)

// ledgerRepeatWithin: the same text this soon after an open entry is that
// entry sent again (a paste left in the input box), not a new one. A
// variable for the tests.
var ledgerRepeatWithin = time.Minute

// notTyped: a prompt that reaches UserPromptSubmit without the person
// typing it, so no request of theirs:
//   - "[pekoe …": what pekoed itself typed into tapi's input box (an inbox
//     answer, "[pekoe inbox #12] …")
//   - "<task-notification>…": Claude Code telling the agent a background
//     task (tapi's `pekoe wait`) finished, which it hands over as a new turn
//
// Only a prefix counts; the same words inside what a person typed stay.
func notTyped(prompt string) bool {
	p := strings.TrimSpace(prompt)
	return strings.HasPrefix(p, "[pekoe") || strings.HasPrefix(p, "<task-notification>")
}

// recordRequest puts what the person typed into tapi into the ledger and
// returns the entry it is in (nil: nothing recorded). folded: no new entry,
// the text repeats the newest one, which is open and not a minute old.
func (d *Daemon) recordRequest(prompt string) (e *state.LedgerEntry, folded bool) {
	text := cleanInbox(prompt, true)
	if text == "" || notTyped(text) {
		return nil, false
	}
	if r := []rune(text); len(r) > ledgerTextMax {
		text = string(r[:ledgerTextMax]) + "…"
	}
	now := time.Now()
	d.ledgerMu.Lock()
	last, _ := d.DB.LedgerLast()
	if last != nil && last.Status == state.LedgerOpen && now.Sub(last.At) <= ledgerRepeatWithin &&
		strings.TrimSpace(last.Text) == strings.TrimSpace(text) {
		last.Repeats++
		e, folded = last, true
		if err := d.DB.SaveLedger(e); err != nil {
			e = nil
		}
	} else {
		e = &state.LedgerEntry{At: now, Text: text, Status: state.LedgerOpen}
		if err := d.DB.AddLedger(e); err != nil {
			e = nil
		}
	}
	d.ledgerMu.Unlock()
	if e == nil {
		return nil, false
	}
	d.ledgerChanged(e)
	return e, folded
}

// ---- tapi's turn: closing what was no request ----
//
// The turn's state (tapiState.turnEntry, turnKeep) is in memory only: lost
// with tapi.reset or a restart of pekoed, and then nothing is closed.

// tapiPrompt is a UserPromptSubmit from tapi's window: the prompt goes
// into the ledger and becomes the input of tapi's turn, the one entry
// tapiTurnEnd may close. Whatever the turn's input was before is let go
// (it stays open): its turn never ended (interrupted), or two inputs share
// a turn. A prompt that is not recorded (notTyped) starts a turn with no
// input, so its Stop closes nothing. A folded repeat stays the turn's
// input only when it already was: one of an earlier turn was left open by
// that turn and is not closed by the turn of its repeat.
func (d *Daemon) tapiPrompt(prompt string) {
	d.mu.Lock()
	prev := d.tapi.turnEntry
	d.tapi.turnEntry = 0
	d.mu.Unlock()
	e, folded := d.recordRequest(prompt)
	if e == nil || folded && e.ID != prev {
		return
	}
	d.mu.Lock()
	d.tapi.turnEntry = e.ID
	d.mu.Unlock()
}

// tapiAsks are the calls by which tapi asks a session or a person for
// something (pekoe new, send, ask-human, land) or starts waiting for one
// (pekoe wait: "land it when the report comes"): a turn with one of them
// works on a request, so its input stays open. Small talk in a turn in
// which tapi starts its wait again stays open too; tapi closes that.
var tapiAsks = map[string]bool{"session.new": true, "session.send": true, "inbox.post": true, "land": true, "wait": true}

// noteTapiAsk remembers that tapi asked someone for something this turn:
// a call from tapi's window, with the token or not (tapi has none, but
// whatever runs there acts in tapi's turn). A call that is then refused
// counts too: tapi tried to. Only a Stop clears it (tapiTurnEnd), not the
// next prompt: an input added in the middle of a turn does not undo what
// tapi asked for before it.
func (d *Daemon) noteTapiAsk(who *Caller, method string) {
	if !tapiAsks[method] || who.Pane() != TapiSlug {
		return
	}
	d.mu.Lock()
	d.tapi.turnKeep = true
	d.mu.Unlock()
}

// tapiTurnEnd is a Stop from tapi's window: tapi finished its reply. The
// turn's input is closed as chat (auto) when it is still open, no session
// is tied to it and tapi neither asked anyone for anything this turn
// (tapiAsks) nor kept it (ledger.keep): small talk, thanks, a report, a question answered
// on the spot. Nothing else is ever closed here, so a request of an
// earlier turn stays until tapi or a person closes it.
func (d *Daemon) tapiTurnEnd() {
	d.mu.Lock()
	id, keep := d.tapi.turnEntry, d.tapi.turnKeep
	d.tapi.turnEntry, d.tapi.turnKeep = 0, false
	d.mu.Unlock()
	if id == 0 || keep {
		return
	}
	closed := false
	e, err := d.changeLedger(id, func(e *state.LedgerEntry) error {
		if e.Status != state.LedgerOpen || len(e.Sessions) > 0 {
			return nil
		}
		now := time.Now()
		e.Status, e.Auto, e.ClosedAt = state.LedgerChat, true, &now
		closed = true
		return nil
	})
	if err == nil && closed {
		d.ledgerChanged(e)
	}
}

// tapiTurnGone is a SessionStart or SessionEnd from tapi's window: the
// conversation started or ended, so whatever turn was running never ends.
// Its input is let go (it stays open), as with the next prompt.
func (d *Daemon) tapiTurnGone() {
	d.mu.Lock()
	d.tapi.turnEntry = 0
	d.mu.Unlock()
}

// autoClosed: pekoed closed e as chat (tapiTurnEnd). tapi may open it
// again; what tapi or a person closed only a person reopens.
func autoClosed(e *state.LedgerEntry) bool { return e.Status == state.LedgerChat && e.Auto }

func reopenLedger(e *state.LedgerEntry) { e.Status, e.Auto, e.ClosedAt = state.LedgerOpen, false, nil }

func (d *Daemon) ledgerChanged(e *state.LedgerEntry) {
	d.emit("ledger.updated", e)
	d.emit("tapi.updated", d.tapiView())
}

// ledgerAllowed: the ledger is tapi chat's. Without the human token only
// a caller in tapi's window may use it.
func ledgerAllowed(who *Caller, method string) error {
	if who.Human() || who.Pane() == TapiSlug {
		return nil
	}
	return fmt.Errorf("%s: the ledger holds what a person asked tapi chat for; only tapi chat (or a person) uses it, not %s%s", method, who.ID(), outsideHint(who))
}

// outsideHint explains "unknown" to an agent that may be tapi chat run
// outside its window (docs/tapi-chat.md「窓の外に出た会話」).
func outsideHint(who *Caller) string {
	if who.Human() || who.Pane() != "" {
		return ""
	}
	return ". A process in no pekoe window is unknown, whatever $PEKOE_SESSION says: if this is tapi chat, " +
		"Claude Code has moved its conversation out of tapi's window (a background session: ← on an empty prompt, /background). " +
		"Ask the person to stop that background session in Claude Code, then to quit Claude Code in tapi's window and restart it there (↵), or to reset tapi chat (T, pekoe tapi reset)"
}

func ledgerErr(id int64, err error) error {
	if errors.Is(err, state.ErrNoLedgerEntry) {
		return fmt.Errorf("no such ledger entry: #%d", id)
	}
	return err
}

type ledgerListP struct {
	Status string `json:"status"` // open (default) | all
	Since  int64  `json:"since"`
}

func (d *Daemon) hLedgerList(who *Caller, p ledgerListP) (any, error) {
	if err := ledgerAllowed(who, "ledger.list"); err != nil {
		return nil, err
	}
	switch p.Status {
	case "", state.LedgerOpen:
		return d.DB.LedgerList(false, p.Since)
	case "all":
		return d.DB.LedgerList(true, p.Since)
	}
	return nil, fmt.Errorf("ledger.list: status must be open or all (got %q)", p.Status)
}

type ledgerCloseP struct {
	ID       int64    `json:"id"`
	As       string   `json:"as"`
	Evidence []string `json:"evidence"`
	Note     string   `json:"note"`
}

// changeLedger changes entry id under ledgerMu with f, which may refuse,
// and saves it.
func (d *Daemon) changeLedger(id int64, f func(e *state.LedgerEntry) error) (*state.LedgerEntry, error) {
	d.ledgerMu.Lock()
	defer d.ledgerMu.Unlock()
	e, err := d.DB.LedgerGet(id)
	if err != nil {
		return nil, ledgerErr(id, err)
	}
	if err := f(e); err != nil {
		return nil, err
	}
	if err := d.DB.SaveLedger(e); err != nil {
		return nil, ledgerErr(id, err)
	}
	return e, nil
}

func notOpen(method string, e *state.LedgerEntry) error {
	if autoClosed(e) {
		return fmt.Errorf("%s: ledger #%d is chat already (pekoed closed it as no request when tapi's turn ended; pekoe ledger keep %d opens it again)", method, e.ID, e.ID)
	}
	return fmt.Errorf("%s: ledger #%d is %s already (a person can reopen it: pekoe ledger reopen %d)", method, e.ID, e.Status, e.ID)
}

func (d *Daemon) hLedgerClose(who *Caller, p ledgerCloseP) (any, error) {
	if err := ledgerAllowed(who, "ledger.close"); err != nil {
		return nil, err
	}
	switch p.As {
	case state.LedgerDone, state.LedgerDropped, state.LedgerChat:
	default:
		return nil, fmt.Errorf("ledger.close: as must be done, dropped or chat (got %q)", p.As)
	}
	note := cleanInbox(p.Note, true)
	if n := utf8.RuneCountInString(note); n > ledgerNoteMax {
		return nil, fmt.Errorf("ledger.close: the note is %d characters, at most %d", n, ledgerNoteMax)
	}
	if p.As == state.LedgerDropped && note == "" {
		return nil, fmt.Errorf("ledger.close: say why it was dropped (note)")
	}
	ev, err := cleanEvidence("ledger.close", p.Evidence)
	if err != nil {
		return nil, err
	}
	e, err := d.changeLedger(p.ID, func(e *state.LedgerEntry) error {
		if e.Status != state.LedgerOpen {
			return notOpen("ledger.close", e)
		}
		now := time.Now()
		e.Status, e.Note, e.ClosedAt = p.As, note, &now
		e.Evidence = append(e.Evidence, ev...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.ledgerChanged(e)
	return e, nil
}

type ledgerLinkP struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
}

func (d *Daemon) hLedgerLink(who *Caller, p ledgerLinkP) (any, error) {
	if err := ledgerAllowed(who, "ledger.link"); err != nil {
		return nil, err
	}
	if _, err := d.snapshot(p.Slug); err != nil {
		return nil, err
	}
	return d.linkLedger("ledger.link", p.ID, p.Slug)
}

// linkLedger ties session slug to open entry id, both ways (the entry's
// sessions, the session's request). An entry pekoed closed itself opens
// again: a session works on it, so it was a request.
func (d *Daemon) linkLedger(method string, id int64, slug string) (*state.LedgerEntry, error) {
	e, err := d.changeLedger(id, func(e *state.LedgerEntry) error {
		switch {
		case autoClosed(e):
			reopenLedger(e)
		case e.Status != state.LedgerOpen:
			return notOpen(method, e)
		}
		for _, s := range e.Sessions {
			if s == slug {
				return nil
			}
		}
		e.Sessions = append(e.Sessions, slug)
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.update(slug, func(s *state.Session) { s.Request = id })
	d.ledgerChanged(e)
	return e, nil
}

// checkRequest is session.new's --request, before the session is made:
// the caller may use the ledger and the entry is open (or pekoed closed it
// itself: linkLedger opens it again).
func (d *Daemon) checkRequest(who *Caller, id int64) error {
	if id == 0 {
		return nil
	}
	if id < 0 {
		return fmt.Errorf("session.new: bad request id %d", id)
	}
	if err := ledgerAllowed(who, "session.new --request"); err != nil {
		return err
	}
	e, err := d.DB.LedgerGet(id)
	if err != nil {
		return ledgerErr(id, err)
	}
	if e.Status != state.LedgerOpen && !autoClosed(e) {
		return notOpen("session.new --request", e)
	}
	return nil
}

type ledgerKeepP struct {
	ID int64 `json:"id"` // 0: the input of tapi's turn
}

// hLedgerKeep is tapi saying "this is a request": the input of its turn
// is not closed when the turn ends (for one it hands to nobody yet: later,
// after an answer, after other work), and an entry pekoed closed itself
// opens again. An open entry comes back as it is.
func (d *Daemon) hLedgerKeep(who *Caller, p ledgerKeepP) (any, error) {
	if err := ledgerAllowed(who, "ledger.keep"); err != nil {
		return nil, err
	}
	if p.ID < 0 {
		return nil, fmt.Errorf("ledger.keep: bad request id %d", p.ID)
	}
	id := p.ID
	d.mu.Lock()
	if id == 0 {
		id = d.tapi.turnEntry
	}
	if id != 0 && id == d.tapi.turnEntry {
		d.tapi.turnKeep = true
	}
	d.mu.Unlock()
	if id == 0 {
		return nil, errors.New("ledger.keep: this turn has no input pekoed would close (it closes only the input of the turn that just ended; what is open stays open). To open a request pekoed closed, name it: pekoe ledger keep <id>")
	}
	reopened := false
	e, err := d.changeLedger(id, func(e *state.LedgerEntry) error {
		switch {
		case autoClosed(e):
			reopenLedger(e)
			reopened = true
		case e.Status != state.LedgerOpen:
			return notOpen("ledger.keep", e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if reopened {
		d.ledgerChanged(e)
	}
	return e, nil
}

type ledgerIDP struct {
	ID int64 `json:"id"`
}

// hLedgerReopen opens a closed entry again (a person only).
func (d *Daemon) hLedgerReopen(p ledgerIDP) (any, error) {
	e, err := d.changeLedger(p.ID, func(e *state.LedgerEntry) error {
		if e.Status == state.LedgerOpen {
			return fmt.Errorf("ledger.reopen: ledger #%d is open", e.ID)
		}
		reopenLedger(e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.ledgerChanged(e)
	return e, nil
}

// hLedgerDelete removes an entry (a person only).
func (d *Daemon) hLedgerDelete(p ledgerIDP) (any, error) {
	d.ledgerMu.Lock()
	err := d.DB.DeleteLedger(p.ID)
	d.ledgerMu.Unlock()
	if err != nil {
		return nil, ledgerErr(p.ID, err)
	}
	d.emit("ledger.removed", map[string]int64{"id": p.ID})
	d.emit("tapi.updated", d.tapiView())
	return nil, nil
}
