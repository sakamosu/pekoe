package tui

// Error cards: pekoed finds errors in a session's dev window and browser and
// offers them here ("send this error?"). One card at a time in the agent
// pane (a pick card comes first: the human asked for that one); the rest
// wait as rows of the Picks panel. Nothing reaches the agent until s.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/engine"
	"pekoe/internal/errcatch"
	"pekoe/internal/theme"
)

type errsMsg []errcatch.Report

type errDetailMsg struct {
	id      int64
	excerpt string
}

func (m *model) loadErrors() tea.Cmd {
	return func() tea.Msg {
		var rs []errcatch.Report
		_ = m.cl.Call("error.list", nil, &rs)
		return errsMsg(rs)
	}
}

// onErrorEvent: error.updated. The list is small; reload it.
func (m *model) onErrorEvent(data json.RawMessage) tea.Cmd {
	var ev struct {
		Report errcatch.Report `json:"report"`
		Fresh  bool            `json:"fresh"`
	}
	if json.Unmarshal(data, &ev) == nil && ev.Fresh {
		m.setMsg(fmt.Sprintf("error from %s · %s", errOrigin(ev.Report), ev.Report.Slug), false)
	}
	return m.loadErrors()
}

// sessionErrors counts the open errors of a session (Sessions list marker).
func (m *model) sessionErrors(slug string) int {
	n := 0
	for i := range m.errs {
		if m.errs[i].Slug == slug {
			n++
		}
	}
	return n
}

// pendingError is the card's error: the oldest open error of the selected
// session, unless a pick card is up (one card at a time; picks first).
func (m *model) pendingError() *errcatch.Report {
	s := m.current()
	if s == nil || m.window != engine.WinAgent || m.pendingPick() != nil {
		return nil
	}
	var best *errcatch.Report
	for i := range m.errs {
		r := &m.errs[i]
		if r.Slug == s.Slug && r.Status == errcatch.StatusNew && (best == nil || r.First.Before(best.First)) {
			best = r
		}
	}
	return best
}

// errOrigin: "dev" / "browser :3001".
func errOrigin(r errcatch.Report) string {
	if r.Source == errcatch.SourceBrowser {
		if r.Port > 0 {
			return fmt.Sprintf("browser :%d", r.Port)
		}
		return "browser"
	}
	return "dev"
}

func (m *model) errSourceIcon(r errcatch.Report) string {
	if r.Source == errcatch.SourceBrowser {
		return m.ic.Browser
	}
	return m.ic.Agent // terminal: the dev window
}

func (m *model) sendError(r errcatch.Report) tea.Cmd {
	return tea.Batch(m.act("error.send", map[string]any{"id": r.ID}, "sent error to "+r.Slug), m.dropError(r.ID))
}

func (m *model) ignoreError(r errcatch.Report) tea.Cmd {
	return tea.Batch(m.act("error.ignore", map[string]any{"id": r.ID}, "ignored error"), m.dropError(r.ID))
}

// dropError hides the error right away; the event reload confirms it.
func (m *model) dropError(id int64) tea.Cmd {
	for i := range m.errs {
		if m.errs[i].ID == id {
			m.errs = append(m.errs[:i], m.errs[i+1:]...)
			break
		}
	}
	return nil
}

// errorKey handles the card's keys while the agent pane has the focus (not
// typing: then every key belongs to the agent).
func (m *model) errorKey(key string) (tea.Cmd, bool) {
	r := m.pendingError()
	if r == nil || m.typing || (m.screen != nil && m.screen.Dead) {
		return nil, false // dead pane: ↵ restarts the agent
	}
	switch key {
	case "s":
		return m.sendError(*r), true
	case "i":
		return m.ignoreError(*r), true
	case "enter", "\r":
		return m.openErrorDetail(*r), true
	}
	return nil, false
}

// errorCard draws the card at the bottom of the agent pane.
func (m *model) errorCard(cw int) []row {
	r := m.pendingError()
	if r == nil || cw < 4 { // no room for the frame
		return nil
	}
	th := m.th
	focused := m.focus == pAgent && !m.typing
	inner := cw - 4
	head := th.FG(theme.Secondary, theme.Prefix(m.errSourceIcon(*r))) + th.FG(theme.Err, theme.Prefix(m.ic.Fail)) + th.Bold(theme.Text, "error") + " " +
		th.FG(theme.Dim, "from "+errOrigin(*r)+" · "+r.Kind+" · "+theme.Ago(time.Since(r.Last))+" ago")
	where := ""
	if loc := r.Loc(); loc != "" {
		where = th.FG(theme.Dim, "at ") + loc + th.FG(theme.Dim, "  ·  ")
	}
	where += th.FG(theme.Dim, fmt.Sprintf("×%d", r.Count))
	keys := [][2]string{{"s", "send"}, {"i", "ignore"}, {"↵", "detail"}}
	if !focused {
		keys = [][2]string{{"4", "focus card"}}
	}
	lines := []string{head, firstLine(r.Message), where, m.keyHints(keys, inner)}
	b := func(s string) string { return th.FG(theme.Border, s) }
	out := []row{{Text: b("╭" + strings.Repeat("─", cw-2) + "╮")}}
	for _, l := range lines {
		out = append(out, row{Text: b("│") + " " + fit(l, cw-4) + " " + b("│")})
	}
	return append(out, row{Text: b("╰" + strings.Repeat("─", cw-2) + "╯")})
}

// errorRow is an error waiting in the Picks panel.
func (m *model) errorRow(r errcatch.Report, width int, cur string) string {
	th := m.th
	when := theme.Ago(time.Since(r.Last))
	meta := errOrigin(r) + fmt.Sprintf(" ×%d", r.Count)
	if r.Slug != cur {
		meta += " → " + r.Slug
	}
	msgW := max(width-12-w(meta)-w(when), 8)
	return th.FG(theme.Secondary, m.ic.Unsent) + " " + th.FG(theme.Err, "err") + "   " + fit(firstLine(r.Message), msgW) + " " + th.FG(theme.Dim, meta) + " " + th.FG(theme.Dim, when)
}

func (m *model) selectedError() *errcatch.Report {
	i := m.psel - len(m.picks)
	if i >= 0 && i < len(m.errs) {
		return &m.errs[i]
	}
	return nil
}

// ---- detail modal ----

type errModal struct {
	m       *model
	r       errcatch.Report
	excerpt string
	top     int
}

func (m *model) openErrorDetail(r errcatch.Report) tea.Cmd {
	m.modal = &errModal{m: m, r: r}
	id := r.ID
	return func() tea.Msg {
		var det struct {
			Excerpt string `json:"excerpt"`
		}
		_ = m.cl.Call("error.get", map[string]any{"id": id}, &det)
		return errDetailMsg{id: id, excerpt: det.Excerpt}
	}
}

func (e *errModal) mode() (string, string) { return "ERROR", e.m.ic.Fail }
func (e *errModal) hints() [][2]string {
	return [][2]string{{"s", "send"}, {"i", "ignore"}, {"j/k", "scroll"}, {"esc", "close"}}
}

func (e *errModal) body() []string {
	th, r := e.m.th, e.r
	var lines []string
	for _, l := range strings.Split(cleanText(r.Message), "\n") { // a message may span lines
		lines = append(lines, th.FG(theme.Text, l))
	}
	lines = append(lines,
		th.FG(theme.Dim, fmt.Sprintf("%s · %s · ×%d · first %s · last %s", errOrigin(r), r.Kind, r.Count, r.First.Format("15:04:05"), r.Last.Format("15:04:05"))),
	)
	if loc := r.Loc(); loc != "" {
		lines = append(lines, "\x00sunk"+loc)
	}
	if r.URL != "" {
		lines = append(lines, th.FG(theme.Dim, "page ")+r.URL)
	}
	lines = append(lines, "\x00rule")
	for _, l := range strings.Split(r.Text, "\n") {
		lines = append(lines, strings.ReplaceAll(l, "\t", "    "))
	}
	if e.excerpt != "" {
		lines = append(lines, "\x00rule")
		for _, l := range strings.Split(e.excerpt, "\n") {
			lines = append(lines, strings.ReplaceAll(l, "\t", "    "))
		}
	}
	return lines
}

func (e *errModal) view() []string {
	m := e.m
	all := e.body()
	room := max(m.height-8, 4)
	e.top = clampi(e.top, 0, max(len(all)-room, 0))
	shown := all[e.top:min(e.top+room, len(all))]
	title := m.th.FG(theme.Err, theme.Prefix(m.ic.Fail)) + m.th.Bold(theme.Primary, "Error")
	if len(all) > room {
		title += m.th.FG(theme.Dim, fmt.Sprintf(" %d/%d", e.top+1, len(all)))
	}
	return m.modalFrame(m.modalWidth(100), title, append([]string{""}, shown...), e.hints(), false)
}

func (e *errModal) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "j", "down":
		e.top++
	case "k", "up":
		e.top = max(e.top-1, 0)
	case "s":
		return done(e.m.sendError(e.r))
	case "i":
		return done(e.m.ignoreError(e.r))
	case "esc", "q", "enter":
		return done(nil)
	}
	return nil
}
