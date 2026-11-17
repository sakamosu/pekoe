package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/engine"
	"pekoe/internal/errcatch"
)

// [4]'s body is bottom-aligned: an error card (or the pick card) pushes the
// pty's top rows out. A click forwarded to a full-screen program that takes
// the mouse goes to the pty row drawn where it landed, not to the body's y;
// a click on the card is not forwarded.
func TestPtyMouseRowUnderCard(t *testing.T) {
	m, f, _ := chatModel(t)
	aw, ah := m.agentSize()
	lines := make([]string, ah)
	for i := range lines {
		lines[i] = fmt.Sprintf("row%02d", i)
	}
	m.window = engine.WinAgent
	m.screen = &engine.Screen{Width: aw, Height: ah, Lines: lines, AltScreen: true, MouseAny: true}
	m.focus, m.typing = pAgent, true
	now := time.Now()
	m.errs = []errcatch.Report{{ID: 1, Slug: "api-1", Source: "dev", Kind: "go", Message: "panic: boom", Count: 1, First: now, Last: now, Status: "new"}}

	out := strings.Split(ansi.Strip(m.View()), "\n")
	at := func(s string) (int, int) {
		for y, l := range out {
			if i := strings.Index(l, s); i >= 0 {
				return ansi.StringWidth(l[:i]), y
			}
		}
		t.Fatalf("%q not drawn", s)
		return 0, 0
	}
	sent := func() string {
		ks := f.find("session.keys")
		if len(ks) == 0 {
			return ""
		}
		var seq strings.Builder
		for _, h := range ks[len(ks)-1]["hex"].([]any) {
			var b byte
			fmt.Sscanf(h.(string), "%02x", &b)
			seq.WriteByte(b)
		}
		return seq.String()
	}
	press := func(x, y int) {
		runCmd(m, m.onMouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}))
	}

	sx, sy := at("row10")
	press(sx, sy)
	ox, _ := m.agentOrigin()
	if want := fmt.Sprintf("\x1b[<0;%d;%dM", sx-ox+1, 10+1); sent() != want {
		t.Errorf("click on the drawn row10 sent %q, want %q", sent(), want)
	}

	n := len(f.find("session.keys"))
	cx, cy := at("panic: boom")
	press(cx, cy)
	if got := len(f.find("session.keys")); got != n {
		t.Errorf("a click on the card reached the pty: %q", sent())
	}
	// the wheel over the card still scrolls the program (at the row nearest)
	runCmd(m, m.onMouse(tea.MouseMsg{X: cx, Y: cy, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}))
	if got := sent(); len(f.find("session.keys")) != n+1 || !strings.HasPrefix(got, "\x1b[<64;") {
		t.Errorf("the wheel over the card: %q", got)
	}
}
