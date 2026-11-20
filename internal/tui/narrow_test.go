package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/daemon"
	"pekoe/internal/state"
)

// Below 100 columns the left column draws one tab (narrowTab). The keys
// that move only the focus (2 / 3 / tab / shift+tab) bring the tab along,
// so j / k / ↵ / D act on what is drawn; so does narrowing the terminal
// with a panel focused that has no tab (Browser, Picks).
func TestNarrowFocusFollowsTab(t *testing.T) {
	m, _, _ := chatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 90, Height: m.height}) // the first update syncs the repository (it clears the worktrees)
	m.sessions = append(m.sessions, sess("api-2", appA, state.Idle, time.Now().Add(time.Second)))
	m.worktrees = []daemon.WorktreeView{{Path: "/w/a", Branch: "wt-a"}, {Path: "/w/b", Branch: "wt-b"}}
	m.focus = pSessions
	// Update without running its commands: they would reload the worktrees
	// from the fake pekoed
	m.Update(key("2"))
	if m.focus != pSecond || m.narrowTab != 1 {
		t.Fatalf("2: focus %d tab %d", m.focus, m.narrowTab)
	}
	if top := strings.SplitN(ansi.Strip(m.View()), "\n", 2)[0]; !strings.Contains(top, "Worktrees") {
		t.Fatalf("top border: %s", top)
	}
	m.Update(key("j"))
	if m.wsel != 1 {
		t.Fatalf("j on the Worktrees tab: wsel %d", m.wsel)
	}
	for _, c := range []struct {
		key        string
		focus, tab int
	}{{"3", pThird, 3}, {"tab", pAgent, 3}, {"tab", pSessions, 0}, {"shift+tab", pAgent, 0}, {"shift+tab", pThird, 3}, {"1", pSessions, 0}} {
		m.Update(key(c.key))
		if m.focus != c.focus || m.narrowTab != c.tab {
			t.Errorf("%s: focus %d tab %d, want %d %d", c.key, m.focus, m.narrowTab, c.focus, c.tab)
		}
	}

	// [2] on Issues stays on the Issues tab
	m.sub2 = subIssues
	m.Update(key("2"))
	if m.narrowTab != 2 {
		t.Errorf("2 with Issues: tab %d", m.narrowTab)
	}

	// narrowed with Browser / Picks focused: Worktrees / Changes
	m.width, m.focus, m.sub2 = 120, pSecond, 1
	m.Update(tea.WindowSizeMsg{Width: 90, Height: m.height})
	if m.narrowTab != 1 || m.sub2 != 0 {
		t.Errorf("narrowed on Browser: tab %d sub2 %d", m.narrowTab, m.sub2)
	}
	m.width, m.focus, m.sub3 = 120, pThird, 1
	m.Update(tea.WindowSizeMsg{Width: 90, Height: m.height})
	if m.narrowTab != 3 || m.sub3 != 0 {
		t.Errorf("narrowed on Picks: tab %d sub3 %d", m.narrowTab, m.sub3)
	}
}

// A click on the narrow top panel (a row or its title) leaves the tab drawn
// and focuses it: it used to focus [1], and the tab followed the focus back
// to Sessions, so the list clicked on went away.
func TestNarrowClickKeepsTab(t *testing.T) {
	for _, c := range []struct {
		key        string
		focus, tab int
		y          int
	}{
		{"3", pThird, 3, 2},  // a row of Changes
		{"3", pThird, 3, 0},  // the title (the tab names)
		{"2", pSecond, 1, 1}, // a row of Worktrees
	} {
		m, _, _ := chatModel(t)
		m.Update(tea.WindowSizeMsg{Width: 90, Height: m.height})
		m.worktrees = []daemon.WorktreeView{{Path: "/w/a", Branch: "wt-a"}, {Path: "/w/b", Branch: "wt-b"}}
		m.changes = []change{{Status: "M", Path: "a.go"}, {Status: "M", Path: "b.go"}, {Status: "A", Path: "c.go"}}
		m.Update(key(c.key))
		m.focus, m.typing = pAgent, true // typing into [4]: the click leaves it
		m.Update(tea.MouseMsg{X: 5, Y: c.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		m.Update(tea.MouseMsg{X: 5, Y: c.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
		if m.focus != c.focus || m.narrowTab != c.tab || m.typing {
			t.Errorf("%s, click at y=%d: focus %d tab %d typing %v, want focus %d tab %d", c.key, c.y, m.focus, m.narrowTab, m.typing, c.focus, c.tab)
		}
	}

	// a click on the Sessions tab leaves [2] on Issues: 2 goes back there
	m, _, _ := chatModel(t)
	m.Update(tea.WindowSizeMsg{Width: 90, Height: m.height})
	m.focus, m.sub2 = pSessions, subIssues
	m.Update(tea.MouseMsg{X: 5, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{X: 5, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.focus != pSessions || m.narrowTab != 0 || m.sub2 != subIssues {
		t.Errorf("click on Sessions: focus %d tab %d sub2 %d, want [2] still on Issues", m.focus, m.narrowTab, m.sub2)
	}
}
