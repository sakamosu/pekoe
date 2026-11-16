package tui

// The pty [4] shows: a session's window (sessionPty) or tapi chat (chatPty).
// Both are drawn, typed into, clicked, resized and restarted by the same
// code below; what differs between them is only how a pty is built (where
// its screen lives, which pekoed methods reach it, what ↵ and x do once it
// exited). pty_test.go runs the same inputs through both.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/engine"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

type pty struct {
	sc *engine.Screen
	// shown: [4] draws this pty's rows (a session being created, or one
	// whose setup failed, shows something else)
	shown bool
	// call sends one of the pty methods pekoed has for both ("keys",
	// "send", "resize") as session.<method> with the slug and window, or
	// as tapi.<method>; nil when there is nothing to send to
	call func(method string, params map[string]any) error
	// enterable: a click or the wheel may start typing into it
	enterable bool
	// unread is the absolute line where the user left it (the "new" line);
	// -1 for none. seen forgets it: the user typed into the pty.
	unread int
	seen   func()
	// fitReq dedupes resize requests (fitPty); fitID tells ptys apart in it
	fitReq *string
	fitID  string
	// restart: ↵ on the exited pty; close: x or esc on it
	restart func() tea.Cmd
	close   func() tea.Cmd
	// deadRows ends the rows of an exited pty; nil: the common exit line
	deadRows func(lines []string) []string
	// cards are drawn inside the pane at the bottom (pick, error card)
	cards func(cw int) []row
	// leave is what the prefix does while typing, leaveLabel its word in
	// the bar: a session's window stays and the focus goes back to the
	// panels ("back"); tapi chat closes ("leave")
	leave      func() tea.Cmd
	leaveLabel string
}

// sessionPty is the selected session's shown window.
func (m *model) sessionPty() pty {
	s := m.current()
	p := pty{sc: m.screen, unread: -1, fitReq: &m.fitReq, cards: m.agentCards, leaveLabel: "back"}
	p.leave = func() tea.Cmd {
		m.typing = false
		if !m.layout.Zoom || narrow(m.width) { // zoomed, the left column is hidden
			m.focus = pSessions
		}
		m.prefix, m.prefixType = true, true
		return nil
	}
	if s == nil {
		return p
	}
	slug, win := s.Slug, m.window
	p.shown = win != winPR && win != winPreview && s.State != state.Creating && s.State != state.Queued && !(s.State == state.Failed && m.screen != nil && m.screen.Dead)
	p.enterable = s.State != state.Creating && s.State != state.Queued
	p.call = func(method string, params map[string]any) error {
		if m.cl == nil {
			return nil
		}
		params["slug"], params["window"] = slug, win
		return m.cl.Call("session."+method, params, nil)
	}
	p.fitID = slug + "\x00" + win
	if at, ok := m.unreadAt[slug]; ok && win == engine.WinAgent {
		p.unread = at
	}
	p.seen = func() { delete(m.unreadAt, slug) }
	switch win {
	case engine.WinAgent:
		p.restart = func() tea.Cmd {
			aw, ah := m.agentSize()
			return m.act("session.restart", map[string]any{"slug": slug, "width": aw, "height": ah}, "restarted "+slug)
		}
		p.close = func() tea.Cmd { return m.act("session.kill", map[string]any{"slug": slug}, "ended "+slug) }
	case engine.WinRun:
		p.restart = m.finishRun
		p.close = func() tea.Cmd { return m.closeRun(false) } // without telling the agent
		p.deadRows = func(lines []string) []string { return append(dropPaneDead(lines), "", m.runExitLine()) }
	case engine.WinDev:
		p.restart = func() tea.Cmd {
			m.window = engine.WinAgent
			return m.act("dev.toggle", map[string]any{"slug": slug}, "dev server restarted")
		}
	}
	if win != engine.WinAgent && win != engine.WinRun {
		// the dead screen goes with the window: left as the agent's, ↵
		// would restart the running agent
		p.close = func() tea.Cmd {
			m.window = engine.WinAgent
			m.screen = nil
			return tea.Batch(m.act("window.kill", map[string]any{"slug": slug, "window": win}, ""), m.fetchScreen())
		}
	}
	return p
}

// chatPty is tapi chat.
func (m *model) chatPty() pty {
	p := pty{
		sc: m.chat.screen, shown: m.chat.screen != nil, enterable: true,
		unread: -1, seen: func() { m.chat.unreadSet = false },
		fitReq: &m.chat.fitReq, fitID: "tapi",
		restart: m.restartChat, close: m.closeChat,
		leaveLabel: "leave",
	}
	p.leave = func() tea.Cmd { return m.leaveChat(true) }
	p.call = func(method string, params map[string]any) error {
		if m.cl == nil {
			return nil
		}
		return m.cl.Call("tapi."+method, params, nil)
	}
	if m.chat.unreadSet {
		p.unread = m.chat.unreadAt
	}
	return p
}

// shownPty is the pty [4] shows now.
func (m *model) shownPty() pty {
	if m.chat.open {
		return m.chatPty()
	}
	return m.sessionPty()
}

// ptySend writes a key (or a paste) to the pty, in the order typed
// (order.go). The user typed into it: the unread line has done its job
// (entering alone keeps it: tapi chat is entered as it opens).
func (m *model) ptySend(p pty, k tea.KeyMsg) tea.Cmd {
	if p.call == nil {
		return nil
	}
	if p.seen != nil {
		p.seen()
	}
	f, call := toTmux(k), p.call
	return m.inOrder(func() error {
		switch {
		case f.paste != "":
			return call("send", map[string]any{"text": f.paste})
		case f.literal != "":
			return call("keys", map[string]any{"literal": f.literal})
		case len(f.keys) > 0:
			return call("keys", map[string]any{"keys": f.keys})
		}
		return nil
	})
}

// ptyKeys writes raw keys ("hex" or "keys") to the pty, in order. Its
// errors are dropped: a mouse motion or a wheel notch that missed is not
// worth an error line each.
func (m *model) ptyKeys(p pty, params map[string]any) tea.Cmd {
	if p.call == nil {
		return nil
	}
	call := p.call
	return m.inOrder(func() error {
		_ = call("keys", params)
		return nil
	})
}

// ptyTypingKey: a key while typing into the pty. Everything goes to it (esc
// too: it interrupts Claude Code) except the prefix, which leaves; once its
// program exited, ↵ restarts it and x or esc closes it.
func (m *model) ptyTypingKey(p pty, k tea.KeyMsg) tea.Cmd {
	if m.prefixKey(k) {
		m.stopTyping()
		return p.leave()
	}
	if p.sc != nil && p.sc.Dead {
		return m.ptyDeadKey(p, k)
	}
	return m.ptySend(p, k)
}

// stopTyping leaves the pty [4] shows (the prefix, a click outside it):
// what was there has been seen, typed into or not, so its unread line goes
// (tapi chat marks the new place as it closes).
func (m *model) stopTyping() {
	if m.typing {
		if p := m.shownPty(); p.seen != nil {
			p.seen()
		}
	}
	m.typing = false
}

// ptyDeadKey: a key while typing into a pty whose program exited: ↵
// restarts it, x or esc closes it.
func (m *model) ptyDeadKey(p pty, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		return m.restartPty(p)
	case "x", "esc":
		m.stopTyping()
		if p.close != nil {
			return p.close()
		}
	}
	return nil
}

// restartPty: ↵ on an exited pty restarts what ran in it and types into
// the new one, as ↵ on a live pty types into it.
func (m *model) restartPty(p pty) tea.Cmd {
	if p.restart == nil {
		return nil
	}
	m.focus = pAgent
	m.typing = true
	return p.restart()
}

// enterPty starts typing into the pty from the mouse, as Enter does. It
// does nothing when already typing or where Enter would do something else
// (an exited pty: Enter restarts it; a session still being created). It
// reports whether it entered.
func (m *model) enterPty(p pty) bool {
	if m.typing || p.sc == nil || p.sc.Dead || !p.enterable {
		return false
	}
	m.focus = pAgent
	m.typing = true
	return true
}

// fitPty resizes the pty when it doesn't match the pane. resizeAll only
// runs at start and on terminal resizes, so a window spawned later by
// someone who didn't know the pane size (`pekoe new` run by an agent, whose
// stdout isn't a terminal) would otherwise keep its size: the agent wraps
// narrower than the pane, or paints past its border.
func (m *model) fitPty(p pty) tea.Cmd {
	aw, ah := m.agentSize()
	if m.layoutPending() {
		return nil // layoutSettled resizes them all
	}
	sc := p.sc
	if p.call == nil || sc == nil || sc.Dead || sc.Width <= 0 || sc.Height <= 0 || (sc.Width == aw && sc.Height == ah) {
		return nil
	}
	key := fmt.Sprintf("%s\x00%dx%d>%dx%d", p.fitID, sc.Width, sc.Height, aw, ah)
	if *p.fitReq == key {
		return nil // already asked; wait for tmux to apply it
	}
	*p.fitReq = key
	call := p.call
	return func() tea.Msg {
		_ = call("resize", map[string]any{"width": aw, "height": ah})
		return nil
	}
}

// ptyRows renders the pty's screen: the cursor (when typing), trailing
// blank rows dropped, the unread line where the user left, and the way out
// once the program exited.
func (m *model) ptyRows(p pty, cw int) []row {
	sc := p.sc
	if sc == nil {
		return []row{{Text: m.th.FG(theme.Dim, "…")}}
	}
	lines := append([]string(nil), sc.Lines...)
	if m.typing && sc.Cursor.Visible && sc.Cursor.Y < len(lines) {
		lines[sc.Cursor.Y] = withCursor(lines[sc.Cursor.Y], sc.Cursor.X)
	}
	// trim trailing blank rows so short output sits at the bottom
	end := len(lines)
	if !sc.AltScreen {
		for end > 0 && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" && end-1 > sc.Cursor.Y {
			end--
		}
	}
	lines = lines[:end]
	if p.unread >= 0 {
		if r := p.unread - sc.History + 1; r > 0 && r < len(lines) {
			mark := m.th.FG(theme.Border, strings.Repeat("─", 3)) + m.th.FG(theme.Dim, " new ") + m.th.FG(theme.Border, strings.Repeat("─", max(cw-8, 0)))
			lines = append(lines[:r], append([]string{mark}, lines[r:]...)...)
		}
	}
	if sc.Dead {
		if p.deadRows != nil {
			lines = p.deadRows(lines)
		} else {
			lines = append(dropPaneDead(lines), m.th.FG(theme.Dim, "exited · ")+m.th.Bold(theme.Secondary, "↵")+m.th.FG(theme.Dim, " restart  ")+m.th.Bold(theme.Secondary, "x")+m.th.FG(theme.Dim, " close"))
		}
	}
	rows := make([]row, 0, len(lines))
	for _, l := range lines {
		// capture-pane -e leaves the last SGR open (an agent's full-width
		// background is "set bg + erase line", not cells), so the padding,
		// the border and whatever follows would inherit it: close it here.
		rows = append(rows, row{Text: clip(l, cw) + "\x1b[0m"})
	}
	return rows
}

// ptyBody is the pane's body as drawn: the pty's rows with its card at the
// bottom. Rows are bottom-aligned in a ch-tall body.
func (m *model) ptyBody(p pty, cw, ch int) []row {
	rows := m.ptyRows(p, cw)
	var card []row
	if p.cards != nil {
		card = p.cards(cw)
	}
	if len(card) > 0 {
		keep := max(ch-len(card), 0)
		if len(rows) > keep {
			rows = rows[len(rows)-keep:]
		}
		rows = append(rows, card...)
	}
	return rows
}

// ptyLinkAt returns the URL drawn at (x, y) of [4]'s body.
func (m *model) ptyLinkAt(p pty, x, y int) string {
	if !p.shown || p.sc == nil {
		return ""
	}
	cw, ch := m.agentSize()
	return rowsLinkAt(m.ptyBody(p, cw, ch), ch, x, y)
}

// rowsLinkAt returns the URL drawn at (x, y) of a ch-tall body that shows
// rows bottom-aligned (box's bottom), as the pty windows do.
func rowsLinkAt(rows []row, ch, x, y int) string {
	if len(rows) > ch {
		rows = rows[len(rows)-ch:]
	}
	i := y - (ch - len(rows))
	if i < 0 || i >= len(rows) {
		return ""
	}
	return linkAt(rows[i].Text, x)
}

// ptyMouse: a mouse event at (x, y) of [4]'s body over a pty.
//
// A click on a link opens it, like ⌘-click in a bare terminal (mouse
// reports carry no ⌘, so any left click on a link counts); the click is not
// forwarded to the program. Elsewhere a click, or the wheel, starts typing
// into the pty as Enter does; the click that enters only enters (its
// press, drag and release stay with pekoe), the wheel's first notch already
// scrolls. While typing, the mouse goes to a program that asked for it, and
// the wheel of a full-screen program that did not becomes Up / Down.
func (m *model) ptyMouse(ev tea.MouseMsg, x, y int, p pty) tea.Cmd {
	wheel := ev.Button == tea.MouseButtonWheelUp || ev.Button == tea.MouseButtonWheelDown
	if ev.Button == tea.MouseButtonLeft {
		switch ev.Action {
		case tea.MouseActionPress:
			m.linkDown = m.ptyLinkAt(p, x, y)
			if m.linkDown != "" {
				m.focus = pAgent
				return nil
			}
		case tea.MouseActionRelease:
			if u := m.linkDown; u != "" {
				m.linkDown = ""
				if m.ptyLinkAt(p, x, y) != u {
					return nil
				}
				return func() tea.Msg {
					if err := openURL(u); err != nil {
						return errMsg{err}
					}
					return infoMsg("opened " + u)
				}
			}
		}
	}
	if m.clickEntered && ev.Button == tea.MouseButtonLeft && ev.Action != tea.MouseActionPress {
		if ev.Action == tea.MouseActionRelease {
			m.clickEntered = false
		}
		return nil
	}
	if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft {
		m.clickEntered = false
		m.focus = pAgent
		if m.enterPty(p) {
			m.clickEntered = true
			return nil
		}
	}
	if wheel {
		m.enterPty(p)
	}
	sc := p.sc
	if !m.typing || sc == nil {
		return nil
	}
	if sc.MouseAny {
		if hex := sgrMouse(ev, x, y); hex != nil {
			return m.ptyKeys(p, map[string]any{"hex": hex})
		}
		return nil
	}
	if wheel && sc.AltScreen {
		k := "Up"
		if ev.Button == tea.MouseButtonWheelDown {
			k = "Down"
		}
		return m.ptyKeys(p, map[string]any{"keys": []string{k}})
	}
	return nil
}

// ptyTypingHints are the bar's keys while typing into the pty.
func (m *model) ptyTypingHints(p pty) [][2]string {
	pf := m.prefixLabel()
	if p.sc != nil && p.sc.Dead {
		return [][2]string{{"↵", "restart"}, {"x", "close"}, {pf, p.leaveLabel}}
	}
	if !narrow(m.width) {
		zoom := "zoom"
		if m.zoomed() {
			zoom = "unzoom"
		}
		return [][2]string{{pf, p.leaveLabel}, {pf + " z", zoom}, {pf + " < >", "width"}}
	}
	return [][2]string{{pf, p.leaveLabel}}
}
