package daemon

// The ledger (docs/orchestration.md "6. 依頼の台帳"): every prompt a person
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

// recordRequest puts what the person typed into tapi into the ledger.
func (d *Daemon) recordRequest(prompt string) {
	text := cleanInbox(prompt, true)
	if text == "" || notTyped(text) {
		return
	}
	if r := []rune(text); len(r) > ledgerTextMax {
		text = string(r[:ledgerTextMax]) + "…"
	}
	e := &state.LedgerEntry{At: time.Now(), Text: text, Status: state.LedgerOpen}
	if err := d.DB.AddLedger(e); err != nil {
		return
	}
	d.ledgerChanged(e)
}

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
	return fmt.Errorf("%s: the ledger holds what a person asked tapi chat for; only tapi chat (or a person) uses it, not %s", method, who.ID())
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
// sessions, the session's request).
func (d *Daemon) linkLedger(method string, id int64, slug string) (*state.LedgerEntry, error) {
	e, err := d.changeLedger(id, func(e *state.LedgerEntry) error {
		if e.Status != state.LedgerOpen {
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
// the caller may use the ledger and the entry is open.
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
	if e.Status != state.LedgerOpen {
		return notOpen("session.new --request", e)
	}
	return nil
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
		e.Status, e.ClosedAt = state.LedgerOpen, nil
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
