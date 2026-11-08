package tui

import (
	"strings"
	"testing"
	"time"

	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/state"
	"pekoe/internal/theme"

	"github.com/charmbracelet/x/ansi"
)

func TestLinkAt(t *testing.T) {
	osc := "\x1b[38;5;246mCreated PR \x1b[1;4m\x1b]8;id=1selpf1;https://github.com/o/r/pull/1\x1b\\#1\x1b[0m\x1b]8;;\x1b\\ done"
	bare := "see https://example.com/a_(b). ok"
	cases := []struct {
		line string
		x    int
		want string
	}{
		{osc, 0, ""},
		{osc, 11, "https://github.com/o/r/pull/1"},
		{osc, 12, "https://github.com/o/r/pull/1"},
		{osc, 13, ""},
		{"\x1b]8;;https://a.test\x07ab\x1b]8;;\x07", 1, "https://a.test"},
		{"日本 \x1b]8;;https://w.test\x1b\\x\x1b]8;;\x1b\\", 5, "https://w.test"},
		{bare, 3, ""},
		{bare, 4, "https://example.com/a_(b)"},
		{bare, 28, "https://example.com/a_(b)"},
		{bare, 29, ""},
		{"(https://x.test/p)", 17, ""},
		{"(https://x.test/p)", 16, "https://x.test/p"},
		{"go \x1b[94mhttps://c.test\x1b[39m now", 5, "https://c.test"},
		{"go " + cursorMark + "\x1b[7mh\x1b[27mttps://c.test", 4, "https://c.test"},
	}
	for _, c := range cases {
		if got := linkAt(c.line, c.x); got != c.want {
			t.Errorf("linkAt(%q, %d) = %q, want %q", c.line, c.x, got, c.want)
		}
	}
}

// The click lookup must agree with what View draws: find the link text on
// the rendered frame and resolve the same cell through agentLinkAt.
func TestAgentLinkAtMatchesView(t *testing.T) {
	g := &config.Global{}
	m := &model{
		g: g, th: theme.Load(&g.Theme), ic: theme.Icons(theme.IconsFromEnv()),
		window: engine.WinAgent, width: 140, height: 30,
		unreadAt: map[string]int{}, dismissed: map[string]time.Time{},
		progress: map[string][]daemon.Step{}, answers: map[string]string{},
		sentImages: map[int64]bool{}, ime: &imeCursor{},
	}
	m.newTapi()
	m.tapi.splash = false
	m.sessions = []*state.Session{{Slug: "s", Branch: "feat/x", State: state.Idle, Agent: "claude"}}
	aw, ah := m.agentSize()
	lines := make([]string, ah)
	lines[3] = "PR \x1b]8;;https://x.test/pr/9\x1b\\#9\x1b]8;;\x1b\\"
	m.screen = &engine.Screen{Width: aw, Height: ah, Lines: lines}

	x0, y0 := m.agentOrigin()
	for y, l := range strings.Split(m.View(), "\n") {
		plain := ansi.Strip(l)
		i := strings.Index(plain, "PR #9")
		if i < 0 {
			continue
		}
		x := ansi.StringWidth(plain[:i]) + 3
		if got := m.agentLinkAt(x-x0, y-y0); got != "https://x.test/pr/9" {
			t.Fatalf("click at %d,%d: got %q", x, y, got)
		}
		if got := m.agentLinkAt(x-x0-1, y-y0); got != "" {
			t.Fatalf("click left of link: got %q", got)
		}
		return
	}
	t.Fatal("link not drawn")
}
