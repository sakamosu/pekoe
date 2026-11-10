package tui

// The tapi row (docs/orchestration.md「Sessions の先頭の行」): tapi, the
// orchestrator, always heads Sessions under its own heading (orchestrator
// ━━━), above the repository headings and outside the filter. It is a
// row, not a session: m.sel stays an index into visible(), and tapiSel (-1)
// stands for the tapi row, so current() is nil there and every session key
// finds nothing to act on. ↵ opens tapi chat.
// The row starts with the state icon, never a face (docs/mascot.md: one
// face at a time).

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/daemon"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// tapiSel is m.sel on the tapi row.
const tapiSel = -1

// tapiHeadingName is the heading above the tapi row, shaped like a
// repository heading (repoHeading) so the two sections read alike.
const tapiHeadingName = "orchestrator"

// tapiHeading: the tapi section's heading. It stands out a little from the
// repository headings without a color of its own (a color means something,
// docs/ui-guidelines.md): the name is always text, never dim, and the rule
// is heavy (━), which still tells the two apart in the none mode.
func (m *model) tapiHeading(cw int) string {
	th := m.th
	name := tapiHeadingName
	if w(name) > cw-4 {
		name = fit(name, max(cw-4, 1))
	}
	head := "  " + th.FG(theme.Text, name) + " "
	return head + th.FG(theme.Border, strings.Repeat("━", max(cw-w(head), 0)))
}

// tapiRowName is the name on the row; tapiWordX is where its state word
// starts (icon, space, name, space).
const (
	tapiRowName = "tapi"
	tapiWordX   = 2 + len(tapiRowName) + 1
)

func (m *model) onTapiRow() bool { return m.sel == tapiSel }

// clampSel keeps m.sel on a visible session, or on the tapi row.
func (m *model) clampSel() {
	if m.sel == tapiSel {
		return
	}
	m.sel = clampi(m.sel, 0, max(len(m.visible())-1, 0))
}

// moveSel moves the selection by delta over the tapi row and the visible
// sessions, wrapping at the ends.
func (m *model) moveSel(delta int) {
	p := m.sel + 1 // 0 is the tapi row
	m.moveWrap(&p, len(m.visible())+1, delta)
	m.sel = p - 1
}

// selectTapi puts the selection on the tapi row.
func (m *model) selectTapi() tea.Cmd {
	m.sel = tapiSel
	return m.onSelChange()
}

// tapiLook is the state icon and word of the tapi row.
func (m *model) tapiLook() theme.StateLook {
	v := m.chat.info
	switch {
	case v == nil || !v.Started:
		lk := theme.Look(m.ic, state.Idle, m.frame, m.g.AnimateOn())
		lk.Word = "asleep"
		return lk
	case !v.Alive:
		lk := theme.Look(m.ic, state.Failed, m.frame, m.g.AnimateOn())
		lk.Word = "exited"
		return lk
	}
	return theme.Look(m.ic, v.State, m.frame, m.g.AnimateOn())
}

// tapiBadges are the counts after the age: the open questions of the
// inbox (✉3) and the open requests of the ledger (☐2) once pekoed has them
// (docs/orchestration.md 段階 1・2). None yet.
func (m *model) tapiBadges() []string {
	return nil
}

// tapiLastLine is the first non-empty line of tapi's last reply.
func tapiLastLine(v *daemon.Tapi) string {
	if v == nil {
		return ""
	}
	for _, l := range strings.Split(v.LastMessage, "\n") {
		if l = strings.TrimSpace(stripCtl(l)); l != "" {
			return l
		}
	}
	return ""
}

// stripCtl drops control characters (the reply is the agent's text).
func stripCtl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// tapiLine: icon  tapi  state  age  badges  the last reply
func (m *model) tapiLine(cw int, now time.Time) string {
	th := m.th
	v := m.chat.info
	lk := m.tapiLook()
	age := ""
	if v != nil && v.Started && v.Alive && !v.StateSince.IsZero() {
		age = theme.Ago(now.Sub(v.StateSince))
	}
	nameRole := theme.Text
	if v == nil || !v.Started {
		nameRole = theme.Dim
	}
	line := th.FG(lk.Role, fit(lk.Icon, 1)) + " " + th.Bold(nameRole, tapiRowName) + " " + th.FG(lk.Role, fit(lk.Word, stateW)) + " " + th.FG(theme.Dim, fit(age, ageW))
	for _, b := range m.tapiBadges() {
		line += " " + b
	}
	if room := cw - w(line) - 1; room > 0 {
		if last := tapiLastLine(v); last != "" {
			line += " " + th.FG(theme.Dim, fit(last, room))
		}
	}
	return line
}

// tapiRowKey is a Sessions key on the tapi row; session keys do nothing.
func (m *model) tapiRowKey(key string) tea.Cmd {
	switch key {
	case "j", "down":
		m.moveSel(1)
		return m.onSelChange()
	case "k", "up":
		m.moveSel(-1)
		return m.onSelChange()
	case "g", "home":
		return nil // already at the top
	case "G", "end":
		m.sel = max(len(m.visible())-1, 0)
		if len(m.visible()) == 0 {
			m.sel = tapiSel
		}
		return m.onSelChange()
	case "enter", "right":
		return m.openChat()
	case "n":
		return m.openNew(nil)
	}
	return nil
}

// tapiRowHints are the bottom bar's keys on the tapi row.
func tapiRowHints() [][2]string {
	return [][2]string{{"j/k", "move"}, {"↵", "open tapi"}, {"T", "reset tapi"}, {"n", "new"}, {"?", "help"}}
}

// tapiRowKeysFull is the help list of Sessions on the tapi row.
func tapiRowKeysFull() [][3]string {
	return [][3]string{{"j/k", "move (wraps at the ends)", "j"}, {"↵", "open tapi chat", "\r"}, {"n", "new session", "n"}}
}

// sessionsPos is the "2/5" of the Sessions title ("tapi/5" on the tapi row).
func (m *model) sessionsPos() string {
	n := len(m.visible())
	if m.onTapiRow() {
		return fmt.Sprintf("%s/%d", tapiRowName, n)
	}
	return fmt.Sprintf("%d/%d", min(m.sel+1, n), n)
}

// tapiWaits: tapi waits for the user (permission or a question); ]a goes
// there like to a waiting session.
func (m *model) tapiWaits() bool {
	v := m.chat.info
	return v != nil && v.Alive && v.State == state.Waiting
}

// tapiInfoMsg: tapi.get at startup, so the row shows tapi's state before
// tapi chat is opened (errors are left out: the row stays "asleep").
type tapiInfoMsg struct{ v *daemon.Tapi }

func (m *model) loadTapi() tea.Cmd {
	return func() tea.Msg {
		var v daemon.Tapi
		if err := m.cl.Call("tapi.get", nil, &v); err != nil {
			return nil
		}
		return tapiInfoMsg{v: &v}
	}
}

// sessionRowAt is what a click at (x, y) selects in [1]: tapiSel or an
// index of visible(); ok=false off the rows (a heading, the border, another
// panel). It scrolls the rows the way box (and viewNarrow) does.
func (m *model) sessionRowAt(x, y int) (int, bool) {
	if m.zoomed() || (narrow(m.width) && m.narrowTab != 0) {
		return 0, false
	}
	r := m.panelRect(pSessions)
	rows, sels, footer := m.sessionRowsSel(r.w - 4)
	avail := r.h - 2
	if footer != "" && !narrow(m.width) && avail >= 2 { // the narrow tab has no footer
		avail--
	}
	start := 0
	if len(rows) > avail { // keep the selected row in view
		start = clampi(selRow(rows)-avail+1, 0, len(rows)-avail)
	}
	i := y - (r.y + 1)
	if i < 0 || i >= avail || x < r.x+1 || x >= r.x+r.w-1 {
		return 0, false
	}
	if i += start; i >= len(rows) || sels[i] == rowNone {
		return 0, false
	}
	return sels[i], true
}
