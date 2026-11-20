package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/ids"
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

// deliverUntil runs the delivery tick until ok. An answer, a tapi send and
// going idle each start a delivery of their own in the background (go
// d.deliverInbox); while one to the same window runs, a tick to it returns
// at once (TryLock) and the next tick delivers, as in pekoed.
func deliverUntil(t *testing.T, d *Daemon, ok func() bool) {
	t.Helper()
	waitFor(t, func() bool { d.deliverAllInbox(); return ok() })
}

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
	if l := ask(t, d, "main", map[string]any{"question": "gh auth login をお願いします", "kind": "login"}); strings.Join(l.Options, ",") != "done,can't" {
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
	if len(w.Changes) != 1 || w.Changes[0].Kind != kindInbox || !strings.HasSuffix(w.Changes[0].Summary, "answered: 2 B · note: B で") {
		t.Fatalf("wait: %+v", w)
	}
	if g := inboxGet(t, d, it.ID); g.Delivery != state.DeliveryWaited {
		t.Fatalf("delivery: %+v", g.Delivery)
	}
	d.deliverAllInbox()
	if sentText(fe, "[pekoe inbox ") {
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
	if sentText(fe, "[pekoe inbox ") {
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
	if sentText(fe, "[pekoe inbox ") {
		t.Fatalf("typed into a permission prompt:\n%s", fe.last())
	}
	// a person typed into the window just now
	fe.mu.Lock()
	fe.lines = nil
	fe.mu.Unlock()
	d.noteTyped("main")
	d.deliverAllInbox()
	if sentText(fe, "[pekoe inbox ") {
		t.Fatal("typed over a person's typing")
	}
	d.mu.Lock()
	d.typedAt = map[string]time.Time{}
	d.mu.Unlock()

	n := len(fe.sent)
	deliverUntil(t, d, func() bool { return len(sentSince(fe, n)) >= 2 })
	want := "text:[pekoe inbox q" + itoa(it.ID) + "] 答え: 1「A」(メモ: A で) 詳しくは pekoe --json inbox q" + itoa(it.ID)
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
	if sentText(fe, "[pekoe inbox ") {
		t.Fatal("typed after the asker read it")
	}
}

func TestInboxWithdraw(t *testing.T) {
	d, _, _ := inboxSetup(t)
	it := ask(t, d, "main", map[string]any{"question": "q", "kind": "decision"})
	if err := callAsErr(d, agentAs(TapiSlug), "inbox.withdraw", map[string]any{"id": it.ID}); err == nil || !strings.Contains(err.Error(), "only its own") {
		t.Fatalf("someone else's: %v", err)
	}
	// an agent that reads the token (sandbox off, tapi chat) is still an
	// agent: the token does not make it the person
	for _, from := range []string{TapiSlug, "feat-b"} {
		tok := &Caller{human: true, Via: viaSocket, peer: fixedPeer(from)}
		if err := callAsErr(d, tok, "inbox.withdraw", map[string]any{"id": it.ID}); err == nil || !strings.Contains(err.Error(), "only its own") {
			t.Fatalf("someone else's with the token from %s: %v", from, err)
		}
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
	if sentText(fe, "[pekoe inbox ") {
		t.Fatal("typed over a person's typing in tapi chat")
	}
	d.mu.Lock()
	d.typedAt = map[string]time.Time{}
	d.mu.Unlock()
	deliverUntil(t, d, func() bool { return sentText(fe, "答え: 2「B」") })
}

// inbox.updated reaches only subscribers with the token (it carries a
// person's answers).
func TestInboxUpdatedIsHumanOnly(t *testing.T) {
	if agentEvent("inbox.updated") {
		t.Fatal("inbox.updated goes to subscribers without the token")
	}
}

// A question with options takes a note alone as its answer (none of the
// options): answer.choice is absent (never 0), the wait summary and the
// line typed into the asker say so with the note. Neither a choice nor a
// note is still refused.
func TestInboxAnswerNoteOnly(t *testing.T) {
	d, fe, _ := inboxSetup(t)
	it := ask(t, d, "main", map[string]any{"question": "どれにする?", "kind": "decision", "options": []string{"A", "B", "C"}})
	for name, p := range map[string]map[string]any{
		"nothing":    {"id": it.ID, "rev": it.Rev},
		"blank note": {"id": it.ID, "rev": it.Rev, "note": " \n "},
		"choice -1":  {"id": it.ID, "rev": it.Rev, "choice": -1, "note": "x"},
		"choice out": {"id": it.ID, "rev": it.Rev, "choice": 3, "note": "x"},
	} {
		err := callAsErr(d, &Caller{human: true}, "inbox.answer", p)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if name == "nothing" && !strings.Contains(err.Error(), "note alone") {
			t.Errorf("nothing: %v", err)
		}
	}
	got := make(chan waitRes, 1)
	go func() {
		got <- callAs[waitRes](t, d, agentAs("main"), "wait", map[string]any{"on": []string{"inbox"}, "timeout_s": 10})
	}()
	waitFor(t, func() bool {
		d.changes.mu.Lock()
		defer d.changes.mu.Unlock()
		return len(d.changes.waiters) == 1
	})
	g := call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "note": "画像を送りたい"})
	if g.Status != state.InboxAnswered || g.Answer == nil || g.Answer.Choice != nil || g.Answer.Note != "画像を送りたい" {
		t.Fatalf("answered: %+v", g.Answer)
	}
	b, _ := json.Marshal(inboxGet(t, d, it.ID).Answer)
	if strings.Contains(string(b), `"choice"`) || !strings.Contains(string(b), `"note":"画像を送りたい"`) {
		t.Fatalf("answer JSON: %s", b)
	}
	var w waitRes
	select {
	case w = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
	want := "inbox " + ids.Question(it.ID) + " answered: none of the options · note: 画像を送りたい"
	if len(w.Changes) != 1 || w.Changes[0].Summary != want {
		t.Fatalf("wait: %+v\nwant %s", w.Changes, want)
	}
	if l := inboxLine(&g); !strings.Contains(l, "答え: 選択肢のどれでもない(メモ: 画像を送りたい)") {
		t.Fatalf("line: %s", l)
	}
	_ = fe
}

// The wait summary and the deliver line carry the note cut short, on one
// line, as untrusted data (control characters dropped).
func TestInboxSummaryNote(t *testing.T) {
	long := strings.Repeat("あ", 200)
	it := &state.InboxItem{ID: 155, Status: state.InboxAnswered, Options: []string{"x", "操作できることが少ない"},
		Answer: &state.InboxAnswer{Choice: intp(1), Note: "画像を\n送りたい\x1b[31m"}}
	if s := inboxSummary(it); s != "inbox q155 answered: 2 操作できることが少ない · note: 画像を 送りたい [31m" {
		t.Fatalf("summary: %q", s)
	}
	it.Answer.Note = long
	s := inboxSummary(it)
	if !strings.HasSuffix(s, "· note: "+strings.Repeat("あ", inboxNoteCut)+"…") {
		t.Fatalf("long note: %q", s)
	}
	it.Answer.Note = ""
	if s := inboxSummary(it); s != "inbox q155 answered: 2 操作できることが少ない" {
		t.Fatalf("no note: %q", s)
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

// tokenAgent is an agent window whose CLI sends the human token (tapi, or
// an agent with sandbox = false): docs/api.md "token の有無より窓を優先".
func tokenAgent(slug string) *Caller {
	return &Caller{human: true, Via: viaSocket, peer: fixedPeer(slug)}
}

// The open limit is the window's, token or not.
func TestInboxOpenLimitTokenAgent(t *testing.T) {
	d, _, _ := inboxSetup(t)
	for _, who := range []string{"main", TapiSlug} {
		for i := 0; i < inboxOpenMax; i++ {
			callAs[state.InboxItem](t, d, tokenAgent(who), "inbox.post", map[string]any{"question": "q", "kind": "decision"})
		}
		err := callAsErr(d, tokenAgent(who), "inbox.post", map[string]any{"question": "q", "kind": "decision"})
		if err == nil || !strings.Contains(err.Error(), "fold") {
			n, _ := d.DB.InboxOpenCount(who)
			t.Errorf("%s with the token: 11th accepted (%d open): %v", who, n, err)
		}
	}
	// a person (the token, no agent window) has no limit
	for i := 0; i <= inboxOpenMax; i++ {
		call[state.InboxItem](t, d, "inbox.post", map[string]any{"question": "q", "kind": "decision"})
	}
}

// The asker reading its own answer counts as received when its CLI sends
// the token too: nothing is typed into its window.
func TestInboxGetByTokenAskerIsDelivery(t *testing.T) {
	d, fe, _ := inboxSetup(t)
	it := callAs[state.InboxItem](t, d, tokenAgent("main"), "inbox.post", map[string]any{"question": "名前は?", "kind": "decision"})
	call[any](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "note": "pekoe で"})
	g := callAs[state.InboxItem](t, d, tokenAgent("main"), "inbox.get", map[string]any{"id": it.ID})
	if g.Delivery != state.DeliveryWaited {
		t.Errorf("asker (token, own window) read it: delivery %q, want waited", g.Delivery)
	}
	d.setState("main", state.Idle, "")
	d.deliverAllInbox()
	if sentText(fe, "[pekoe inbox ") {
		t.Errorf("typed after the asker read it:\n%s", fe.last())
	}
}

// An item belongs to a slug, which a later session of the same branch
// takes again. When the session that asked is removed, its open items are
// withdrawn by pekoed and an answer not yet typed is never typed: the new
// session neither gets the old answers nor owns the old questions.
func TestInboxSlugReuse(t *testing.T) {
	d, fe, repo := inboxSetup(t)
	old := newSession(t, d, repo, "feat/x")
	d.setState(old.Slug, state.Running, "")
	q := ask(t, d, old.Slug, map[string]any{"question": "旧セッションの問い", "kind": "decision", "options": []string{"A", "B"}})
	a := ask(t, d, old.Slug, map[string]any{"question": "答えた問い", "kind": "decision", "options": []string{"A", "B"}})
	call[any](t, d, "inbox.answer", map[string]any{"id": a.ID, "rev": a.Rev, "choice": 1})
	if g := inboxGet(t, d, a.ID); g.Delivery != state.DeliveryPending {
		t.Fatalf("answered while running: delivery %q", g.Delivery)
	}
	call[any](t, d, "worktree.rm", map[string]any{"slug": old.Slug, "force": true})
	if _, err := d.get(old.Slug); err == nil {
		t.Fatalf("%s not removed", old.Slug)
	}
	if g := inboxGet(t, d, q.ID); g.Status != state.InboxWithdrawn || g.Answer == nil || g.Answer.By != callerPekoed || g.Answer.Note != "session removed" {
		t.Fatalf("open item of the removed session: %+v", g)
	}
	if g := inboxGet(t, d, a.ID); g.Delivery != state.DeliveryNone || g.Status != state.InboxAnswered {
		t.Fatalf("pending answer of the removed session: %+v", g)
	}
	if n, _ := d.DB.InboxOpenCount(old.Slug); n != 0 {
		t.Fatalf("%d open left", n)
	}
	cur := newSession(t, d, repo, "feat/x")
	if cur.Slug != old.Slug {
		t.Fatalf("slug not reused: %s vs %s", cur.Slug, old.Slug)
	}
	if err := callAsErr(d, &Caller{human: true}, "inbox.answer", map[string]any{"id": q.ID, "rev": inboxGet(t, d, q.ID).Rev, "choice": 0}); err == nil {
		t.Error("answered the removed session's question")
	}
	n := len(fe.sent)
	d.setState(cur.Slug, state.Idle, "")
	d.deliverAllInbox()
	time.Sleep(50 * time.Millisecond)
	d.deliverAllInbox()
	for _, s := range sentSince(fe, n) {
		if strings.Contains(s, "[pekoe inbox ") {
			t.Fatalf("the removed session's answer was typed into the new %s: %s", cur.Slug, s)
		}
	}
	// the new session's own questions work as usual
	mine := ask(t, d, cur.Slug, map[string]any{"question": "新しい問い", "kind": "decision"})
	callAs[state.InboxItem](t, d, agentAs(cur.Slug), "inbox.withdraw", map[string]any{"id": mine.ID})
}

// A person withdrawing an agent's question tells the asker as a dismissal
// does: a wait of its own takes it, else a line is typed into its window.
// The asker's own withdraw tells it nothing.
func TestInboxHumanWithdrawTellsAsker(t *testing.T) {
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
	w := call[state.InboxItem](t, d, "inbox.withdraw", map[string]any{"id": it.ID, "note": "もう要らない"})
	if w.Status != state.InboxWithdrawn || w.Answer.By != callerHuman {
		t.Fatalf("withdrawn: %+v", w)
	}
	select {
	case r := <-got:
		if len(r.Changes) != 1 || r.Changes[0].Kind != kindInbox || !strings.Contains(r.Changes[0].Summary, "withdrawn") {
			t.Fatalf("wait: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
	if g := inboxGet(t, d, it.ID); g.Delivery != state.DeliveryWaited {
		t.Fatalf("delivery: %+v", g.Delivery)
	}

	// nobody waits: typed into the asker's window
	it2 := ask(t, d, "main", map[string]any{"question": "名前は?", "kind": "decision"})
	call[any](t, d, "inbox.withdraw", map[string]any{"id": it2.ID})
	deliverUntil(t, d, func() bool { return sentText(fe, "[pekoe inbox q"+itoa(it2.ID)+"] 人が取り下げました") })
	waitFor(t, func() bool { return inboxGet(t, d, it2.ID).Delivery == state.DeliveryDelivered })

	// the asker's own withdraw: nothing goes to it
	it3 := ask(t, d, "main", map[string]any{"question": "x", "kind": "decision"})
	n := len(fe.sent)
	if w := callAs[state.InboxItem](t, d, agentAs("main"), "inbox.withdraw", map[string]any{"id": it3.ID}); w.Delivery != state.DeliveryNone {
		t.Fatalf("own withdraw: %+v", w)
	}
	// with the token from its own agent window (sandbox off): still its own
	it4 := ask(t, d, "main", map[string]any{"question": "y", "kind": "decision"})
	self := &Caller{human: true, Via: viaSocket, peer: fixedPeer("main")}
	if w := callAs[state.InboxItem](t, d, self, "inbox.withdraw", map[string]any{"id": it4.ID}); w.Delivery != state.DeliveryNone || w.Answer.By != "main" {
		t.Fatalf("own withdraw with the token: %+v", w)
	}
	d.deliverAllInbox()
	time.Sleep(50 * time.Millisecond)
	if got := sentSince(fe, n); len(got) != 0 {
		t.Fatalf("typed for an own withdraw: %v", got)
	}
}

// ask-human and inbox --mine typed in a session's run window (TUI `!`)
// with the token are a person's, not the session's agent's: the answer is
// not typed into the agent's window.
func TestInboxFromRunWindowIsHuman(t *testing.T) {
	d, fe, _ := inboxSetup(t)
	run := &Caller{human: true, Via: viaSocket, peer: fixedWindow("main", engine.WinRun)}
	it := callAs[state.InboxItem](t, d, run, "inbox.post", map[string]any{"question": "どっち?", "kind": "decision", "options": []string{"A", "B"}})
	if it.From != callerHuman {
		t.Fatalf("from: %q", it.From)
	}
	agentQ := ask(t, d, "main", map[string]any{"question": "agent の問い", "kind": "decision"})
	if l := callAs[[]state.InboxItem](t, d, run, "inbox.list", map[string]any{"mine": true}); len(l) != 1 || l[0].ID != it.ID {
		t.Fatalf("mine from the run window: %+v", l)
	}
	// a person in the run window withdraws the agent's question: the agent is told
	if w := callAs[state.InboxItem](t, d, run, "inbox.withdraw", map[string]any{"id": agentQ.ID}); w.Answer.By != callerHuman || w.Delivery == state.DeliveryNone {
		t.Fatalf("withdraw from the run window: %+v", w)
	}
	n := len(fe.sent)
	call[any](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": it.Rev, "choice": 0})
	d.setState("main", state.Idle, "")
	d.deliverAllInbox()
	time.Sleep(50 * time.Millisecond)
	for _, s := range sentSince(fe, n) {
		if strings.Contains(s, "q"+itoa(it.ID)+"]") {
			t.Fatalf("a person's answer typed into the agent's window: %v", sentSince(fe, n))
		}
	}
	if g := inboxGet(t, d, it.ID); g.Delivery != state.DeliveryNone {
		t.Fatalf("delivery: %+v", g.Delivery)
	}
}

// A window named human (a session made before the name was reserved) is
// not a person: it cannot post (from would read human and lift the open
// cap), withdraw someone else's question or read the person's items as its
// own. The same goes for a window named unknown. The token from a run
// window of such a session is still a person's.
func TestInboxReservedWindowIsNotHuman(t *testing.T) {
	d, _, _ := inboxSetup(t)
	agentQ := ask(t, d, "main", map[string]any{"question": "agent の問い", "kind": "decision"})
	for _, name := range []string{callerHuman, callerUnknown} {
		for _, who := range []*Caller{agentAs(name), {human: true, Via: viaSocket, peer: fixedPeer(name)}} {
			if err := callAsErr(d, who, "inbox.post", map[string]any{"question": "q", "kind": "decision"}); err == nil || !strings.Contains(err.Error(), "named "+name) {
				t.Fatalf("post from a window named %s (token %v): %v", name, who.human, err)
			}
			if err := callAsErr(d, who, "inbox.withdraw", map[string]any{"id": agentQ.ID}); err == nil || !strings.Contains(err.Error(), "named "+name) {
				t.Fatalf("withdraw from a window named %s (token %v): %v", name, who.human, err)
			}
			if err := callAsErr(d, who, "inbox.list", map[string]any{"mine": true}); err == nil || !strings.Contains(err.Error(), "named "+name) {
				t.Fatalf("list --mine from a window named %s (token %v): %v", name, who.human, err)
			}
		}
	}
	if g := inboxGet(t, d, agentQ.ID); g.Status != state.InboxOpen {
		t.Fatalf("withdrawn: %+v", g)
	}
	// the token from the run window of a session named human is a person
	run := &Caller{human: true, Via: viaSocket, peer: fixedWindow(callerHuman, engine.WinRun)}
	if it := callAs[state.InboxItem](t, d, run, "inbox.post", map[string]any{"question": "どっち?", "kind": "decision"}); it.From != callerHuman {
		t.Fatalf("from: %q", it.From)
	}
	if w := callAs[state.InboxItem](t, d, run, "inbox.withdraw", map[string]any{"id": agentQ.ID}); w.Status != state.InboxWithdrawn {
		t.Fatalf("withdraw: %+v", w)
	}
}

// waitAsync runs wait as who in the background; the result (or error)
// comes on the channel.
func waitAsync(d *Daemon, who *Caller, p map[string]any) <-chan waitRes {
	ch := make(chan waitRes, 1)
	go func() {
		b, _ := json.Marshal(p)
		res, err := d.CallAs(who, "wait", b)
		var out waitRes
		if err == nil {
			rb, _ := json.Marshal(res)
			json.Unmarshal(rb, &out)
		}
		ch <- out
	}()
	return ch
}

func waiters(d *Daemon) int {
	d.changes.mu.Lock()
	defer d.changes.mu.Unlock()
	return len(d.changes.waiters)
}

// ask-human --wait q1 is a wait on q1 (wait's inbox): an answer to the
// same asker's q2 that comes meanwhile is not taken by it (it stays
// pending and is typed when the asker is free), and q1's answer is.
func TestInboxWaitOnItemLeavesOtherAnswers(t *testing.T) {
	d, fe, _ := inboxSetup(t)
	q1 := ask(t, d, "main", map[string]any{"question": "q1", "kind": "decision", "options": []string{"A", "B"}})
	q2 := ask(t, d, "main", map[string]any{"question": "q2", "kind": "decision", "options": []string{"A", "B"}})
	got := waitAsync(d, agentAs("main"), map[string]any{"on": []string{"inbox"}, "inbox": q1.ID, "timeout_s": 60})
	waitFor(t, func() bool { return waiters(d) == 1 })

	call[any](t, d, "inbox.answer", map[string]any{"id": q2.ID, "rev": q2.Rev, "choice": 1, "note": "q2 は B"})
	if g := inboxGet(t, d, q2.ID); g.Delivery != state.DeliveryPending {
		t.Fatalf("q2's answer: delivery %s; the wait on q%d took it", g.Delivery, q1.ID)
	}
	d.changes.add(Change{Kind: kindReport, Slug: "main", Summary: "main: report done", parent: "main"}) // not an inbox change
	call[any](t, d, "inbox.answer", map[string]any{"id": q1.ID, "rev": q1.Rev, "choice": 0})
	w := <-got
	if len(w.Changes) != 1 || w.Changes[0].Kind != kindInbox || !strings.Contains(w.Changes[0].Summary, ids.Question(q1.ID)+" ") {
		t.Fatalf("the wait on q%d returned %+v", q1.ID, w.Changes)
	}
	if g := inboxGet(t, d, q1.ID); g.Delivery != state.DeliveryWaited {
		t.Fatalf("q1: delivery %s, want waited", g.Delivery)
	}
	d.setState("main", state.Idle, "")
	deliverUntil(t, d, func() bool { return inboxGet(t, d, q2.ID).Delivery == state.DeliveryDelivered })
	if !sentText(fe, "[pekoe inbox "+ids.Question(q2.ID)+"]") {
		t.Fatalf("q2's line not typed: %q", fe.last())
	}
}

// An answer that comes after the asker's inbox.get and before its wait on
// the item takes hold: the wait returns it at once (an item closes once,
// so its change counts from before since), and it is not typed too.
func TestInboxWaitOnItemAnsweredBeforeWait(t *testing.T) {
	d, fe, _ := inboxSetup(t) // main is running: nothing is typed meanwhile
	q := ask(t, d, "main", map[string]any{"question": "q", "kind": "decision", "options": []string{"A", "B"}})
	if g := callAs[state.InboxItem](t, d, agentAs("main"), "inbox.get", map[string]any{"id": q.ID}); g.Status != state.InboxOpen {
		t.Fatal(g.Status)
	}
	call[any](t, d, "inbox.answer", map[string]any{"id": q.ID, "rev": q.Rev, "choice": 0})
	w := callAs[waitRes](t, d, agentAs("main"), "wait", map[string]any{"on": []string{"inbox"}, "inbox": q.ID, "since": "", "timeout_s": 2})
	if w.TimedOut || len(w.Changes) != 1 || w.Changes[0].Kind != kindInbox {
		t.Fatalf("the wait missed the answer given before it: %+v", w)
	}
	if g := inboxGet(t, d, q.ID); g.Delivery != state.DeliveryWaited {
		t.Fatalf("delivery %s, want waited", g.Delivery)
	}
	n := len(fe.sent)
	d.setState("main", state.Idle, "")
	d.deliverAllInbox()
	if s := sentSince(fe, n); len(s) > 0 {
		t.Fatalf("typed as well: %v", s)
	}
}

// pekoed's merge item sent back to a session waits (pending) to be typed
// into that session; when the session goes (rm, or a landed one replaced
// by session.new) the line is never typed: not into the next session of
// the same slug.
func TestInboxSendBackNotTypedIntoSlugReuse(t *testing.T) {
	for _, how := range []string{"rm", "replace-landed"} {
		t.Run(how, func(t *testing.T) {
			d, _, repo := inboxSetup(t)
			old := newSession(t, d, repo, "feat/x")
			d.setState(old.Slug, state.Running, "")
			it := &state.InboxItem{From: callerPekoed, Kind: "approve", About: old.Slug, Question: "evidence ready to merge #42",
				Options: []string{mergeOptionMerge, "wait", "send back"}, Status: state.InboxOpen, Rev: 1,
				CreatedAt: time.Now(), UpdatedAt: time.Now(), Delivery: state.DeliveryNone}
			it.Facts = d.inboxFacts(old.Slug)
			if err := d.DB.AddInbox(it); err != nil {
				t.Fatal(err)
			}
			cur := inboxGet(t, d, it.ID)
			g := call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": it.ID, "rev": cur.Rev, "choice": mergeSendBack, "note": "前のセッションへの差し戻し"})
			if g.Delivery != state.DeliveryPending {
				t.Fatalf("delivery %s", g.Delivery)
			}
			switch how {
			case "rm":
				call[any](t, d, "worktree.rm", map[string]any{"slug": old.Slug, "force": true})
			case "replace-landed":
				d.setState(old.Slug, state.Landed, "")
			}
			nw := newSession(t, d, repo, "feat/x")
			if nw.Slug != old.Slug {
				t.Fatalf("slug %s vs %s", nw.Slug, old.Slug)
			}
			if g := inboxGet(t, d, it.ID); g.Delivery != state.DeliveryNone {
				t.Fatalf("after %s and a new %s: delivery %s, want none (it would be typed into the new session)", how, nw.Slug, g.Delivery)
			}
			d.setState(nw.Slug, state.Idle, "")
			d.deliverAllInbox()
			for _, r := range call[[]state.SendRecord](t, d, "send.log", nil) {
				if r.To == nw.Slug && strings.Contains(r.Text, "差し戻しました") {
					t.Fatalf("the old session's send back was typed into the new %s: %s", nw.Slug, r.Text)
				}
			}
		})
	}
}

// An agent that reads the token (tapi chat, sandbox off) may answer and
// dismiss (the token cannot be kept from it), but the record says it did,
// not a person: precedents are the items a person answered.
func TestInboxAnswerByTokenAgent(t *testing.T) {
	d, _, _ := inboxSetup(t)
	q := ask(t, d, "main", map[string]any{"question": "q", "kind": "decision", "options": []string{"A", "B"}})
	q2 := ask(t, d, "main", map[string]any{"question": "q2", "kind": "decision"})
	q3 := ask(t, d, "main", map[string]any{"question": "q3", "kind": "decision", "options": []string{"A", "B"}})
	tapi := tokenAgent(TapiSlug)
	a := callAs[state.InboxItem](t, d, tapi, "inbox.answer", map[string]any{"id": q.ID, "rev": inboxGet(t, d, q.ID).Rev, "choice": 0})
	x := callAs[state.InboxItem](t, d, tapi, "inbox.dismiss", map[string]any{"id": q2.ID})
	if a.Answer.By != TapiSlug || x.Answer.By != TapiSlug {
		t.Fatalf("tapi's answer / dismissal recorded as by %q / %q, want %q", a.Answer.By, x.Answer.By, TapiSlug)
	}
	// a person (the token outside any agent window) is still a person
	if h := call[state.InboxItem](t, d, "inbox.answer", map[string]any{"id": q3.ID, "rev": q3.Rev, "choice": 1}); h.Answer.By != callerHuman {
		t.Fatalf("a person's answer by %q", h.Answer.By)
	}
}
