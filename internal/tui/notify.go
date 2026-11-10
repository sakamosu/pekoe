package tui

// How the TUI tells you that tapi or an agent's question waits for you
// (docs/orchestration.md「知らせ方」, [orchestrator] notify). tapi chat used
// to be a front desk that only worked while you talked to it, so its wait
// was only a weak word in the bar (docs/tapi-chat.md「気づかせ方」). As the
// orchestrator it works while you are away, waiting on reports, and an
// agent that asks the inbox stops until you answer: missing either holds
// the work up. So both take a session's wait escalation (docs/mascot.md):
// a toast at once, the bar's speech blinking after 30 s, an OS notification
// after 2 min while the terminal is not focused. notify = "quiet" puts
// back the old ways: tapi's "chat waits · t" in the bar only, and one
// message line per new question.
//
// The questions that escalate are the ones an agent waits on: open, not
// fyi (that never waits), and not asked by a person (from: human, sent to
// nobody).

import (
	"fmt"
	"time"

	"pekoe/internal/mascot"
	"pekoe/internal/state"
)

const tapiNeedsYou = "tapi needs you · t"

func (m *model) quietNotify() bool { return m.g.Orchestrator.QuietNotify() }

// tapiWaiting: tapi's agent shows a permission dialog or a question.
func (m *model) tapiWaiting() bool {
	v := m.chat.info
	return v != nil && v.Alive && v.State == state.Waiting
}

// inboxWaits are the open questions an agent waits on, oldest first.
func (m *model) inboxWaits() []state.InboxItem {
	var out []state.InboxItem
	for _, it := range m.inbox {
		if inboxHoldsUp(it) {
			out = append(out, it)
		}
	}
	return out
}

func inboxHoldsUp(it state.InboxItem) bool {
	return it.Status == state.InboxOpen && it.Kind != "fyi" && it.From != "human"
}

// orchWait is what the bar says waits for you besides the sessions:
// tapi behind its closed chat, else the open questions; ok is false when
// nothing does. since is when the wait began (the blink counts from it).
func (m *model) orchWait() (text string, since time.Time, ok bool) {
	if m.chatWaits() {
		if m.quietNotify() {
			return "chat waits · t", m.chat.info.StateSince, true
		}
		return tapiNeedsYou, m.chat.info.StateSince, true
	}
	if m.quietNotify() {
		return "", time.Time{}, false
	}
	its := m.inboxWaits()
	if len(its) == 0 {
		return "", time.Time{}, false
	}
	text = "✉ " + its[0].From + " asks · ]a"
	if len(its) > 1 {
		text = fmt.Sprintf("✉ %d questions · ]a", len(its))
	}
	return text, its[0].CreatedAt, true
}

// waitToast puts up a wait / error toast; a PR toast steps aside for it
// and comes back later.
func (m *model) waitToast(slug string, mood mascot.Mood, text string) {
	t := m.tapi
	if c := t.toast; c != nil && c.react != nil && time.Now().Before(c.until) {
		m.queueReaction(queuedReaction{r: *c.react, at: time.Now(), dur: time.Until(c.until)})
	}
	t.toast = &toast{slug: slug, mood: mood, text: text, until: time.Now().Add(toastFor)}
}

// tapiOnChat runs on tapi.updated: tapi starting to wait behind its closed
// chat toasts once.
func (m *model) tapiOnChat() {
	if m.quietNotify() || !m.chatWaits() || m.tapi == nil {
		return
	}
	key := "+tapi|" + m.chat.info.StateSince.String()
	if m.tapi.toasted[key] {
		return
	}
	m.tapi.toasted[key] = true
	m.waitToast("+tapi", mascot.Wait, tapiNeedsYou)
}

// orchNotify is escalate's part for tapi and the inbox: the OS
// notifications of waits left alone for WaitNotifyAfter, once each.
func (m *model) orchNotify(now time.Time) []string {
	if m.quietNotify() {
		return nil
	}
	var out []string
	once := func(key string) bool {
		if m.tapi.notified[key] {
			return false
		}
		m.tapi.notified[key] = true
		return true
	}
	// tapi's chat may be open in a terminal nobody looks at: escalate is
	// only called while it is not focused
	if v := m.chat.info; m.tapiWaiting() && now.Sub(v.StateSince) >= mascot.WaitNotifyAfter && once("+tapi|"+v.StateSince.String()) {
		out = append(out, "tapi needs you")
	}
	for _, it := range m.inboxWaits() {
		if now.Sub(it.CreatedAt) >= mascot.WaitNotifyAfter && once(fmt.Sprintf("inbox|%d", it.ID)) {
			out = append(out, it.From+" asks: "+clip(inboxText(firstLine(it.Question)), 60))
		}
	}
	return out
}
