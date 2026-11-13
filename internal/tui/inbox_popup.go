package tui

// The inbox popup ([orchestrator] inbox_popup, on by default): a new
// question an agent waits on (inboxHoldsUp) comes up in front as the
// answer dialog, so it can be answered without going to ]a. It must not
// catch keys meant for something else — answering "merge" makes pekoed
// merge a PR — so:
//   - it waits while another modal, the review surface or the tour is up,
//     and until no key has come for popupIdle. The TUI cannot see an IME's
//     preedit (the terminal sends nothing until it commits), so a pause in
//     the keys is how "not typing / not converting" is told
//   - for popupGrace after it shows, every key is dropped
//   - "skip" is chosen first: Enter alone skips; Esc skips too. A key the
//     dialog does not know (typing meant for somewhere else) goes back to
//     skip and waits popupGrace again, and Enter within popupConfirm of
//     choosing ("1⏎" typed in one go for an agent's own prompt) asks for
//     Enter again
//   - it does not come up while the terminal is not focused, and the
//     pause counts from when focus comes back
// One at a time, oldest first. Each comes up once: skipped (or answered
// elsewhere, or there when the TUI started) it stays in the inbox for ]a
// and the bar, and never pops up again. notify = "quiet" pops nothing.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/state"
)

const (
	popupGrace   = time.Second
	popupIdle    = 2 * time.Second
	popupConfirm = 500 * time.Millisecond
)

func (m *model) inboxPopupOn() bool {
	return m.g.Orchestrator.InboxPopupOn() && !m.quietNotify()
}

// queueInboxPopup: it was just seen for the first time (onInboxEvent).
func (m *model) queueInboxPopup(it state.InboxItem) {
	if !inboxHoldsUp(it) || !m.inboxPopupOn() {
		return
	}
	if m.popupDue == nil {
		m.popupDue = map[int64]bool{}
	}
	m.popupDue[it.ID] = true
}

// maybeInboxPopup puts the oldest due question in front when nothing else
// is and the keys have paused (called every tick).
func (m *model) maybeInboxPopup(now time.Time) tea.Cmd {
	if len(m.popupDue) == 0 {
		return nil
	}
	if !m.inboxPopupOn() {
		m.popupDue = nil // turned off: what was due stays in the inbox only
		return nil
	}
	if m.modal != nil || m.review != nil || m.tour != nil || (m.tapi != nil && !m.tapi.focused) || now.Sub(m.lastKeyAt) < popupIdle {
		return nil
	}
	var pick *state.InboxItem
	open := map[int64]bool{}
	for i := range m.inbox { // oldest first
		it := &m.inbox[i]
		open[it.ID] = true
		if pick == nil && m.popupDue[it.ID] && inboxHoldsUp(*it) {
			pick = it
		}
	}
	for id := range m.popupDue {
		if !open[id] { // answered or withdrawn meanwhile
			delete(m.popupDue, id)
		}
	}
	if pick == nil {
		return nil
	}
	delete(m.popupDue, pick.ID)
	im := &inboxModal{m: m, it: *pick, sel: -1, note: &input{}, opened: true, popup: true, shownAt: now}
	m.modal = im
	return m.openInboxRefresh(im) // the facts as of now
}
