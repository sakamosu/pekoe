package tui

// tapi chat (docs/tapi-chat.md): `t` from any panel puts the tapi chat
// agent in the main panel and types into it; the prefix key leaves. The
// agent belongs to no session: it is pekoed's one tapi (tapi.* methods),
// so the window follows no selection and the session's screen is kept
// apart (m.chat.screen, not m.screen).

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/mascot"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

type chatState struct {
	open     bool
	info     *daemon.Tapi // tapi.get / tapi.updated
	screen   *engine.Screen
	fetching bool
	dirty    bool
	fitReq   string
	// reopen: the prefix just left the chat; a layout key (<prefix> z < > =)
	// comes back to it, typing, as it does to an agent
	reopen bool
}

type chatInfoMsg struct {
	v   *daemon.Tapi
	err error
}

// chatResetMsg: tapi.reset answered (T); set in Update, not in the Cmd.
type chatResetMsg struct{ v *daemon.Tapi }

type chatScreenMsg struct {
	sc  *engine.Screen
	err error
}

// openChat shows tapi chat and types into it, starting the agent on the
// first open.
func (m *model) openChat() tea.Cmd {
	if m.review != nil {
		return nil
	}
	m.chat.open = true
	m.focus = pAgent
	m.typing = true
	m.prefix, m.bracket = false, ""
	m.iss.open = false
	aw, ah := m.agentSize()
	return func() tea.Msg {
		var v daemon.Tapi
		if err := m.cl.Call("tapi.open", map[string]any{"width": aw, "height": ah}, &v); err != nil {
			return chatInfoMsg{err: err}
		}
		return chatInfoMsg{v: &v}
	}
}

// closeChat goes back to the selected session's window; the conversation
// stays (t again continues it). Zoomed, the focus stays on the main panel
// (the left column is hidden; focusing it would unzoom).
func (m *model) closeChat() tea.Cmd {
	m.chat.open = false
	m.typing = false
	if !m.zoomed() {
		m.focus = pSessions
	}
	return m.fetchScreen()
}

func (m *model) fetchChat() tea.Cmd {
	if !m.chat.open || m.cl == nil {
		return nil
	}
	if m.chat.fetching {
		m.chat.dirty = true
		return nil
	}
	m.chat.fetching = true
	return func() tea.Msg {
		var sc engine.Screen
		err := m.cl.Call("tapi.screen", nil, &sc)
		return chatScreenMsg{sc: &sc, err: err}
	}
}

// fitChat resizes tapi's pty to the pane, like fitScreen for sessions.
func (m *model) fitChat(sc *engine.Screen) tea.Cmd {
	aw, ah := m.agentSize()
	if m.layoutPending() {
		return nil // layoutSettled resizes every pty, tapi's too
	}
	if sc == nil || sc.Dead || sc.Width <= 0 || sc.Height <= 0 || (sc.Width == aw && sc.Height == ah) {
		return nil
	}
	key := fmt.Sprintf("%dx%d>%dx%d", sc.Width, sc.Height, aw, ah)
	if m.chat.fitReq == key {
		return nil
	}
	m.chat.fitReq = key
	return func() tea.Msg {
		_ = m.cl.Call("tapi.resize", map[string]any{"width": aw, "height": ah}, nil)
		return nil
	}
}

// chatUpdate handles tapi chat's own messages; ok=false passes msg on.
func (m *model) chatUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case chatInfoMsg:
		if msg.err != nil {
			m.setMsg("tapi: "+msg.err.Error(), true)
			if m.chat.open {
				return m.closeChat(), true
			}
			return nil, true
		}
		m.chat.info = msg.v
		return m.fetchChat(), true
	case chatResetMsg:
		m.chat.info = msg.v
		m.setMsg("tapi chat reset", false)
		return nil, true
	case chatScreenMsg:
		m.chat.fetching = false
		var cmd tea.Cmd
		if m.chat.dirty {
			m.chat.dirty = false
			cmd = m.fetchChat()
		}
		if !m.chat.open {
			return cmd, true
		}
		if msg.err != nil {
			m.chat.screen = nil
			return cmd, true
		}
		m.chat.screen = msg.sc
		return tea.Batch(cmd, m.fitChat(msg.sc)), true
	}
	return nil, false
}

// chatEvent handles tapi.updated / tapi.output.
func (m *model) chatEvent(ev client.Event) (tea.Cmd, bool) {
	switch ev.Event {
	case "tapi.updated":
		var v daemon.Tapi
		if json.Unmarshal(ev.Data, &v) == nil {
			m.chat.info = &v
		}
		return nil, true
	case "tapi.output":
		return m.fetchChat(), true
	}
	return nil, false
}

// chatTypingKey: a key while typing into tapi. Everything goes to its pty
// (esc too: it interrupts Claude Code) except the prefix, which leaves.
func (m *model) chatTypingKey(k tea.KeyMsg) tea.Cmd {
	if m.prefixKey(k) {
		cmd := m.closeChat()
		// like leaving an agent: the prefix stays armed for z < > =
		m.prefix, m.prefixType, m.chat.reopen = true, true, true
		return cmd
	}
	if m.chat.screen != nil && m.chat.screen.Dead {
		switch k.String() {
		case "enter":
			return m.restartChat()
		case "x", "esc":
			return m.closeChat()
		}
		return nil
	}
	return m.chatSend(toTmux(k))
}

// chatKey: a key in the main panel while tapi chat shows but nobody types
// (a click on the left panels stops typing).
func (m *model) chatKey(key string) tea.Cmd {
	switch key {
	case "enter", "i", "t", "a":
		if m.chat.screen != nil && m.chat.screen.Dead && key == "enter" {
			m.typing = true
			return m.restartChat()
		}
		m.typing = true
		return m.fetchChat()
	case "esc", "h", "left":
		return m.closeChat()
	}
	return nil
}

func (m *model) restartChat() tea.Cmd {
	aw, ah := m.agentSize()
	m.chat.screen = nil
	return func() tea.Msg {
		var v daemon.Tapi
		if err := m.cl.Call("tapi.restart", map[string]any{"width": aw, "height": ah}, &v); err != nil {
			return chatInfoMsg{err: err}
		}
		return chatInfoMsg{v: &v}
	}
}

// confirmResetChat asks before forgetting the conversation (T).
func (m *model) confirmResetChat() tea.Cmd {
	m.modal = newConfirmModal(m, "Reset tapi chat", "tapi forgets this conversation (its Claude Code record is deleted).", "pekoe tapi reset", true, func() tea.Cmd {
		var cmd tea.Cmd
		if m.chat.open {
			cmd = m.closeChat()
		}
		return tea.Batch(cmd, func() tea.Msg {
			var v daemon.Tapi
			if err := m.cl.Call("tapi.reset", nil, &v); err != nil {
				return errMsg{err}
			}
			return chatResetMsg{v: &v}
		})
	})
	return nil
}

// chatSend writes a key to tapi's pty, in the order typed (order.go).
func (m *model) chatSend(f fwd) tea.Cmd {
	return m.inOrder(func() error {
		switch {
		case f.paste != "":
			return m.cl.Call("tapi.send", map[string]any{"text": f.paste}, nil)
		case f.literal != "":
			return m.cl.Call("tapi.keys", map[string]any{"literal": f.literal}, nil)
		case len(f.keys) > 0:
			return m.cl.Call("tapi.keys", map[string]any{"keys": f.keys}, nil)
		}
		return nil
	})
}

func (m *model) chatKeys(params map[string]any) tea.Cmd {
	return m.inOrder(func() error { return m.cl.Call("tapi.keys", params, nil) })
}

// chatMouse: a mouse event inside the main panel while tapi chat shows.
func (m *model) chatMouse(ev tea.MouseMsg, x, y int) tea.Cmd {
	if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft {
		m.focus = pAgent
	}
	sc := m.chat.screen
	if !m.typing || sc == nil {
		return nil
	}
	if sc.MouseAny {
		if hex := sgrMouse(ev, x, y); hex != nil {
			return m.chatKeys(map[string]any{"hex": hex})
		}
		return nil
	}
	if sc.AltScreen && (ev.Button == tea.MouseButtonWheelUp || ev.Button == tea.MouseButtonWheelDown) {
		k := "Up"
		if ev.Button == tea.MouseButtonWheelDown {
			k = "Down"
		}
		return m.chatKeys(map[string]any{"keys": []string{k}})
	}
	return nil
}

// chatMood is tapi's face in the chat window's title.
func (m *model) chatMood() mascot.Mood {
	v := m.chat.info
	switch {
	case v == nil || !v.Started:
		return mascot.Sleep
	case (m.chat.screen != nil && m.chat.screen.Dead) || !v.Alive:
		return mascot.Error
	case v.State == state.Waiting:
		return mascot.Wait
	case v.State == state.Running:
		return mascot.Work
	}
	return mascot.Idle
}

// chatWaits: tapi waits for the user while its window is closed.
func (m *model) chatWaits() bool {
	return !m.chat.open && m.chat.info != nil && m.chat.info.Alive && m.chat.info.State == state.Waiting
}

// viewChat is the main panel showing tapi chat. tapi's face is in the
// title; the status bar leaves its own out meanwhile (one face at a time).
func (m *model) viewChat(width, height int) []string {
	th := m.th
	f := m.focus == pAgent
	mood := m.chatMood()
	face := m.tapi.r.Line(m.eyesFor(mood, false))
	extra := ""
	if v := m.chat.info; v != nil {
		extra = "· " + v.Agent
		switch {
		case mood == mascot.Wait && v.WaitKind != "":
			extra += " · " + m.tapi.r.Paint(mascot.Wait, v.WaitKind)
		case mood == mascot.Work:
			extra += " · " + m.tapi.r.Paint(mascot.Work, "thinking")
		}
	}
	if m.typing {
		extra += " · " + th.FG(theme.Secondary, "typing")
	}
	title := face + " " + m.panelTitle(4, "", "tapi chat", extra, f)
	if m.tour != nil { // the tutorial's card has tapi's face (one face at a time)
		title = m.panelTitle(4, "", "tapi chat", extra, f)
	}
	cw, ch := width-4, height-2
	var rows []row
	switch {
	case m.chat.screen == nil && (m.chat.info == nil || !m.chat.info.Started):
		rows = m.bigRows(cw, ch, mascot.Sleep, "waking tapi up …", "")
	default:
		rows = m.chatRows(cw)
	}
	return m.box(width, height, boxOpt{title: title, rows: rows, focused: f, bottom: true})
}

// chatRows draws tapi's pty like the agent window: the cursor while typing,
// trailing blank rows dropped, and the way out when the agent exited.
func (m *model) chatRows(cw int) []row {
	sc := m.chat.screen
	if sc == nil {
		return []row{{Text: m.th.FG(theme.Dim, "…")}}
	}
	lines := append([]string(nil), sc.Lines...)
	if m.typing && sc.Cursor.Visible && sc.Cursor.Y < len(lines) {
		lines[sc.Cursor.Y] = withCursor(lines[sc.Cursor.Y], sc.Cursor.X)
	}
	end := len(lines)
	if !sc.AltScreen {
		for end > 0 && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" && end-1 > sc.Cursor.Y {
			end--
		}
	}
	lines = lines[:end]
	if sc.Dead {
		lines = append(dropPaneDead(lines), m.th.FG(theme.Dim, "tapi left · ")+m.th.Bold(theme.Secondary, "↵")+m.th.FG(theme.Dim, " restart  ")+m.th.Bold(theme.Secondary, "x")+m.th.FG(theme.Dim, " close"))
	}
	rows := make([]row, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, row{Text: clip(l, cw) + "\x1b[0m"})
	}
	return rows
}

// chatHints are the bottom bar's keys while tapi chat shows.
func (m *model) chatHints() [][2]string {
	p := strings.ReplaceAll(m.g.Keys.Prefix, "ctrl+", "^")
	if p == "" {
		p = "^q"
	}
	dead := m.chat.screen != nil && m.chat.screen.Dead
	if m.typing {
		if dead {
			return [][2]string{{"↵", "restart"}, {"x", "close"}, {p, "leave"}}
		}
		if !narrow(m.width) {
			zoom := "zoom"
			if m.zoomed() {
				zoom = "unzoom"
			}
			return [][2]string{{p, "leave"}, {p + " z", zoom}, {p + " < >", "width"}}
		}
		return [][2]string{{p, "leave"}}
	}
	if dead {
		return [][2]string{{"↵", "restart"}, {"esc", "close"}, {"T", "reset"}, {"?", "help"}}
	}
	return [][2]string{{"↵", "type"}, {"esc", "close"}, {"T", "reset"}, {"?", "help"}}
}

// chatKeysFull is the help list of the main panel while tapi chat shows.
func chatKeysFull() [][3]string {
	return [][3]string{{"↵", "type into tapi", "\r"}, {"esc", "close tapi chat", "esc"}, {"T", "reset tapi chat (forget the conversation)", "T"}}
}
