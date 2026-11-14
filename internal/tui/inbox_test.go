package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/state"
)

func inboxEv(t *testing.T, m *model, it state.InboxItem) {
	t.Helper()
	b, _ := json.Marshal(it)
	m.onInboxEvent(b)
}

func openItem(id int64) state.InboxItem {
	rec := 1
	return state.InboxItem{ID: id, Rev: 1, Kind: "decision", From: "feat-x", About: "feat-x", Status: state.InboxOpen,
		Question: "ページングを変えてよい?\x1b[2J‮", Options: []string{"変える", "今のまま"}, Recommend: &rec,
		Links:     []string{"https://github.com/me/app/pull/42", "javascript:alert(1)"},
		Facts:     &state.InboxFacts{Slug: "feat-x", State: state.Idle, Branch: "feat/x", Head: "0123456789abcdef", PR: 42, PRURL: "https://github.com/me/app/pull/42"},
		CreatedAt: time.Now()}
}

// A new question toasts once; one answered elsewhere leaves the list.
// (notify = "quiet": a message line. The default is in notify_test.go.)
func TestInboxToast(t *testing.T) {
	m := wrapModel(t, "feat-x")
	m.g.Orchestrator.Notify = "quiet"
	m.onInboxList(nil)
	inboxEv(t, m, openItem(1))
	if !strings.Contains(m.msg, "feat-x asks: ページングを変えてよい?") || strings.Contains(m.msg, "\x1b") {
		t.Fatalf("toast: %q", m.msg)
	}
	m.msg = ""
	it := openItem(1)
	it.Rev = 2
	inboxEv(t, m, it)
	if m.msg != "" || len(m.inbox) != 1 || m.inbox[0].Rev != 2 {
		t.Fatalf("toasted twice / not updated: %q %+v", m.msg, m.inbox)
	}
	it.Status = state.InboxAnswered
	inboxEv(t, m, it)
	if len(m.inbox) != 0 {
		t.Fatalf("answered one still listed: %+v", m.inbox)
	}
	// items there at start are counted, not toasted one by one
	m2 := wrapModel(t)
	m2.onInboxList([]state.InboxItem{openItem(3), openItem(2)})
	if !strings.Contains(m2.msg, "2 open in the inbox") || m2.inbox[0].ID != 2 {
		t.Fatalf("start: %q %+v", m2.msg, m2.inbox)
	}
}

// ]a goes to the open question first; the modal shows pekoe's facts before
// the agent's words, labels the recommendation as the agent's, and Enter
// alone answers nothing.
func TestInboxModal(t *testing.T) {
	m := wrapModel(t, "feat-x")
	m.onInboxList(nil)
	inboxEv(t, m, openItem(1))
	m.nextWaiting()
	im, ok := m.modal.(*inboxModal)
	if !ok {
		t.Fatalf("modal: %T", m.modal)
	}
	raw := strings.Join(im.view(), "\n")
	view := ansi.Strip(raw)
	facts, words := strings.Index(view, "pekoe read"), strings.Index(view, "the agent says")
	if facts < 0 || words < facts || strings.Index(view, "ページング") < words {
		t.Fatalf("order:\n%s", view)
	}
	if !strings.Contains(view, "recommended (agent)") || strings.Contains(view, "› ") {
		t.Fatalf("recommendation preselected or unlabelled:\n%s", view)
	}
	if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "‮") {
		t.Fatal("control characters drawn")
	}
	if !strings.Contains(raw, "\x1b]8;;https://github.com/me/app/pull/42\x1b\\") || strings.Contains(raw, "\x1b]8;;javascript") {
		t.Fatalf("links: %q", raw)
	}
	if cmd := im.key(key("enter")); cmd != nil || im.sending || !strings.Contains(im.err, "choose first") {
		t.Fatalf("Enter with nothing chosen: %v %q", im.sending, im.err)
	}
	im.key(key("1"))
	if im.sel != 0 {
		t.Fatalf("1 chooses the first: %d", im.sel)
	}
	cmd := im.key(key("enter"))
	if cmd == nil || !im.sending {
		t.Fatal("Enter after choosing does not answer")
	}
	m.onInboxAnswer(cmd().(inboxAnswerMsg))
	if m.modal != nil {
		t.Fatal("modal left open after the answer")
	}
}

// The item changed while the modal was open: choose again; answered
// elsewhere: the modal closes.
func TestInboxModalStale(t *testing.T) {
	m := wrapModel(t, "feat-x")
	m.onInboxList(nil)
	inboxEv(t, m, openItem(1))
	m.openInbox(m.inbox[0])
	im := m.modal.(*inboxModal)
	inboxEv(t, m, openItem(1)) // the read on opening: same rev
	im.key(key("2"))
	moved := openItem(1)
	moved.Rev, moved.Facts.Head = 2, "fedcba9876543210"
	inboxEv(t, m, moved)
	if !im.changed || im.sel != -1 || im.it.Rev != 2 {
		t.Fatalf("changed: %+v", im)
	}
	done := moved
	done.Status = state.InboxWithdrawn
	inboxEv(t, m, done)
	if m.modal != nil || !strings.Contains(m.msg, "withdrawn") {
		t.Fatalf("withdrawn elsewhere: %T %q", m.modal, m.msg)
	}
}

// pekoed's merge toasts once per state, and once more when it starts
// waiting for GitHub to compute whether the PR can merge.
func TestInboxMergeToast(t *testing.T) {
	m := wrapModel(t, "feat-x")
	m.onInboxList(nil)
	it := state.InboxItem{ID: 7, Rev: 2, Kind: "approve", From: "pekoed", About: "feat-x", Status: state.InboxAnswered,
		Answer: &state.InboxAnswer{Merge: &state.MergeRun{State: state.MergeRunning, PR: 42}}}
	step := func(st, detail, want string) {
		t.Helper()
		it.Rev++
		it.Answer.Merge = &state.MergeRun{State: st, PR: 42, Detail: detail}
		m.msg = ""
		inboxEv(t, m, it)
		if !strings.Contains(m.msg, want) || (want == "" && m.msg != "") {
			t.Fatalf("%s %q: toast %q", st, detail, m.msg)
		}
	}
	step(state.MergeRunning, "", "merging PR #42 (feat-x) · checking again first")
	step(state.MergeRunning, "waiting: GitHub is still computing whether PR #42 can merge (merge_state UNKNOWN); …", "GitHub is still computing whether it can merge: waiting")
	step(state.MergeRunning, "waiting: GitHub is still computing whether PR #42 can merge (merge_state UNKNOWN); …", "")
	step(state.MergeMerged, "merged PR #42 (merge) at HEAD 0123456", "✓ pekoed merged PR #42")
}
