package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

type chatReplies struct {
	mu   sync.Mutex
	dead bool
}

func (r *chatReplies) reply(method string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch method {
	case "tapi.open", "tapi.restart":
		return daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Idle}
	case "tapi.reset":
		return daemon.Tapi{Agent: "claude"}
	case "tapi.screen":
		return engine.Screen{Lines: []string{"tapi says hello"}, Width: 100, Height: 30, Dead: r.dead}
	case "session.screen":
		return engine.Screen{Lines: []string{"the session's agent"}, Width: 100, Height: 30}
	}
	return nil
}

func chatModel(t *testing.T) (*model, *fakePekoed, *chatReplies) {
	t.Helper()
	f, cl := startFakePekoed(t)
	cr := &chatReplies{}
	f.reply = cr.reply
	t.Setenv("PEKOE_COLOR", "none")
	m := newModel(cl, "/src/api")
	m.tapi.splash = false
	m.width, m.height = 140, 40
	m.sessions = []*state.Session{sess("api-1", appA, state.Idle, time.Now())}
	m.sessionsIn, m.homePicked, m.reposLoaded = true, true, true
	m.lastSel = "api-1"
	return m, f, cr
}

// t opens tapi chat from any panel and types into it; esc goes to the
// agent (it interrupts Claude Code); the prefix leaves; t again continues.
func TestChatOpenTypeLeave(t *testing.T) {
	for _, focus := range []int{pSessions, pSecond, pThird, pAgent} {
		m, f, _ := chatModel(t)
		m.focus = focus
		runCmd(m, m.onKey(key("t")))
		op := f.find("tapi.open")
		if len(op) != 1 || op[0]["width"] == nil {
			t.Fatalf("focus %d: tapi.open %v", focus, op)
		}
		if !m.chat.open || !m.typing || m.focus != pAgent {
			t.Fatalf("focus %d: open %v typing %v focus %d", focus, m.chat.open, m.typing, m.focus)
		}
	}

	m, f, _ := chatModel(t)
	runCmd(m, m.onKey(key("t")))
	out := checkSize(t, m, "chat")
	if !strings.Contains(out, "[4] tapi the orchestrator · claude · typing") || !strings.Contains(out, "tapi says hello") || strings.Contains(out, "the session's agent") {
		t.Fatalf("chat window:\n%s", out)
	}
	if !strings.Contains(out, "TAPI") || !strings.Contains(out, "^q leave") {
		t.Fatalf("bar:\n%s", out)
	}
	runCmd(m, m.onKey(key("a")))
	runCmd(m, m.onKey(key("esc")))
	runCmd(m, m.onKey(key("t"))) // a t while typing is text, not "open"
	ks := f.find("tapi.keys")
	if len(ks) != 3 || ks[0]["literal"] != "a" || ks[2]["literal"] != "t" {
		t.Fatalf("tapi.keys: %v", ks)
	}
	if esc, _ := json.Marshal(ks[1]["keys"]); string(esc) != `["Escape"]` || !m.chat.open {
		t.Fatalf("esc must reach tapi and keep the chat open: %v open %v", ks[1], m.chat.open)
	}
	if n := len(f.find("session.keys")); n != 0 {
		t.Fatalf("keys reached the session: %d", n)
	}
	if n := len(f.find("tapi.open")); n != 1 {
		t.Fatalf("tapi.open %d times", n)
	}

	runCmd(m, m.onKey(ctrlQ))
	if m.chat.open || m.typing || m.focus != pSessions {
		t.Fatalf("after prefix: open %v typing %v focus %d", m.chat.open, m.typing, m.focus)
	}
	if out := checkSize(t, m, "closed"); strings.Contains(out, "tapi the orchestrator") || !strings.Contains(out, "the session's agent") {
		t.Fatalf("closed:\n%s", out)
	}

	// t while typing into a session's agent is the agent's
	m.focus, m.typing = pAgent, true
	runCmd(m, m.onKey(key("t")))
	if m.chat.open || len(f.find("session.keys")) != 1 {
		t.Fatal("t while typing into a session must reach that session")
	}
}

// Not typing (a click on the left panel): ↵ types again, esc closes.
func TestChatNotTyping(t *testing.T) {
	m, _, _ := chatModel(t)
	runCmd(m, m.onKey(key("t")))
	m.typing = false
	if out := checkSize(t, m, "idle chat"); !strings.Contains(out, "↵ type") || !strings.Contains(out, "esc close") {
		t.Fatalf("hints:\n%s", out)
	}
	runCmd(m, m.onKey(key("enter")))
	if !m.typing || !m.chat.open {
		t.Fatal("↵ types into tapi again")
	}
	m.typing = false
	runCmd(m, m.onKey(key("esc")))
	if m.chat.open {
		t.Fatal("esc closes when not typing")
	}
}

// The agent exited: ↵ restarts it, x closes.
func TestChatDead(t *testing.T) {
	m, f, cr := chatModel(t)
	cr.dead = true
	runCmd(m, m.onKey(key("t")))
	if out := checkSize(t, m, "dead"); !strings.Contains(out, "exited · ↵ restart  x close") {
		t.Fatalf("dead:\n%s", out)
	}
	cr.mu.Lock()
	cr.dead = false
	cr.mu.Unlock()
	runCmd(m, m.onKey(key("enter")))
	if len(f.find("tapi.restart")) != 1 {
		t.Fatal("↵ restarts tapi")
	}
	if len(f.find("session.restart")) != 0 {
		t.Fatal("restarted the session instead")
	}
}

// T resets after a confirmation; the help lists t and T and runs them.
func TestChatResetAndHelp(t *testing.T) {
	m, f, _ := chatModel(t)
	runCmd(m, m.onKey(key("T")))
	if _, ok := m.modal.(*confirmModal); !ok {
		t.Fatalf("T opened %T", m.modal)
	}
	runCmd(m, m.onKey(key("y")))
	if len(f.find("tapi.reset")) != 1 {
		t.Fatal("tapi.reset not called")
	}

	m.modal = nil
	runCmd(m, m.onKey(key("?")))
	h, ok := m.modal.(*helpModal)
	if !ok {
		t.Fatalf("? opened %T", m.modal)
	}
	out := checkSize(t, m, "help")
	if !strings.Contains(out, "talk to tapi") || !strings.Contains(out, "reset tapi chat") {
		t.Fatalf("help:\n%s", out)
	}
	for i, it := range h.items {
		if it.key == "t" {
			h.sel = i
		}
	}
	runCmd(m, m.onKey(key("enter")))
	if !m.chat.open {
		t.Fatal("t from the help list opens tapi chat")
	}
	runCmd(m, m.onKey(ctrlQ))
	m.focus = pAgent
	runCmd(m, m.onKey(key("t")))
	m.typing = false
	runCmd(m, m.onKey(key("?")))
	if out := checkSize(t, m, "help in chat"); !strings.Contains(out, "type into tapi") {
		t.Fatalf("chat panel keys:\n%s", out)
	}
}

// tapi waiting behind a closed window shows in the bar; while the chat is
// open the bar leaves its face to the window's title. (notify = "quiet":
// the old word. The default is in notify_test.go.)
func TestChatWaitsInBar(t *testing.T) {
	m, _, _ := chatModel(t)
	m.g.Orchestrator.Notify = "quiet"
	data, _ := json.Marshal(daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Waiting, WaitKind: "permission", StateSince: time.Now()})
	runCmd(m, m.onEvent(client.Event{Event: "tapi.updated", Data: data}))
	if out := checkSize(t, m, "waits"); !strings.Contains(out, "chat waits · t") {
		t.Fatalf("bar:\n%s", out)
	}
	runCmd(m, m.onKey(key("t")))
	m.chat.info.State, m.chat.info.WaitKind = state.Waiting, "permission"
	out := checkSize(t, m, "open")
	if strings.Contains(out, "chat waits") || !strings.Contains(out, "tapi the orchestrator · claude · permission") {
		t.Fatalf("open:\n%s", out)
	}
	if n := strings.Count(out, m.tapi.r.Line(m.tapi.r.Eyes("wait"))); n > 1 {
		t.Fatalf("%d faces", n)
	}
}

// The layout keys work around tapi chat as around an agent: t while zoomed
// opens it full width at the zoomed pty size, <prefix> z / < from the chat
// come back to it typing, and once the keys settle tapi's pty follows
// m.agentSize(). The prefix alone leaves; zoomed, the left stays hidden.
func TestChatLayout(t *testing.T) {
	m, f, _ := chatModel(t)
	t.Setenv("PEKOE_HOME", t.TempDir())
	m.layoutPath = paths.LayoutFile()
	settle := func() {
		t.Helper()
		runCmd(m, m.layoutSettled(m.layoutSeq))
	}

	// zoom from Sessions, then t
	m.onKey(ctrlQ)
	runCmd(m, m.onKey(key("z")))
	settle()
	if !m.zoomed() {
		t.Fatal("not zoomed")
	}
	runCmd(m, m.onKey(key("t")))
	aw, ah := m.agentSize()
	if aw != m.width-4 {
		t.Fatalf("zoomed agent width %d", aw)
	}
	op := f.find("tapi.open")
	if len(op) != 1 || op[0]["width"] != float64(aw) || op[0]["height"] != float64(ah) {
		t.Fatalf("tapi.open %v, want %dx%d", op, aw, ah)
	}
	out := checkSize(t, m, "zoomed chat")
	if !strings.Contains(out, "[4] tapi the orchestrator") || strings.Contains(out, "Sessions") || !strings.Contains(out, "zoom ^q z") {
		t.Fatalf("zoomed chat:\n%s", out)
	}
	if !strings.Contains(out, "^q leave  ^q o dashboard  ^q z unzoom  ^q < > width") {
		t.Fatalf("hints:\n%s", out)
	}
	// the fake pty is 100 wide: it is fitted to the zoomed pane
	if rs := f.find("tapi.resize"); len(rs) == 0 || rs[len(rs)-1]["width"] != float64(aw) {
		t.Fatalf("tapi.resize %v, want width %d", rs, aw)
	}

	// ^q z from the chat: unzoomed, still typing into tapi
	m.onKey(ctrlQ)
	runCmd(m, m.onKey(key("z")))
	if m.zoomed() || !m.chat.open || !m.typing || m.focus != pAgent {
		t.Fatalf("after ^q z: zoom %v open %v typing %v focus %d", m.zoomed(), m.chat.open, m.typing, m.focus)
	}
	settle()
	aw2, _ := m.agentSize()
	if aw2 >= aw {
		t.Fatalf("unzoomed width %d", aw2)
	}
	if rs := f.find("tapi.resize"); rs[len(rs)-1]["width"] != float64(aw2) {
		t.Fatalf("tapi pty not refitted after unzoom: %v (want %d)", rs[len(rs)-1], aw2)
	}
	if out := checkSize(t, m, "unzoomed chat"); !strings.Contains(out, "Sessions") || !strings.Contains(out, "[4] tapi the orchestrator") {
		t.Fatalf("unzoomed chat:\n%s", out)
	}

	// ^q < narrows the left column, back in the chat
	m.onKey(ctrlQ)
	runCmd(m, m.onKey(key("<")))
	settle()
	aw3, _ := m.agentSize()
	if !m.chat.open || !m.typing || aw3 != aw2+leftStep {
		t.Fatalf("after ^q <: open %v typing %v width %d (was %d)", m.chat.open, m.typing, aw3, aw2)
	}
	if rs := f.find("tapi.resize"); rs[len(rs)-1]["width"] != float64(aw3) {
		t.Fatalf("tapi pty not refitted after <: %v", rs[len(rs)-1])
	}
	checkSize(t, m, "narrowed chat")

	// zoomed, the prefix alone closes the chat and keeps the zoom
	m.onKey(ctrlQ)
	runCmd(m, m.onKey(key("z")))
	settle()
	runCmd(m, m.onKey(ctrlQ))
	runCmd(m, m.onKey(key("j"))) // any other key: the chat stays closed
	if m.chat.open || !m.zoomed() || m.focus != pAgent {
		t.Fatalf("leave zoomed: open %v zoom %v focus %d", m.chat.open, m.zoomed(), m.focus)
	}
	if out := checkSize(t, m, "left zoomed"); strings.Contains(out, "tapi the orchestrator") {
		t.Fatalf("left zoomed:\n%s", out)
	}
}

// Another session going away keeps the selection and typing where they
// are: a removed A must not move B's keystrokes onto C as commands.
func TestRemovedOtherSessionKeepsTyping(t *testing.T) {
	now := time.Now()
	for _, gone := range []string{"api-a", "api-c"} {
		m, _, _ := chatModel(t)
		m.sessions = []*state.Session{
			sess("api-a", appA, state.Idle, now),
			sess("api-b", appA, state.Idle, now.Add(time.Second)),
			sess("api-c", appA, state.Idle, now.Add(2*time.Second)),
		}
		m.sel, m.lastSel = 1, "api-b"
		m.focus, m.typing = pAgent, true
		data, _ := json.Marshal(state.Session{Slug: gone})
		runCmd(m, m.onEvent(client.Event{Event: "session.removed", Data: data}))
		if s := m.current(); s == nil || s.Slug != "api-b" || !m.typing {
			t.Fatalf("%s removed: current %v typing %v", gone, m.lastSel, m.typing)
		}
	}
	// the current one going away moves on and stops typing
	m, _, _ := chatModel(t)
	m.sessions = []*state.Session{sess("api-a", appA, state.Idle, now), sess("api-b", appA, state.Idle, now.Add(time.Second))}
	m.sel, m.lastSel, m.typing = 1, "api-b", true
	data, _ := json.Marshal(state.Session{Slug: "api-b"})
	runCmd(m, m.onEvent(client.Event{Event: "session.removed", Data: data}))
	if s := m.current(); s == nil || s.Slug != "api-a" || m.typing {
		t.Fatalf("current removed: current %v typing %v", m.lastSel, m.typing)
	}
}

// Tabs closing under the selection: the selection follows the list, and E
// on a stale selection sends the first tab with errors instead of panicking.
func TestBrowserTabsShrink(t *testing.T) {
	m, f, _ := chatModel(t)
	m.tabs = []daemon.Tab{{Port: 3000}, {Port: 3001}, {Port: 3002}}
	m.tsel = 2
	data, _ := json.Marshal([]daemon.Tab{{Port: 3000}, {Port: 3001, Errors: 2}})
	runCmd(m, m.onEvent(client.Event{Event: "browser.updated", Data: data}))
	if m.tsel != 1 {
		t.Fatalf("browser.updated: tsel %d", m.tsel)
	}
	m.Update(tabsMsg{{Port: 3000}})
	if m.tsel != 0 {
		t.Fatalf("tabsMsg: tsel %d", m.tsel)
	}
	m.tabs = []daemon.Tab{{Port: 3000}, {Port: 3001, Errors: 2}}
	m.tsel = 5
	runCmd(m, m.keyBrowser("E"))
	if got := f.find("browser.send_errors"); len(got) != 1 || got[0]["port"] != float64(3001) {
		t.Fatalf("send_errors %v", got)
	}
}

// The permission dialog closes once the wait ends elsewhere or the session
// goes away, and y names the tool it showed (the daemon checks it).
func TestPermissionModalFollowsSession(t *testing.T) {
	waiting := func() *state.Session {
		s := sess("api-1", appA, state.Waiting, time.Now())
		s.WaitKind = "permission"
		s.PendingTool = &state.Tool{Name: "Bash", Input: "rm -rf build"}
		return s
	}
	open := func(t *testing.T) (*model, *fakePekoed) {
		m, f, _ := chatModel(t)
		m.sessions = []*state.Session{waiting()}
		m.maybePermission()
		if _, ok := m.modal.(*permissionModal); !ok {
			t.Fatalf("modal %T", m.modal)
		}
		return m, f
	}

	m, _ := open(t)
	answered := waiting()
	answered.State, answered.WaitKind, answered.PendingTool = state.Running, "", nil
	data, _ := json.Marshal(answered)
	runCmd(m, m.onEvent(client.Event{Event: "session.updated", Data: data}))
	if m.modal != nil {
		t.Fatalf("answered elsewhere: modal %T still open", m.modal)
	}

	m, _ = open(t)
	data, _ = json.Marshal(state.Session{Slug: "api-1"})
	runCmd(m, m.onEvent(client.Event{Event: "session.removed", Data: data}))
	if m.modal != nil {
		t.Fatalf("removed: modal %T still open", m.modal)
	}

	m, f := open(t)
	runCmd(m, m.onKey(key("y")))
	got := f.find("session.answer")
	if len(got) != 1 {
		t.Fatalf("session.answer %v", got)
	}
	tool, _ := got[0]["expect"].(map[string]any)["tool"].(map[string]any)
	if got[0]["answer"] != "allow" || tool["name"] != "Bash" || tool["input"] != "rm -rf build" {
		t.Fatalf("session.answer %v", got[0])
	}
}

// The wheel over a full-screen program without mouse support (less in the
// run window) scrolls that window: the Up/Down go to it, not the agent.
func TestAltScreenWheelGoesToShownWindow(t *testing.T) {
	m, f, _ := chatModel(t)
	m.window = engine.WinRun
	m.focus, m.typing = pAgent, true
	m.screen = &engine.Screen{Lines: []string{"commit abc"}, Width: 100, Height: 30, AltScreen: true}
	m.screenOf = "api-1"
	x0, y0 := m.agentOrigin()
	runCmd(m, m.onMouse(tea.MouseMsg{X: x0 + 1, Y: y0 + 1, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}))
	got := f.find("session.keys")
	if len(got) != 1 || got[0]["window"] != engine.WinRun {
		t.Fatalf("session.keys %v", got)
	}
}

// The wheel over [4]'s pty window starts typing into it, as Enter does,
// and that first notch already reaches the program: SGR for one that asked
// for the mouse, Up/Down for a full-screen one that did not. A wheel
// outside [4], over a dead pane, or under a modal moves no focus.
func TestWheelFocusesAgentPane(t *testing.T) {
	wheel := func(m *model, x, y int) {
		runCmd(m, m.onMouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}))
	}
	for _, c := range []struct {
		name   string
		screen engine.Screen
		want   string // what session.keys carries
	}{
		{"mouse", engine.Screen{MouseAny: true, AltScreen: true}, "hex"},
		{"altscreen", engine.Screen{AltScreen: true}, "keys"},
	} {
		m, f, _ := chatModel(t)
		m.window = engine.WinAgent
		m.focus, m.typing = pSessions, false
		m.unreadAt = map[string]int{"api-1": 3}
		sc := c.screen
		sc.Lines, sc.Width, sc.Height = []string{"hello"}, 100, 30
		m.screen, m.screenOf = &sc, "api-1"
		x0, y0 := m.agentOrigin()
		wheel(m, x0+1, y0+1)
		if m.focus != pAgent || !m.typing {
			t.Fatalf("%s: focus %d typing %v", c.name, m.focus, m.typing)
		}
		// the unread line stays until something is typed (pty_test.go)
		if _, ok := m.unreadAt["api-1"]; !ok {
			t.Errorf("%s: the unread line went with the wheel", c.name)
		}
		got := f.find("session.keys")
		if len(got) != 1 || got[0][c.want] == nil || got[0]["window"] != engine.WinAgent {
			t.Fatalf("%s: session.keys %v", c.name, got)
		}
		if c.want == "keys" {
			if k, _ := got[0]["keys"].([]any); len(k) != 1 || k[0] != "Up" {
				t.Fatalf("%s: keys %v", c.name, got[0]["keys"])
			}
		}
	}

	for _, c := range []struct {
		name  string
		setup func(m *model) (x, y int)
	}{
		{"outside", func(m *model) (int, int) { return 1, 1 }},
		{"dead", func(m *model) (int, int) {
			m.screen.Dead = true
			x0, y0 := m.agentOrigin()
			return x0 + 1, y0 + 1
		}},
		{"modal", func(m *model) (int, int) {
			m.modal = newProgressModal(m, "api-1")
			x0, y0 := m.agentOrigin()
			return x0 + 1, y0 + 1
		}},
	} {
		m, f, _ := chatModel(t)
		m.window = engine.WinAgent
		m.focus, m.typing = pSessions, false
		m.screen = &engine.Screen{Lines: []string{"hello"}, Width: 100, Height: 30, MouseAny: true}
		m.screenOf = "api-1"
		x, y := c.setup(m)
		wheel(m, x, y)
		if m.focus != pSessions || m.typing {
			t.Errorf("%s: focus %d typing %v", c.name, m.focus, m.typing)
		}
		if got := f.find("session.keys"); len(got) != 0 {
			t.Errorf("%s: session.keys %v", c.name, got)
		}
	}
}

// A left click on [4]'s pty window starts typing into it, as Enter does.
// The click that enters is not the program's (its press, drag and release
// stay with pekoe); the next click goes to a program that asked for the
// mouse. A click on a link only opens it; a dead pane or a session being
// created is not entered.
func TestClickEntersAgentPane(t *testing.T) {
	mouse := func(m *model, x, y int, b tea.MouseButton, a tea.MouseAction) {
		runCmd(m, m.onMouse(tea.MouseMsg{X: x, Y: y, Button: b, Action: a}))
	}
	click := func(m *model, x, y int) {
		mouse(m, x, y, tea.MouseButtonLeft, tea.MouseActionPress)
		mouse(m, x+1, y, tea.MouseButtonLeft, tea.MouseActionMotion)
		mouse(m, x+1, y, tea.MouseButtonLeft, tea.MouseActionRelease)
	}
	setup := func(t *testing.T) (*model, *fakePekoed) {
		m, f, _ := chatModel(t)
		m.window = engine.WinAgent
		m.focus, m.typing = pSessions, false
		m.unreadAt = map[string]int{"api-1": 3}
		// a full-screen program's screen has a line for every row of the pane
		_, ah := m.agentSize()
		lines := append([]string{"hello", "see https://example.com/x ok"}, make([]string, ah-2)...)
		m.screen = &engine.Screen{Lines: lines, Width: 100, Height: ah, MouseAny: true, AltScreen: true}
		m.screenOf = "api-1"
		return m, f
	}

	m, f := setup(t)
	x0, y0 := m.agentOrigin()
	click(m, x0+1, y0+1)
	if m.focus != pAgent || !m.typing {
		t.Fatalf("click: focus %d typing %v", m.focus, m.typing)
	}
	// the unread line stays until something is typed (pty_test.go)
	if _, ok := m.unreadAt["api-1"]; !ok {
		t.Errorf("click: the unread line went with the click")
	}
	if got := f.find("session.keys"); len(got) != 0 {
		t.Fatalf("the click that entered reached the program: %v", got)
	}
	click(m, x0+1, y0+1)
	if got := f.find("session.keys"); len(got) != 3 || got[0]["hex"] == nil {
		t.Fatalf("the second click: session.keys %v", got)
	}

	// a link only opens: no typing
	m, f = setup(t)
	_, ah := m.agentSize()
	ly := -1
	for y := 0; y < ah; y++ {
		if m.agentLinkAt(5, y) != "" {
			ly = y
		}
	}
	if ly < 0 {
		t.Fatal("no link drawn")
	}
	mouse(m, x0+5, y0+ly, tea.MouseButtonLeft, tea.MouseActionPress)
	if m.typing || m.linkDown == "" {
		t.Fatalf("link: typing %v linkDown %q", m.typing, m.linkDown)
	}

	for _, c := range []struct {
		name string
		set  func(m *model)
	}{
		{"dead", func(m *model) { m.screen.Dead = true }},
		{"creating", func(m *model) { m.sessions[0].State = state.Creating }},
	} {
		m, f := setup(t)
		c.set(m)
		click(m, x0+1, y0+1)
		if m.typing {
			t.Errorf("%s: typing after a click", c.name)
		}
		if got := f.find("session.keys"); len(got) != 0 {
			t.Errorf("%s: session.keys %v", c.name, got)
		}
	}
}

// msgsOf runs cmd (and batches inside it) without passing anything to
// Update: what a Cmd's goroutine does on its own.
func msgsOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, msgsOf(c)...)
		}
		return out
	case modalDoneMsg:
		return append([]tea.Msg{msg}, msgsOf(msg.cmd)...)
	default:
		return []tea.Msg{msg}
	}
}

// Cmds hand their results to Update as messages; they never write the
// model from their own goroutine (a data race with View).
func TestCmdsDoNotWriteModel(t *testing.T) {
	m, _, _ := chatModel(t)
	runCmd(m, m.onKey(key("T")))
	before := m.chat.info
	var reset tea.Msg
	for _, msg := range msgsOf(m.onKey(key("y"))) {
		if _, ok := msg.(chatResetMsg); ok {
			reset = msg
		}
	}
	if reset == nil || m.chat.info != before {
		t.Fatalf("tapi.reset: msg %v, info written in the Cmd: %v", reset, m.chat.info != before)
	}
	m.Update(reset)
	if m.chat.info == before || m.chat.info == nil {
		t.Fatal("chatResetMsg did not set the info")
	}

	m, f, _ := chatModel(t)
	f.reply = func(method string) any {
		if method == "session.new" {
			return errors.New("branch exists")
		}
		return nil
	}
	runCmd(m, m.openNew(nil))
	n, ok := m.modal.(*newModal)
	if !ok {
		t.Fatalf("modal %T", m.modal)
	}
	for _, r := range "feat/x" {
		n.key(key(string(r)))
	}
	msgs := msgsOf(n.key(key("enter")))
	if n.err != "" || len(msgs) != 1 {
		t.Fatalf("session.new: err written in the Cmd %q, msgs %v", n.err, msgs)
	}
	m.Update(msgs[0])
	if n.err != "branch exists" {
		t.Fatalf("err %q", n.err)
	}
}

// ↵ twice on a pick card before the reload shows it sent sends it once;
// a send that fails brings the card back.
func TestPickSentOnce(t *testing.T) {
	m, f, _ := chatModel(t)
	f.reply = func(method string) any {
		if method == "pick.list" { // the reload races the send: still unsent
			return []*state.Pick{{ID: 7, Slug: "api-1", At: time.Now()}}
		}
		return nil
	}
	m.picks = []*state.Pick{{ID: 7, Slug: "api-1", At: time.Now()}}
	m.window, m.focus = engine.WinAgent, pAgent
	m.ensurePickInput()
	first := m.onKey(key("enter"))
	m.View()
	second := m.onKey(key("enter"))
	runCmd(m, first)
	runCmd(m, second)
	if n := len(f.find("pick.send")); n != 1 {
		t.Fatalf("pick.send %d times", n)
	}
	if m.pendingPick() != nil {
		t.Fatal("the card is back while the pick is being sent")
	}

	m, f, _ = chatModel(t)
	f.reply = func(method string) any {
		if method == "pick.send" {
			return errors.New("tmux is gone")
		}
		return nil
	}
	m.picks = []*state.Pick{{ID: 7, Slug: "api-1", At: time.Now()}}
	m.window, m.focus = engine.WinAgent, pAgent
	m.ensurePickInput()
	msgs := msgsOf(m.onKey(key("enter")))
	for _, msg := range msgs {
		if _, ok := msg.(picksMsg); !ok { // keep the pick: the fake has no list
			m.Update(msg)
		}
	}
	if pk := m.pendingPick(); pk == nil || pk.ID != 7 {
		t.Fatalf("failed send: card %v, msgs %v", pk, msgs)
	}
}

// tapi chat in [4] takes the mouse like a session's pty window: a click on a
// link (OSC 8 or bare, found on the chat's own screen) only opens it, a
// click or the wheel elsewhere starts typing (the click that enters is not
// forwarded), and the next click goes to a program that asked for the mouse.
func TestMouseInTapiChat(t *testing.T) {
	mouse := func(m *model, x, y int, b tea.MouseButton, a tea.MouseAction) tea.Cmd {
		return m.onMouse(tea.MouseMsg{X: x, Y: y, Button: b, Action: a})
	}
	click := func(m *model, x, y int) {
		runCmd(m, mouse(m, x, y, tea.MouseButtonLeft, tea.MouseActionPress))
		runCmd(m, mouse(m, x+1, y, tea.MouseButtonLeft, tea.MouseActionMotion))
		runCmd(m, mouse(m, x+1, y, tea.MouseButtonLeft, tea.MouseActionRelease))
	}
	setup := func(t *testing.T) (*model, *fakePekoed) {
		m, f, _ := chatModel(t)
		m.window = engine.WinAgent
		m.chat.open = true
		m.focus, m.typing = pSessions, false
		// the session's window shows no links: the chat's are looked up
		m.screen = &engine.Screen{Lines: []string{"hello"}, Width: 100, Height: 30}
		m.screenOf = "api-1"
		m.chat.screen = &engine.Screen{Lines: []string{
			"tapi says hello",
			"PR \x1b]8;id=a;https://x.test/pr/9\x1b\\#9\x1b]8;;\x1b\\ ready",
			"see https://example.com/x ok",
		}, Width: 100, Height: 30, MouseAny: true, AltScreen: true}
		return m, f
	}
	// the rendered frame tells where a text sits
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

	for _, c := range []struct{ text, want string }{
		{"#9", "https://x.test/pr/9"},
		{"example.com", "https://example.com/x"},
	} {
		m, f := setup(t)
		x, y := at(t, m, c.text)
		if cmd := mouse(m, x, y, tea.MouseButtonLeft, tea.MouseActionPress); cmd != nil {
			t.Errorf("%s: the press did something: %v", c.text, msgsOf(cmd))
		}
		if m.linkDown != c.want || m.typing || m.focus != pAgent {
			t.Fatalf("%s: linkDown %q typing %v focus %d", c.text, m.linkDown, m.typing, m.focus)
		}
		// the release opens it (the Cmd is not run: it would start a browser)
		if cmd := mouse(m, x, y, tea.MouseButtonLeft, tea.MouseActionRelease); cmd == nil || m.linkDown != "" {
			t.Fatalf("%s: release did not open (linkDown %q)", c.text, m.linkDown)
		}
		if got := f.find("tapi.keys"); len(got) != 0 {
			t.Fatalf("%s: the link click reached tapi: %v", c.text, got)
		}
	}

	// a click off the links enters; the next one is tapi's
	m, f := setup(t)
	x, y := at(t, m, "tapi says")
	click(m, x, y)
	if !m.typing || m.focus != pAgent {
		t.Fatalf("click: typing %v focus %d", m.typing, m.focus)
	}
	if got := f.find("tapi.keys"); len(got) != 0 {
		t.Fatalf("the click that entered reached tapi: %v", got)
	}
	click(m, x, y)
	if got := f.find("tapi.keys"); len(got) != 3 || got[0]["hex"] == nil {
		t.Fatalf("the second click: tapi.keys %v", got)
	}
	if got := f.find("session.keys"); len(got) != 0 {
		t.Fatalf("chat clicks reached the session: %v", got)
	}

	// the wheel enters and already scrolls
	m, f = setup(t)
	x, y = at(t, m, "tapi says")
	runCmd(m, mouse(m, x, y, tea.MouseButtonWheelUp, tea.MouseActionPress))
	if !m.typing || m.focus != pAgent {
		t.Fatalf("wheel: typing %v focus %d", m.typing, m.focus)
	}
	if got := f.find("tapi.keys"); len(got) != 1 || got[0]["hex"] == nil {
		t.Fatalf("wheel: tapi.keys %v", got)
	}

	// a tapi that left is not entered (Enter restarts it)
	m, _ = setup(t)
	m.chat.screen.Dead = true
	x, y = at(t, m, "tapi says")
	click(m, x, y)
	if m.typing {
		t.Fatal("dead: typing after a click")
	}
}

// With [orchestrator] deliver = "idle", typing into tapi chat holds pekoed's
// delivery back (tapi.deliver_pause on, said again every few seconds), and
// leaving lets it go (off, once). With it off nothing is said.
func TestChatDeliverPause(t *testing.T) {
	m, f, _ := chatModel(t)
	now := time.Now()
	pauses := func() []any {
		var on []any
		for _, p := range f.find("tapi.deliver_pause") {
			on = append(on, p["on"])
		}
		return on
	}
	m.chat.open, m.typing = true, true
	m.chat.info = &daemon.Tapi{Deliver: &daemon.TapiDeliver{Mode: "off"}}
	runCmd(m, m.deliverPause(now))
	if got := pauses(); len(got) != 0 {
		t.Fatalf("deliver off: %v", got)
	}
	m.chat.info = &daemon.Tapi{Deliver: &daemon.TapiDeliver{Mode: "idle"}}
	runCmd(m, m.deliverPause(now))
	runCmd(m, m.deliverPause(now.Add(time.Second))) // not again so soon
	if got := pauses(); len(got) != 1 || got[0] != true {
		t.Fatalf("typing: %v", got)
	}
	runCmd(m, m.deliverPause(now.Add(deliverPauseEvery)))
	if got := pauses(); len(got) != 2 {
		t.Fatalf("said again: %v", got)
	}
	m.typing = false
	runCmd(m, m.deliverPause(now.Add(deliverPauseEvery+time.Second)))
	runCmd(m, m.deliverPause(now.Add(deliverPauseEvery+2*time.Second)))
	if got := pauses(); len(got) != 3 || got[2] != false {
		t.Fatalf("left: %v", got)
	}
}
