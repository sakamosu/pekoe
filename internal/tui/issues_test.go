package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/daemon"
	"pekoe/internal/errcatch"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

func issueModel(t *testing.T, color string, width int) *model {
	t.Helper()
	t.Setenv("PEKOE_COLOR", color)
	m := newModel(nil, "")
	m.tapi.splash = false
	m.width, m.height = width, 40
	now := time.Now()
	m.sessions = []*state.Session{
		{Slug: "fix-login-safari", Branch: "fix/login-safari", State: state.Waiting, RepoID: "r", Repo: "app", RepoDir: "/src/app", Issue: 12, StateSince: now},
		{Slug: "other", Branch: "other", State: state.Running, RepoID: "r", Repo: "app", RepoDir: "/src/app", StateSince: now},
	}
	m.iss.list = &daemon.IssueList{RepoID: "r", Filter: "mine", FetchedAt: now, Issues: []daemon.Issue{
		{Number: 12, Title: "Login fails on Safari", Labels: []string{"bug", "ui"}, Comments: 2, UpdatedAt: now.Add(-2 * time.Hour), URL: "https://github.com/me/app/issues/12",
			Session: &daemon.IssueSession{Slug: "fix-login-safari", Branch: "fix/login-safari", State: state.Waiting}},
		{Number: 7, Title: "CSV export がタイムアウトする", UpdatedAt: now.Add(-72 * time.Hour)},
		{Number: 1234, Title: "A very long issue title that will certainly not fit in a narrow left column at all", Labels: []string{"enhancement"}, Comments: 15, UpdatedAt: now.Add(-30 * 24 * time.Hour)},
	}}
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func checkSize(t *testing.T, m *model, label string) string {
	t.Helper()
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != m.height {
		t.Errorf("%s: %d lines, want %d", label, len(lines), m.height)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != m.width {
			t.Errorf("%s line %d: width %d, want %d: %q", label, i, got, m.width, ansi.Strip(l))
		}
	}
	return ansi.Strip(out)
}

func TestSubtabCyclesThreeWays(t *testing.T) {
	m := issueModel(t, "none", 160)
	m.focus = pSecond
	for i, want := range []int{1, subIssues, 0, 1} {
		m.subtab(true)
		if m.sub2 != want {
			t.Fatalf("] step %d: sub2 = %d, want %d", i, m.sub2, want)
		}
	}
	for i, want := range []int{0, subIssues, 1} {
		m.subtab(false)
		if m.sub2 != want {
			t.Fatalf("[ step %d: sub2 = %d, want %d", i, m.sub2, want)
		}
	}
	if m.subtab(false); m.sub2 != 0 {
		t.Fatal("back to worktrees")
	}
	// [3] keeps its two tabs
	m.focus = pThird
	m.subtab(true)
	m.subtab(true)
	if m.sub3 != 0 {
		t.Fatalf("sub3 = %d after two steps", m.sub3)
	}
	// entering Issues shows the selected issue in the main panel
	m.focus = pSecond
	m.subtab(false) // 0 → issues
	if m.sub2 != subIssues || !m.iss.open {
		t.Fatalf("entering issues: sub2=%d open=%v", m.sub2, m.iss.open)
	}
}

func TestNarrowTabsIncludeIssues(t *testing.T) {
	m := issueModel(t, "none", 80)
	m.focus = pSessions
	type tab struct{ focus, sub2 int }
	var got []tab
	for i := 0; i < 4; i++ {
		m.subtab(true)
		got = append(got, tab{m.focus, m.sub2})
	}
	want := []tab{{pSecond, 0}, {pSecond, subIssues}, {pThird, 0}, {pSessions, 0}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("narrow tabs: %v, want %v", got, want)
		}
	}
	m.subtab(false) // sessions → changes
	m.subtab(false) // → issues
	if m.focus != pSecond || m.sub2 != subIssues || m.narrowTab != 2 {
		t.Fatalf("[ back to issues: focus=%d sub2=%d tab=%d", m.focus, m.sub2, m.narrowTab)
	}
	out := checkSize(t, m, "narrow issues")
	if !strings.Contains(out, "Issues 3") || !strings.Contains(out, "#12") || !strings.Contains(out, "#1234") {
		t.Fatalf("narrow issues panel:\n%s", out)
	}
	// the issue is read in the main panel below the tabs
	if !strings.Contains(out, "Issue #12") {
		t.Fatalf("narrow main panel should show the issue:\n%s", out)
	}
	// labels give way first when a row gets tight, then the comment count
	is := &m.iss.list.Issues[2]
	for cw, want := range map[int][2]bool{70: {true, true}, 36: {false, true}, 24: {false, false}} {
		l := ansi.Strip(m.issueLine(is, cw, time.Now()))
		if w(l) != cw {
			t.Errorf("cw %d: width %d: %q", cw, w(l), l)
		}
		if strings.Contains(l, "enhan") != want[0] || strings.Contains(l, "15") != want[1] {
			t.Errorf("cw %d: labels/comments %v: %q", cw, want, l)
		}
	}
}

func TestIssuesPanelRender(t *testing.T) {
	for _, c := range []struct {
		color, icons string
	}{{"truecolor", "nerd"}, {"ansi", "unicode"}, {"none", "ascii"}} {
		m := issueModel(t, c.color, 180)
		m.ic = theme.Icons(c.icons)
		m.focus = pSecond
		m.sub2 = subIssues
		out := checkSize(t, m, c.color)
		if !strings.Contains(out, "[2] "+theme.Prefix(m.ic.Issues)+"Issues 3 app mine") {
			t.Errorf("%s: title line missing:\n%s", c.color, out)
		}
		var row12 string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "#12 ") {
				row12 = l
			}
		}
		lk := theme.Look(m.ic, state.Waiting, 0, false)
		if !strings.Contains(row12, lk.Icon+" #12") || !strings.Contains(row12, "Login fails on Safari") || !strings.Contains(row12, "bug,ui") || !strings.Contains(row12, "2h") {
			t.Errorf("%s: issue row: %q", c.color, row12)
		}
		if c.icons == "ascii" && !strings.Contains(row12, "2c") {
			t.Errorf("ascii comment count: %q", row12)
		}
		if !strings.Contains(out, "ISSUES") || !strings.Contains(out, "f scope") {
			t.Errorf("%s: bottom bar:\n%s", c.color, out)
		}
	}
}

func TestIssuesPanelUnavailable(t *testing.T) {
	m := issueModel(t, "none", 160)
	m.iss.list = &daemon.IssueList{Filter: "mine", Unavailable: "no GitHub remote", FetchedAt: time.Now()}
	m.focus, m.sub2 = pSecond, subIssues
	out := checkSize(t, m, "unavailable")
	if !strings.Contains(out, "no GitHub remote") || !strings.Contains(out, "Issues 0 app mine") {
		t.Fatalf("unavailable:\n%s", out)
	}
	m.onKey(key("j"))
	m.onKey(key("n"))
	if m.modal != nil {
		t.Fatal("n without an issue must not open a modal")
	}
}

func TestIssueKeys(t *testing.T) {
	m := issueModel(t, "none", 160)
	m.focus, m.sub2 = pSecond, subIssues

	// j shows the next issue in the main panel
	m.onKey(key("j"))
	if m.iss.sel != 1 || !m.iss.open {
		t.Fatalf("j: sel=%d open=%v", m.iss.sel, m.iss.open)
	}
	m.iss.ensure()
	m.iss.details[7] = &daemon.IssueDetail{Issue: m.iss.list.Issues[1], State: "OPEN", Author: "alice",
		Body:   "## 再現手順\n- 一覧を開く\n- **Export** を押す\n\n```go\nfunc main() {}\n```\n> 引用\n<!-- template comment -->",
		Thread: []daemon.IssueComment{{Author: "bob", Body: "same here", CreatedAt: time.Now().Add(-time.Hour)}}}
	out := checkSize(t, m, "issue open")
	for _, want := range []string{"Issue #7", "@alice", "## 再現手順", "• 一覧を開く", "│ func main() {}", "▎ 引用", "@bob · 1h ago", "same here"} {
		if !strings.Contains(out, want) {
			t.Errorf("issue view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "template comment") {
		t.Error("HTML comments of issue templates must be hidden")
	}
	// / narrows locally
	m.onKey(key("/"))
	for _, r := range "safari" {
		m.onKey(key(string(r)))
	}
	m.onKey(key("enter"))
	if v := m.visibleIssues(); len(v) != 1 || v[0].Number != 12 {
		t.Fatalf("filter: %+v", v)
	}
	if out := checkSize(t, m, "filtered"); !strings.Contains(out, "Issues 1 app mine filter:safari") {
		t.Fatalf("filtered title:\n%s", out)
	}
	m.onKey(key("/"))
	m.onKey(key("esc"))

	// f: mine → open → (label modal)
	m.onKey(key("f"))
	if m.iss.filter != "open" {
		t.Fatalf("f: %s", m.iss.filter)
	}
	m.onKey(key("f"))
	if _, ok := m.modal.(*labelModal); !ok {
		t.Fatalf("f from open should ask for a label, got %T", m.modal)
	}
	m.modal.key(tea.KeyMsg{Type: tea.KeyCtrlU})
	for _, r := range "bug" {
		m.modal.key(key(string(r)))
	}
	m.modal.key(key("enter"))
	m.modal = nil
	if m.iss.filter != "label" || m.iss.label != "bug" || m.issueFilterName() != "label:bug" {
		t.Fatalf("label filter: %s %s", m.iss.filter, m.iss.label)
	}

	// n on #12 suggests fix/ + kebab title and gh#12
	m.iss.sel = 0
	m.onKey(key("n"))
	nm, ok := m.modal.(*newModal)
	if !ok {
		t.Fatalf("n: modal %T", m.modal)
	}
	if nm.inputs[0].String() != "fix/login-fails-on-safari" || nm.inputs[1].String() != "gh#12" {
		t.Fatalf("new modal: branch=%q from=%q", nm.inputs[0].String(), nm.inputs[1].String())
	}
	if out := checkSize(t, m, "new modal"); !strings.Contains(out, "fix/login-fails-on-safari") {
		t.Fatalf("modal view:\n%s", out)
	}
	m.modal = nil

	// ↵ on an issue with a session jumps to it
	m.onKey(key("enter"))
	if m.focus != pSessions || m.current() == nil || m.current().Slug != "fix-login-safari" || m.iss.open {
		t.Fatalf("enter should jump to the session: focus=%d cur=%v open=%v", m.focus, m.current(), m.iss.open)
	}

	// ↵ on one without a session reads it in the main panel; esc goes back to the agent
	m.focus = pSecond
	m.iss.sel = 1
	m.onKey(key("enter"))
	if m.focus != pAgent || !m.iss.open {
		t.Fatalf("enter without session: focus=%d open=%v", m.focus, m.iss.open)
	}
	m.onKey(key("j"))
	m.onKey(key("esc"))
	if m.iss.open {
		t.Fatal("esc must return to the agent window")
	}
	if out := checkSize(t, m, "agent again"); strings.Contains(out, "Issue #") {
		t.Fatalf("agent window expected:\n%s", out)
	}
}

func TestIssueViewScrollClamps(t *testing.T) {
	m := issueModel(t, "truecolor", 140)
	m.focus, m.sub2 = pAgent, subIssues
	m.iss.open = true
	m.iss.ensure()
	m.iss.details[12] = &daemon.IssueDetail{Issue: m.iss.list.Issues[0], State: "OPEN", Body: strings.Repeat("line\n\n", 80)}
	m.onKey(key("G"))
	checkSize(t, m, "bottom")
	if m.iss.scroll == 0 || m.iss.scroll > 200 {
		t.Fatalf("G scroll = %d", m.iss.scroll)
	}
	m.onKey(key("g"))
	if m.iss.scroll != 0 {
		t.Fatal("g goes to the top")
	}
}

func TestIssueTextIsSanitized(t *testing.T) {
	m := issueModel(t, "none", 160)
	got := renderMarkdown(m.th, m.ic, "evil \x1b]52;c;aGk=\x07 text\x1b[31m red", 60)
	if len(got) != 1 || strings.ContainsRune(got[0], 0x1b) || strings.ContainsRune(got[0], 0x07) {
		t.Fatalf("escape sequences must be stripped: %q", got)
	}
}

// An issue opened over the PR view takes the main panel and its keys;
// opening the PR view (p) puts the PR back.
func TestIssueOverPRView(t *testing.T) {
	m := issueModel(t, "none", 160)
	m.sel = 1
	m.window = winPR
	m.focus, m.sub2 = pSecond, subIssues
	m.onKey(key("j"))
	m.focus = pAgent
	if !m.issueShown() || !strings.Contains(checkSize(t, m, "issue over pr"), "Issue #7") {
		t.Fatal("the issue should be shown over the PR view")
	}
	m.onKey(key("esc"))
	if m.iss.open || m.window != winPR {
		t.Fatalf("esc closes the issue and leaves the PR view: open=%v window=%s", m.iss.open, m.window)
	}
	m.iss.open = true
	m.openPR()
	if m.iss.open {
		t.Fatal("opening the PR view closes the issue")
	}
}

// A pending error card (s / i / ↵) must not take keys while an issue is read.
func TestIssueKeysBeatErrorCard(t *testing.T) {
	m := issueModel(t, "none", 160)
	m.sel = 0 // fix-login-safari, which works on #12
	m.errs = []errcatch.Report{{Slug: "fix-login-safari", Status: errcatch.StatusNew, First: time.Now()}}
	if m.pendingError() == nil {
		t.Fatal("test setup: expected a pending error card")
	}
	m.focus, m.sub2 = pAgent, subIssues
	m.iss.open = true
	m.onKey(key("enter"))
	if m.iss.open || m.focus != pSessions {
		t.Fatalf("↵ should jump to the issue's session, got open=%v focus=%d", m.iss.open, m.focus)
	}
}
