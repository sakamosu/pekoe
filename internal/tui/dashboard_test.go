package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"golang.org/x/text/width"

	"pekoe/internal/client"
	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/state"
	"pekoe/internal/theme"
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
		case "stats":
			return dashStatsReply(now)
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
	order := []string{"Dashboard · tapi the orchestrator", "Overview", "3 questions to answer", "1 running", "1 to merge", "1 PR open", "1 request",
		"Inbox", "3 open", "login", "#14", "decide", "#12 feat-csv", "ページングを変えてよい? <script>alert(1)</script>", "merge ready", "#13 pekoed",
		"Pipeline", "impl › review › PR › CI › merge", "from tapi · #3 CSV の空行", "feat-csv", "✓·✓✓·", "needs review", "#42", "独立レビューの報告がない",
		"from you", "fix-ci", "working",
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
	for _, look := range []struct{ color, icons string }{{"none", "nerd"}, {"truecolor", "unicode"}, {"ansi", "ascii"}} {
		for _, size := range [][2]int{{150, 45}, {140, 40}, {100, 30}, {99, 30}, {80, 24}, {60, 20}, {40, 16}} {
			dashWidths(t, look.color, look.icons, size)
		}
	}
}

func dashWidths(t *testing.T, color, icons string, size [2]int) {
	{
		label := fmt.Sprintf("dashboard %s %s %dx%d", color, icons, size[0], size[1])
		m, _ := dashModel(t)
		dashLook(t, m, color, icons)
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

// dashLook paints the model in a color mode and icon set.
func dashLook(t *testing.T, m *model, color, icons string) {
	t.Helper()
	t.Setenv("PEKOE_COLOR", color)
	m.th = theme.Load(&config.Theme{})
	m.ic = theme.Icons(icons)
}

// dashPipelineModel adds what the pipeline sorts out: a repository's own
// checkout (main), a merged session, a session that reviewed an older
// commit while CI still runs, and an idle one without a report.
func dashPipelineModel(t *testing.T) *model {
	t.Helper()
	m, f := dashModel(t)
	base := f.reply
	f.reply = func(method string) any {
		if method == "merge.check" {
			return append(base(method).([]state.MergeCheck), state.MergeCheck{Slug: "fix-stale", PR: 121, Missing: []string{"review", "ci", "merge_state"}, Conds: []state.MergeCond{
				{Name: "land", OK: true, Detail: "pekoe land 済み(PR #121)"},
				{Name: "review", OK: false, Detail: "レビューした 17b962a と PR の HEAD ea9697f が違う"},
				{Name: "ci", OK: false, Detail: "2 件が終わっていない"},
				{Name: "merge_state", OK: false, Detail: "merge_state が BLOCKED"},
			}})
		}
		return base(method)
	}
	m.sessions = append(m.sessions,
		&state.Session{Slug: "merged-one", State: state.Landed, Parent: "+tapi", PR: 5, Landing: true, PRStatus: &state.PRStatus{State: "MERGED"}},
		&state.Session{Slug: "repo-main", State: state.Running, Path: "/r/app", RepoDir: "/r/app"},
		&state.Session{Slug: "fix-stale", State: state.Idle, Parent: "+tapi", PR: 121, Landing: true, PRStatus: &state.PRStatus{State: "OPEN", Pass: 3, Pending: 2},
			Report: &state.ReportSummary{ID: 68, Status: "done"}, Path: "/w/stale", RepoDir: "/r/app"},
		&state.Session{Slug: "quiet-one", State: state.Idle, Path: "/w/quiet", RepoDir: "/r/app"},
	)
	m.dash = dashState{}
	runCmd(m, m.loadDash())
	return m
}

// The pipeline leaves out a repository's own checkout, puts merged
// sessions last in a quiet group of their own, says where each session is
// in words, and gives pekoed's reason for a missing condition the phrase
// does not say (a review of an older commit is not "needs review").
func TestDashboardPipeline(t *testing.T) {
	m := dashPipelineModel(t)
	out := ansi.Strip(strings.Join(m.viewAgent(150, 60), "\n"))
	if strings.Contains(out, "repo-main") {
		t.Errorf("a repository's own checkout is in the pipeline:\n%s", out)
	}
	order := []string{"Pipeline", "feat-csv", "needs review", "fix-stale", "CI running", "#121", "review: レビューした 17b962a と PR の HEAD ea9697f が違う",
		"fix-ci", "other", "quiet-one", "no report yet", "merged · 1", "merged-one", "merged", "#5", "Requests"}
	at := 0
	for _, want := range order {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order (after byte %d):\n%s", want, at, out)
		}
		at += i + len(want)
	}
	// the overview still counts what the checkout's agent does
	if !strings.Contains(out, "2 running") {
		t.Errorf("overview:\n%s", out)
	}
	// rows are chosen in the order they are drawn: merged last
	var slugs []string
	for _, it := range m.dashItems() {
		if it.slug != "" {
			slugs = append(slugs, it.slug)
		}
	}
	if got := strings.Join(slugs, " "); got != "feat-csv fix-stale fix-ci quiet-one merged-one" {
		t.Errorf("pipeline rows: %s", got)
	}
}

// With color and a picture-friendly icon set the stages are a bar (▰ ▱)
// and the sections carry emoji; the ascii icon set gets ASCII marks and no
// emoji; without color the bar becomes marks (it could not tell done from
// failing). Every emoji is two cells (East Asian Wide).
func TestDashboardGlyphs(t *testing.T) {
	m := dashPipelineModel(t)
	dashLook(t, m, "truecolor", "unicode")
	out := ansi.Strip(strings.Join(m.viewAgent(150, 60), "\n"))
	for _, want := range []string{"🧭 Overview", "📬 Inbox", "🚦 Pipeline", "▰▰▱▱▰▰▰▰▱▱  👀 needs review", "🎉 merged · 1", "📨 Sends", "💡 Decided"} {
		if !strings.Contains(out, want) {
			t.Errorf("truecolor unicode: %q missing:\n%s", want, out)
		}
	}
	for _, r := range out {
		if r >= 0x1F000 && width.LookupRune(r).Kind() != width.EastAsianWide {
			t.Errorf("%q (U+%04X) is not East Asian Wide", r, r)
		}
	}

	dashLook(t, m, "ansi", "ascii")
	out = ansi.Strip(strings.Join(m.viewAgent(150, 60), "\n"))
	if !strings.Contains(out, "v.vv.  needs review") {
		t.Errorf("ascii: no ASCII marks:\n%s", out)
	}
	for _, r := range out {
		if r >= 0x1F000 || r == '▰' || r == '▱' {
			t.Fatalf("ascii: %q drawn:\n%s", r, out)
		}
	}

	dashLook(t, m, "none", "unicode")
	if out = ansi.Strip(strings.Join(m.viewAgent(150, 60), "\n")); !strings.Contains(out, "✓·✓✓·  👀 needs review") {
		t.Errorf("no color: no marks:\n%s", out)
	}
}

// A nothing-to-do inbox and an empty pipeline say so, kindly.
func TestDashboardEmpty(t *testing.T) {
	m, _ := dashModel(t)
	m.inbox, m.sessions = nil, nil
	out := ansi.Strip(strings.Join(m.viewAgent(120, 60), "\n"))
	for _, want := range []string{"nothing needs you", "nothing to answer", "no sessions yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing:\n%s", want, out)
		}
	}
}

// A send's line is what it is about, not its "## 何を" heading.
func TestSendGist(t *testing.T) {
	for in, want := range map[string]string{
		"## 何を\nTUI のダッシュボードを直す\n\n## なぜ": "TUI のダッシュボードを直す",
		"\n  テストも足して\n二行目":                "テストも足して",
		"## 何を\n\n":                       "何を",
		"#42 を見て":                         "#42 を見て",
		"":                                "",
	} {
		if got := sendGist(in); got != want {
			t.Errorf("sendGist(%q) = %q, want %q", in, got, want)
		}
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

// The stages follow the work (impl → review → PR → CI → merge): asking
// for the independent review hands the change over (impl done) and puts
// the review under way, even when the agent ended its turn (state done, no
// done report); its verdict decides the review stage; after the PR a pass
// counts only for the PR's head or the same change rebased (merge.check),
// and a head that changed what was reviewed asks for a re-review.
func TestDashStagesReview(t *testing.T) {
	m, _ := dashModel(t)
	dashLook(t, m, "none", "unicode")
	pending := &state.ReviewSummary{ID: 2, Verdict: "pending", ReviewedSHA: "abc1234"}
	pass := &state.ReviewSummary{ID: 3, Verdict: "pass", ReviewedSHA: "abc1234"}
	fail := &state.ReviewSummary{ID: 3, Verdict: "fail", ReviewedSHA: "abc1234"}
	rep := func(id int64, status string) *state.ReportSummary {
		return &state.ReportSummary{ID: id, Status: status}
	}
	opened := func(s *state.Session) *state.Session {
		s.PR, s.Landing = 42, true
		s.PRStatus = &state.PRStatus{State: "OPEN", HeadSHA: "def5678", Pass: 3, MergeState: "CLEAN"}
		return s
	}
	review := func(ok bool, detail string, stale bool) *state.MergeCheck {
		mc := &state.MergeCheck{PR: 42, PRHead: "def5678", ReviewStale: stale, Missing: []string{}, Conds: []state.MergeCond{
			{Name: "land", OK: true}, {Name: "review", OK: ok, Detail: detail}, {Name: "ci", OK: true}}}
		if !ok {
			mc.Missing = []string{"review"}
		}
		return mc
	}
	cases := []struct {
		name   string
		s      *state.Session
		mc     *state.MergeCheck
		want   [5]int
		phrase string
		why    string
	}{
		{"before review", &state.Session{State: state.Running},
			nil, [5]int{stRun, stNone, stNone, stNone, stNone}, "working", ""},
		{"done, not reviewed", &state.Session{State: state.Done, Report: rep(1, "done")},
			nil, [5]int{stOK, stNone, stNone, stNone, stNone}, "needs review", ""},
		{"in review, the agent's turn ended", &state.Session{State: state.Done, Report: rep(2, "review"), Review: pending},
			nil, [5]int{stOK, stRun, stNone, stNone, stNone}, "in review", ""},
		{"in review, a progress report since", &state.Session{State: state.Running, Report: rep(3, "progress"), Review: pending},
			nil, [5]int{stOK, stRun, stNone, stNone, stNone}, "in review", ""},
		{"passed", &state.Session{State: state.Done, Report: rep(3, "review"), Review: pass},
			nil, [5]int{stOK, stOK, stNone, stNone, stNone}, "ready to land", ""},
		{"failed", &state.Session{State: state.Running, Report: rep(3, "review"), Review: fail},
			nil, [5]int{stOK, stFail, stNone, stNone, stNone}, "review failed", ""},
		{"PR, reviewed its head", opened(&state.Session{State: state.Done, Review: pass}),
			review(true, "報告 #3 で pass", false), [5]int{stOK, stOK, stOK, stOK, stNone}, "waiting", ""},
		{"PR, head only rebased", opened(&state.Session{State: state.Done, Review: pass}),
			review(true, "報告 #3 で pass(abc1234)。PR の HEAD def5678 は rebase し直しただけで差分の中身が同じ", false),
			[5]int{stOK, stOK, stOK, stOK, stNone}, "waiting", ""},
		{"PR, head changed the reviewed change", opened(&state.Session{State: state.Done, Review: pass}),
			review(false, "レビューした abc1234 から PR の HEAD def5678 で差分の中身が変わった(再レビューが要る)", true),
			[5]int{stOK, stStale, stOK, stOK, stNone}, "needs re-review", "差分の中身が変わった(再レビューが要る)"},
		{"PR, re-review under way", opened(&state.Session{State: state.Done, Review: &state.ReviewSummary{ID: 4, Verdict: "pending", ReviewedSHA: "def5678"}}),
			review(false, "独立レビューの最中(報告 #4・def5678)", false), [5]int{stOK, stRun, stOK, stOK, stNone}, "in review", ""},
	}
	for _, c := range cases {
		c.s.Slug = "feat-x"
		got := dashStages(c.s, c.mc)
		if got != c.want {
			t.Errorf("%s: stages %v, want %v", c.name, got, c.want)
		}
		p, named := m.stagePhraseCond(c.s, got, c.mc)
		if p = ansi.Strip(p); !strings.HasSuffix(p, c.phrase) {
			t.Errorf("%s: phrase %q, want %q", c.name, p, c.phrase)
		}
		if why := stageWhy(got, c.mc, named); !strings.Contains(why, c.why) || (c.why == "" && why != "") {
			t.Errorf("%s: why %q, want %q", c.name, why, c.why)
		}
	}
}

// dashStatsReply is a week of stats: today 2 merges in pekoe and tokens in
// pekoe and tapi chat, 3 days ago 1 merge, older days nothing.
func dashStatsReply(now time.Time) daemon.Stats {
	day := func(back int) string { return now.AddDate(0, 0, -back).Format("2006-01-02") }
	return daemon.Stats{From: day(6), To: day(0), Merged: 3, Rows: []daemon.StatsRow{
		{Day: day(0), Repo: "pekoe", Merged: 2, Tokens: state.Usage{Input: 1000, Output: 80000, CacheCreation: 400000, CacheRead: 9_000_000}},
		{Day: day(0), Repo: "", Tokens: state.Usage{Input: 200, Output: 5000, CacheCreation: 10000, CacheRead: 800_000}},
		{Day: day(3), Repo: "pekoe", Merged: 1, Tokens: state.Usage{Output: 7}},
	}}
}

// The overview shows today's merges and tokens by kind (cache read apart
// from what the work wrote) and the week's merges as a bar in the stage
// bar's words; the ascii set gets each day's count.
func TestDashboardStats(t *testing.T) {
	m, f := dashModel(t)
	calls := f.find("stats")
	if len(calls) == 0 {
		t.Fatal("selecting the tapi row did not read stats")
	}
	if d, _ := calls[0]["days"].(float64); d != 7 {
		t.Errorf("stats days: %v", calls[0]["days"])
	}
	dashLook(t, m, "truecolor", "unicode")
	out := ansi.Strip(strings.Join(m.viewAgent(150, 60), "\n"))
	for _, want := range []string{
		"📊 today  2 merged · out 85.0k · cache write 410.0k · cache read 9.80M · in 1.2k",
		"📅 7 days  ▱▱ ▱▱ ▱▱ ▰▱ ▱▱ ▱▱ ▰▰  3 merged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "📅"), strings.Index(out, "📬 Inbox"); i < 0 || j < i {
		t.Errorf("stats not in the overview:\n%s", out)
	}
	for _, r := range out {
		if r >= 0x1F000 && width.LookupRune(r).Kind() != width.EastAsianWide {
			t.Errorf("%q (U+%04X) is not East Asian Wide", r, r)
		}
	}

	dashLook(t, m, "ansi", "ascii")
	out = ansi.Strip(strings.Join(m.viewAgent(150, 60), "\n"))
	for _, want := range []string{"today  2 merged · out 85.0k", "7 days  0 0 0 1 0 0 2  3 merged"} {
		if !strings.Contains(out, want) {
			t.Errorf("ascii: %q missing:\n%s", want, out)
		}
	}
	for _, r := range out {
		if r >= 0x1F000 || r == '▰' || r == '▱' {
			t.Fatalf("ascii: %q drawn:\n%s", r, out)
		}
	}

	// narrow: one cell a day, unspaced, and the overview flows onto lines
	dashLook(t, m, "truecolor", "unicode")
	out = ansi.Strip(strings.Join(m.viewAgent(44, 40), "\n"))
	if !strings.Contains(out, "7 days  ▱▱▱▰▱▱▰  3 merged") {
		t.Errorf("narrow week:\n%s", out)
	}
	if !strings.Contains(out, "cache read 9.80M") {
		t.Errorf("narrow: tokens not flowed onto the next lines:\n%s", out)
	}
}

// A busy day scales the bar down and keeps a ▰ for a day with one merge.
func TestDashboardStatsScale(t *testing.T) {
	m, _ := dashModel(t)
	dashLook(t, m, "truecolor", "unicode")
	now := time.Now()
	day := func(back int) string { return now.AddDate(0, 0, -back).Format("2006-01-02") }
	m.dash.stats = &daemon.Stats{From: day(6), To: day(0), Merged: 9, Rows: []daemon.StatsRow{
		{Day: day(0), Repo: "a", Merged: 8}, {Day: day(1), Repo: "a", Merged: 1}}}
	out := ansi.Strip(strings.Join(m.viewAgent(150, 60), "\n"))
	if !strings.Contains(out, "▱▱▱▱ ▱▱▱▱ ▱▱▱▱ ▱▱▱▱ ▱▱▱▱ ▰▱▱▱ ▰▰▰▰  9 merged") {
		t.Errorf("scaled week:\n%s", out)
	}
	if !strings.Contains(out, "📊 today  8 merged · out 0") {
		t.Errorf("today without tokens:\n%s", out)
	}
}

// A failed read says stats are unavailable and why, with no numbers (not
// even the last good ones); nothing recorded says so; neither breaks the
// widths.
func TestDashboardStatsFailAndEmpty(t *testing.T) {
	m, f := dashModel(t)
	base := f.reply
	f.reply = func(method string) any {
		if method == "stats" {
			return fmt.Errorf("unknown method stats\x1b[2J")
		}
		return base(method)
	}
	m.dash.statsAt = time.Time{}
	runCmd(m, m.loadDashStats(dashStatsOpen))
	raw := strings.Join(m.viewAgent(120, 60), "\n")
	if strings.Contains(raw, "\x1b[2J") {
		t.Fatal("the error's escape was drawn")
	}
	out := ansi.Strip(raw)
	if !strings.Contains(out, "today  stats unavailable · unknown method stats") || strings.Contains(out, "merged ·") || strings.Contains(out, "7 days") {
		t.Errorf("failed stats:\n%s", out)
	}
	for _, size := range [][2]int{{120, 40}, {60, 20}, {40, 16}} {
		m.width, m.height, m.focus = size[0], size[1], pAgent
		checkSize(t, m, fmt.Sprintf("failed stats %dx%d", size[0], size[1]))
	}

	now := time.Now()
	f.reply = func(method string) any {
		if method == "stats" {
			d := now.Format("2006-01-02")
			return daemon.Stats{From: now.AddDate(0, 0, -6).Format("2006-01-02"), To: d, Rows: []daemon.StatsRow{}}
		}
		return base(method)
	}
	m.dash.statsAt = time.Time{}
	runCmd(m, m.loadDashStats(dashStatsOpen))
	out = ansi.Strip(strings.Join(m.viewAgent(120, 60), "\n"))
	for _, want := range []string{"today  no merges · no tokens recorded yet", "7 days  ▱▱▱▱▱▱▱  0 merged"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty stats: %q missing:\n%s", want, out)
		}
	}
}

// Stats are read when the dashboard opens and then every
// dashStatsRefresh, not every tick.
func TestDashboardStatsRefresh(t *testing.T) {
	m, f := dashModel(t)
	n := len(f.find("stats"))
	if m.loadDashStats(dashStatsOpen) != nil {
		t.Error("stats read again right after opening")
	}
	m.dash.at = time.Now() // the rest is fresh too
	runCmd(m, m.dashTick())
	if got := len(f.find("stats")); got != n {
		t.Errorf("a tick read stats again: %d → %d", n, got)
	}
	m.dash.statsAt = time.Now().Add(-dashStatsRefresh)
	runCmd(m, m.dashTick())
	if got := len(f.find("stats")); got != n+1 {
		t.Errorf("stats not read after dashStatsRefresh: %d → %d", n, got)
	}
}
