package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/client"
	"pekoe/internal/state"
)

// hostile is agent text with an escape that would clear the screen, a
// bidi override and HTML.
const hostile = "<script>alert(1)</script>\x1b[2J‮x"

// dashModel is a model on the tapi row with a fake pekoed that answers the
// dashboard's reads.
func dashModel(t *testing.T) (*model, *fakePekoed) {
	t.Helper()
	m, f, _ := chatModel(t)
	now := time.Now()
	choice := 1
	f.reply = func(method string) any {
		switch method {
		case "ledger.list":
			return []state.LedgerEntry{{ID: 3, At: now.Add(-time.Hour), Text: "CSV の空行を飛ばして " + hostile, Status: "open", Sessions: []string{"feat-csv"}}}
		case "send.log":
			return []state.SendRecord{
				{ID: 41, At: now.Add(-time.Minute), From: "+tapi", To: "feat-csv", Kind: "send", Text: "テストも足して\n二行目 " + hostile, OK: true},
				{ID: 40, At: now.Add(-2 * time.Minute), From: "+tapi", To: "feat-csv", Kind: "new", Text: "CSV を直す", OK: false, Error: "permission dialog"},
			}
		case "inbox.list":
			return []state.InboxItem{
				{ID: 15, Kind: "fyi", From: "+tapi", Status: state.InboxAnswered, Question: "PR を 2 つに分けた " + hostile, Options: []string{"1 つ", "2 つ"},
					Answer: &state.InboxAnswer{Choice: &choice, By: "+tapi"}, CreatedAt: now},
				{ID: 9, Kind: "decision", From: "feat-x", Status: state.InboxAnswered, Question: "answered, not fyi"},
			}
		case "merge.check":
			return []state.MergeCheck{{Slug: "feat-csv", PR: 42, Conds: []state.MergeCond{
				{Name: "land", OK: true, Detail: "pekoe land 済み(PR #42)"},
				{Name: "review", OK: false, Detail: "独立レビューの報告がない"},
				{Name: "ci", OK: true, Detail: "5 件すべて成功"},
			}, Missing: []string{"review"}}}
		}
		return nil
	}
	m.sessions = []*state.Session{
		{Slug: "feat-csv", Branch: "feat/csv", State: state.Idle, Parent: "+tapi", Request: 3, PR: 42, Landing: true,
			PRStatus: &state.PRStatus{State: "OPEN", Pass: 5}, Report: &state.ReportSummary{ID: 7, Status: "done", Summary: "直した " + hostile}},
		{Slug: "fix-ci", Branch: "fix/ci", State: state.Running, Parent: "human"},
		{Slug: "review-pr", Branch: "review", State: state.Idle, PRReview: true, PR: 7},
	}
	m.inbox = []state.InboxItem{
		{ID: 12, Rev: 1, Kind: "decision", From: "feat-csv", About: "feat-csv", Status: state.InboxOpen, Question: "ページングを変えてよい? " + hostile,
			Options: []string{"変える", "今のまま"}, Links: []string{"https://github.com/me/app/pull/42"}, CreatedAt: now},
		{ID: 13, Rev: 1, Kind: "approve", From: "pekoed", About: "feat-csv", Status: state.InboxOpen, Question: "#42 をマージしてよい根拠が揃いました",
			Options: []string{"マージした", "待つ", "差し戻す"}, CreatedAt: now},
		{ID: 14, Rev: 1, Kind: "login", From: "fix-ci", Status: state.InboxOpen, Question: "gh auth login をお願いします", Options: []string{"済んだ", "できない"}, CreatedAt: now},
	}
	m.focus, m.sel = pSessions, 0
	runCmd(m, m.onKey(key("k"))) // the first session → the tapi row
	if !m.onTapiRow() {
		t.Fatalf("k: sel %d", m.sel)
	}
	return m, f
}

// Every section draws from what the fake pekoed answers; the agents'
// words are drawn as text (no escape, no bidi override), in order.
func TestDashboardSections(t *testing.T) {
	m, f := dashModel(t)
	for _, meth := range []string{"ledger.list", "send.log", "inbox.list", "merge.check"} {
		if len(f.find(meth)) == 0 {
			t.Errorf("selecting the tapi row did not read %s", meth)
		}
	}
	raw := strings.Join(m.viewAgent(120, 60), "\n")
	if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "‮") {
		t.Fatalf("an agent's escape or bidi override was drawn: %q", raw)
	}
	out := ansi.Strip(raw)
	order := []string{"Dashboard · tapi the orchestrator", "Overview", "1 running", "1 to merge", "1 PR open", "3 in the inbox", "1 request",
		"Inbox", "login", "#14", "decide", "#12 feat-csv", "ページングを変えてよい? <script>alert(1)</script>", "merge ready", "#13 pekoed",
		"Pipeline", "from tapi · request #3 CSV の空行", "feat-csv", "impl ✓ › PR ✓ › CI ✓ › review · › merge ·", "#42", "needs review",
		"from you", "fix-ci",
		"Requests", "#3 CSV の空行を飛ばして <script>", "→ feat-csv",
		"Sends", "+tapi → feat-csv send", "テストも足して", "failed", "CSV を直す",
		"Decided", "#15 +tapi", "PR を 2 つに分けた", "→ 2 つ"}
	at := 0
	for _, want := range order {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order (after byte %d):\n%s", want, at, out)
		}
		at += i + len(want)
	}
	if strings.Contains(out, "review-pr") || strings.Contains(out, "answered, not fyi") || strings.Contains(out, "二行目") {
		t.Errorf("someone else's PR, a non-fyi answer or a folded line shown:\n%s", out)
	}
}

// j/k move over the rows that can be chosen; ↵ on an inbox item opens the
// answer modal, on another row unfolds it; o opens nothing without a link.
func TestDashboardKeys(t *testing.T) {
	m, _ := dashModel(t)
	runCmd(m, m.onKey(key("4")))
	if !m.dashShown() || m.focus != pAgent {
		t.Fatal("[4] on the tapi row is not the dashboard")
	}
	if name, _ := m.modeName(); name != "DASHBOARD" {
		t.Errorf("mode %q", name)
	}
	m.viewAgent(120, 60)
	if m.dash.sel != "inbox:14" { // the first row: login comes first
		t.Fatalf("first row: %q", m.dash.sel)
	}
	runCmd(m, m.onKey(key("j")))
	runCmd(m, m.onKey(key("enter")))
	im, ok := m.modal.(*inboxModal)
	if !ok || im.it.ID != 12 {
		t.Fatalf("↵ on #12: %T", m.modal)
	}
	m.modal = nil
	for m.dash.sel != "send:41" {
		before := m.dash.sel
		runCmd(m, m.onKey(key("j")))
		if m.dash.sel == before {
			t.Fatalf("j stuck at %q", before)
		}
	}
	runCmd(m, m.onKey(key("enter")))
	out := ansi.Strip(strings.Join(m.viewAgent(120, 60), "\n"))
	if !strings.Contains(out, "二行目 <script>alert(1)</script>") {
		t.Fatalf("↵ did not unfold the send:\n%s", out)
	}
	runCmd(m, m.onKey(key("enter")))
	if out := ansi.Strip(strings.Join(m.viewAgent(120, 60), "\n")); strings.Contains(out, "二行目") {
		t.Fatal("↵ again did not fold it")
	}
	if runCmd(m, m.onKey(key("o"))); !strings.Contains(m.msg, "no http(s) link") {
		t.Errorf("o on a send: %q", m.msg)
	}
	runCmd(m, m.onKey(key("h")))
	if m.focus != pSessions {
		t.Fatal("h does not go back to Sessions")
	}
	// ↵ on the tapi row in [1] still opens tapi chat
	runCmd(m, m.onKey(key("enter")))
	if !m.chat.open {
		t.Fatal("↵ on the tapi row does not open tapi chat")
	}
}

// Another session's row shows that session, not the dashboard.
func TestDashboardOnlyOnTapiRow(t *testing.T) {
	m, _ := dashModel(t)
	runCmd(m, m.onKey(key("j")))
	if m.dashShown() {
		t.Fatal("dashboard on a session's row")
	}
	if out := checkSize(t, m, "session"); strings.Contains(out, "Dashboard") || !strings.Contains(out, "Agent feat/csv") {
		t.Fatalf("a session's row:\n%s", out)
	}
}

// The dashboard keeps every line to the terminal's width, wide and narrow,
// with rows unfolded.
func TestDashboardWidths(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {100, 30}, {99, 30}, {80, 24}, {60, 20}, {40, 16}} {
		label := fmt.Sprintf("dashboard %dx%d", size[0], size[1])
		m, _ := dashModel(t)
		m.width, m.height = size[0], size[1]
		m.focus = pAgent
		m.dash.open = map[string]bool{"s:feat-csv": true, "req:3": true, "send:41": true, "fyi:15": true}
		checkSize(t, m, label)
		for range 30 {
			m.onKey(key("j"))
		}
		checkSize(t, m, label+" scrolled")
	}
}

// A change while the dashboard reads is not lost; a new report or PR
// summary on a session marks it to be read again (merge.check reads them).
func TestDashboardStale(t *testing.T) {
	m, _ := dashModel(t)
	m.dash.at = time.Now()
	cmd := m.loadDash()
	m.dash.stale = true // a ledger event while reading
	m.Update(cmd())
	if !m.dash.stale {
		t.Fatal("the change during the read was dropped")
	}
	m.dash.stale = false
	s := *m.sessions[1]
	s.Report = &state.ReportSummary{ID: 99, Status: "done"}
	m.onEvent(sessionEv(t, "session.updated", &s))
	if !m.dash.stale {
		t.Fatal("a new report did not mark the dashboard stale")
	}
	m.dash.stale = false
	m.onEvent(sessionEv(t, "session.updated", &s))
	if m.dash.stale {
		t.Fatal("the same session again marked it stale")
	}
}

func sessionEv(t *testing.T, name string, s *state.Session) client.Event {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return client.Event{Event: name, Data: b}
}
