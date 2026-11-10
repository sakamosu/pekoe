package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

func intp(n int) *int { return &n }

// inboxSetup: a session "main" (its agent window open) that asks.
func inboxSetup(t *testing.T) (*Daemon, *fakeEngine, string) {
	t.Helper()
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.setState("main", state.Running, "")
	return d, fe, repo
}

func ask(t *testing.T, d *Daemon, from string, p map[string]any) state.InboxItem {
	t.Helper()
	return callAs[state.InboxItem](t, d, agentAs(from), "inbox.post", p)
}

func inboxGet(t *testing.T, d *Daemon, id int64) state.InboxItem {
	t.Helper()
	return call[state.InboxItem](t, d, "inbox.get", map[string]any{"id": id})
}

func sentText(fe *fakeEngine, sub string) bool { return strings.Contains(fe.last(), sub) }

func TestInboxPost(t *testing.T) {
	d, _, _ := inboxSetup(t)
	who := agentAs("main")
	who.Claimed = "other"
	it := callAs[state.InboxItem](t, d, who, "inbox.post", map[string]any{
		"question": "ページングを\x1b[31mカーソル式\x1b[0mに変えてよい?‮", "kind": "decision",
		"options": []string{"変える", "今のまま"}, "recommend": 0, "context": "理由\n\t詳細\x07", "links": []string{"https://github.com/me/app/pull/42"}})
	if it.ID == 0 || it.Rev != 1 || it.From != "main" || it.Claimed != "other" || it.About != "main" || it.Status != state.InboxOpen || it.Delivery != state.DeliveryNone {
		t.Fatalf("item: %+v", it)
	}
	// ANSI, control and invisible format characters are gone before it is stored
	if it.Question != "ページングをカーソル式に変えてよい?" || it.Context != "理由\n\t詳細" {
		t.Fatalf("cleaned: %q / %q", it.Question, it.Context)
	}
	if it.Recommend == nil || *it.Recommend != 0 {
		t.Fatalf("recommend: %+v", it.Recommend)
	}
	// facts are pekoed's own
	if f := it.Facts; f == nil || f.Slug != "main" || f.Head == "" || f.Branch == "" || f.State != state.Running {
		t.Fatalf("facts: %+v", it.Facts)
	}
	// login has fixed choices; fyi is closed from the start
	if l := ask(t, d, "main", map[string]any{"question": "gh auth login をお願いします", "kind": "login"}); strings.Join(l.Options, ",") != "済んだ,できない" {
		t.Fatalf("login: %+v", l.Options)
	}
	f := ask(t, d, TapiSlug, map[string]any{"question": "lint を直す PR は分けた", "kind": "fyi", "options": []string{"分ける", "まとめる"}, "decided": 0})
	if f.Status != state.InboxAnswered || f.Answer == nil || f.Answer.By != TapiSlug || *f.Answer.Choice != 0 || f.About != "" || f.Facts != nil {
		t.Fatalf("fyi: %+v", f)
	}
	if a := ask(t, d, TapiSlug, map[string]any{"question": "push してよい?", "kind": "approve", "about": "main"}); len(a.Options) != 2 || a.About != "main" || a.Facts == nil {
		t.Fatalf("approve: %+v", a)
	}

	for name, p := range map[string]map[string]any{
		"kind":          {"question": "q", "kind": "nope"},
		"no question":   {"question": " ", "kind": "decision"},
		"long question": {"question": strings.Repeat("あ", inboxQuestionMax+1), "kind": "decision"},
		"one option":    {"question": "q", "kind": "decision", "options": []string{"a"}},
		"recommend":     {"question": "q", "kind": "decision", "options": []string{"a", "b"}, "recommend": 2},
		"login options": {"question": "q", "kind": "login", "options": []string{"a", "b"}},
		"decided":       {"question": "q", "kind": "decision", "options": []string{"a", "b"}, "decided": 0},
		"links":         {"question": "q", "kind": "decision", "links": make([]string, inboxLinksMax+1)},
		"about":         {"question": "q", "kind": "decision", "about": "nope"},
	} {
		if err := callAsErr(d, agentAs("main"), "inbox.post", p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := callAsErr(d, &Caller{peer: fixedPeer("")}, "inbox.post", map[string]any{"question": "q", "kind": "decision"}); err == nil || !strings.Contains(err.Error(), "cannot tell") {
		t.Fatalf("unknown caller: %v", err)
	}
}

// 10 open questions per caller; answered ones and fyi do not count.
func TestInboxOpenLimit(t *testing.T) {
	d, _, _ := inboxSetup(t)
	n, _ := d.DB.InboxOpenCount("main")
	for ; n < inboxOpenMax; n++ {
		ask(t, d, "main", map[string]any{"question": "q", "kind": "decision"})
	}
	err := callAsErr(d, agentAs("main"), "inbox.post", map[string]any{"question": "q", "kind": "decision"})
	if err == nil || !strings.Contains(err.Error(), "fold") {
		t.Fatalf("11th: %v", err)
	}
	ask(t, d, "main", map[string]any{"question": "決めた", "kind": "fyi"})
	ask(t, d, TapiSlug, map[string]any{"question": "q", "kind": "decision"}) // another caller
}

// Without the token, inbox.answer and inbox.dismiss are refused (an agent
// cannot answer its own question); inbox.post / list / get / withdraw work.
func TestInboxAnswerNeedsPerson(t *testing.T) {
	d, _, _ := inboxSetup(t)
	it := ask(t, d, "main", map[string]any{"question": "q", "kind": "decision", "options": []string{"a", "b"}})
	c := dialPipe(t, d)
	for _, m := range []string{"inbox.answer", "inbox.dismiss"} {
		if e := c.call(m, map[string]any{"id": it.ID, "choice": 0, "rev": it.Rev}, ""); !strings.Contains(e, "only a person") {
			t.Fatalf("%s without the token: %q", m, e)
		}
	}
	for _, m := range []string{"inbox.list", "inbox.get"} {
		if e := c.call(m, map[string]any{"id": it.ID}, ""); e != "" {
			t.Fatalf("%s without the token: %q", m, e)
		}
	}
	if e := c.call("inbox.answer", map[string]any{"id": it.ID, "choice": 0, "rev": it.Rev}, d.humanToken); e != "" {
		t.Fatalf("with the token: %q", e)
	}
	if got := inboxGet(t, d, it.ID); got.Status != state.InboxAnswered || got.Answer.By != callerHuman || got.Answer.Via != viaSocket {
		t.Fatalf("answered: %+v", got)
	}
}

// rev: an answer to an older rev is refused, and so is one given after the
// facts changed (a new commit) since the person looked.
func TestInboxRev(t *testing.T) {
	d, _, repo := inboxSetup(t)
	it := ask(t, d, "main", map[string]any{"question": "merge?", "kind": "approve"})
	if err := callAsErr(d, &Caller{human: true}, "inbox.answer", map[string]any{"id": it.ID, "choice": 0}); err == nil || !strings.Contains(err.Error(), "rev required") {
		t.Fatalf("no rev: %v", err)
	}
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("x"), 0o644)
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "more")
	err := callAsErr(d, &Caller{human: true}, "inbox.answer", map[string]any{"id": it.ID, "choice": 0, "rev": it.Rev})
	if err == nil || !strings.Contains(err.Error(), "look again") {
		t.Fatalf("answer after HEAD moved: %v", err)
	}
	now := inboxGet(t, d, it.ID)
	if now.Rev != it.Rev+1 || now.Facts.Head == it.Facts.Head || now.Status != state.InboxOpen {
		t.Fatalf("after the refresh: %+v", now)
	}
	for name, p := range map[string]map[string]any{
		"no choice": {"id": it.ID, "rev": now.Rev},
		"range":     {"id": it.ID, "rev": now.Rev, "choice": 2},
	} {
		if err := callAsErr(d, &Caller{human: true}, "inbox.answer", p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": now.Rev, "choice": 0})
	if err := callAsErr(d, &Caller{human: true}, "inbox.answer", map[string]any{"id": it.ID, "rev": now.Rev + 1, "choice": 1}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("answered twice: %v", err)
	}
}

// Completion criterion: when the asker waits (wait --on inbox), the answer
// is the wait's result and nothing is typed into its window.
func TestInboxAnswerToWaiter(t *testing.T) {
	d, fe, _ := inboxSetup(t)
	d.setState("main", state.Idle, "")
	it := ask(t, d, "main", map[string]any{"question": "どっち?", "kind": "decision", "options": []string{"A", "B"}})
	got := make(chan waitRes, 1)
	go func() {
		got <- callAs[waitRes](t, d, agentAs("main"), "wait", map[string]any{"on": []string{"inbox"}, "timeout_s": 10})
	}()
	waitFor(t, func() bool {
		d.changes.mu.Lock()
		defer d.changes.mu.Unlock()
		return len(d.changes.waiters) == 1
	})
	// someone else's answer does not wake it
	other := ask(t, d, TapiSlug, map[string]any{"question": "x", "kind": "decision", "options": []string{"A", "B"}})
	call[any](t, d, "inbox.answer", map[string]any{"id": other.ID, "rev": other.Rev, "choice": 0})
	call[any](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "choice": 1, "note": "B で"})
	var w waitRes
	select {
	case w = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
	if len(w.Changes) != 1 || w.Changes[0].Kind != kindInbox || !strings.Contains(w.Changes[0].Summary, "2 B") {
		t.Fatalf("wait: %+v", w)
	}
	if g := inboxGet(t, d, it.ID); g.Delivery != state.DeliveryWaited {
		t.Fatalf("delivery: %+v", g.Delivery)
	}
	d.deliverAllInbox()
	if sentText(fe, "pekoe inbox #") {
		t.Fatalf("typed although it waited:\n%s", fe.last())
	}
}

// Completion criterion: when the asker does not wait, the answer is typed
// into its window when it goes idle; not while a permission prompt is up,
// by the hooks' state or on screen.
func TestInboxAnswerToWindow(t *testing.T) {
	d, fe, _ := inboxSetup(t)
	it := ask(t, d, "main", map[string]any{"question": "どっち?", "kind": "decision", "options": []string{"A", "B"}, "recommend": 0})
	call[any](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "choice": 0, "note": "A\nで"})
	if g := inboxGet(t, d, it.ID); g.Delivery != state.DeliveryPending {
		t.Fatalf("delivery while running: %+v", g.Delivery)
	}
	d.deliverAllInbox()
	if sentText(fe, "pekoe inbox #") {
		t.Fatal("typed while the agent was running")
	}

	// a permission prompt by the hooks
	d.setState("main", state.Waiting, "permission")
	d.deliverAllInbox()
	// a permission dialog on screen the hooks did not report
	fe.mu.Lock()
	fe.lines = []string{"Bash(rm -rf x)", "Do you want to proceed?", "❯ 1. Yes", "  2. No"}
	fe.mu.Unlock()
	d.setState("main", state.Idle, "")
	d.deliverInbox("main")
	time.Sleep(50 * time.Millisecond)
	if sentText(fe, "pekoe inbox #") {
		t.Fatalf("typed into a permission prompt:\n%s", fe.last())
	}
	// a person typed into the window just now
	fe.mu.Lock()
	fe.lines = nil
	fe.mu.Unlock()
	d.noteTyped("main")
	d.deliverAllInbox()
	if sentText(fe, "pekoe inbox #") {
		t.Fatal("typed over a person's typing")
	}
	d.mu.Lock()
	d.typedAt = map[string]time.Time{}
	d.mu.Unlock()

	n := len(fe.sent)
	d.deliverAllInbox()
	want := "text:[pekoe inbox #" + itoa(it.ID) + "] 答え: 1「A」(メモ: A で) 詳しくは pekoe --json inbox " + itoa(it.ID)
	got := sentSince(fe, n)
	if len(got) != 2 || got[0] != want || got[1] != "keys:Enter" {
		t.Fatalf("typed:\n%s\nwant %s", strings.Join(got, "\n"), want)
	}
	if g := inboxGet(t, d, it.ID); g.Delivery != state.DeliveryDelivered {
		t.Fatalf("delivery: %+v", g.Delivery)
	}
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	if len(rs) == 0 || rs[0].From != callerPekoed || rs[0].To != "main" || rs[0].Kind != "inbox" || !rs[0].OK {
		t.Fatalf("send log: %+v", rs)
	}
	// once only
	n = len(fe.sent)
	d.setState("main", state.Running, "")
	d.setState("main", state.Done, "")
	time.Sleep(50 * time.Millisecond)
	if got := sentSince(fe, n); len(got) != 0 {
		t.Fatalf("typed again: %v", got)
	}
}

// Going idle is what sends it, without the tick.
func TestInboxDeliveredOnIdle(t *testing.T) {
	d, fe, _ := inboxSetup(t)
	it := ask(t, d, "main", map[string]any{"question": "名前は?", "kind": "decision"})
	call[any](t, d, "inbox.dismiss", map[string]any{"id": it.ID, "note": "要らない"})
	d.setState("main", state.Idle, "")
	waitFor(t, func() bool { return sentText(fe, "答えずに閉じられました(メモ: 要らない)") })
	waitFor(t, func() bool { return inboxGet(t, d, it.ID).Delivery == state.DeliveryDelivered })
}

// The asker read its answer itself (ask-human --wait reads inbox.get):
// nothing is typed.
func TestInboxGetByAskerIsDelivery(t *testing.T) {
	d, fe, _ := inboxSetup(t)
	it := ask(t, d, "main", map[string]any{"question": "名前は?", "kind": "decision"})
	if err := callAsErr(d, &Caller{human: true}, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev}); err == nil {
		t.Fatal("no options and no note")
	}
	call[any](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "note": "pekoe で"})
	inboxGet(t, d, it.ID) // a person reading does not count
	if g := callAs[state.InboxItem](t, d, agentAs("main"), "inbox.get", map[string]any{"id": it.ID}); g.Delivery != state.DeliveryWaited || g.Answer.Note != "pekoe で" {
		t.Fatalf("asker read it: %+v", g)
	}
	d.setState("main", state.Idle, "")
	d.deliverAllInbox()
	if sentText(fe, "pekoe inbox #") {
		t.Fatal("typed after the asker read it")
	}
}

func TestInboxWithdraw(t *testing.T) {
	d, _, _ := inboxSetup(t)
	it := ask(t, d, "main", map[string]any{"question": "q", "kind": "decision"})
	if err := callAsErr(d, agentAs(TapiSlug), "inbox.withdraw", map[string]any{"id": it.ID}); err == nil || !strings.Contains(err.Error(), "only its own") {
		t.Fatalf("someone else's: %v", err)
	}
	if err := callAsErr(d, &Caller{peer: fixedPeer("")}, "inbox.withdraw", map[string]any{"id": it.ID}); err == nil {
		t.Fatal("unknown caller")
	}
	w := callAs[state.InboxItem](t, d, agentAs("main"), "inbox.withdraw", map[string]any{"id": it.ID, "note": "自分で決めた"})
	if w.Status != state.InboxWithdrawn || w.Rev != it.Rev+1 || w.Answer.By != "main" || w.Delivery != state.DeliveryNone {
		t.Fatalf("withdrawn: %+v", w)
	}
	if err := callAsErr(d, &Caller{human: true}, "inbox.answer", map[string]any{"id": it.ID, "rev": w.Rev, "note": "x"}); err == nil {
		t.Fatal("answered a withdrawn question")
	}
	if l := callAs[[]state.InboxItem](t, d, agentAs("main"), "inbox.list", map[string]any{"status": "all", "mine": true}); len(l) != 1 {
		t.Fatalf("mine: %+v", l)
	}
	if l := call[[]state.InboxItem](t, d, "inbox.list", nil); len(l) != 0 {
		t.Fatalf("open: %+v", l)
	}
}

// tapi chat asks too; its answer goes into tapi's window when it is idle
// and nobody typed into tapi chat lately.
func TestInboxAnswerToTapi(t *testing.T) {
	d, fe, _ := setup(t)
	call[Tapi](t, d, "tapi.open", nil)
	it := ask(t, d, TapiSlug, map[string]any{"question": "どっち?", "kind": "decision", "options": []string{"A", "B"}})
	call[any](t, d, "tapi.send", map[string]any{"text": "h"}) // a person typing into tapi chat
	call[any](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "choice": 1})
	d.deliverAllInbox()
	if sentText(fe, "pekoe inbox #") {
		t.Fatal("typed over a person's typing in tapi chat")
	}
	d.mu.Lock()
	d.typedAt = map[string]time.Time{}
	d.mu.Unlock()
	d.deliverAllInbox()
	if !sentText(fe, "答え: 2「B」") {
		t.Fatalf("not typed into tapi chat:\n%s", fe.last())
	}
}

// inbox.updated reaches only subscribers with the token (it carries a
// person's answers).
func TestInboxUpdatedIsHumanOnly(t *testing.T) {
	if agentEvent("inbox.updated") {
		t.Fatal("inbox.updated goes to subscribers without the token")
	}
}

func TestInboxLine(t *testing.T) {
	it := &state.InboxItem{ID: 3, Status: state.InboxAnswered, Options: []string{"x"}, Answer: &state.InboxAnswer{Choice: intp(0), Note: "a\nb"}}
	l := inboxLine(it)
	if l[0] != '[' || strings.Contains(l, "\n") {
		t.Fatalf("line: %q", l)
	}
	if !strings.HasPrefix(cleanInbox("\x1b]8;;https://evil\x07click\x1b]8;;\x07", false), "click") {
		t.Fatalf("OSC 8 kept: %q", cleanInbox("\x1b]8;;https://evil\x07click\x1b]8;;\x07", false))
	}
	_ = engine.WinAgent
	_ = json.Marshal
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
