package tui

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
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

func ctrlQ() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlQ} }

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

	runCmd(m, m.onKey(ctrlQ()))
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
	runCmd(m, m.onKey(ctrlQ()))
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
