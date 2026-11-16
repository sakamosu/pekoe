package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// ptyKind is one of the two ptys [4] shows: a session's window or tapi chat.
type ptyKind struct {
	name   string
	method string // the pekoed methods that reach it: "session." or "tapi."
	row    string // its row in [1]
	// show puts the pty in [4] with screen sc, not typing, the focus on [4]
	show func(t *testing.T, sc engine.Screen) (*model, *fakePekoed)
	// screen hands [4] a new capture of the pty, as pekoed's answer does
	screen func(m *model, sc engine.Screen) tea.Cmd
	// away leaves the pty for something else; back shows it again
	away, back func(m *model)
}

var ptyKinds = []ptyKind{
	{
		name: "session", method: "session.", row: "api-1",
		show: func(t *testing.T, sc engine.Screen) (*model, *fakePekoed) {
			m, f, _ := chatModel(t)
			m.sessions = append(m.sessions, sess("api-2", appA, state.Idle, time.Now()))
			m.window = engine.WinAgent
			m.focus, m.typing = pAgent, false
			m.screen, m.screenOf = &sc, "api-1"
			return m, f
		},
		screen: func(m *model, sc engine.Screen) tea.Cmd {
			return m.onScreen(screenMsg{slug: "api-1", window: engine.WinAgent, sc: &sc})
		},
		away: func(m *model) { m.sel = 1; m.onSelChange() },
		back: func(m *model) { m.sel = 0; m.onSelChange(); m.focus = pAgent },
	},
	{
		name: "tapi chat", method: "tapi.", row: "orchestrator",
		show: func(t *testing.T, sc engine.Screen) (*model, *fakePekoed) {
			m, f, _ := chatModel(t)
			m.chat.open = true
			m.focus, m.typing = pAgent, false
			m.chat.screen = &sc
			return m, f
		},
		screen: func(m *model, sc engine.Screen) tea.Cmd {
			cmd, _ := m.chatUpdate(chatScreenMsg{sc: &sc})
			return cmd
		},
		away: func(m *model) { m.closeChat() },
		back: func(m *model) { m.openChat() },
	},
}

// Every input reaches a session's window and tapi chat alike: the mouse
// (links, the click and the wheel that enter), keys, pastes, the unread
// line, ↵ on an exited pty and the resize to the pane. A feature added to
// one of them only fails here. What is meant to differ is in
// TestPtyIntendedDifferences.
func TestPtySameForSessionAndChat(t *testing.T) {
	lines := []string{
		"hello from the agent",
		"PR \x1b]8;id=a;https://x.test/pr/9\x1b\\#9\x1b]8;;\x1b\\ ready",
		"see https://example.com/x ok",
	}
	base := engine.Screen{Lines: lines, Width: 100, Height: 30, MouseAny: true, AltScreen: true}
	at := func(t *testing.T, m *model, text string) (int, int) {
		t.Helper()
		for y, l := range strings.Split(m.View(), "\n") {
			plain := ansi.Strip(l)
			if i := strings.Index(plain, text); i >= 0 {
				return ansi.StringWidth(plain[:i]) + 1, y
			}
		}
		t.Fatalf("%q not drawn:\n%s", text, m.View())
		return 0, 0
	}
	mouse := func(m *model, x, y int, b tea.MouseButton, a tea.MouseAction) tea.Cmd {
		return m.onMouse(tea.MouseMsg{X: x, Y: y, Button: b, Action: a})
	}
	click := func(m *model, x, y int) {
		runCmd(m, mouse(m, x, y, tea.MouseButtonLeft, tea.MouseActionPress))
		runCmd(m, mouse(m, x+1, y, tea.MouseButtonLeft, tea.MouseActionMotion))
		runCmd(m, mouse(m, x+1, y, tea.MouseButtonLeft, tea.MouseActionRelease))
	}

	cases := []struct {
		name string
		run  func(t *testing.T, k ptyKind)
	}{
		{"a click on a link opens it, OSC 8 or bare", func(t *testing.T, k ptyKind) {
			for _, c := range []struct{ text, want string }{{"#9", "https://x.test/pr/9"}, {"example.com", "https://example.com/x"}} {
				m, f := k.show(t, base)
				m.focus = pSessions
				x, y := at(t, m, c.text)
				if cmd := mouse(m, x, y, tea.MouseButtonLeft, tea.MouseActionPress); cmd != nil {
					t.Errorf("%s: the press did something", c.text)
				}
				if m.linkDown != c.want || m.typing || m.focus != pAgent {
					t.Fatalf("%s: linkDown %q typing %v focus %d", c.text, m.linkDown, m.typing, m.focus)
				}
				// the release opens it (not run here: it would start a browser)
				if cmd := mouse(m, x, y, tea.MouseButtonLeft, tea.MouseActionRelease); cmd == nil || m.linkDown != "" {
					t.Fatalf("%s: the release did not open it", c.text)
				}
				if got := f.find(k.method + "keys"); len(got) != 0 {
					t.Fatalf("%s: the link click reached the program: %v", c.text, got)
				}
			}
		}},
		{"a click enters, only the next one is the program's", func(t *testing.T, k ptyKind) {
			m, f := k.show(t, base)
			m.focus = pSessions
			x, y := at(t, m, "hello from")
			click(m, x, y)
			if !m.typing || m.focus != pAgent {
				t.Fatalf("typing %v focus %d", m.typing, m.focus)
			}
			if got := f.find(k.method + "keys"); len(got) != 0 {
				t.Fatalf("the click that entered reached the program: %v", got)
			}
			click(m, x, y)
			if got := f.find(k.method + "keys"); len(got) != 3 || got[0]["hex"] == nil {
				t.Fatalf("the second click: %v", got)
			}
		}},
		{"the wheel enters and already scrolls", func(t *testing.T, k ptyKind) {
			for _, c := range []struct {
				name     string
				mouseAny bool
				want     string
			}{{"mouse", true, "hex"}, {"altscreen", false, "keys"}} {
				sc := base
				sc.MouseAny = c.mouseAny
				m, f := k.show(t, sc)
				m.focus = pSessions
				x, y := at(t, m, "hello from")
				runCmd(m, mouse(m, x, y, tea.MouseButtonWheelUp, tea.MouseActionPress))
				if !m.typing || m.focus != pAgent {
					t.Fatalf("%s: typing %v focus %d", c.name, m.typing, m.focus)
				}
				if got := f.find(k.method + "keys"); len(got) != 1 || got[0][c.want] == nil {
					t.Fatalf("%s: %v", c.name, got)
				}
			}
		}},
		{"keys reach the program in the order typed", func(t *testing.T, k ptyKind) {
			m, f := k.show(t, base)
			m.typing = true
			for _, s := range []string{"a", "enter", "esc"} {
				runCmd(m, m.onKey(key(s)))
			}
			got := f.find(k.method + "keys")
			if len(got) != 3 || got[0]["literal"] != "a" {
				t.Fatalf("keys %v", got)
			}
			for i, want := range []string{"Enter", "Escape"} {
				if ks, _ := got[i+1]["keys"].([]any); len(ks) != 1 || ks[0] != want {
					t.Fatalf("key %d: %v", i+1, got[i+1])
				}
			}
		}},
		{"a paste goes as one text", func(t *testing.T, k ptyKind) {
			m, f := k.show(t, base)
			m.typing = true
			runCmd(m, m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted text"), Paste: true}))
			if got := f.find(k.method + "send"); len(got) != 1 || got[0]["text"] != "pasted text" {
				t.Fatalf("send %v", got)
			}
		}},
		{"the unread line shows where the user left until something is typed", func(t *testing.T, k ptyKind) {
			sc := base
			sc.Lines = lines[:1]
			m, _ := k.show(t, sc)
			k.away(m)
			k.back(m)
			more := base
			more.Lines = []string{"hello from the agent", "new output 1", "new output 2"}
			k.screen(m, more)
			out := ansi.Strip(m.View())
			if !strings.Contains(out, "─── new ───") || strings.Index(out, "─── new") > strings.Index(out, "new output 1") || strings.Index(out, "─── new") < strings.Index(out, "hello from") {
				t.Fatalf("no unread line after the line left:\n%s", out)
			}
			// entering is not reading: a click keeps it
			m.typing = false
			x, y := at(t, m, "new output 2")
			click(m, x, y)
			if !m.typing || !strings.Contains(ansi.Strip(m.View()), "─── new ───") {
				t.Fatalf("typing %v, the line went with the click", m.typing)
			}
			runCmd(m, m.onKey(key("a")))
			if strings.Contains(ansi.Strip(m.View()), "─── new ───") {
				t.Fatal("the unread line stayed after typing")
			}
		}},
		{"entering and leaving without typing has seen the unread line", func(t *testing.T, k ptyKind) {
			// every way out of typing: the prefix, a click on the pty's
			// own row in [1], a click on another panel
			for _, leave := range []struct {
				name string
				do   func(m *model)
			}{
				{"prefix", func(m *model) { runCmd(m, m.onKey(ctrlQ)) }},
				{"its row", func(m *model) { x, y := at(t, m, k.row); click(m, x, y) }},
				{"another panel", func(m *model) { x, y := at(t, m, "no changes yet"); click(m, x, y) }},
			} {
				sc := base
				sc.Lines = lines[:1]
				m, _ := k.show(t, sc)
				k.away(m)
				k.back(m)
				more := base
				more.Lines = []string{"hello from the agent", "new output 1", "new output 2"}
				more.Cursor.Y = 2 // the prompt, where the user leaves it
				k.screen(m, more)
				x, y := at(t, m, "new output 2")
				click(m, x, y)
				if !m.typing {
					t.Fatalf("%s: the click did not enter", leave.name)
				}
				leave.do(m)
				if m.typing {
					t.Fatalf("%s: did not leave", leave.name)
				}
				if !m.chat.open || k.name != "tapi chat" {
					k.back(m)
				}
				k.screen(m, more)
				if out := ansi.Strip(m.View()); strings.Contains(out, "─── new ───") {
					t.Fatalf("%s: the unread line came back after it was seen:\n%s", leave.name, out)
				}
			}
		}},
		{"an exited pty: ↵ restarts it and types into it", func(t *testing.T, k ptyKind) {
			for _, typing := range []bool{false, true} {
				sc := base
				sc.Dead = true
				sc.Lines = append(append([]string(nil), lines...), "", "Pane is dead (status 1, Mon Nov 16 20:00:00 2026)")
				m, f := k.show(t, sc)
				m.typing = typing
				out := ansi.Strip(m.View())
				if !strings.Contains(out, "exited · ↵ restart  x close") || strings.Contains(out, "Pane is dead") {
					t.Fatalf("typing %v: dead rows:\n%s", typing, out)
				}
				// the click does not enter: ↵ would restart it
				m.typing = false
				x, y := at(t, m, "hello from")
				click(m, x, y)
				if m.typing {
					t.Fatalf("a click entered an exited pty")
				}
				m.typing = typing
				hints := m.hints()
				if len(hints) == 0 || hints[0] != [2]string{"↵", "restart"} {
					t.Fatalf("typing %v: hints %v", typing, hints)
				}
				runCmd(m, m.onKey(key("enter")))
				if got := f.find(k.method + "restart"); len(got) != 1 {
					t.Fatalf("typing %v: restart %v", typing, got)
				}
				if !m.typing || m.focus != pAgent {
					t.Fatalf("typing %v: after ↵ typing %v focus %d", typing, m.typing, m.focus)
				}
			}
		}},
		{"a pty of another size is resized to the pane, once", func(t *testing.T, k ptyKind) {
			m, f := k.show(t, base)
			aw, ah := m.agentSize()
			small := base
			small.Width, small.Height = 67, 37
			runCmd(m, k.screen(m, small))
			got := f.find(k.method + "resize")
			if len(got) != 1 || got[0]["width"] != float64(aw) || got[0]["height"] != float64(ah) {
				t.Fatalf("resize %v (pane %dx%d)", got, aw, ah)
			}
			runCmd(m, k.screen(m, small))
			if got := f.find(k.method + "resize"); len(got) != 1 {
				t.Fatalf("asked again: %v", got)
			}
		}},
		{"typing: the bar shows the prefix, zoom and width", func(t *testing.T, k ptyKind) {
			m, _ := k.show(t, base)
			m.typing = true
			h := m.hints()
			p := m.prefixLabel()
			if len(h) != 3 || h[0][0] != p || h[1] != [2]string{p + " z", "zoom"} || h[2] != [2]string{p + " < >", "width"} {
				t.Fatalf("hints %v", h)
			}
		}},
	}
	for _, c := range cases {
		for _, k := range ptyKinds {
			t.Run(c.name+"/"+k.name, func(t *testing.T) { c.run(t, k) })
		}
	}
}

// What a session's window and tapi chat do differently on purpose; each is
// built in sessionPty / chatPty (pty.go), not in the shared code.
func TestPtyIntendedDifferences(t *testing.T) {
	sc := engine.Screen{Lines: []string{"hello"}, Width: 100, Height: 30}

	// The prefix: leaving a session's window keeps it in [4] and goes back
	// to the panels; tapi chat belongs to no panel, so leaving closes it.
	sm, _ := ptyKinds[0].show(t, sc)
	cm, _ := ptyKinds[1].show(t, sc)
	for _, m := range []*model{sm, cm} {
		m.typing = true
	}
	if sm.hints()[0][1] != "back" || cm.hints()[0][1] != "leave" {
		t.Fatalf("prefix hints: session %v chat %v", sm.hints()[0], cm.hints()[0])
	}
	sm.onKey(ctrlQ)
	cm.onKey(ctrlQ)
	if sm.typing || sm.window != engine.WinAgent || cm.typing || cm.chat.open {
		t.Fatalf("prefix: session typing %v; chat typing %v open %v", sm.typing, cm.typing, cm.chat.open)
	}

	// x on an exited pty: a session's agent ends the session (the worktree
	// stays); tapi chat just closes (tapi is restarted by ↵ or t).
	dead := sc
	dead.Dead = true
	sm, sf := ptyKinds[0].show(t, dead)
	cm, cf := ptyKinds[1].show(t, dead)
	for _, m := range []*model{sm, cm} {
		m.typing = true
		runCmd(m, m.onKey(key("x")))
	}
	if len(sf.find("session.kill")) != 1 || cm.chat.open || len(cf.find("tapi.restart")) != 0 {
		t.Fatalf("x: session.kill %v; chat open %v", sf.find("session.kill"), cm.chat.open)
	}

	// Cards from outside (a Chrome pick, the error card) are about a
	// session's work: they sit in its window, never in tapi chat. (The
	// title's activity and tokens are a session's too: viewAgent, not
	// the pty.)
	if sm.sessionPty().cards == nil || cm.chatPty().cards != nil {
		t.Fatal("cards: a session's window has them, tapi chat not")
	}
}

// A part of the pty that one constructor fills and the other leaves empty
// is a feature only one of them has. Each such part is named here with why;
// any other one fails, so a hook added to sessionPty alone (or chatPty
// alone) is either given to both or explained here.
func TestPtyPartsBuiltForBoth(t *testing.T) {
	onlyOne := map[string]string{
		"cards": "a Chrome pick and the error card are about a session's work; tapi chat shows none",
	}
	// the session's agent window, the one tapi chat stands beside (the run
	// window's own exit line, deadRows, has no chat counterpart)
	sc := engine.Screen{Lines: []string{"hello"}, Width: 100, Height: 30}
	sm, _ := ptyKinds[0].show(t, sc)
	cm, _ := ptyKinds[1].show(t, sc)
	sm.unreadAt["api-1"] = 3
	cm.chat.unreadAt, cm.chat.unreadSet = 3, true
	sp, cp := reflect.ValueOf(sm.sessionPty()), reflect.ValueOf(cm.chatPty())
	for i := 0; i < sp.NumField(); i++ {
		name := sp.Type().Field(i).Name
		if sp.Field(i).IsZero() == cp.Field(i).IsZero() {
			if _, ok := onlyOne[name]; ok {
				t.Errorf("%s: listed as one-sided, but both have it (or neither): drop it from onlyOne", name)
			}
			continue
		}
		if _, ok := onlyOne[name]; !ok {
			t.Errorf("%s: set for one pty only (session %v, chat %v); build it for both, or say why in onlyOne", name, !sp.Field(i).IsZero(), !cp.Field(i).IsZero())
		}
	}
}

// tapi's restart is a new pty, its History counted from 0: the line where
// the user left the old one would land at a wrong place in it.
func TestChatRestartForgetsUnreadLine(t *testing.T) {
	m, _ := ptyKinds[1].show(t, engine.Screen{Lines: []string{"hello"}, Width: 100, Height: 30, Dead: true})
	m.chat.unreadAt, m.chat.unreadSet = 40, true
	m.typing = true
	runCmd(m, m.onKey(key("enter")))
	if m.chat.unreadSet {
		t.Fatal("the unread line outlived the restart")
	}
}
