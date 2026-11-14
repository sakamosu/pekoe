package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/engine"
)

// onKey routes a key to whoever eats it first. The order is the behaviour
// (keyorder_test.go pins it):
//
//  1. an open modal takes every key
//  2. typing: tapi chat, the prefix (leaves), a dead pane, else the pty
//  3. the key after the prefix: layout keys, the config key, l / h
//     (anything else falls through)
//  4. the [1] filter, then the issue query, take printable keys
//  5. the picks input and the error card in [4]
//  6. ] / [ and the key after them
//  7. global keys, then the prefix itself
//  8. the focused panel
func (m *model) onKey(k tea.KeyMsg) tea.Cmd {
	if m.modal != nil {
		return m.modal.key(k)
	}
	if m.typing {
		return m.onKeyTyping(k)
	}
	if m.prefix {
		if cmd, used := m.onKeyAfterPrefix(k); used {
			return cmd
		}
	}
	if m.filterOn {
		return m.onKeyFilter(k)
	}
	if m.iss.queryOn {
		return m.issueQueryKey(k)
	}
	if cmd, used := m.onKeyAgentCards(k); used {
		return cmd
	}
	if cmd, used := m.onKeyBracket(k); used {
		return cmd
	}
	if cmd, used := m.onKeyGlobal(k); used {
		return cmd
	}
	return m.onKeyPanel(k.String())
}

// onKeyTyping: everything goes to the pty (or tapi chat) except the prefix,
// which leaves.
func (m *model) onKeyTyping(k tea.KeyMsg) tea.Cmd {
	if m.chat.open {
		return m.chatTypingKey(k)
	}
	if m.prefixKey(k) {
		m.typing = false
		if !m.layout.Zoom || narrow(m.width) { // zoomed, the left column is hidden
			m.focus = pSessions
		}
		m.prefix, m.prefixType = true, true
		return nil
	}
	if m.screen != nil && m.screen.Dead {
		return m.onKeyDeadPane(k)
	}
	return m.forward(k)
}

// onKeyDeadPane: the process in this window has exited: nothing to type into.
func (m *model) onKeyDeadPane(k tea.KeyMsg) tea.Cmd {
	s := m.current()
	switch k.String() {
	case "enter":
		if m.window == engine.WinRun {
			return m.finishRun()
		}
		if s != nil && m.window == engine.WinAgent {
			aw, ah := m.agentSize()
			return m.act("session.restart", map[string]any{"slug": s.Slug, "width": aw, "height": ah}, "restarted "+s.Slug)
		}
		if s != nil && m.window == engine.WinDev {
			m.window = engine.WinAgent
			return m.act("dev.toggle", map[string]any{"slug": s.Slug}, "dev server restarted")
		}
	case "x", "esc":
		m.typing = false
		if s != nil && m.window == engine.WinRun {
			return m.closeRun(false) // without telling the agent
		}
		if s != nil && m.window != engine.WinAgent {
			// the dead screen goes with the window: left as the
			// agent's, ↵ would restart the running agent
			w := m.window
			m.window = engine.WinAgent
			m.screen = nil
			return tea.Batch(m.act("window.kill", map[string]any{"slug": s.Slug, "window": w}, ""), m.fetchScreen())
		}
		if s != nil {
			return m.act("session.kill", map[string]any{"slug": s.Slug}, "ended "+s.Slug)
		}
	}
	return nil
}

// onKeyAfterPrefix spends the prefix on the key after it. used is false when
// the key is not a prefix key: it then goes on as a plain key.
func (m *model) onKeyAfterPrefix(k tea.KeyMsg) (tea.Cmd, bool) {
	m.prefix = false
	fromTyping := m.prefixType
	m.prefixType = false
	reopen := m.chat.reopen // the prefix left tapi chat (chat.go)
	m.chat.reopen = false
	if cmd, ok := m.layoutKey(k.String()); ok {
		if fromTyping && !narrow(m.width) { // straight back to the agent
			m.focus, m.typing = pAgent, true
			if reopen { // … or to tapi
				m.chat.open = true
				cmd = tea.Batch(cmd, m.fetchChat())
			}
		}
		return cmd, true
	}
	if k.String() == configKey {
		return m.editConfig(), true
	}
	// after ^q left typing, any other key is a plain key: l still lands
	if !fromTyping {
		switch k.String() {
		case "l", "right":
			m.focus = pAgent
			return nil, true
		case "h", "left":
			m.focus = pSessions
			return nil, true
		}
	}
	return nil, false
}

// onKeyFilter edits the [1] filter: it takes every key while on.
func (m *model) onKeyFilter(k tea.KeyMsg) tea.Cmd {
	switch k.Type {
	case tea.KeyEnter, tea.KeyEsc:
		m.filterOn = false
		if k.Type == tea.KeyEsc {
			m.filter = ""
		}
		return m.onSelChange()
	case tea.KeyBackspace:
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		m.filter += string(k.Runes)
		if k.Type == tea.KeySpace {
			m.filter += " "
		}
	}
	m.sel = 0
	return nil
}

// onKeyAgentCards: the picks input and the error card in [4] take their keys
// before ] / [ and the global keys.
func (m *model) onKeyAgentCards(k tea.KeyMsg) (tea.Cmd, bool) {
	if m.pickInput != nil && m.focus == pAgent && !m.issueShown() && !m.chat.open {
		if cmd, used := m.pickKey(k); used {
			return cmd, true
		}
	}
	if m.focus == pAgent && m.bracket == "" && !m.issueShown() && !m.chat.open {
		if cmd, used := m.errorKey(k.String()); used {
			return cmd, true
		}
	}
	return nil, false
}

// onKeyBracket: ] / [ prefixes: ]a ]r [a, otherwise subtab switch.
func (m *model) onKeyBracket(k tea.KeyMsg) (tea.Cmd, bool) {
	key := k.String()
	if m.bracket != "" {
		b := m.bracket
		m.bracket = ""
		switch b + key {
		case "]a":
			return m.nextWaiting(), true
		case "[a":
			return m.backFromWaiting(), true
		case "]r":
			return m.nextReview(), true
		}
		return tea.Batch(m.subtab(b == "]"), m.onKey(k)), true
	}
	if key == "]" || key == "[" {
		m.bracket = key
		m.bracketSeq++
		seq := m.bracketSeq
		return tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return prefixTimeoutMsg{seq} }), true
	}
	return nil, false
}

// onKeyGlobal: keys that work from any panel, then the prefix itself.
func (m *model) onKeyGlobal(k tea.KeyMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "q", "ctrl+c":
		return m.quit(), true
	case "1":
		m.focus = pSessions
		return nil, true
	case "2":
		m.focus = pSecond
		return nil, true
	case "3":
		m.focus = pThird
		return nil, true
	case "4":
		m.focus = pAgent
		return nil, true
	case "tab":
		m.focus = m.focus%4 + 1
		return nil, true
	case "shift+tab":
		m.focus = (m.focus+2)%4 + 1
		return nil, true
	case "?":
		m.modal = newHelpModal(m)
		return nil, true
	case ":":
		m.modal = newCommandModal(m)
		return nil, true
	case "w":
		return m.openRepoPicker(nil), true
	case "t": // tapi chat, from any panel (chat.go)
		return m.openChat(), true
	case "T":
		return m.confirmResetChat(), true
	case tourKey: // replay the tutorial (tour.go)
		return m.startTour(), true
	case tipKey: // the next tip, while the empty main pane shows one (tips.go)
		if m.tipSpot() {
			m.nextTip()
			return nil, true
		}
	}
	if m.prefixKey(k) {
		m.prefix = true
		return nil, true
	}
	return nil, false
}

// onKeyPanel hands the key to the focused panel.
func (m *model) onKeyPanel(key string) tea.Cmd {
	switch m.focus {
	case pSessions:
		return m.keySessions(key)
	case pSecond:
		switch m.sub2 {
		case 1:
			return m.keyBrowser(key)
		case subIssues:
			return m.keyIssues(key)
		}
		return m.keyWorktrees(key)
	case pThird:
		if m.sub3 == 1 {
			return m.keyPicks(key)
		}
		return m.keyChanges(key)
	case pAgent:
		return m.onKeyMain(key)
	}
	return nil
}

// onKeyMain: [4] shows tapi chat, a PR, a preview, the dashboard or the
// session's agent (an issue, when one is shown, wins over the PR / preview).
func (m *model) onKeyMain(key string) tea.Cmd {
	if m.chat.open {
		return m.chatKey(key)
	}
	if m.window == winPR && !m.issueShown() {
		return m.keyPR(key)
	}
	if m.window == winPreview && !m.issueShown() {
		return m.keyPreview(key)
	}
	if m.dashShown() {
		return m.keyDash(key)
	}
	return m.keyAgent(key)
}
