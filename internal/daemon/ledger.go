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
//     reopens, and what a person reopened in the turn is not closed again
//     by the turn's end.

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"pekoe/internal/ids"
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

// ledgerAutoShown: how long an entry pekoed closed itself is still listed
// with the open ones (ledger.list with auto: `pekoe ledger`, the
// dashboard). Closing by itself is a guess, and tapi forgets `ledger keep`
// now and then: a request closed that way is seen by the next tapi (which
// reads the list first) and by the person, and tapi opens it again. A day
// covers a night's break and a reset the next morning; older ones are in
// `pekoe ledger --all`.
const ledgerAutoShown = 24 * time.Hour

// What pekoed closed itself is shown short: the newest ledgerAutoMax, each
// text cut to ledgerAutoText characters, the rest as a count. tapi reads
// the list at the start of every conversation and whenever it looks up
// an id, and the point of closing small talk is fewer tokens; the whole
// text is `pekoe ledger <id>`.
const (
	ledgerAutoMax  = 10
	ledgerAutoText = 200
)

// ledgerView splits the open entries from what pekoed closed itself lately
// and keeps the latter short.
func ledgerView(es []state.LedgerEntry) *state.LedgerView {
	v := &state.LedgerView{Open: []state.LedgerEntry{}, AutoClosed: []state.LedgerEntry{}}
	for _, e := range es {
		if e.Status == state.LedgerOpen {
			v.Open = append(v.Open, e)
		} else {
			v.AutoClosed = append(v.AutoClosed, e)
		}
	}
	if n := len(v.AutoClosed) - ledgerAutoMax; n > 0 {
		v.AutoClosed, v.AutoClosedMore = v.AutoClosed[n:], n // oldest first: the newest are last
	}
	for i := range v.AutoClosed {
		e := &v.AutoClosed[i]
		if r := []rune(e.Text); len(r) > ledgerAutoText {
			e.Text, e.Cut = string(r[:ledgerAutoText])+"…", true
		}
	}
	return v
}

// notTyped: a prompt that reaches UserPromptSubmit without the person
// typing it, so no request of theirs:
//   - "[pekoe …": what pekoed itself typed into tapi's input box (an inbox
//     answer, "[pekoe inbox q12] …")
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
// The turn's state (tapiState.turnOn, turnEntry, turnKeep, turnKept, the
// ended* fields, turnClosed) is in memory only: lost with tapi.reset or a
// restart of pekoed, and then nothing is closed.

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
	d.tapi.turnOn, d.tapi.turnEntry = true, 0
	d.tapi.ended, d.tapi.turnClosed = false, 0
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
// tapi asked for before it. Calls between turns do not count: what tapi
// left running in the background (`pekoe ask-human --wait` calls wait
// again every minute) says nothing about the next turn's input, and
// neither does such a call again that falls inside a turn: the CLI marks
// it (rewaitCall).
func (d *Daemon) noteTapiAsk(who *Caller, method string, params json.RawMessage) {
	if !tapiAsks[method] || who.Pane() != TapiSlug || method == "wait" && rewaitCall(params) {
		return
	}
	d.mu.Lock()
	closed := int64(0)
	if d.tapi.turnOn {
		d.tapi.turnKeep = true
		closed, d.tapi.turnClosed = d.tapi.turnClosed, 0
	}
	d.mu.Unlock()
	if closed != 0 {
		d.reopenAuto(closed)
	}
}

// reopenAuto opens id again if it is still as tapiTurnEnd closed it
// (chat, auto): not what tapi or a person has closed since.
func (d *Daemon) reopenAuto(id int64) {
	reopened := false
	e, err := d.changeLedger(id, func(e *state.LedgerEntry) error {
		if autoClosed(e) {
			reopenLedger(e)
			reopened = true
		}
		return nil
	})
	if err == nil && reopened {
		d.ledgerChanged(e)
	}
}

// rewaitCall: a wait the CLI calls again by itself, `pekoe ask-human
// --wait` every minute until the answer comes (rewait: true). Its first
// wait counts as any; the calls again only go on with it, often from the
// background long after the turn that started it. An older CLI does not
// mark them, and they count as before.
func rewaitCall(params json.RawMessage) bool {
	var p struct {
		Rewait bool `json:"rewait"`
	}
	return json.Unmarshal(params, &p) == nil && p.Rewait
}

// tapiTurnEnd is a Stop from tapi's window: tapi finished its reply. The
// turn's input is closed as chat (auto) when it is still open, no session
// is tied to it, tapi neither asked anyone for anything this turn
// (tapiAsks) nor kept it (ledger.keep), and no person reopened it during
// the turn (ledger.reopen): small talk, thanks, a report, a question
// answered on the spot. Nothing else is ever closed here, so a request of
// an earlier turn stays until tapi or a person closes it. A keep of an
// earlier turn's input that never ended (interrupted) keeps that entry
// only, not this turn's.
//
// The Stop may not be the end: Claude Code runs its Stop hooks side by
// side, and when another one blocks the stop (exit 2, decision "block")
// the turn goes on, to end with a Stop that says stop_hook_active. So the
// turn is kept as it was (tapiState.ended) until the next prompt, and a
// PreToolUse from tapi's window before that takes it up again
// (tapiTurnGoesOn): what tapi asks for or keeps then counts, and the next
// Stop decides again.
func (d *Daemon) tapiTurnEnd() {
	d.mu.Lock()
	id, keep, kept := d.tapi.turnEntry, d.tapi.turnKeep, d.tapi.turnKept
	d.tapi.turnOn, d.tapi.turnEntry, d.tapi.turnKeep, d.tapi.turnKept = false, 0, false, 0
	d.tapi.ended, d.tapi.endedEntry, d.tapi.endedKeep, d.tapi.endedKept, d.tapi.endedClosed = true, id, keep, kept, false
	d.tapi.turnClosed = 0
	d.mu.Unlock()
	if id == 0 || keep || kept == id {
		return
	}
	// closed: this Stop closed it; auto: an earlier Stop of this turn did
	// (the turn went on and ends again, and may go on once more)
	closed, auto := false, false
	e, err := d.changeLedger(id, func(e *state.LedgerEntry) error {
		if autoClosed(e) {
			auto = true
			return nil
		}
		if e.Status != state.LedgerOpen || len(e.Sessions) > 0 {
			return nil
		}
		now := time.Now()
		e.Status, e.Auto, e.ClosedAt = state.LedgerChat, true, &now
		closed = true
		return nil
	})
	if err != nil || !closed && !auto {
		return
	}
	if closed {
		d.ledgerChanged(e)
	}
	reopen := false
	d.mu.Lock()
	switch t := &d.tapi; {
	case t.ended && t.endedEntry == id:
		t.endedClosed = true
	case t.turnOn && t.turnEntry == id:
		// the turn went on while it was being closed: if it has asked
		// someone or kept it already, open it now, else when it does
		if t.turnKeep || t.turnKept == id {
			reopen = true
		} else {
			t.turnClosed = id
		}
	}
	d.mu.Unlock()
	if reopen {
		d.reopenAuto(id)
	}
}

// tapiTurnGoesOn is a PreToolUse from tapi's window. Right after a Stop
// (no prompt since) it means the turn did not end there (tapiTurnEnd):
// the turn is taken up again as that Stop left it, its input to be opened
// again if the turn asks someone for something (turnClosed). Within a
// turn it changes nothing.
func (d *Daemon) tapiTurnGoesOn() {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := &d.tapi
	if t.turnOn || !t.ended {
		return
	}
	t.turnOn, t.turnEntry, t.turnKeep, t.turnKept = true, t.endedEntry, t.endedKeep, t.endedKept
	if t.endedClosed {
		t.turnClosed = t.endedEntry
	}
	t.ended = false
}

// tapiTurnGone is a SessionStart or SessionEnd from tapi's window: the
// conversation started or ended (or was compacted), so pekoed cannot tell
// how the running turn goes on. Its input is let go (it stays open), as
// with the next prompt; what tapi asked for so far still counts.
func (d *Daemon) tapiTurnGone() {
	d.mu.Lock()
	d.tapi.turnEntry = 0
	d.tapi.ended = false
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
		return fmt.Errorf("no such request: %s", ids.Request(id))
	}
	return err
}

type ledgerListP struct {
	Status string `json:"status"` // open (default) | all
	Since  int64  `json:"since"`
	// Auto, with open: a LedgerView instead of a list, with what pekoed
	// closed itself in the last ledgerAutoShown (status chat, auto true)
	Auto bool `json:"auto"`
}

func (d *Daemon) hLedgerList(who *Caller, p ledgerListP) (any, error) {
	if err := ledgerAllowed(who, "ledger.list"); err != nil {
		return nil, err
	}
	switch p.Status {
	case "", state.LedgerOpen:
		if p.Auto {
			es, err := d.DB.LedgerOpenAndAuto(p.Since, time.Now().Add(-ledgerAutoShown))
			if err != nil {
				return nil, err
			}
			return ledgerView(es), nil
		}
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
		return fmt.Errorf("%s: request %s is chat already (pekoed closed it as no request when tapi's turn ended; pekoe ledger keep %s opens it again)", method, ids.Request(e.ID), ids.Request(e.ID))
	}
	return fmt.Errorf("%s: request %s is %s already (a person can reopen it: pekoe ledger reopen %s)", method, ids.Request(e.ID), e.Status, ids.Request(e.ID))
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
		// what was kept from before a reopen (ledger.reopen leaves it)
		// counts toward the same limit as one report's; an entry given
		// again is not added twice. Without new ones it always closes (an
		// entry over the limit from before this check stays closable)
		add := newEvidence(e.Evidence, ev)
		if len(add) > 0 && len(e.Evidence)+len(add) > reportEvidenceMax {
			return fmt.Errorf("ledger.close: request %s has %d evidence entries already (kept from before it was reopened) and at most %d in all; give at most %d new ones, or none", ids.Request(e.ID), len(e.Evidence), reportEvidenceMax, max(reportEvidenceMax-len(e.Evidence), 0))
		}
		now := time.Now()
		e.Status, e.Note, e.ClosedAt = p.As, note, &now
		e.Evidence = append(e.Evidence, add...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.ledgerChanged(e)
	return e, nil
}

// newEvidence is ev without the entries already in have (or twice in ev).
func newEvidence(have, ev []string) []string {
	seen := map[string]bool{}
	for _, s := range have {
		seen[s] = true
	}
	out := []string{}
	for _, s := range ev {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
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
	d.update(slug, func(s *state.Session) { linkRequest(s, id) })
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
		d.tapi.turnKept = id
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

// hLedgerReopen opens a closed entry again (a person only). When it is
// the input of tapi's running turn, the turn's end leaves it open: the
// person said it is a request, and pekoed's guess does not undo that.
func (d *Daemon) hLedgerReopen(p ledgerIDP) (any, error) {
	e, err := d.changeLedger(p.ID, func(e *state.LedgerEntry) error {
		if e.Status == state.LedgerOpen {
			return fmt.Errorf("ledger.reopen: request %s is open", ids.Request(e.ID))
		}
		reopenLedger(e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	if d.tapi.turnEntry == e.ID {
		d.tapi.turnKept = e.ID
	}
	d.mu.Unlock()
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

// linkRequest ties s to entry id after it was made: id becomes the
// request it works on now and, unless it made the session, one of the
// later ones. A session from before Origin was recorded takes the request
// it had as the one that made it (pekoe new --request was the way in).
func linkRequest(s *state.Session, id int64) {
	if s.Origin == 0 && len(s.Later) == 0 && s.Request != 0 {
		s.Origin = s.Request
	}
	s.Request = id
	if id == s.Origin || slices.Contains(s.Later, id) {
		return
	}
	s.Later = append(s.Later, id)
}

// originOf: the request that made s (Origin; for a session from before it
// was recorded and never linked since, Request).
func originOf(s *state.Session) int64 {
	if s.Origin == 0 && len(s.Later) == 0 {
		return s.Request
	}
	return s.Origin
}
