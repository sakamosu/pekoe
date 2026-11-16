package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Between tapi chat and the dashboard (the [4] of the tapi row).

// keys sends keys one by one the way the user types them, running what
// each returns.
func keys(m *model, ks ...tea.KeyMsg) {
	for _, k := range ks {
		runCmd(m, m.onKey(k))
	}
}

var (
	kRight = tea.KeyMsg{Type: tea.KeyRight}
	kEnter = tea.KeyMsg{Type: tea.KeyEnter}
)

// onDash: [4] has the focus and shows the dashboard, nobody types.
func onDash(t *testing.T, m *model, label string) {
	t.Helper()
	if !m.dashShown() || m.focus != pAgent || m.typing || m.chat.open || !m.onTapiRow() {
		t.Fatalf("%s: not on the dashboard: dash %v focus %d typing %v chat %v sel %d", label, m.dashShown(), m.focus, m.typing, m.chat.open, m.sel)
	}
}

// clickAt presses and releases the left button at (x, y).
func clickAt(m *model, x, y int) {
	runCmd(m, m.onMouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}))
	runCmd(m, m.onMouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}))
}

// tapiRowXY is where the tapi row of [1] is drawn.
func tapiRowXY(t *testing.T, m *model) (int, int) {
	t.Helper()
	r := m.panelRect(pSessions)
	for y := r.y; y < r.y+r.h; y++ {
		if sel, ok := m.sessionRowAt(r.x+3, y); ok && sel == tapiSel {
			return r.x + 3, y
		}
	}
	t.Fatal("the tapi row is not drawn")
	return 0, 0
}

// The user's way there (^q 1 4) works when the prefix leaves a chat
// typed into from the tapi row.
func TestChatToDashPlain(t *testing.T) {
	m, _, _ := chatModel(t)
	runCmd(m, m.selectTapi())
	keys(m, key("t"))
	keys(m, ctrlQ, runes("1"), runes("4"))
	onDash(t, m, "^q 1 4")

	m, _, _ = chatModel(t)
	runCmd(m, m.selectTapi())
	keys(m, key("t"), ctrlQ, runes("4"))
	onDash(t, m, "^q 4")
}

// Repro: a click on the left panels stops typing but leaves tapi chat
// open; ^q then only arms the prefix, so ^q 1 4 lands on tapi chat again.
func TestChatToDashAfterClickOut(t *testing.T) {
	m, _, _ := chatModel(t)
	runCmd(m, m.selectTapi())
	keys(m, key("t"))
	r := m.panelRect(pSecond)
	clickAt(m, r.x+3, r.y+2)
	if m.typing {
		t.Fatal("the click did not stop typing")
	}
	keys(m, ctrlQ, runes("1"), runes("4"))
	onDash(t, m, "click, ^q 1 4")
}

// Repro: a click on the tapi row (to go back to it) keeps tapi chat on
// [4]; the next 4 (or a click on [4]) is tapi chat again.
func TestChatToDashClickTapiRow(t *testing.T) {
	m, _, _ := chatModel(t)
	runCmd(m, m.selectTapi())
	keys(m, key("t"))
	x, y := tapiRowXY(t, m)
	clickAt(m, x, y)
	keys(m, runes("4"))
	onDash(t, m, "click the tapi row, 4")
}

// Repro: → on the tapi row (right, to [4]) opened tapi chat; it goes to
// the dashboard as l does. ↵ still opens tapi chat.
func TestChatToDashRight(t *testing.T) {
	for _, k := range []tea.KeyMsg{kRight, runes("l")} {
		m, _, _ := chatModel(t)
		runCmd(m, m.selectTapi())
		keys(m, key("t"), ctrlQ, k)
		onDash(t, m, "^q "+k.String())

		m, _, _ = chatModel(t)
		runCmd(m, m.selectTapi())
		m.focus = pSessions
		keys(m, k)
		onDash(t, m, k.String()+" on the tapi row")
	}
	m, _, _ := chatModel(t)
	runCmd(m, m.selectTapi())
	m.focus = pSessions
	keys(m, kEnter)
	if !m.chat.open || !m.typing {
		t.Fatal("↵ on the tapi row opens tapi chat")
	}
}

// ^q o goes to the dashboard in one move from tapi chat being typed into,
// whichever row was selected; t comes back to tapi chat, typing.
func TestChatToDashKey(t *testing.T) {
	for _, onTapi := range []bool{true, false} {
		m, f, _ := chatModel(t)
		if onTapi {
			runCmd(m, m.selectTapi())
		}
		keys(m, key("t"), runes("a")) // typing into tapi
		keys(m, ctrlQ, runes(dashKey))
		onDash(t, m, "^q o")
		if m.prefix || m.chat.reopen {
			t.Fatalf("the prefix is spent: prefix %v reopen %v", m.prefix, m.chat.reopen)
		}
		if out := checkSize(t, m, "dash"); !contains(out, "DASHBOARD") || contains(out, "tapi says hello") {
			t.Fatalf("[4]:\n%s", out)
		}
		keys(m, key("t"))
		if !m.chat.open || !m.typing || m.focus != pAgent {
			t.Fatalf("t from the dashboard: open %v typing %v focus %d", m.chat.open, m.typing, m.focus)
		}
		if n := len(f.find("session.keys")); n != 0 {
			t.Fatalf("keys reached the session: %d", n)
		}
		// and back again
		keys(m, ctrlQ, runes(dashKey))
		onDash(t, m, "^q o again")
	}

	// from a session's agent too, and from tapi chat left untyped
	m, _, _ := chatModel(t)
	m.focus, m.typing = pAgent, true
	keys(m, ctrlQ, runes(dashKey))
	onDash(t, m, "^q o from an agent")

	m, _, _ = chatModel(t)
	keys(m, key("t"))
	r := m.panelRect(pSecond)
	clickAt(m, r.x+3, r.y+2)
	keys(m, ctrlQ, runes(dashKey))
	onDash(t, m, "^q o from tapi chat left untyped")
}

// The prefix leaving tapi chat that nobody types into keeps z < > =
// as they were: the chat shows again, untyped, on the same panel.
func TestChatLeaveUntypedLayout(t *testing.T) {
	m, _, _ := chatModel(t)
	runCmd(m, m.selectTapi())
	keys(m, key("t"))
	r := m.panelRect(pSecond)
	clickAt(m, r.x+3, r.y+2)
	keys(m, ctrlQ)
	if m.chat.open {
		t.Fatal("the prefix leaves tapi chat")
	}
	keys(m, runes(">"))
	if !m.chat.open || m.typing || m.focus != pSessions {
		t.Fatalf("^q >: open %v typing %v focus %d", m.chat.open, m.typing, m.focus)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
