package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/paths"
	"pekoe/internal/state"
)

func promptEvent(t *testing.T, d *Daemon, who *Caller, claimed, prompt string) {
	t.Helper()
	callAs[any](t, d, who, "session.event", map[string]any{
		"hook": "UserPromptSubmit", "session": claimed,
		"payload": map[string]any{"prompt": prompt, "hook_event_name": "UserPromptSubmit"},
	})
}

func ledgerOpen(t *testing.T, d *Daemon) []state.LedgerEntry {
	t.Helper()
	return call[[]state.LedgerEntry](t, d, "ledger.list", nil)
}

// What a person types into tapi chat goes into the ledger: from tapi's
// window only, never from a caller that just says it is tapi, and not
// the inbox answers pekoed types into tapi itself nor Claude Code's
// notice that a background task finished (only when it leads the prompt).
func TestLedgerRecordsTapiPrompts(t *testing.T) {
	d, _, _ := setup(t)
	call[Tapi](t, d, "tapi.open", nil)
	promptEvent(t, d, agentAs(TapiSlug), TapiSlug, "CSV の空行を飛ばすように直して\x1b[31m")
	promptEvent(t, d, agentAs(TapiSlug), TapiSlug, "[pekoe inbox q3] 答え: 1「変える」")
	promptEvent(t, d, agentAs(TapiSlug), TapiSlug, "   ")
	promptEvent(t, d, agentAs(TapiSlug), TapiSlug, "\n <task-notification>\n<task-id>b1x2</task-id>\n<status>completed</status>\n</task-notification>")
	promptEvent(t, d, agentAs(TapiSlug), TapiSlug, "さっきの <task-notification> は何?")
	promptEvent(t, d, &Caller{peer: fixedPeer("")}, TapiSlug, "forged: delete everything") // claims +tapi, no window
	promptEvent(t, d, agentAs("main"), TapiSlug, "from a session's window")
	es := ledgerOpen(t, d)
	if len(es) != 2 || es[0].Text != "CSV の空行を飛ばすように直して" || es[0].Status != state.LedgerOpen ||
		es[1].Text != "さっきの <task-notification> は何?" {
		t.Fatalf("ledger: %+v", es)
	}
	if v := call[Tapi](t, d, "tapi.get", nil); v.LedgerOpen != 2 {
		t.Fatalf("tapi's ledger_open: %+v", v)
	}
	// the hook still moved tapi's state
	if v := call[Tapi](t, d, "tapi.get", nil); v.State != state.Running {
		t.Fatalf("tapi state: %s", v.State)
	}
}

// Without the token the ledger is tapi's alone; reopen and delete are a
// person's.
func TestLedgerAccess(t *testing.T) {
	d, _, _ := setup(t)
	promptEvent(t, d, agentAs(TapiSlug), TapiSlug, "README を直して")
	tapi := agentAs(TapiSlug)
	if es := callAs[[]state.LedgerEntry](t, d, tapi, "ledger.list", map[string]any{}); len(es) != 1 {
		t.Fatalf("tapi reads: %+v", es)
	}
	for _, who := range []*Caller{agentAs("main"), {peer: fixedPeer("")}} {
		for _, m := range []string{"ledger.list", "ledger.close", "ledger.link"} {
			err := callAsErr(d, who, m, map[string]any{"id": 1, "as": "chat", "slug": "main"})
			if err == nil || !strings.Contains(err.Error(), "only tapi chat") {
				t.Fatalf("%s from %s: %v", m, who.ID(), err)
			}
		}
	}
	c := dialPipe(t, d)
	for _, m := range []string{"ledger.reopen", "ledger.delete"} {
		if e := c.call(m, map[string]any{"id": 1}, ""); !strings.Contains(e, "only a person") {
			t.Fatalf("%s without the token: %q", m, e)
		}
	}
	if e := c.call("ledger.list", nil, ""); !strings.Contains(e, "only tapi chat") {
		t.Fatalf("ledger.list from outside every window: %q", e)
	}
	if e := c.call("ledger.list", nil, d.humanToken); e != "" {
		t.Fatalf("a person reads: %q", e)
	}
}

func TestLedgerClose(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	for _, s := range []string{"直して", "ありがと", "やっぱりいいや"} {
		promptEvent(t, d, tapi, TapiSlug, s)
	}
	if err := callAsErr(d, tapi, "ledger.close", map[string]any{"id": 3, "as": "dropped"}); err == nil || !strings.Contains(err.Error(), "why") {
		t.Fatalf("dropped without a note: %v", err)
	}
	if err := callAsErr(d, tapi, "ledger.close", map[string]any{"id": 3, "as": "later"}); err == nil {
		t.Fatal("unknown as")
	}
	if err := callAsErr(d, tapi, "ledger.close", map[string]any{"id": 9, "as": "chat"}); err == nil || !strings.Contains(err.Error(), "no such request: r9") {
		t.Fatalf("unknown id: %v", err)
	}
	e := callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 1, "as": "done", "evidence": []string{"https://x/pull/4\n", " "}, "note": "PR 出た"})
	if e.Status != state.LedgerDone || len(e.Evidence) != 1 || e.Evidence[0] != "https://x/pull/4" || e.ClosedAt == nil || e.Note != "PR 出た" {
		t.Fatalf("done: %+v", e)
	}
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 2, "as": "chat"})
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 3, "as": "dropped", "note": "人が取り下げた"})
	if err := callAsErr(d, tapi, "ledger.close", map[string]any{"id": 1, "as": "chat"}); err == nil || !strings.Contains(err.Error(), "done already") {
		t.Fatalf("closing twice: %v", err)
	}
	if es := ledgerOpen(t, d); len(es) != 0 {
		t.Fatalf("open left: %+v", es)
	}
	if es := call[[]state.LedgerEntry](t, d, "ledger.list", map[string]any{"status": "all"}); len(es) != 3 || es[1].Status != state.LedgerChat {
		t.Fatalf("all: %+v", es)
	}
	// a person reopens and deletes
	if e := call[state.LedgerEntry](t, d, "ledger.reopen", map[string]any{"id": 2}); e.Status != state.LedgerOpen || e.ClosedAt != nil {
		t.Fatalf("reopened: %+v", e)
	}
	call[any](t, d, "ledger.delete", map[string]any{"id": 2})
	if es := call[[]state.LedgerEntry](t, d, "ledger.list", map[string]any{"status": "all"}); len(es) != 2 {
		t.Fatalf("after delete: %+v", es)
	}
}

// pekoe new --request ties the session and the request both ways; only
// tapi (or a person) may, and only to an open request.
func TestNewWithRequest(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	tapi := agentAs(TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "CSV を直して")
	promptEvent(t, d, tapi, TapiSlug, "ありがと")
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 2, "as": "chat"})

	if err := callAsErr(d, agentAs("main"), "session.new", map[string]any{"repo": repo, "branch": "feat/a", "request": 1}); err == nil || !strings.Contains(err.Error(), "only tapi chat") {
		t.Fatalf("--request from a session: %v", err)
	}
	if err := callAsErr(d, tapi, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "request": 2}); err == nil || !strings.Contains(err.Error(), "chat already") {
		t.Fatalf("--request to a closed entry: %v", err)
	}
	if err := callAsErr(d, tapi, "session.new", map[string]any{"repo": repo, "branch": "feat/c", "request": 7}); err == nil || !strings.Contains(err.Error(), "no such request: r7") {
		t.Fatalf("--request to no entry: %v", err)
	}
	if ss := call[[]state.Session](t, d, "session.list", nil); len(ss) != 1 {
		t.Fatalf("a refused --request made a session: %+v", ss)
	}
	s := callAs[state.Session](t, d, tapi, "session.new", map[string]any{"repo": repo, "branch": "feat/csv", "prompt": "直す", "request": 1})
	if s.Request != 1 || s.Parent != TapiSlug {
		t.Fatalf("session: %+v", s)
	}
	waitCreated(t, d, s.Slug)
	if es := ledgerOpen(t, d); len(es) != 1 || strings.Join(es[0].Sessions, ",") != "feat-csv" {
		t.Fatalf("entry: %+v", es)
	}
	// ledger.link adds another session once
	callAs[state.LedgerEntry](t, d, tapi, "ledger.link", map[string]any{"id": 1, "slug": "main"})
	e := callAs[state.LedgerEntry](t, d, tapi, "ledger.link", map[string]any{"id": 1, "slug": "main"})
	if strings.Join(e.Sessions, ",") != "feat-csv,main" {
		t.Fatalf("linked: %+v", e)
	}
	if m := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); m.Request != 1 {
		t.Fatalf("main's request: %+v", m)
	}
	if err := callAsErr(d, tapi, "ledger.link", map[string]any{"id": 1, "slug": "nope"}); err == nil {
		t.Fatal("link to no session")
	}
}

// The request that made a session stays its origin when a later one is
// linked (the GUI's Pipeline keeps the branch under it and shows the later
// one as a badge, marked while it is open and the one at work); a session
// made without a request has no origin, whatever is linked to it after.
func TestLinkKeepsOrigin(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	tapi := agentAs(TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "CSV を直して")
	promptEvent(t, d, tapi, TapiSlug, "CSV の見出しも直して")
	callAs[state.LedgerEntry](t, d, tapi, "ledger.keep", map[string]any{"id": 2})
	s := callAs[state.Session](t, d, tapi, "session.new", map[string]any{"repo": repo, "branch": "feat/csv", "prompt": "直す", "request": 1})
	waitCreated(t, d, s.Slug)
	callAs[state.LedgerEntry](t, d, tapi, "ledger.link", map[string]any{"id": 2, "slug": s.Slug})
	callAs[state.LedgerEntry](t, d, tapi, "ledger.link", map[string]any{"id": 2, "slug": "main"})
	got := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if got.Origin != 1 || got.Request != 2 || fmt.Sprint(got.Later) != "[2]" {
		t.Fatalf("feat-csv: %+v", got)
	}
	f := d.webFlowOf(&got, nil)
	if f.Origin != 1 || f.Parent != TapiSlug || fmt.Sprint(f.Later) != "[{2 true}]" {
		t.Fatalf("flow: %+v", f)
	}
	m := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if mf := d.webFlowOf(&m, nil); m.Origin != 0 || mf.Origin != 0 || fmt.Sprint(mf.Later) != "[{2 true}]" {
		t.Fatalf("main: %+v %+v", m, mf)
	}
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 2, "as": "done", "evidence": []string{"https://x/pull/5"}})
	got = call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug})
	if f := d.webFlowOf(&got, nil); fmt.Sprint(f.Later) != "[{2 false}]" {
		t.Fatalf("done: %+v", f.Later)
	}
}

// A session from before Origin was recorded takes the request it had as
// its origin, until a link: then that request stays the origin.
func TestLinkRequestBeforeOrigin(t *testing.T) {
	s := &state.Session{Request: 4}
	if originOf(s) != 4 {
		t.Fatalf("old session: %d", originOf(s))
	}
	linkRequest(s, 9)
	linkRequest(s, 9)
	linkRequest(s, 4)
	if s.Origin != 4 || s.Request != 4 || fmt.Sprint(s.Later) != "[9]" || originOf(s) != 4 {
		t.Fatalf("linked: %+v", s)
	}
	n := &state.Session{}
	linkRequest(n, 3)
	if n.Origin != 0 || originOf(n) != 0 || fmt.Sprint(n.Later) != "[3]" {
		t.Fatalf("no origin: %+v", n)
	}
}

// tapi.reset forgets the conversation, not the ledger.
func TestTapiResetKeepsLedger(t *testing.T) {
	d, _, _ := setup(t)
	call[Tapi](t, d, "tapi.open", nil)
	promptEvent(t, d, agentAs(TapiSlug), TapiSlug, "CSV を直して")
	call[Tapi](t, d, "tapi.reset", nil)
	if es := ledgerOpen(t, d); len(es) != 1 {
		t.Fatalf("after reset: %+v", es)
	}
	v := call[Tapi](t, d, "tapi.get", nil)
	if v.LedgerOpen != 1 || v.InboxOpen != 0 {
		t.Fatalf("tapi after reset: %+v", v)
	}
	// the fresh tapi is told to read the ledger first, and what was left
	ask(t, d, TapiSlug, map[string]any{"question": "どっち?", "kind": "decision", "options": []string{"A", "B"}})
	call[Tapi](t, d, "tapi.open", nil)
	b, err := os.ReadFile(filepath.Join(paths.TapiDir(), "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{"## 最初にすること", "`pekoe --json ledger`", "`pekoe --json inbox`", "open の依頼 1 件、受信箱の未回答 1 件", "--request <依頼の id>", "pekoe-orchestrate", "`pekoe ledger keep`", "`pekoe ledger keep <id>`", "毎回残りを書き直す"} {
		if !strings.Contains(doc, want) {
			t.Errorf("CLAUDE.md lacks %q", want)
		}
	}
	if strings.Contains(doc, "CSV を直して") {
		t.Error("CLAUDE.md carries the ledger's words")
	}
	if !strings.Contains(tapiPersona, "統括役") || !strings.Contains(tapiPersona, "pekoe --json ledger") {
		t.Error("the persona does not say tapi orchestrates")
	}
}

func stopEvent(t *testing.T, d *Daemon, who *Caller, claimed string) {
	t.Helper()
	callAs[any](t, d, who, "session.event", map[string]any{
		"hook": "Stop", "session": claimed,
		"payload": map[string]any{"hook_event_name": "Stop", "last_assistant_message": "…"},
	})
}

func ledgerAll(t *testing.T, d *Daemon) []state.LedgerEntry {
	t.Helper()
	return call[[]state.LedgerEntry](t, d, "ledger.list", map[string]any{"status": "all"})
}

// The same text right after an open entry (a prompt sent twice) is folded
// into it: no new id, and the entry says so. Not after a minute, and not
// into a closed entry.
func TestLedgerFoldsRepeats(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "CSV の空行を飛ばすように直して")
	promptEvent(t, d, tapi, TapiSlug, "  CSV の空行を飛ばすように直して\n")
	promptEvent(t, d, tapi, TapiSlug, "CSV の空行を飛ばすように直して")
	es := ledgerAll(t, d)
	if len(es) != 1 || es[0].Repeats != 2 || es[0].Status != state.LedgerOpen {
		t.Fatalf("folded: %+v", es)
	}
	// another text in between: the repeat is of the entry before the last
	promptEvent(t, d, tapi, TapiSlug, "README も")
	promptEvent(t, d, tapi, TapiSlug, "CSV の空行を飛ばすように直して")
	if es = ledgerAll(t, d); len(es) != 3 || es[2].Repeats != 0 {
		t.Fatalf("not the last entry: %+v", es)
	}
	// the last entry is closed: typed again, it may be asked again
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 3, "as": "chat"})
	promptEvent(t, d, tapi, TapiSlug, "CSV の空行を飛ばすように直して")
	if es = ledgerAll(t, d); len(es) != 4 || es[3].Status != state.LedgerOpen {
		t.Fatalf("after a closed one: %+v", es)
	}
	// later than the window
	was := ledgerRepeatWithin
	ledgerRepeatWithin = -time.Second
	defer func() { ledgerRepeatWithin = was }()
	promptEvent(t, d, tapi, TapiSlug, "CSV の空行を飛ばすように直して")
	if es = ledgerAll(t, d); len(es) != 5 || es[3].Repeats != 0 {
		t.Fatalf("after the window: %+v", es)
	}
}

// What was no request closes itself: tapi's turn ended and it asked nobody
// for anything.
func TestLedgerClosesChatAtTurnEnd(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "ありがと")
	if es := ledgerOpen(t, d); len(es) != 1 {
		t.Fatalf("open while tapi answers: %+v", es)
	}
	stopEvent(t, d, tapi, TapiSlug)
	es := ledgerAll(t, d)
	if len(es) != 1 || es[0].Status != state.LedgerChat || !es[0].Auto || es[0].ClosedAt == nil {
		t.Fatalf("closed as chat by pekoed: %+v", es)
	}
	if v := call[Tapi](t, d, "tapi.get", nil); v.LedgerOpen != 0 {
		t.Fatalf("ledger_open: %+v", v)
	}
	// sent twice in one turn: one entry, closed once
	promptEvent(t, d, tapi, TapiSlug, "マージした")
	promptEvent(t, d, tapi, TapiSlug, "マージした")
	stopEvent(t, d, tapi, TapiSlug)
	if es = ledgerAll(t, d); len(es) != 2 || es[1].Repeats != 1 || es[1].Status != state.LedgerChat || !es[1].Auto {
		t.Fatalf("a repeat in the same turn: %+v", es)
	}
}

// A real request never closes itself: every way a turn can show that it
// works on a request, and every case pekoed cannot judge, leaves it open.
func TestLedgerKeepsRequestsAtTurnEnd(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	tapi := agentAs(TapiSlug)
	n := 0 // the id of the last entry
	prompt := func(text string) int64 {
		t.Helper()
		promptEvent(t, d, tapi, TapiSlug, text)
		n++
		return int64(n)
	}
	open := func(why string, ids ...int64) {
		t.Helper()
		for _, id := range ids {
			if e := ledgerAll(t, d)[id-1]; e.ID != id || e.Status != state.LedgerOpen || e.Auto {
				t.Fatalf("%s: #%d closed itself: %+v", why, id, e)
			}
		}
	}
	turns := []struct {
		why string
		do  func(id int64)
	}{
		{"a session tied to it (pekoe new --request)", func(id int64) {
			s := callAs[state.Session](t, d, tapi, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "request": id})
			waitCreated(t, d, s.Slug)
		}},
		{"a session made without --request", func(int64) {
			s := callAs[state.Session](t, d, tapi, "session.new", map[string]any{"repo": repo, "branch": "feat/b"})
			waitCreated(t, d, s.Slug)
		}},
		{"a session tied later (pekoe ledger link)", func(id int64) {
			callAs[state.LedgerEntry](t, d, tapi, "ledger.link", map[string]any{"id": id, "slug": "main"})
		}},
		{"an instruction to a session (pekoe send)", func(int64) {
			callAs[any](t, d, tapi, "session.send", map[string]any{"slug": "main", "text": "テストも足して", "submit": true})
		}},
		{"a send that failed", func(int64) {
			if err := callAsErr(d, tapi, "session.send", map[string]any{"slug": "nope", "text": "x", "submit": true}); err == nil {
				t.Fatal("send to no session")
			}
		}},
		{"a question to a person (pekoe ask-human)", func(int64) {
			ask(t, d, TapiSlug, map[string]any{"question": "公開していい?", "kind": "approve"})
		}},
		{"a question asked with the human token, from tapi's window", func(int64) {
			who := &Caller{human: true, Via: viaSocket, peer: fixedPeer(TapiSlug)}
			callAs[state.InboxItem](t, d, who, "inbox.post", map[string]any{"question": "gh auth login をお願い", "kind": "login"})
		}},
		{"a wait started (pekoe wait)", func(int64) {
			callAs[any](t, d, tapi, "wait", map[string]any{"mine": true, "on": []string{"report"}, "timeout_s": 1})
		}},
		{"a land (refused here or not)", func(int64) {
			_ = callAsErr(d, tapi, "land", map[string]any{"slug": "main"})
		}},
		{"tapi kept it (pekoe ledger keep)", func(id int64) {
			if e := callAs[state.LedgerEntry](t, d, tapi, "ledger.keep", map[string]any{}); e.ID != id || e.Status != state.LedgerOpen {
				t.Fatalf("keep: %+v", e)
			}
		}},
		{"tapi kept it by id", func(id int64) {
			callAs[state.LedgerEntry](t, d, tapi, "ledger.keep", map[string]any{"id": id})
		}},
	}
	var all []int64
	for _, turn := range turns {
		id := prompt("頼みごと: " + turn.why)
		turn.do(id)
		stopEvent(t, d, tapi, TapiSlug)
		all = append(all, id)
		open(turn.why, id)
	}
	// small talk after them closes itself and touches none of them
	chat := prompt("ありがと")
	stopEvent(t, d, tapi, TapiSlug)
	if e := ledgerAll(t, d)[chat-1]; e.Status != state.LedgerChat || !e.Auto {
		t.Fatalf("small talk: %+v", e)
	}
	open("a later turn's end", all...)

	// a turn that never ended (interrupted): its input is not closed by
	// the next turn's end
	cut := prompt("B も直して")
	next := prompt("やっぱり今のは後で")
	stopEvent(t, d, tapi, TapiSlug)
	open("an interrupted turn", cut)
	if e := ledgerAll(t, d)[next-1]; e.Status != state.LedgerChat {
		t.Fatalf("the turn that did end: %+v", e)
	}

	// a turn pekoed's own typing or a finished background task started:
	// its end closes nothing
	for i, typed := range []string{"[pekoe inbox q3] 答え: 1「変える」", "<task-notification>\n<status>completed</status>\n</task-notification>"} {
		id := prompt(fmt.Sprintf("C%d を直して", i))
		promptEvent(t, d, tapi, TapiSlug, typed)
		stopEvent(t, d, tapi, TapiSlug)
		open("the end of a turn started by "+typed[:12], id)
	}

	// a Stop that only names tapi: not from tapi's window
	id := prompt("D を直して")
	stopEvent(t, d, agentAs("main"), TapiSlug)
	stopEvent(t, d, &Caller{peer: fixedPeer("")}, TapiSlug)
	open("a Stop from outside tapi's window", id)
	stopEvent(t, d, tapi, TapiSlug) // tapi's own: this one was no request
	if e := ledgerAll(t, d)[id-1]; e.Status != state.LedgerChat {
		t.Fatalf("tapi's own Stop: %+v", e)
	}

	// tapi asked for something, then an input came in the middle of the
	// turn: both stay
	first := prompt("E を直して")
	callAs[any](t, d, tapi, "session.send", map[string]any{"slug": "main", "text": "E を直して", "submit": true})
	second := prompt("あと F も")
	stopEvent(t, d, tapi, TapiSlug)
	open("an input in the middle of a turn", first, second)

	// a request an earlier turn left open, sent again: the repeat's turn
	// does not close it
	was := prompt("G を直して")
	callAs[state.LedgerEntry](t, d, tapi, "ledger.keep", map[string]any{})
	stopEvent(t, d, tapi, TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "G を直して")
	stopEvent(t, d, tapi, TapiSlug)
	if e := ledgerAll(t, d)[was-1]; e.Status != state.LedgerOpen || e.Repeats != 1 || len(ledgerAll(t, d)) != n {
		t.Fatalf("a repeat of an earlier turn's request: %+v", e)
	}

	// between turns, what tapi left running in the background calls again
	// (pekoe ask-human --wait asks wait every minute): that says nothing
	// about the next turn's small talk
	callAs[any](t, d, tapi, "wait", map[string]any{"mine": true, "on": []string{"inbox"}, "timeout_s": 1})
	_ = callAsErr(d, tapi, "session.send", map[string]any{"slug": "nope", "text": "x", "submit": true})
	talk := prompt("いま何時?")
	stopEvent(t, d, tapi, TapiSlug)
	if e := ledgerAll(t, d)[talk-1]; e.Status != state.LedgerChat || !e.Auto {
		t.Fatalf("small talk after calls between turns: %+v", e)
	}

	// the conversation ended or started anew in the middle of a turn: a
	// Stop after it closes nothing
	for _, hook := range []string{"SessionEnd", "SessionStart"} {
		id := prompt(hook + " の前の依頼")
		callAs[any](t, d, tapi, "session.event", map[string]any{"hook": hook, "session": TapiSlug, "payload": map[string]any{"hook_event_name": hook}})
		stopEvent(t, d, tapi, TapiSlug)
		open("a Stop after "+hook, id)
	}

	// tapi.reset in the middle of a turn: the conversation is gone, the
	// input stays
	gone := prompt("H を直して")
	call[Tapi](t, d, "tapi.reset", nil)
	stopEvent(t, d, tapi, TapiSlug)
	open("tapi.reset", gone)
}

// What pekoed closed tapi may open again (keep, or tying a session to
// it); what tapi or a person closed only a person reopens.
func TestLedgerKeepReopens(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	tapi := agentAs(TapiSlug)
	for _, s := range []string{"どのリポジトリの話かというと", "CSV を直して", "README を直して", "ありがと", "おやすみ"} {
		promptEvent(t, d, tapi, TapiSlug, s)
		stopEvent(t, d, tapi, TapiSlug)
	}
	for _, e := range ledgerAll(t, d) {
		if e.Status != state.LedgerChat || !e.Auto {
			t.Fatalf("closed by pekoed: %+v", e)
		}
	}
	// no turn is running: nothing to keep without an id
	if err := callAsErr(d, tapi, "ledger.keep", map[string]any{}); err == nil || !strings.Contains(err.Error(), "pekoe ledger keep <id>") {
		t.Fatalf("keep with no input of the turn: %v", err)
	}
	if e := callAs[state.LedgerEntry](t, d, tapi, "ledger.keep", map[string]any{"id": 1}); e.Status != state.LedgerOpen || e.Auto || e.ClosedAt != nil {
		t.Fatalf("keep <id>: %+v", e)
	}
	if e := callAs[state.LedgerEntry](t, d, tapi, "ledger.link", map[string]any{"id": 2, "slug": "main"}); e.Status != state.LedgerOpen || e.Auto || strings.Join(e.Sessions, ",") != "main" {
		t.Fatalf("link: %+v", e)
	}
	s := callAs[state.Session](t, d, tapi, "session.new", map[string]any{"repo": repo, "branch": "feat/readme", "request": 3})
	waitCreated(t, d, s.Slug)
	if e := ledgerAll(t, d)[2]; e.Status != state.LedgerOpen || e.Auto || strings.Join(e.Sessions, ",") != "feat-readme" || s.Request != 3 {
		t.Fatalf("new --request: %+v %+v", e, s)
	}
	// closing one pekoed closed says how to get it back
	if err := callAsErr(d, tapi, "ledger.close", map[string]any{"id": 4, "as": "done"}); err == nil || !strings.Contains(err.Error(), "pekoe ledger keep r4") {
		t.Fatalf("close of an auto one: %v", err)
	}
	// only tapi (or a person)
	if err := callAsErr(d, agentAs("main"), "ledger.keep", map[string]any{"id": 4}); err == nil || !strings.Contains(err.Error(), "only tapi chat") {
		t.Fatalf("keep from a session: %v", err)
	}
	if err := callAsErr(d, tapi, "ledger.keep", map[string]any{"id": 9}); err == nil || !strings.Contains(err.Error(), "no such request: r9") {
		t.Fatalf("keep of no entry: %v", err)
	}
	// what tapi closed itself stays closed for tapi, however it tries
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 1, "as": "chat"})
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 2, "as": "done"})
	for id := 1; id <= 2; id++ {
		for m, p := range map[string]map[string]any{
			"ledger.keep": {"id": id},
			"ledger.link": {"id": id, "slug": "main"},
			"session.new": {"repo": repo, "branch": "feat/again", "request": id},
		} {
			if err := callAsErr(d, tapi, m, p); err == nil || !strings.Contains(err.Error(), "a person can reopen it") {
				t.Fatalf("%s of #%d, closed by tapi: %v", m, id, err)
			}
		}
	}
	// a person reopens anything; the mark goes
	if e := call[state.LedgerEntry](t, d, "ledger.reopen", map[string]any{"id": 5}); e.Status != state.LedgerOpen || e.Auto {
		t.Fatalf("a person reopens an auto one: %+v", e)
	}
	if e := call[state.LedgerEntry](t, d, "ledger.reopen", map[string]any{"id": 1}); e.Status != state.LedgerOpen {
		t.Fatalf("a person reopens tapi's: %+v", e)
	}
}

// What tapi closed itself in the turn stays as tapi closed it: the turn's
// end does not turn it into pekoed's guess, which tapi could then undo.
func TestLedgerTurnEndLeavesTapisClose(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	for i, as := range []string{state.LedgerDone, state.LedgerDropped, state.LedgerChat} {
		id := i + 1
		promptEvent(t, d, tapi, TapiSlug, "頼みごと "+as)
		callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": id, "as": as, "note": "その場で済んだ"})
		stopEvent(t, d, tapi, TapiSlug)
		if e := ledgerAll(t, d)[i]; e.Status != as || e.Auto || e.Note != "その場で済んだ" {
			t.Fatalf("closed %s by tapi, after the turn's end: %+v", as, e)
		}
		if err := callAsErr(d, tapi, "ledger.keep", map[string]any{"id": id}); err == nil || !strings.Contains(err.Error(), "a person can reopen it") {
			t.Fatalf("keep of what tapi closed as %s: %v", as, err)
		}
	}
}

// Over the socket, from tapi's window: a send that authorize turns down
// (text that starts with a digit) is still tapi asking a session for
// something, so the turn's input stays open.
func TestLedgerKeepsRequestOnRefusedSend(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.panes[TapiSlug+"~agent"] = os.Getpid()
	pid := childOfTest(t)
	d.peerPID = func(net.Conn) int { return pid }
	c := dialPipe(t, d)
	event := func(hook, prompt string) {
		t.Helper()
		if e := c.call("session.event", map[string]any{"hook": hook, "session": TapiSlug, "payload": map[string]any{"prompt": prompt, "hook_event_name": hook}}, ""); e != "" {
			t.Fatalf("%s: %s", hook, e)
		}
	}
	event("UserPromptSubmit", "main に 2 番の案でと伝えて")
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "2 番の案で", "submit": true}, ""); !strings.Contains(e, "may not start with a digit") {
		t.Fatalf("a send authorize refuses: %q", e)
	}
	event("Stop", "")
	if es := ledgerAll(t, d); len(es) != 1 || es[0].Status != state.LedgerOpen {
		t.Fatalf("after a refused send: %+v", es)
	}
	// the same window, nothing asked: closed
	event("UserPromptSubmit", "ありがと")
	event("Stop", "")
	if es := ledgerAll(t, d); len(es) != 2 || es[1].Status != state.LedgerChat || !es[1].Auto {
		t.Fatalf("small talk over the socket: %+v", es)
	}
}

// What pekoed closed itself stays in the list tapi reads first for a day
// (ledger.list with auto): apart from the open ones, the newest 10, each
// text cut short, the rest counted. Closed 23 hours ago is in, 25 hours
// ago is not. Open alone is as before, and a request among them opened
// again is an open one.
func TestLedgerListShowsLatelyAuto(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	long := strings.Repeat("あ", 300)
	texts := []string{"B が済んだら C も直して", "ありがと", long}
	for i := 0; i < 11; i++ {
		texts = append(texts, fmt.Sprintf("雑談 %d", i))
	}
	for _, s := range texts { // #1..#14, all closed by pekoed
		promptEvent(t, d, tapi, TapiSlug, s)
		stopEvent(t, d, tapi, TapiSlug)
	}
	promptEvent(t, d, tapi, TapiSlug, "CSV を直して") // #15, kept
	callAs[state.LedgerEntry](t, d, tapi, "ledger.keep", map[string]any{})
	stopEvent(t, d, tapi, TapiSlug)
	closedAgo := func(id int64, ago time.Duration) {
		t.Helper()
		e, _ := d.DB.LedgerGet(id)
		at := time.Now().Add(-ago)
		e.ClosedAt = &at
		if err := d.DB.SaveLedger(e); err != nil {
			t.Fatal(err)
		}
	}
	closedAgo(1, 23*time.Hour) // still shown
	closedAgo(2, 25*time.Hour) // not any more
	view := func() state.LedgerView {
		t.Helper()
		return callAs[state.LedgerView](t, d, tapi, "ledger.list", map[string]any{"auto": true})
	}
	ids := func(es []state.LedgerEntry) (out []int64) {
		for _, e := range es {
			out = append(out, e.ID)
		}
		return
	}
	if es := callAs[[]state.LedgerEntry](t, d, tapi, "ledger.list", map[string]any{}); len(es) != 1 || es[0].ID != 15 {
		t.Fatalf("open alone: %+v", es)
	}
	v := view()
	// shown lately: #1 and #3..#14 (13); the newest 10 are #5..#14
	if got := ids(v.Open); len(got) != 1 || got[0] != 15 {
		t.Fatalf("open: %v", got)
	}
	if got := ids(v.AutoClosed); len(got) != ledgerAutoMax || got[0] != 5 || got[9] != 14 || v.AutoClosedMore != 3 {
		t.Fatalf("auto closed, newest 10: %v · more %d", got, v.AutoClosedMore)
	}
	for _, e := range v.AutoClosed {
		if e.Status != state.LedgerChat || !e.Auto || e.Cut {
			t.Fatalf("auto closed: %+v", e)
		}
	}
	// 23 hours ago is in, 25 hours ago is not: with fewer than 10 every
	// one shows (#1 in, #2 out, #3 and #4 closed just now)
	for id := int64(5); id <= 14; id++ {
		callAs[state.LedgerEntry](t, d, tapi, "ledger.keep", map[string]any{"id": id})
	}
	v = view()
	if got := ids(v.AutoClosed); len(got) != 3 || got[0] != 1 || got[1] != 3 || got[2] != 4 || v.AutoClosedMore != 0 {
		t.Fatalf("23h in, 25h out: %v · more %d", got, v.AutoClosedMore)
	}
	// a long text is cut to 200 characters; the entry itself is whole
	if e := v.AutoClosed[1]; !e.Cut || []rune(e.Text)[ledgerAutoText] != '…' || len([]rune(e.Text)) != ledgerAutoText+1 {
		t.Fatalf("cut: %d characters, cut %v", len([]rune(e.Text)), e.Cut)
	}
	if es := callAs[[]state.LedgerEntry](t, d, tapi, "ledger.list", map[string]any{"status": "all", "since": 2}); es[0].ID != 3 || es[0].Text != long || es[0].Cut {
		t.Fatalf("the entry by id is whole: %+v", es[0])
	}
	if got := ids(v.Open); len(got) != 11 || got[0] != 5 {
		t.Fatalf("opened again: %v", got)
	}
	if tv := call[Tapi](t, d, "tapi.get", nil); tv.LedgerOpen != 11 {
		t.Fatalf("ledger_open counts the open ones only: %+v", tv)
	}
	// all is all, auto or not, as a list
	if es := callAs[[]state.LedgerEntry](t, d, tapi, "ledger.list", map[string]any{"status": "all", "auto": true}); len(es) != 15 {
		t.Fatalf("all: %d", len(es))
	}
}

// ledger.keep keeps the entry it was given in its turn only: a turn that
// never ended (interrupted) does not hand it to the next turn's input.
func TestLedgerKeepStaysInItsTurn(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "A を後でやって")
	callAs[state.LedgerEntry](t, d, tapi, "ledger.keep", map[string]any{})
	// interrupted (no Stop); the next turn is small talk
	promptEvent(t, d, tapi, TapiSlug, "ありがと")
	stopEvent(t, d, tapi, TapiSlug)
	es := ledgerAll(t, d)
	if es[0].Status != state.LedgerOpen {
		t.Fatalf("the kept request closed: %+v", es[0])
	}
	if es[1].Status != state.LedgerChat || !es[1].Auto {
		t.Fatalf("the next turn's small talk stayed open: %+v", es[1])
	}
}

// `pekoe ask-human --wait` calls wait again every minute, marked rewait;
// one of those that falls inside a turn is not tapi starting a wait.
// Its first wait (and an unmarked one, from an older CLI) still counts.
func TestLedgerRewaitInsideTurn(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "いま何時?")
	callAs[any](t, d, tapi, "wait", map[string]any{"on": []string{"inbox"}, "since": "", "timeout_s": 1, "rewait": true})
	stopEvent(t, d, tapi, TapiSlug)
	if e := ledgerAll(t, d)[0]; e.Status != state.LedgerChat || !e.Auto {
		t.Fatalf("small talk with a wait called again in its turn stayed open: %+v", e)
	}
	promptEvent(t, d, tapi, TapiSlug, "q3 の答えを待って続けて")
	callAs[any](t, d, tapi, "wait", map[string]any{"on": []string{"inbox"}, "timeout_s": 1})
	stopEvent(t, d, tapi, TapiSlug)
	if e := ledgerAll(t, d)[1]; e.Status != state.LedgerOpen {
		t.Fatalf("a wait started in the turn did not count: %+v", e)
	}
}

// What a person reopened is a request: the end of the turn whose input
// it is does not close it again by pekoed's guess.
func TestLedgerReopenInTurnStaysOpen(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "B を直して")
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 1, "as": "chat"})
	call[state.LedgerEntry](t, d, "ledger.reopen", map[string]any{"id": 1}) // a person: "it is a request"
	stopEvent(t, d, tapi, TapiSlug)
	if e := ledgerAll(t, d)[0]; e.Status != state.LedgerOpen {
		t.Fatalf("a request a person reopened closed itself: %+v", e)
	}
	// the next turn's small talk still closes
	promptEvent(t, d, tapi, TapiSlug, "ありがと")
	stopEvent(t, d, tapi, TapiSlug)
	if e := ledgerAll(t, d)[1]; e.Status != state.LedgerChat || !e.Auto {
		t.Fatalf("the next turn's small talk: %+v", e)
	}
}

// Evidence kept across a reopen counts toward the limit of 20: the same
// entries given again are not added twice, more than 20 in all is refused.
func TestLedgerEvidenceLimitAcrossReopen(t *testing.T) {
	d, _, _ := setup(t)
	tapi := agentAs(TapiSlug)
	promptEvent(t, d, tapi, TapiSlug, "C を直して")
	evs := func(from, n int) []string {
		ev := []string{}
		for i := from; i < from+n; i++ {
			ev = append(ev, fmt.Sprintf("https://x/%d", i))
		}
		return ev
	}
	for i := 0; i < 2; i++ {
		callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 1, "as": "done", "evidence": evs(0, 20)})
		call[state.LedgerEntry](t, d, "ledger.reopen", map[string]any{"id": 1})
	}
	if e := ledgerAll(t, d)[0]; len(e.Evidence) != 20 {
		t.Fatalf("the same evidence given again: %d entries", len(e.Evidence))
	}
	err := callAsErr(d, tapi, "ledger.close", map[string]any{"id": 1, "as": "done", "evidence": evs(20, 1)})
	if err == nil || !strings.Contains(err.Error(), "at most 0 new") {
		t.Fatalf("a 21st entry: %v", err)
	}
	if e := ledgerAll(t, d)[0]; e.Status != state.LedgerOpen || len(e.Evidence) != 20 {
		t.Fatalf("after a refused close: %+v", e)
	}
	// closing without new evidence still works
	callAs[state.LedgerEntry](t, d, tapi, "ledger.close", map[string]any{"id": 1, "as": "done"})

	// an entry over the limit already (saved before the limit counted what
	// a reopen kept) still closes without new evidence
	call[state.LedgerEntry](t, d, "ledger.reopen", map[string]any{"id": 1})
	e, err := d.DB.LedgerGet(1)
	if err != nil {
		t.Fatal(err)
	}
	e.Evidence = evs(0, 25)
	if err := d.DB.SaveLedger(e); err != nil {
		t.Fatal(err)
	}
	if err := callAsErr(d, tapi, "ledger.close", map[string]any{"id": 1, "as": "done", "evidence": evs(0, 3)}); err != nil {
		t.Fatalf("evidence it has already, over the limit: %v", err)
	}
	if e := ledgerAll(t, d)[0]; e.Status != state.LedgerDone || len(e.Evidence) != 25 {
		t.Fatalf("an entry over the limit: %+v", e)
	}
}

func preToolEvent(t *testing.T, d *Daemon, who *Caller, command string) {
	t.Helper()
	callAs[any](t, d, who, "session.event", map[string]any{
		"hook": "PreToolUse", "session": TapiSlug,
		"payload": map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": command}},
	})
}

// Claude Code runs its Stop hooks side by side: when another one blocks
// tapi's stop (exit 2, decision "block"), pekoe's Stop has come all the
// same, and the turn goes on (PreToolUse from tapi's window) to end with a
// Stop that says stop_hook_active. What tapi asks for or keeps in that
// part counts as in any turn: the input the first Stop closed as chat
// opens again, and `pekoe ledger keep` (no id) is not refused. A turn that
// goes on with small talk only stays chat, and the next prompt's turn
// does not take up the one before it.
func TestLedgerStopHookBlockedTurnGoesOn(t *testing.T) {
	stopAgain := func(t *testing.T, d *Daemon, tapi *Caller) {
		t.Helper()
		callAs[any](t, d, tapi, "session.event", map[string]any{"hook": "Stop", "session": TapiSlug,
			"payload": map[string]any{"hook_event_name": "Stop", "stop_hook_active": true, "last_assistant_message": "…"}})
	}
	for _, tc := range []struct {
		name    string
		goOn    func(t *testing.T, d *Daemon, tapi *Caller)
		stopped bool // the part that went on ended with a Stop
		want    string
	}{
		{"send", func(t *testing.T, d *Daemon, tapi *Caller) {
			preToolEvent(t, d, tapi, "pekoe send main 'README の誤字を直して'")
			callAs[any](t, d, tapi, "session.send", map[string]any{"slug": "main", "text": "README の誤字を直して", "submit": true})
		}, true, state.LedgerOpen},
		{"keep", func(t *testing.T, d *Daemon, tapi *Caller) {
			preToolEvent(t, d, tapi, "pekoe ledger keep")
			if err := callAsErr(d, tapi, "ledger.keep", map[string]any{}); err != nil {
				t.Fatalf("keep in the turn that went on: %v", err)
			}
		}, true, state.LedgerOpen},
		{"send, then interrupted", func(t *testing.T, d *Daemon, tapi *Caller) {
			preToolEvent(t, d, tapi, "pekoe send main 'README の誤字を直して'")
			callAs[any](t, d, tapi, "session.send", map[string]any{"slug": "main", "text": "README の誤字を直して", "submit": true})
		}, false, state.LedgerOpen},
		{"small talk", func(t *testing.T, d *Daemon, tapi *Caller) {
			preToolEvent(t, d, tapi, "pekoe --json ls")
		}, true, state.LedgerChat},
		{"blocked twice, then send", func(t *testing.T, d *Daemon, tapi *Caller) {
			preToolEvent(t, d, tapi, "pekoe --json ls")
			stopAgain(t, d, tapi) // blocked once more
			preToolEvent(t, d, tapi, "pekoe send main 'README の誤字を直して'")
			callAs[any](t, d, tapi, "session.send", map[string]any{"slug": "main", "text": "README の誤字を直して", "submit": true})
		}, true, state.LedgerOpen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, repo := setup(t)
			call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
			tapi := agentAs(TapiSlug)
			promptEvent(t, d, tapi, TapiSlug, "README の誤字を直して")
			stopEvent(t, d, tapi, TapiSlug) // another Stop hook blocks it
			tc.goOn(t, d, tapi)
			if tc.stopped {
				stopAgain(t, d, tapi)
			}
			if e := ledgerAll(t, d)[0]; e.Status != tc.want || e.Auto != (tc.want == state.LedgerChat) {
				t.Fatalf("the input after a turn that went on (%s): %s (auto %v), want %s", tc.name, e.Status, e.Auto, tc.want)
			}
		})
	}
	t.Run("next prompt", func(t *testing.T) {
		d, _, repo := setup(t)
		call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
		tapi := agentAs(TapiSlug)
		promptEvent(t, d, tapi, TapiSlug, "ありがと")
		stopEvent(t, d, tapi, TapiSlug)
		promptEvent(t, d, tapi, TapiSlug, "README の誤字を直して")
		preToolEvent(t, d, tapi, "pekoe send main 'README の誤字を直して'")
		callAs[any](t, d, tapi, "session.send", map[string]any{"slug": "main", "text": "README の誤字を直して", "submit": true})
		stopEvent(t, d, tapi, TapiSlug)
		es := ledgerAll(t, d)
		if es[0].Status != state.LedgerChat || !es[0].Auto || es[1].Status != state.LedgerOpen {
			t.Fatalf("the earlier turn's small talk %s, this turn's request %s; want chat and open", es[0].Status, es[1].Status)
		}
	})
}
