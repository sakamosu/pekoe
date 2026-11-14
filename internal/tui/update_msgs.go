package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/client"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// updateMain handles a message on the main screen (route has given tapi
// chat, the tapi window and the review screen their turn). Each group below
// switches on concrete message types, so a message is handled by exactly one
// group; a message no group knows goes to the open modal, if it wants it.
func (m *model) updateMain(msg tea.Msg) (tea.Model, tea.Cmd) {
	for _, h := range []func(tea.Msg) (tea.Cmd, bool){
		m.onNavMsg,
		m.onTimeMsg,
		m.onStatusMsg,
		m.onLoadedMsg,
		m.onFeatureMsg,
		m.onInputMsg,
	} {
		if cmd, ok := h(msg); ok {
			return m, cmd
		}
	}
	if m.modal != nil {
		if c, ok := m.modal.(interface{ other(tea.Msg) tea.Cmd }); ok {
			return m, c.other(msg)
		}
	}
	return m, nil
}

// onNavMsg: jumps to a session (]a, the inbox) and the review surface.
func (m *model) onNavMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case jumpMsg:
		return m.onJump(msg), true
	case reviewSurfaceMsg:
		if s := m.current(); s != nil && s.Slug == msg.slug {
			m.chat.open = false
			m.window = engine.WinReview
			m.screen = nil
			m.focus = pAgent
			m.typing = true
			return m.fetchScreen(), true
		}
		return nil, true
	}
	return nil, false
}

func (m *model) onJump(msg jumpMsg) tea.Cmd {
	// the session queue came round again: so does the inbox
	if m.attnRound[msg.slug] && len(m.inbox) > 0 {
		m.inboxRoundReset()
		if it := m.nextInbox(); it != nil {
			return m.openInbox(*it)
		}
	}
	if m.attnRound == nil {
		m.attnRound = map[string]bool{}
	}
	m.attnRound[msg.slug] = true
	if msg.from != "" && msg.from != msg.slug {
		m.returnTo = append(m.returnTo, msg.from)
	}
	m.focus = pSessions
	m.typing = false
	m.chat.open = false // the jump shows the session
	// tapi waits: its row (↵ opens tapi chat)
	if msg.slug == daemon.TapiSlug {
		return m.selectTapi()
	}
	cmd := m.selectSlug(msg.slug)
	if msg.pr { // in the queue for its PR: show the PR
		return tea.Batch(cmd, m.openPR())
	}
	return tea.Batch(cmd, m.maybePermission())
}

// onTimeMsg: the terminal size, the clocks and the connection.
func (m *model) onTimeMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.pv.cellW, m.pv.cellH = cellPixels()
		if m.review != nil {
			m.review.width, m.review.height = msg.Width, msg.Height
		}
		return tea.Batch(m.resizeAll(), m.fetchScreen(), m.fetchChat()), true

	case tickMsg:
		return m.onTick(), true

	case spinMsg:
		if !m.tapi.focused { // animations stop while the window is unfocused
			m.spinning = false
			return nil, true
		}
		m.frame++
		return spin(m.g.AnimateOn()), true

	case disconnectedMsg:
		m.setMsg("lost connection to pekoed", true)
		return tea.Quit, true
	}
	return nil, false
}

func (m *model) onTick() tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, tick())
	if m.frame%5 == 0 || m.current() != nil && m.current().State == state.Running {
		cmds = append(cmds, m.fetchScreen())
	}
	if m.chat.open && (m.frame%5 == 0 || m.chat.info != nil && m.chat.info.State == state.Running) {
		cmds = append(cmds, m.fetchChat())
	}
	if time.Since(m.msgAt) > 6*time.Second {
		m.msg = ""
	}
	if m.window == winPR && m.current() != nil && time.Since(m.pr.at) >= prRefresh {
		cmds = append(cmds, m.prFollow())
	}
	cmds = append(cmds, m.maybePermission(), m.maybeInboxPopup(time.Now()), m.escalate(), m.dashTick())
	m.tapiExpire()
	return tea.Batch(cmds...)
}

// onStatusMsg: errors and notes for the status line.
func (m *model) onStatusMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case errMsg:
		if msg.err != nil {
			m.setMsg(msg.err.Error(), true)
		}
		return nil, true

	case pickSendFailedMsg: // the card comes back to try again
		delete(m.pickSent, msg.id)
		m.setMsg(msg.err.Error(), true)
		return nil, true

	case infoMsg:
		m.setMsg(string(msg), false)
		return nil, true
	}
	return nil, false
}

// onLoadedMsg: lists and screens loaded from pekoed.
func (m *model) onLoadedMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case reposMsg:
		m.repos, m.reposLoaded = msg, true
		if m.sessionsIn && !m.homePicked {
			m.pickHome()
			return m.onSelChange(), true
		}
		if m.chosen != nil {
			for _, r := range m.repos {
				if r.ID == m.chosen.ID {
					rr := r
					m.chosen = &rr
				}
			}
		}
		return nil, true

	case sessionsMsg:
		cur := ""
		if s := m.current(); s != nil {
			cur = s.Slug
		}
		m.sessions = []*state.Session(msg)
		m.sortSessions()
		if cur != "" {
			if i, _ := m.find(cur); i >= 0 {
				m.sel = i
			}
		}
		m.clampSel()
		m.sessionsIn = true
		m.pickHome()
		return tea.Batch(m.onSelChange(), m.maybePermission()), true

	case worktreesMsg:
		if msg.dir != m.curRepoDir() {
			return nil, true // the current repository changed meanwhile
		}
		m.worktrees = msg.list
		m.wsel = clampi(m.wsel, 0, max(len(m.worktrees)-1, 0))
		return nil, true

	case changesMsg:
		if s := m.current(); s != nil && s.Slug == msg.slug {
			m.changes, m.changesOf = msg.list, msg.slug
			m.csel = clampi(m.csel, 0, max(len(m.changes)-1, 0))
		}
		return nil, true

	case tabsMsg:
		m.tabs = msg
		m.tsel = clampi(m.tsel, 0, max(len(m.tabs)-1, 0))
		if len(m.tabs) > 0 && m.sub2 == 0 && m.focus != pSecond {
			m.sub2 = 1 // linked tabs exist: show Browser in [2] (mock 05)
		}
		return nil, true

	case picksMsg:
		m.picks = msg
		m.prunePickSent()
		m.ensurePickInput()
		return m.sendPickImages(), true

	case errsMsg:
		m.errs = msg
		return nil, true

	case errDetailMsg:
		if em, ok := m.modal.(*errModal); ok && em.r.ID == msg.id {
			em.excerpt = msg.excerpt
		}
		return nil, true

	case screenMsg:
		return m.onScreen(msg), true

	case evMsg:
		return m.onEvent(client.Event(msg)), true
	}
	return nil, false
}

func (m *model) onScreen(msg screenMsg) tea.Cmd {
	m.fetching = false
	var cmd tea.Cmd
	if m.dirty {
		m.dirty = false
		cmd = m.fetchScreen()
	}
	s := m.current()
	if s == nil || s.Slug != msg.slug || msg.window != m.window {
		return cmd
	}
	if msg.err != nil {
		m.screen = nil
		return cmd
	}
	m.screen, m.screenOf = msg.sc, msg.slug
	cmd = tea.Batch(cmd, m.fitScreen(msg.slug, msg.window, msg.sc))
	if msg.window == engine.WinReview && msg.sc.Dead {
		// nvim exited (:PekoeSend → :qa): back to the agent screen
		slug := s.Slug
		m.window = engine.WinAgent
		m.screen = nil
		return tea.Batch(cmd, func() tea.Msg {
			_ = m.cl.Call("window.kill", map[string]any{"slug": slug, "window": engine.WinReview}, nil)
			return nil
		}, m.fetchScreen(), m.loadSessions())
	}
	return cmd
}

// onFeatureMsg: messages a feature's own file handles (issues, cleanup, the
// inbox, the dashboard, config, layout, PR, preview, runs, review), and the
// ] / [ timeout and a closing modal.
func (m *model) onFeatureMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case issuesMsg, issueViewMsg:
		return m.onIssuesMsg(msg), true

	case cleanupMsg:
		return m.onCleanup(msg), true

	case inboxMsg:
		m.onInboxList(msg)
		return nil, true

	case dashMsg:
		m.onDash(msg)
		return nil, true

	case dashStatsMsg:
		m.onDashStats(msg)
		return nil, true

	case inboxAnswerMsg:
		return m.onInboxAnswer(msg), true

	case noWaitMsg:
		// nothing else waits: go round the inbox again
		m.inboxRoundReset()
		if it := m.nextInbox(); it != nil {
			return m.openInbox(*it), true
		}
		m.setMsg("nothing is waiting", false)
		return nil, true

	case configEditedMsg:
		return m.onConfigEdited(msg), true

	case configLoadedMsg:
		return m.applyConfig(msg), true

	case layoutSettledMsg:
		return m.layoutSettled(msg.seq), true

	case prefixTimeoutMsg:
		if msg.seq == m.bracketSeq && m.bracket != "" {
			b := m.bracket
			m.bracket = ""
			return m.subtab(b == "]"), true
		}
		return nil, true

	case modalDoneMsg:
		m.modal = nil
		return msg.cmd, true

	case prMsg:
		return m.onPRMsg(msg), true

	case prLogMsg:
		m.onPRLogMsg(msg)
		return nil, true

	case prSendMsg:
		return m.onPRSendMsg(msg), true

	case previewMsg:
		return m.onPreviewMsg(msg), true

	case runPromptMsg:
		return m.onRunPrompt(msg), true

	case runStartedMsg:
		return m.onRunStarted(msg), true

	case runClosedMsg:
		return m.onRunClosed(msg), true

	case reviewOpenMsg:
		rm := newReviewModel(m.cl, msg.slug, m.th, m.ic, m.width, m.height, m.square)
		m.review = rm
		return rm.init(), true
	}
	return nil, false
}

// onInputMsg: keys and the mouse.
func (m *model) onInputMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case kittyKeyMsg:
		m.lastKeyAt = time.Now()
		// keys with no legacy encoding (Shift+Enter …) only matter to the pty
		if m.typing && m.modal == nil && m.chat.open {
			return m.chatKeys(map[string]any{"keys": []string{msg.name}}), true
		}
		if m.typing && m.modal == nil {
			if s := m.current(); s != nil {
				slug, win, name := s.Slug, m.window, msg.name
				return m.inOrder(func() error {
					return m.cl.Call("session.keys", map[string]any{"slug": slug, "window": win, "keys": []string{name}}, nil)
				}), true
			}
		}
		return nil, true

	case tea.MouseMsg:
		return m.onMouse(msg), true

	case tea.KeyMsg:
		return m.onKey(msg), true
	}
	return nil, false
}
