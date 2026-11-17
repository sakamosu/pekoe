package daemon

// tapi への配送 ([orchestrator] deliver = "idle"; docs/orchestration.md「3.
// tapi への配送」): for a tapi that does not wait (an agent without `pekoe
// wait`, or a wait it forgot to start again), pekoed types one line summing
// up what changed for tapi into tapi's input box when it is free. Off by
// default (U7): the line competes with a person typing and with permission
// prompts, and `pekoe wait` is the better way.
//
// What goes: the changes wait would give tapi's own wait (`--mine --on
// report,failed,waiting,ci`) and the inbox answers to tapi's questions. A
// change a wait of tapi's took (returned, or one blocked now would take) is
// not delivered: tapi would wake twice for it. The inbox answers share the
// delivery of deliverInbox (one line a time per recipient, the same checks):
// with deliver on, tapi's answers go in this line instead of their own.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"pekoe/internal/ids"
	"pekoe/internal/state"
	"pekoe/internal/untrusted"
)

const (
	// deliverEvery: at most one line per this long
	deliverEvery = 30 * time.Second
	// deliverPauseLease: how long one tapi.deliver_pause keeps the line
	// back (the TUI says it again while a person types into tapi chat; a
	// TUI that went away leaves no pause behind for long)
	deliverPauseLease = 20 * time.Second
	// deliverListed: the changes named in the line; more are counted
	deliverListed = 8
)

// deliverKinds are the changes of tapi's sessions that go (the inbox's go
// from the inbox itself: deliverInbox's pending answers).
var deliverKinds = map[string]bool{kindReport: true, kindFailed: true, kindWaiting: true, kindCI: true}

// The reasons a delivery waits (TapiDeliver.Held).
const (
	heldStopped  = "stopped"  // tapi's window is not there
	heldBusy     = "busy"     // tapi is not idle
	heldPrompt   = "prompt"   // a permission prompt is up
	heldTyping   = "typing"   // a person typed into tapi chat in the last 30 s
	heldChat     = "chat"     // a person has tapi chat open, typing (tapi.deliver_pause)
	heldInterval = "interval" // the last line went less than 30 s ago
)

// TapiDeliver is Tapi.deliver: the delivery's state.
type TapiDeliver struct {
	Mode      string     `json:"mode"`                 // off | idle
	Pending   int        `json:"pending"`              // changes and inbox answers that would go
	Held      string     `json:"held,omitempty"`       // why the last try sent nothing (pending > 0)
	LastAt    *time.Time `json:"last_at,omitempty"`    // the last line typed
	LastError string     `json:"last_error,omitempty"` // it did not go through
}

// deliverState is the delivery's memory (under d.mu).
type deliverState struct {
	lastAt     time.Time
	pauseUntil time.Time
	held       string
	lastErr    string
}

// forTapi: c is a change of tapi's own sessions that the delivery carries.
func forTapi(c Change) bool { return c.parent == TapiSlug && deliverKinds[c.Kind] }

// takeLocked marks changes as gone to tapi (delivered, or returned by a
// wait of tapi's).
func (l *changeLog) takeLocked(cs []Change) {
	for _, c := range cs {
		if c.Seq > l.delivAfter {
			l.taken[c.Seq] = true
		}
	}
	l.advanceLocked()
}

func (l *changeLog) take(cs []Change) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.takeLocked(cs)
}

// advanceLocked moves delivAfter over the changes that need nothing more:
// not tapi's, or taken. Changes that fell out of the ring are gone.
func (l *changeLog) advanceLocked() {
	if o := l.oldestLocked(); l.delivAfter < o-1 {
		l.delivAfter = o - 1
	}
	for _, c := range l.ring {
		if c.Seq <= l.delivAfter {
			continue
		}
		if forTapi(c) && !l.taken[c.Seq] {
			break
		}
		l.delivAfter = c.Seq
	}
	for s := range l.taken {
		if s <= l.delivAfter {
			delete(l.taken, s)
		}
	}
}

// deliverSkip: the delivery is off; what happened so far is not for later.
func (l *changeLog) deliverSkip() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.delivAfter = l.seq
	clear(l.taken)
}

// tapiPending is what would go to tapi now: tapi's changes not taken, and
// not one a wait of tapi's blocked now would take (it gets it). A change
// older than what that wait returns from (wait without --since: from when
// it started) is not one it takes, so it still goes.
func (l *changeLog) tapiPending() []Change {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.advanceLocked()
	var got []Change
	for _, c := range l.ring {
		if c.Seq <= l.delivAfter || !forTapi(c) || l.taken[c.Seq] {
			continue
		}
		watched := false
		for w := range l.waiters {
			if w.id == TapiSlug && w.takes(c) {
				watched = true
				break
			}
		}
		if !watched {
			got = append(got, c)
		}
	}
	return got
}

func (d *Daemon) deliverOn() bool { return d.cfg().Orchestrator.DeliverIdle() }

// deliverTapi tries a delivery (the tick, tapi going idle, a pause ended).
func (d *Daemon) deliverTapi() {
	if !d.deliverOn() {
		d.changes.deliverSkip()
		return
	}
	d.deliverInbox(TapiSlug)
}

// tapiInboxPending: the answers to tapi's questions not delivered yet,
// oldest first.
func (d *Daemon) tapiInboxPending() []state.InboxItem {
	all, err := d.DB.InboxList(state.InboxFilter{Delivery: state.DeliveryPending})
	if err != nil {
		return nil
	}
	var its []state.InboxItem
	for i := len(all) - 1; i >= 0; i-- {
		if inboxRecipient(&all[i]) == TapiSlug {
			its = append(its, all[i])
		}
	}
	return its
}

// deliverHeld says why no line may go now ("" it may).
func (d *Daemon) deliverHeld() string {
	if !d.tapiStarted() {
		return heldStopped
	}
	if d.typedRecently(TapiSlug) {
		return heldTyping
	}
	d.mu.Lock()
	st, wk := d.tapi.state, d.tapi.waitKind
	paused := time.Now().Before(d.deliv.pauseUntil)
	last := d.deliv.lastAt
	d.mu.Unlock()
	switch {
	case paused:
		return heldChat
	case st != state.Idle:
		return heldBusy
	case d.promptUp(TapiSlug, st, wk):
		return heldPrompt
	case !last.IsZero() && time.Since(last) < deliverEvery:
		return heldInterval
	}
	return ""
}

func (d *Daemon) setDeliverHeld(why string) {
	d.mu.Lock()
	changed := d.deliv.held != why
	d.deliv.held = why
	d.mu.Unlock()
	if changed {
		d.emit("tapi.updated", d.tapiView())
	}
}

// deliverTapiLocked types the line, if there is anything and tapi can take
// it. The caller holds tapi's delivery lock (deliverInbox).
func (d *Daemon) deliverTapiLocked() {
	cs := d.changes.tapiPending()
	its := d.tapiInboxPending()
	if len(cs)+len(its) == 0 {
		d.setDeliverHeld("")
		return
	}
	if why := d.deliverHeld(); why != "" {
		d.setDeliverHeld(why)
		return
	}
	line := deliverLine(cs, its, d.changes.epochNow())
	_, err := d.hTapiSend(tapiSendP{Text: line, Submit: true})
	if errors.Is(err, errPromptUp) {
		d.setDeliverHeld(heldPrompt) // a prompt came up before the text went: next time
		return
	}
	// errPromptUpTyped: the line is in the input box, not submitted; as
	// for an inbox answer it is not typed again, the log says so
	r := &state.SendRecord{At: time.Now(), From: callerPekoed, To: TapiSlug, Kind: "deliver", Text: line, Via: callerPekoed, OK: err == nil}
	if err != nil {
		r.Error = err.Error()
	}
	if e := d.DB.AddSend(r); e != nil {
		log.Printf("send log: %v", e)
	}
	d.mu.Lock()
	d.deliv.held, d.deliv.lastErr = "", ""
	if err != nil {
		d.deliv.lastErr = err.Error()
	}
	if err == nil || errors.Is(err, errPromptUpTyped) {
		d.deliv.lastAt = time.Now()
	}
	d.mu.Unlock()
	if err != nil && !errors.Is(err, errPromptUpTyped) {
		log.Printf("deliver to tapi: %v", err)
		d.emit("tapi.updated", d.tapiView())
		return
	}
	d.changes.take(cs)
	d.inboxMu.Lock()
	for i := range its {
		if ok, _ := d.DB.SetInboxDelivery(its[i].ID, state.DeliveryPending, state.DeliveryDelivered); ok {
			its[i].Delivery = state.DeliveryDelivered
			d.emit("inbox.updated", &its[i])
		}
	}
	d.inboxMu.Unlock()
	d.emit("tapi.updated", d.tapiView())
}

func (l *changeLog) epochNow() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.epoch
}

// deliverLine is the one line typed into tapi's input box. It starts with
// "[" (never a digit: in a permission menu a digit picks a choice) and,
// like inboxLine, "[pekoe" (the ledger does not take it for a request).
// Only pekoed's own words go in it: slugs, kinds, a report's status, red
// or green; never an agent's summary (tapi reads that with wait, as
// untrusted data).
func deliverLine(cs []Change, its []state.InboxItem, epoch string) string {
	var names []string
	for _, c := range cs {
		w := c.brief
		if w == "" {
			w = c.Kind
		}
		names = append(names, c.Slug+" "+w)
	}
	for _, it := range its {
		switch it.Status {
		case state.InboxDismissed:
			names = append(names, "受信箱 "+ids.Question(it.ID)+" は答えずに閉じられた")
		case state.InboxWithdrawn:
			names = append(names, "受信箱 "+ids.Question(it.ID)+" は人が取り下げた")
		default:
			names = append(names, "受信箱 "+ids.Question(it.ID)+" に答え")
		}
	}
	n := len(names)
	if n > deliverListed {
		names = append(names[:deliverListed], fmt.Sprintf("ほか %d 件", n-deliverListed))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[pekoe] %d 件の変化(%s)。詳しくは ", n, strings.Join(names, "、"))
	var more []string
	if len(cs) > 0 {
		more = append(more, fmt.Sprintf("pekoe --json wait --mine --since %s-%d", epoch, cs[0].Seq-1))
	}
	switch len(its) {
	case 0:
	case 1:
		more = append(more, "pekoe --json inbox "+ids.Question(its[0].ID))
	default:
		more = append(more, "pekoe --json inbox --mine --all")
	}
	b.WriteString(strings.Join(more, "、"))
	return untrusted.Line(b.String())
}

// tapiDeliverView is Tapi.deliver.
func (d *Daemon) tapiDeliverView() *TapiDeliver {
	if !d.deliverOn() {
		return &TapiDeliver{Mode: "off"}
	}
	v := &TapiDeliver{Mode: "idle", Pending: len(d.changes.tapiPending()) + len(d.tapiInboxPending())}
	d.mu.Lock()
	if v.Pending > 0 {
		v.Held = d.deliv.held
	}
	if t := d.deliv.lastAt; !t.IsZero() {
		v.LastAt = &t
	}
	v.LastError = d.deliv.lastErr
	d.mu.Unlock()
	return v
}

type deliverPauseP struct {
	On bool `json:"on"`
}

// hTapiDeliverPause: the TUI says a person has tapi chat open and types
// into it (on, again every few seconds while it lasts) or stopped (off).
// Human only: an agent must not keep the line back (nor let it go).
func (d *Daemon) hTapiDeliverPause(p deliverPauseP) (any, error) {
	d.mu.Lock()
	if p.On {
		d.deliv.pauseUntil = time.Now().Add(deliverPauseLease)
	} else {
		d.deliv.pauseUntil = time.Time{}
	}
	d.mu.Unlock()
	if !p.On {
		go d.deliverTapi()
	}
	return nil, nil
}
