package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// The tapi row heads [1] under the orchestrator heading, above the
// repository headings, and stays under a filter that matches nothing.
func TestTapiRowHeadsSessions(t *testing.T) {
	m := repoModel(t, "none", 180)
	m.chat.info = &daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Waiting,
		StateSince: time.Now().Add(-2 * time.Minute), LastMessage: "\n  CSV の件、PR 出たよ\x1b[31m\nsecond line"}
	rows, sels, _ := m.sessionRowsSel(60)
	if h := ansi.Strip(rows[0].Text); !strings.HasPrefix(h, "  orchestrator ━") || ansi.StringWidth(rows[0].Text) != 60 || sels[0] != rowNone {
		t.Errorf("the orchestrator heading heads [1], 60 wide, selecting nothing: %q (%d)", h, sels[0])
	}
	first := ansi.Strip(rows[1].Text)
	for _, want := range []string{"tapi", "wait", "2m", "CSV の件、PR 出たよ"} {
		if !strings.Contains(first, want) {
			t.Errorf("tapi row %q: no %q", first, want)
		}
	}
	if sels[1] != tapiSel {
		t.Errorf("the tapi row selects %d", sels[1])
	}
	if strings.Contains(first, "second line") || strings.Contains(rows[1].Text, "\x1b[31m") {
		t.Errorf("only the first line of the reply, without control characters: %q", rows[1].Text)
	}
	if ansi.StringWidth(rows[1].Text) > 60 {
		t.Errorf("tapi row wider than the panel: %d", ansi.StringWidth(rows[1].Text))
	}
	if h := ansi.Strip(rows[2].Text); !strings.Contains(h, "api ─") || strings.Contains(h, "api-1") {
		t.Errorf("the repository heading should follow the tapi row: %q", h)
	}
	// no face on the row (docs/mascot.md: one face at a time)
	if face := ansi.Strip(m.tapi.r.Line(m.eyesFor(m.chatMood(), false))); strings.Contains(first, face) {
		t.Errorf("tapi's face %q on the row %q", face, first)
	}

	m.filter = "nothing-matches"
	rows, _ = m.sessionRows(60)
	if len(rows) != 3 || !strings.Contains(ansi.Strip(rows[0].Text), "orchestrator") || !strings.Contains(ansi.Strip(rows[1].Text), "tapi") || !strings.Contains(ansi.Strip(rows[2].Text), "no match") {
		t.Errorf("filtered: %+v", rows)
	}

	// before tapi.get answers (or tapi never started) the row still shows
	m.filter, m.chat.info = "", nil
	rows, _ = m.sessionRows(60)
	if got := ansi.Strip(rows[1].Text); !strings.Contains(got, "tapi") || !strings.Contains(got, "asleep") {
		t.Errorf("no tapi yet: %q", got)
	}
}

// On the tapi row the session keys do nothing; [4] shows the dashboard; ↵
// opens tapi chat; the bar and the help say so.
func TestTapiRowKeys(t *testing.T) {
	m, f, _ := chatModel(t)
	m.focus = pSessions
	runCmd(m, m.onKey(key("k"))) // api-1 → the tapi row
	if !m.onTapiRow() || m.current() != nil {
		t.Fatalf("k from the first session: sel %d", m.sel)
	}
	if out := checkSize(t, m, "tapi row"); !strings.Contains(out, "Sessions tapi/1") || !strings.Contains(out, "Dashboard · tapi the orchestrator") {
		t.Errorf("title or main panel:\n%s", out)
	}
	hints := ""
	for _, h := range m.hints() {
		hints += h[0] + " " + h[1] + " · "
	}
	if !strings.Contains(hints, "↵ open tapi") || strings.Contains(hints, "review") {
		t.Errorf("bar on the tapi row: %s", hints)
	}
	if m.panelKeySet() != hsTapiRow {
		t.Errorf("help set %q", m.panelKeySet())
	}
	before := len(f.calls)
	for _, k := range []string{"/", "x", "D", "l", "r", "p", "d", "!", "v", "u", "R", "b", "esc"} {
		runCmd(m, m.onKey(key(k)))
		if m.modal != nil || m.filterOn || m.window != engine.WinAgent || m.review != nil || !m.onTapiRow() {
			t.Fatalf("%q acted on the tapi row: modal %v filter %v window %q", k, m.modal != nil, m.filterOn, m.window)
		}
	}
	for _, c := range f.calls[before:] {
		if c.method != "session.screen" && c.method != "changes" {
			t.Errorf("a session key reached pekoed: %s %v", c.method, c.params)
		}
	}
	runCmd(m, m.onKey(key("enter")))
	if !m.chat.open || !m.typing || len(f.find("tapi.open")) != 1 {
		t.Fatalf("↵ on the tapi row: open %v typing %v", m.chat.open, m.typing)
	}
}

// ]a goes to tapi when it waits (the oldest waiter first), [a comes back.
func TestTapiRowNextWaiting(t *testing.T) {
	m, f, _ := chatModel(t)
	now := time.Now()
	w := sess("api-1", appA, state.Waiting, now.Add(-time.Minute))
	m.sessions = []*state.Session{w}
	f.reply = func(method string) any {
		if method == "attention.next" {
			return w
		}
		return nil
	}
	m.chat.info = &daemon.Tapi{Started: true, Alive: true, State: state.Waiting, StateSince: now.Add(-3 * time.Minute)}
	m.focus = pSessions
	runCmd(m, m.onKey(key("]")))
	runCmd(m, m.onKey(key("a")))
	if !m.onTapiRow() {
		t.Fatalf("]a: tapi waits longer, sel %d", m.sel)
	}
	runCmd(m, m.onKey(key("]")))
	runCmd(m, m.onKey(key("a")))
	if s := m.current(); s == nil || s.Slug != "api-1" {
		t.Fatalf("]a from the tapi row: %v", s)
	}
	runCmd(m, m.onKey(key("[")))
	runCmd(m, m.onKey(key("a")))
	if !m.onTapiRow() {
		t.Fatalf("[a: back to the tapi row, sel %d", m.sel)
	}
	// the session waits longer: it comes first
	m.sel = 0
	m.chat.info.StateSince = now
	runCmd(m, m.onKey(key("]")))
	runCmd(m, m.onKey(key("a")))
	if s := m.current(); s == nil || s.Slug != "api-1" {
		t.Fatalf("]a: the older session first, got sel %d", m.sel)
	}
}

// A click on a row of [1] selects that row, with the tapi row on top, the
// headings in between, scrolled and narrow.
func TestSessionsClickSelectsRow(t *testing.T) {
	for _, c := range []struct {
		name          string
		width, height int
		more          int // extra sessions (scrolls the panel)
	}{{"wide", 180, 40, 0}, {"narrow", 90, 40, 0}, {"scrolled", 180, 20, 30}} {
		m := repoModel(t, "none", c.width)
		m.height = c.height
		m.focus = pSessions
		now := time.Now()
		for i := 0; i < c.more; i++ {
			m.sessions = append(m.sessions, sess("web-x"+string(rune('a'+i%26))+string(rune('a'+i/26)), appB, state.Idle, now.Add(time.Duration(i)*time.Second)))
		}
		m.sortSessions()
		if c.more > 0 {
			m.sel = len(m.visible()) - 3 // scrolled down
		}
		lw := m.leftWidth()
		if narrow(m.width) {
			lw = m.width
		}
		click := func(y int) {
			m.onMouse(tea.MouseMsg{X: 3, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		}
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		clicked := 0
		vis := m.visible()
		for i := len(vis) - 1; i >= 0; i-- { // bottom up: a click scrolls the clicked row to the bottom
			s := vis[i]
			y := lineIndex(lines, s.Branch+" ", lw)
			if y < 0 {
				continue // scrolled out of view
			}
			click(y)
			if cur := m.current(); cur == nil || cur.Slug != s.Slug {
				t.Fatalf("%s: click on %q (line %d) selected %v", c.name, s.Branch, y, cur)
			}
			clicked++
			lines = strings.Split(ansi.Strip(m.View()), "\n")
		}
		if clicked < 3 {
			t.Fatalf("%s: only %d rows found", c.name, clicked)
		}
		if c.more > 0 {
			continue // the tapi row is scrolled out
		}
		y := lineIndex(lines, "tapi ", lw)
		click(y)
		if !m.onTapiRow() || m.chat.open {
			t.Fatalf("%s: click on the tapi row (line %d): sel %d", c.name, y, m.sel)
		}
		// a heading selects nothing
		sel, heading := m.sel, -1
		for i, l := range lines {
			left := string([]rune(l)[:min(lw, len([]rune(l)))])
			if strings.HasPrefix(left, "│") && strings.Contains(left, "web") && !strings.Contains(left, "web-") {
				heading = i
			}
		}
		if heading < 0 {
			t.Fatalf("%s: no heading", c.name)
		}
		orch := lineIndex(lines, "orchestrator ━", lw)
		if orch < 0 || orch >= y {
			t.Fatalf("%s: the orchestrator heading (line %d) should be above the tapi row (line %d)", c.name, orch, y)
		}
		for _, y := range []int{heading, orch} {
			click(y)
			if m.sel != sel {
				t.Errorf("%s: a click on a heading (line %d) moved the selection to %d", c.name, y, m.sel)
			}
		}
	}
}

// The tour's highlights sit on the rows as drawn: "Reading states" marks
// every state icon and word, the tapi row's too, and "land" the done
// session's row.
func TestTourCellsFollowTapiRow(t *testing.T) {
	m := tourModel(t, "none", 120, 40)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(key(tourKey))
	seen := map[string]bool{}
	for i := 0; m.tour != nil && i < 50; i++ {
		title := tourStepTitle(m)
		st := tourSteps[m.tour.step]
		left := strings.Split(ansi.Strip(strings.Join(m.viewLeft(m.leftWidth(), m.height-1), "\n")), "\n")
		at := func(r rect) string {
			rs := []rune(left[r.y])
			return strings.TrimSpace(string(rs[r.x:min(r.x+r.w, len(rs))]))
		}
		switch title {
		case "状態の見かた":
			seen[title] = true
			cells := st.cells(m)
			if len(cells) < 8 {
				t.Fatalf("cells %v", cells)
			}
			if got := at(cells[1]); got != "idle" || !strings.Contains(left[cells[1].y], "tapi") {
				t.Errorf("tapi row's word: %q in %q", got, left[cells[1].y])
			}
			for _, c := range cells[2:] {
				if c.w == 1 {
					continue
				}
				if got := at(c); got != "run" && got != "wait" && got != "done" && got != "idle" && got != "approved" && got != "landed" && got != "fail" && got != "review" {
					t.Errorf("a state cell on %q: %q", left[c.y], got)
				}
			}
		case "land":
			seen[title] = true
			r, _ := st.target(m)
			if !strings.Contains(left[r.y], "#128") {
				t.Errorf("land points at %q", left[r.y])
			}
		}
		m.Update(key("enter"))
	}
	if !seen["状態の見かた"] || !seen["land"] {
		t.Fatalf("steps not reached: %v", seen)
	}
}

// Calm states paint the tapi row's icon in tapi's body color (cream on a
// dark theme, caramel on a light one) and fill the hollow idle circle;
// run and wait keep the session colors. Other session rows don't change.
func TestTapiRowCalmIconIsTapiBody(t *testing.T) {
	t.Setenv("PEKOE_ICONS", "unicode")
	for _, tc := range []struct {
		palette, body string
	}{{"", "38;2;227;195;154"}, {"light", "38;2;138;85;56"}} {
		m := repoModel(t, "truecolor", 120)
		m.ic = theme.Icons("unicode")
		m.g.Mascot.Palette = tc.palette
		m.buildTapi(nil)
		alive := func(s state.State) *daemon.Tapi {
			return &daemon.Tapi{Started: true, Alive: true, State: s, StateSince: time.Now()}
		}
		for _, c := range []struct {
			name string
			info *daemon.Tapi
			calm bool
			icon string
		}{
			{"asleep", nil, true, "●"},
			{"idle", alive(state.Idle), true, "●"},
			{"exited", &daemon.Tapi{Started: true, Alive: false}, true, "✗"},
			{"run", alive(state.Running), false, ""},
			{"wait", alive(state.Waiting), false, "●"},
		} {
			m.chat.info = c.info
			rows, _ := m.sessionRows(80)
			line := rows[1].Text
			if got := ansi.Strip(line); c.icon != "" && !strings.HasPrefix(got, c.icon+" tapi") {
				t.Errorf("%s/%q: row %q should start with %s", tc.palette, c.name, got, c.icon)
			}
			if has := strings.HasPrefix(line, "\x1b["+tc.body+"m"); has != c.calm {
				t.Errorf("%s/%q: body-colored icon %v, want %v: %q", tc.palette, c.name, has, c.calm, line)
			}
		}
		for _, r := range sessionRowTexts(m) {
			if strings.Contains(r, tc.body) {
				t.Errorf("a session row took tapi's color: %q", r)
			}
		}
	}
}

func sessionRowTexts(m *model) []string {
	rows, _ := m.sessionRows(80)
	var out []string
	for _, r := range rows[2:] {
		out = append(out, r.Text)
	}
	return out
}
