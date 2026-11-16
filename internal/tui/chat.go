package tui

// tapi chat (docs/tapi-chat.md): `t` from any panel puts the tapi chat
// agent in the main panel and types into it; the prefix key leaves. The
// agent belongs to no session: it is pekoed's one tapi (tapi.* methods),
// so the window follows no selection and the session's screen is kept
// apart (m.chat.screen, not m.screen).

import (
	"encoding/json"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
	// unreadAt is the absolute line where the user left tapi chat (the
	// "new" line, like a session's unreadAt), when unreadSet
	unreadAt  int
	unreadSet bool
	// paused: pekoed was told (tapi.deliver_pause) that a person types
	// into tapi chat, last at pausedAt
	paused   bool
	pausedAt time.Time
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

// hideChat takes tapi chat off [4], remembering where the user left it
// (the "new" line when it shows again).
func (m *model) hideChat() {
	if m.chat.open && m.chat.screen != nil {
		m.chat.unreadAt, m.chat.unreadSet = m.chat.screen.History+m.chat.screen.Cursor.Y, true
	}
	m.chat.open = false
}

// closeChat goes back to the selected session's window; the conversation
// stays (t again continues it). Zoomed, the focus stays on the main panel
// (the left column is hidden; focusing it would unzoom).
func (m *model) closeChat() tea.Cmd {
	m.hideChat()
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
	case tapiInfoMsg:
		if m.chat.info == nil { // tapi.updated may have come first
			m.chat.info = msg.v
		}
		return nil, true
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
		return tea.Batch(cmd, m.fitPty(m.chatPty())), true
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
			m.tapiOnChat()
		}
		return nil, true
	case "tapi.output":
		return m.fetchChat(), true
	}
	return nil, false
}

// chatKey: a key in the main panel while tapi chat shows but nobody types
// (a click on the left panels stops typing).
func (m *model) chatKey(key string) tea.Cmd {
	switch key {
	case "enter", "i", "t", "a":
		if m.chat.screen != nil && m.chat.screen.Dead && key == "enter" {
			return m.restartPty(m.chatPty())
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
	m.chat.unreadSet = false // a new pty: the old line is not in it
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
		m.chat.unreadSet = false // the conversation is gone, its line too
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
	title := face + " " + m.panelTitle(4, "", "tapi the orchestrator", extra, f)
	if m.tour != nil { // the tutorial's card has tapi's face (one face at a time)
		title = m.panelTitle(4, "", "tapi the orchestrator", extra, f)
	}
	cw, ch := width-4, height-2
	var rows []row
	switch {
	case m.chat.screen == nil && (m.chat.info == nil || !m.chat.info.Started):
		rows = m.bigRows(cw, ch, mascot.Sleep, "waking tapi up …", "")
	default:
		rows = m.ptyBody(m.chatPty(), cw, ch)
	}
	return m.box(width, height, boxOpt{title: title, rows: rows, focused: f, bottom: true})
}

// chatHints are the bottom bar's keys while tapi chat shows.
func (m *model) chatHints() [][2]string {
	if m.typing {
		return m.ptyTypingHints(m.chatPty())
	}
	if m.chat.screen != nil && m.chat.screen.Dead {
		return [][2]string{{"↵", "restart"}, {"esc", "close"}, {"T", "reset"}, {"?", "help"}}
	}
	return [][2]string{{"↵", "type"}, {"esc", "close"}, {"T", "reset"}, {"?", "help"}}
}

// chatKeysFull is the help list of the main panel while tapi chat shows.
func chatKeysFull() [][3]string {
	return [][3]string{{"↵", "type into tapi", "\r"}, {"esc", "close tapi chat", "esc"}, {"T", "reset tapi chat (forget the conversation)", "T"}}
}

// deliverPauseEvery: how often the TUI says again that a person types into
// tapi chat (pekoed keeps it for 20 s: deliverPauseLease)
const deliverPauseEvery = 5 * time.Second

// deliverPause keeps pekoed's delivery to tapi ([orchestrator] deliver)
// out of tapi chat's input box while a person has it open and types into
// it (the keys themselves hold it back for 30 s too, in pekoed), and lets
// it go as the person leaves. Called every second (onTick).
func (m *model) deliverPause(now time.Time) tea.Cmd {
	on := m.chat.open && m.typing && m.chat.info != nil && m.chat.info.Deliver != nil && m.chat.info.Deliver.Mode == "idle"
	switch {
	case m.cl == nil:
		return nil
	case !on && !m.chat.paused:
		return nil
	case on && m.chat.paused && now.Sub(m.chat.pausedAt) < deliverPauseEvery:
		return nil
	}
	m.chat.paused = on
	if on {
		m.chat.pausedAt = now
	}
	cl := m.cl
	return func() tea.Msg {
		_ = cl.Call("tapi.deliver_pause", map[string]any{"on": on}, nil)
		return nil
	}
}
