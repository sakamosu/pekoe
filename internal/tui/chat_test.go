package tui

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

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
	if !strings.Contains(out, "[4] tapi chat · claude · typing") || !strings.Contains(out, "tapi says hello") || strings.Contains(out, "the session's agent") {
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
	if out := checkSize(t, m, "closed"); strings.Contains(out, "tapi chat") || !strings.Contains(out, "the session's agent") {
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
	if out := checkSize(t, m, "dead"); !strings.Contains(out, "tapi left · ↵ restart  x close") {
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
// open the bar leaves its face to the window's title.
func TestChatWaitsInBar(t *testing.T) {
	m, _, _ := chatModel(t)
	data, _ := json.Marshal(daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Waiting, WaitKind: "permission", StateSince: time.Now()})
	runCmd(m, m.onEvent(client.Event{Event: "tapi.updated", Data: data}))
	if out := checkSize(t, m, "waits"); !strings.Contains(out, "chat waits · t") {
		t.Fatalf("bar:\n%s", out)
	}
	runCmd(m, m.onKey(key("t")))
	m.chat.info.State, m.chat.info.WaitKind = state.Waiting, "permission"
	out := checkSize(t, m, "open")
	if strings.Contains(out, "chat waits") || !strings.Contains(out, "tapi chat · claude · permission") {
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
	if !strings.Contains(out, "[4] tapi chat") || strings.Contains(out, "Sessions") || !strings.Contains(out, "zoom ^q z") {
		t.Fatalf("zoomed chat:\n%s", out)
	}
	if !strings.Contains(out, "^q leave  ^q z unzoom  ^q < > width") {
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
	if out := checkSize(t, m, "unzoomed chat"); !strings.Contains(out, "Sessions") || !strings.Contains(out, "[4] tapi chat") {
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
	if out := checkSize(t, m, "left zoomed"); strings.Contains(out, "tapi chat") {
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
