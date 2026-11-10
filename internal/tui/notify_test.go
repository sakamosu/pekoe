package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/state"
)

func tapiWaitEv(t *testing.T, m *model, since time.Time) {
	t.Helper()
	data, _ := json.Marshal(daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Waiting, WaitKind: "permission", StateSince: since})
	runCmd(m, m.onEvent(client.Event{Event: "tapi.updated", Data: data}))
}

// tapi waiting behind its closed chat takes a session's wait: a toast once,
// "tapi needs you" in the bar, and an OS notification after 2 minutes.
func TestTapiWaitEscalates(t *testing.T) {
	m, _, _ := chatModel(t)
	since := time.Now()
	tapiWaitEv(t, m, since)
	if m.tapi.toast == nil || m.tapi.toast.text != tapiNeedsYou {
		t.Fatalf("toast: %+v", m.tapi.toast)
	}
	if out := checkSize(t, m, "tapi waits"); !strings.Contains(out, tapiNeedsYou) {
		t.Fatalf("bar:\n%s", out)
	}
	m.tapi.toast = nil
	tapiWaitEv(t, m, since) // the same wait again: no second toast
	if m.tapi.toast != nil {
		t.Fatal("toasted twice")
	}
	if n := m.orchNotify(time.Now()); len(n) != 0 {
		t.Fatalf("notified before 2 min: %v", n)
	}
	later := since.Add(3 * time.Minute)
	if n := m.orchNotify(later); len(n) != 1 || n[0] != "tapi needs you" {
		t.Fatalf("notify: %v", n)
	}
	if n := m.orchNotify(later); len(n) != 0 {
		t.Fatalf("notified twice: %v", n)
	}
	// a session's wait keeps the bar
	m.sessions[0].State, m.sessions[0].WaitKind, m.sessions[0].StateSince = state.Waiting, "permission", time.Now()
	if out := checkSize(t, m, "both wait"); strings.Contains(out, tapiNeedsYou) {
		t.Fatalf("tapi took the bar from a session's wait:\n%s", out)
	}

	// quiet: no toast, no notification, the old word
	q, _, _ := chatModel(t)
	q.g.Orchestrator.Notify = "quiet"
	tapiWaitEv(t, q, time.Now().Add(-time.Hour))
	if q.tapi.toast != nil || len(q.orchNotify(time.Now())) != 0 {
		t.Fatalf("quiet escalated: %+v", q.tapi.toast)
	}
	if out := checkSize(t, q, "quiet"); !strings.Contains(out, "chat waits · t") {
		t.Fatalf("quiet bar:\n%s", out)
	}
}

// A question an agent waits on toasts with tapi's face, stays in the bar
// and becomes an OS notification after 2 minutes; fyi and a person's own
// question do not.
func TestInboxWaitEscalates(t *testing.T) {
	m, _, _ := chatModel(t)
	m.onInboxList(nil)
	it := openItem(1)
	inboxEv(t, m, it)
	if m.tapi.toast == nil || !strings.Contains(m.tapi.toast.text, "feat-x asks: ページングを変えてよい?") || strings.Contains(m.tapi.toast.text, "\x1b") {
		t.Fatalf("toast: %+v", m.tapi.toast)
	}
	if out := checkSize(t, m, "one question"); !strings.Contains(out, "? feat-x asks · ]a") {
		t.Fatalf("bar:\n%s", out)
	}
	mine := openItem(2)
	mine.From = "human"
	inboxEv(t, m, mine)
	if len(m.inboxWaits()) != 1 {
		t.Fatalf("a person's own question holds nobody up: %+v", m.inboxWaits())
	}
	other := openItem(3)
	other.From, other.Kind = "feat-y", "approve"
	inboxEv(t, m, other)
	if out := checkSize(t, m, "two questions"); !strings.Contains(out, "? 2 questions · ]a") {
		t.Fatalf("bar:\n%s", out)
	}
	later := it.CreatedAt.Add(3 * time.Minute)
	n := m.orchNotify(later)
	if len(n) != 2 || !strings.HasPrefix(n[0], "feat-x asks: ページング") || !strings.HasPrefix(n[1], "feat-y asks:") {
		t.Fatalf("notify: %q", n)
	}
	if len(m.orchNotify(later)) != 0 {
		t.Fatal("notified twice")
	}

	// quiet: a message line only
	q := wrapModel(t)
	q.g.Orchestrator.Notify = "quiet"
	q.onInboxList(nil)
	inboxEv(t, q, openItem(1))
	if q.tapi.toast != nil || !strings.Contains(q.msg, "feat-x asks") {
		t.Fatalf("quiet toast: %+v %q", q.tapi.toast, q.msg)
	}
	if _, _, ok := q.orchWait(); ok || len(q.orchNotify(later)) != 0 {
		t.Fatal("quiet escalated a question")
	}
}
