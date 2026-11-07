package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"boba/internal/engine"
	"boba/internal/mascot"
	"boba/internal/state"
	"boba/internal/theme"
)

func (m *model) View() string {
	if m.width == 0 {
		m.ime.set(0, 0, false)
		return ""
	}
	if m.tapi.splash {
		m.ime.set(0, 0, false)
		return m.paintBg(m.splashView())
	}
	if m.review != nil {
		out := strings.Split(m.review.view(), "\n")
		m.ime.set(extractCursor(out))
		return strings.Join(out, "\n")
	}
	var body []string
	if narrow(m.width) {
		body = m.viewNarrow()
	} else {
		left := m.viewLeft(leftWidth(m.width), m.height-1)
		right := m.viewAgent(m.width-leftWidth(m.width)-1, m.height-1)
		body = joinH(left, right, 1)
	}
	if c, ok := m.modal.(*confirmModal); ok && len(body) >= 2 {
		// boba asks: one line at the bottom of the main pane, puppy first
		x, _ := m.agentOrigin()
		cw := m.agentContentWidth()
		lines := append(m.noteLines(c, cw), m.askLine(c, cw))
		if keep := len(body) - 3; len(lines) > keep { // never over the panel's top border
			lines = lines[len(lines)-max(keep, 1):]
		}
		body = placeAt(body, len(body)-1-len(lines), x, lines)
	} else if m.modal != nil {
		body = overlay(body, m.modal.view(), m.width)
	}
	if tb := m.toastBlock(); len(tb) > 0 && len(body) > len(tb)+1 {
		body = placeAt(body, len(body)-len(tb)-2, max(m.width-w(tb[0])-3, 0), tb)
	}
	body = append(body, m.viewBar())
	for i := range body {
		body[i] = fit(body[i], m.width)
	}
	m.ime.set(extractCursor(body))
	return m.paintBg(strings.Join(body, "\n"))
}

// paintBg: explicit background so the cold low-chroma bg holds in every terminal theme.
func (m *model) paintBg(out string) string {
	if m.th.Mode == theme.Truecolor {
		out = lipgloss.NewStyle().Background(m.th.Color(theme.Bg)).Render(strings.ReplaceAll(out, "\x1b[0m", "\x1b[0m"+bgSeq(m.th, theme.Bg)))
	}
	return out
}

func (m *model) agentContentWidth() int {
	if narrow(m.width) {
		return m.width - 4
	}
	return m.width - leftWidth(m.width) - 1 - 4
}

// ---- left column ----

func (m *model) leftFocus() int {
	if m.focus == pAgent {
		return pSessions // Sessions stays expanded while the agent has focus
	}
	return m.focus
}

func (m *model) viewLeft(width, height int) []string {
	type panel struct {
		n     int
		build func(h int, focused bool) []string
		min   int
	}
	sessionsRows, sessionsFooter := m.sessionRows(width - 4)
	panels := []panel{
		{pSessions, func(h int, f bool) []string {
			return m.box(width, h, boxOpt{title: m.sessionsTitle(f), rows: sessionsRows, footer: sessionsFooter, focused: f})
		}, len(sessionsRows)},
		{pSecond, func(h int, f bool) []string { return m.secondPanel(width, h, f) }, m.secondCount()},
		{pThird, func(h int, f bool) []string { return m.thirdPanel(width, h, f) }, m.thirdCount()},
	}
	exp := m.leftFocus()
	// accordion: collapsed panels show title + up to 2 rows (more if space allows)
	heights := make([]int, len(panels))
	used := 0
	for i, p := range panels {
		if p.n == exp {
			continue
		}
		n := clampi(p.min, 1, 2)
		if exp == pSessions && p.n != pSessions && height >= 30 {
			n = clampi(p.min, 1, 6) // context panels can breathe while Sessions leads
		}
		heights[i] = n + 2
		used += heights[i]
	}
	for i, p := range panels {
		if p.n == exp {
			heights[i] = max(height-used, 4)
		}
	}
	// give back space when the expanded panel does not need it all
	for i, p := range panels {
		if p.n == exp {
			need := p.min + 3
			if heights[i] > need && exp != pSessions {
				extra := heights[i] - need
				heights[i] = need
				heights[0] += extra
			}
		}
	}
	var out []string
	for i, p := range panels {
		f := m.focus == p.n && !m.typing
		out = append(out, p.build(heights[i], f)...)
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", width))
	}
	return out[:height]
}

func (m *model) sessionsTitle(f bool) string {
	n := len(m.visible())
	pos := fmt.Sprintf("%d/%d", min(m.sel+1, n), n)
	extra := ""
	if m.filter != "" || m.filterOn {
		extra = "filter:" + m.filter
		if m.filterOn {
			extra += cursorMark + "▏"
		}
	}
	return m.panelTitle(1, m.ic.Sessions, "Sessions "+pos, extra, f)
}

func (m *model) sessionRows(cw int) ([]row, string) {
	now := time.Now()
	vis := m.visible()
	rows := make([]row, 0, len(vis))
	waiting, toReview := 0, 0
	var oldest time.Duration
	for _, s := range m.sessions {
		if s.State == state.Waiting {
			waiting++
			if d := now.Sub(s.StateSince); d > oldest {
				oldest = d
			}
		}
		if s.State == state.Done {
			toReview++
		}
	}
	for i, s := range vis {
		rows = append(rows, row{Text: m.sessionLine(s, cw, now), Sel: i == m.sel})
	}
	if len(vis) == 0 {
		hint := "no sessions · n new"
		if m.filter != "" {
			hint = "no match"
		}
		rows = append(rows, row{Text: m.th.FG(theme.Dim, hint)})
	}
	footer := ""
	var parts []string
	if cur := m.current(); cur != nil && cur.State == state.Running && cur.LastTool != "" {
		// what the selected agent is doing right now (PreToolUse)
		parts = append(parts, m.th.FG(theme.Ok, theme.Look(m.ic, state.Running, m.frame, m.g.AnimateOn()).Icon)+" "+m.th.FG(theme.Dim, truncRunes(cur.LastTool, 40)))
	}
	if waiting > 0 || toReview > 0 || len(parts) > 0 {
		if waiting > 0 {
			parts = append(parts, m.th.FG(theme.Warn, m.ic.Wait)+m.th.FG(theme.Dim, fmt.Sprintf(" %d waiting · oldest %s", waiting, theme.Ago(oldest))))
		}
		if toReview > 0 {
			parts = append(parts, m.th.FG(theme.Secondary, m.ic.Done)+m.th.FG(theme.Dim, fmt.Sprintf(" %d to review", toReview)))
		}
		footer = strings.Join(parts, "    ")
	}
	return rows, footer
}

// sessionLine: icon  name  state  age  +add -del  test  (grid columns)
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (m *model) sessionLine(s *state.Session, cw int, now time.Time) string {
	th := m.th
	lk := theme.Look(m.ic, s.State, m.frame, m.g.AnimateOn())
	icon := th.FG(lk.Role, fit(lk.Icon, 1))
	word := th.FG(lk.Role, fit(lk.Word, 8))
	stateW := 8
	ageW := 4
	diff := ""
	if s.Diffstat.Added+s.Diffstat.Deleted > 0 {
		diff = th.FG(theme.Ok, fmt.Sprintf("+%d", s.Diffstat.Added)) + " " + th.FG(theme.Err, fmt.Sprintf("-%d", s.Diffstat.Deleted))
	}
	test := " "
	switch s.Test.Status {
	case "pass":
		test = th.FG(theme.Ok, m.ic.Check)
	case "fail":
		test = th.FG(theme.Err, m.ic.Fail)
	case "running":
		test = th.FG(theme.Dim, "…")
	}
	diffW := 11
	// icon+sp + name + sp + state + sp + age + sp + diff + sp + test
	nameW := cw - 2 - 1 - stateW - 1 - ageW - 1 - diffW - 2
	if nameW < 8 {
		diffW, test = 0, ""
		nameW = cw - 2 - stateW - 1 - ageW
	}
	nameRole := theme.Text
	if s.State == state.Idle || s.State == state.Creating || s.State == state.Landed {
		nameRole = theme.Dim
	}
	// the PR and conflict tags never get truncated away; the branch name gives way first
	tag := ""
	if s.PR > 0 {
		tag += " " + th.FG(theme.Dim, fmt.Sprintf("#%d", s.PR))
	}
	if ct := m.conflictTag(s); ct != "" {
		tag += " " + ct
	}
	if s.Unread > 0 && s.Slug != m.lastSel {
		tag += " " + th.FG(theme.Secondary, "•")
	}
	branch := s.Branch
	if tw := w(tag); tw < nameW && w(branch)+tw > nameW {
		branch = fit(branch, nameW-tw)
	}
	name := th.FG(nameRole, branch) + tag
	age := ""
	if s.State != state.Idle {
		age = theme.Ago(now.Sub(s.StateSince))
	}
	line := icon + " " + fit(name, nameW) + " " + word + " " + th.FG(theme.Dim, fit(age, ageW))
	if diffW > 0 {
		line += " " + fit(diff, diffW) + " " + test
	}
	return line
}

func (m *model) secondCount() int {
	if m.sub2 == 1 {
		return len(m.tabs) + 1
	}
	return len(m.worktrees)
}

func (m *model) thirdCount() int {
	if m.sub3 == 1 {
		return len(m.picks)
	}
	return max(len(m.changes), 1)
}

func (m *model) secondPanel(width, h int, f bool) []string {
	th := m.th
	cw := width - 4
	cur := m.current()
	if m.sub2 == 1 {
		var rows []row
		errs, errPort := 0, 0
		for i, t := range m.tabs {
			mark := "  "
			if cur != nil && t.Slug == cur.Slug {
				mark = th.FG(theme.Secondary, fit(m.ic.Current, 1)) + " "
			}
			target := th.FG(theme.Dim, "?")
			if t.Slug != "" {
				target = t.Slug
			}
			path := t.Path
			if path == "" {
				path = "/"
			}
			rows = append(rows, row{Text: mark + fmt.Sprintf(":%d ", t.Port) + th.FG(theme.Dim, fit(path, max(cw-24, 6))) + " " + th.FG(theme.Dim, "→") + " " + target, Sel: f && i == m.tsel})
			if t.Errors > 0 {
				errs += t.Errors
				errPort = t.Port
			}
		}
		if len(rows) == 0 {
			rows = append(rows, row{Text: th.FG(theme.Dim, "  no localhost tabs · install the extension (boba ext)")})
		}
		footer := ""
		if errs > 0 {
			footer = "  " + th.FG(theme.Err, fmt.Sprint(errs)) + th.FG(theme.Dim, fmt.Sprintf(" console errors on :%d · ", errPort)) + th.Bold(theme.Secondary, "E") + th.FG(theme.Dim, " send to session")
		}
		title := m.panelTitle(2, m.ic.Browser, "Browser", fmt.Sprintf("· %d tabs linked by port", len(m.tabs)), f)
		return m.box(width, h, boxOpt{title: title, right: m.subtabHint(f), rows: rows, footer: footer, focused: f})
	}
	var rows []row
	for i, wt := range m.worktrees {
		mark := "  "
		isCur := cur != nil && wt.Slug == cur.Slug
		if isCur {
			mark = th.FG(theme.Secondary, fit(m.ic.Current, 1)) + " "
		}
		name := wt.Branch
		if name == "" {
			name = filepath.Base(wt.Path)
		}
		nameRole := theme.Text
		if wt.State == string(state.Creating) {
			nameRole = theme.Dim
		}
		extra := ""
		if wt.Port > 0 {
			extra = fmt.Sprintf(":%d", wt.Port)
		}
		if wt.State == string(state.Creating) {
			extra += "  creating…"
		}
		if wt.Main || isCur {
			extra = fit(extra, 7) + homeTilde(wt.Path)
		}
		rows = append(rows, row{Text: mark + th.FG(nameRole, fit(name, 17)) + th.FG(theme.Dim, extra), Sel: f && i == m.wsel})
	}
	if len(rows) == 0 {
		rows = append(rows, row{Text: th.FG(theme.Dim, "  no worktrees")})
	}
	title := m.panelTitle(2, m.ic.Worktrees, fmt.Sprintf("Worktrees %d", len(m.worktrees)), "", f)
	return m.box(width, h, boxOpt{title: title, right: m.subtabHint(f), rows: rows, focused: f})
}

func (m *model) subtabHint(f bool) string {
	if !f {
		return ""
	}
	return m.th.Bold(theme.Secondary, "[ ]") + m.th.FG(theme.Dim, " tab")
}

func homeTilde(p string) string {
	if h, err := userHome(); err == nil && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func (m *model) thirdPanel(width, h int, f bool) []string {
	th := m.th
	cur := m.current()
	curName := ""
	if cur != nil {
		curName = cur.Branch
	}
	if m.sub3 == 1 {
		var rows []row
		for i, pk := range m.picks {
			mark := th.FG(theme.Secondary, m.ic.Unsent)
			when := "now"
			if pk.Sent {
				mark = th.FG(theme.Dim, m.ic.Sent)
				when = "sent " + theme.Ago(time.Since(pk.At))
			} else if d := time.Since(pk.At); d > time.Minute {
				when = theme.Ago(d)
			}
			tag := pickTag(pk)
			rows = append(rows, row{Text: mark + fmt.Sprintf(" #%04d ", pk.ID) + th.FG(theme.Dim, fit(tag, max(width-24, 8))) + " " + th.FG(theme.Dim, when), Sel: f && i == m.psel})
		}
		if len(rows) == 0 {
			rows = append(rows, row{Text: th.FG(theme.Dim, "  no picks · ⌘⇧E in Chrome")})
		}
		title := m.panelTitle(3, m.ic.Picks, fmt.Sprintf("Picks %d", len(m.picks)), "· "+curName, f)
		return m.box(width, h, boxOpt{title: title, right: m.subtabHint(f), rows: rows, focused: f})
	}
	var rows []row
	peers := filePeers(cur)
	for i, c := range m.changes {
		rows = append(rows, row{Text: m.changeLine(c, peers[c.Path], width-4), Sel: f && i == m.csel})
	}
	if len(rows) == 0 {
		rows = append(rows, row{Text: th.FG(theme.Dim, "  no changes yet")})
	}
	title := m.panelTitle(3, m.ic.Changes, fmt.Sprintf("Changes %d", len(m.changes)), curName, f)
	return m.box(width, h, boxOpt{title: title, right: m.subtabHint(f), rows: rows, footer: m.changesFooter(cur), focused: f})
}

func pickTag(pk *state.Pick) string {
	el := pk.Selector
	if i := strings.LastIndex(el, ">"); i >= 0 {
		el = strings.TrimSpace(el[i+1:])
	}
	comp := ""
	if len(pk.Component) > 0 {
		var c struct {
			Name string `json:"name"`
		}
		if jsonUnmarshal(pk.Component, &c) == nil {
			comp = c.Name
		}
	}
	return strings.TrimSpace("<" + el + ">  " + comp)
}

// ---- agent panel ----

func (m *model) agentOrigin() (int, int) {
	if narrow(m.width) {
		return 2, m.narrowTopH() + 1
	}
	return leftWidth(m.width) + 1 + 2, 1
}

func (m *model) viewAgent(width, height int) []string {
	th := m.th
	s := m.current()
	f := m.focus == pAgent
	if s == nil {
		return m.box(width, height, boxOpt{title: m.panelTitle(4, m.ic.Agent, "Agent", "", f), rows: m.bigRows(width-4, height-2, m.emptyMood(), "no session selected", "n new session · boba new <branch>"), focused: f})
	}
	winLabel := "Agent"
	icon := m.ic.Agent
	switch m.window {
	case engine.WinReview:
		winLabel, icon = "Review", m.ic.Vim
	case engine.WinDev:
		winLabel = "Dev"
	}
	extra := "· " + s.Agent
	if s.Port > 0 {
		extra += fmt.Sprintf(" · :%d", s.Port)
	}
	if s.State == state.Creating {
		extra = "· not started"
	}
	if m.typing {
		extra += " · " + th.FG(theme.Secondary, "typing")
	}
	title := m.panelTitle(4, icon, winLabel+" "+s.Branch, extra, f)
	right := ""
	if len(s.Activity) > 0 && s.State != state.Creating {
		right = m.spark(s.Activity)
		if s.Tokens > 0 {
			right += " " + th.FG(theme.Dim, ktok(s.Tokens)+" tok")
		}
	}
	cw := width - 4
	ch := height - 2
	if s.State == state.Creating {
		return m.box(width, height, boxOpt{title: title, rows: m.bigRows(cw, ch, mascot.Work, "creating worktree "+s.Branch, creatingSub(m.progress[s.Slug])), focused: f})
	}
	if s.State == state.Failed && (m.screen == nil || m.screen.Dead) {
		rows := m.bigRows(cw, ch-4, mascot.Error, "setup failed", "")
		for _, l := range strings.Split(s.Error, "\n") {
			rows = append(rows, row{Text: th.FG(theme.Err, l)})
		}
		return m.box(width, height, boxOpt{title: title, rows: rows, focused: f, bottom: true})
	}
	if m.screen == nil && m.window == engine.WinAgent && (s.Stopped || s.State == state.Landed) {
		caption := "session ended · ↵ restart (resume)"
		if s.State == state.Landed {
			caption = "landed · worktree removed"
		}
		return m.box(width, height, boxOpt{title: title, rows: m.bigRows(cw, ch, mascot.Idle, caption, ""), focused: f})
	}
	return m.box(width, height, boxOpt{title: title, right: right, rows: m.agentRows(cw, ch), focused: f, bottom: true})
}

// agentRows is the body of the agent panel as drawn: the pty screen with the
// pick card at the bottom. Rows are bottom-aligned in a ch-tall body.
func (m *model) agentRows(cw, ch int) []row {
	rows := m.screenRows(cw, ch)
	// a card from outside (Chrome pick) sits inside the pane, at the bottom
	if card := m.pickCard(cw); len(card) > 0 {
		keep := max(ch-len(card), 0)
		if len(rows) > keep {
			rows = rows[len(rows)-keep:]
		}
		rows = append(rows, card...)
	}
	return rows
}

// agentLinkAt returns the URL drawn at (x, y) of the agent panel body.
func (m *model) agentLinkAt(x, y int) string {
	s := m.current()
	if m.screen == nil || s == nil || s.State == state.Creating || (s.State == state.Failed && m.screen.Dead) {
		return ""
	}
	cw, ch := m.agentSize()
	rows := m.agentRows(cw, ch)
	if len(rows) > ch {
		rows = rows[len(rows)-ch:]
	}
	i := y - (ch - len(rows))
	if i < 0 || i >= len(rows) {
		return ""
	}
	return linkAt(rows[i].Text, x)
}

// screenRows renders the captured pty screen with the cursor (when typing)
// and the unread line where the user left.
func (m *model) screenRows(cw, ch int) []row {
	sc := m.screen
	if sc == nil {
		return []row{{Text: m.th.FG(theme.Dim, "…")}}
	}
	lines := append([]string(nil), sc.Lines...)
	if m.typing && sc.Cursor.Visible && sc.Cursor.Y < len(lines) {
		lines[sc.Cursor.Y] = withCursor(lines[sc.Cursor.Y], sc.Cursor.X)
	}
	// trim trailing blank rows so short output sits at the bottom like the mock
	end := len(lines)
	if !sc.AltScreen {
		for end > 0 && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" && end-1 > sc.Cursor.Y {
			end--
		}
	}
	lines = lines[:end]
	s := m.current()
	if s != nil && m.window == engine.WinAgent {
		if at, ok := m.unreadAt[s.Slug]; ok {
			r := at - sc.History + 1
			if r > 0 && r < len(lines) {
				mark := m.th.FG(theme.Border, strings.Repeat("─", 3)) + m.th.FG(theme.Dim, " new ") + m.th.FG(theme.Border, strings.Repeat("─", max(cw-8, 0)))
				lines = append(lines[:r], append([]string{mark}, lines[r:]...)...)
			}
		}
	}
	if sc.Dead {
		lines = append(lines, m.th.FG(theme.Dim, "exited · ")+m.th.Bold(theme.Secondary, "↵")+m.th.FG(theme.Dim, " restart  ")+m.th.Bold(theme.Secondary, "x")+m.th.FG(theme.Dim, " close"))
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

func withCursor(line string, x int) string {
	plainW := ansi.StringWidth(line)
	if x >= plainW {
		return line + strings.Repeat(" ", x-plainW) + cursorMark + "\x1b[7m \x1b[27m"
	}
	pre := ansi.Truncate(line, x, "")
	// カーソル下の文字は 1 グラフェム丸ごと取る。幅 1 で切ると全角文字が入らず、
	// 空白が 1 セル挿入されて後ろの文字がずれる。
	ch, _, cw, _ := uniseg.FirstGraphemeClusterInString(ansi.Strip(ansi.TruncateLeft(line, x, "")), -1)
	if ch == "" || cw == 0 {
		ch, cw = " ", 1
	}
	post := ansi.TruncateLeft(line, x+cw, "")
	return pre + cursorMark + "\x1b[7m" + ch + "\x1b[27m" + post
}

// ---- pick card ----

type input struct {
	text   []rune
	pos    int
	pickID int64
}

func (in *input) String() string { return string(in.text) }

func (in *input) key(k tea.KeyMsg) bool {
	switch k.Type {
	case tea.KeyRunes, tea.KeySpace:
		r := k.Runes
		if k.Type == tea.KeySpace {
			r = []rune{' '}
		}
		in.text = append(in.text[:in.pos], append(append([]rune(nil), r...), in.text[in.pos:]...)...)
		in.pos += len(r)
	case tea.KeyBackspace:
		if in.pos > 0 {
			in.text = append(in.text[:in.pos-1], in.text[in.pos:]...)
			in.pos--
		}
	case tea.KeyDelete:
		if in.pos < len(in.text) {
			in.text = append(in.text[:in.pos], in.text[in.pos+1:]...)
		}
	case tea.KeyLeft:
		in.pos = max(in.pos-1, 0)
	case tea.KeyRight:
		in.pos = min(in.pos+1, len(in.text))
	case tea.KeyHome, tea.KeyCtrlA:
		in.pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		in.pos = len(in.text)
	case tea.KeyCtrlU:
		in.text, in.pos = in.text[in.pos:], 0
	default:
		return false
	}
	return true
}

func (in *input) render(th *theme.Theme, focused bool) string {
	if !focused {
		return string(in.text)
	}
	pre := string(in.text[:in.pos])
	post := string(in.text[in.pos:])
	return pre + cursorMark + th.FG(theme.Primary, "▏") + post
}

func (m *model) selectedPick() *state.Pick {
	if m.psel >= 0 && m.psel < len(m.picks) {
		return m.picks[m.psel]
	}
	return nil
}

// pendingPick is the oldest unsent pick for the selected session (or one
// no session owns), shown as the one card in the agent pane.
func (m *model) pendingPick() *state.Pick {
	s := m.current()
	var best *state.Pick
	for _, pk := range m.picks {
		if pk.Sent {
			continue
		}
		if s != nil && (pk.Slug == s.Slug || pk.Slug == "") {
			if best == nil || pk.ID < best.ID {
				best = pk
			}
		}
	}
	return best
}

func (m *model) ensurePickInput() {
	pk := m.pendingPick()
	if pk == nil {
		m.pickInput = nil
		return
	}
	if m.pickInput != nil && m.pickInput.pickID == pk.ID {
		return
	}
	in := &input{text: []rune(pk.Instruction), pickID: pk.ID}
	in.pos = len(in.text)
	m.pickInput = in
}

func (m *model) pickKey(k tea.KeyMsg) (tea.Cmd, bool) {
	in := m.pickInput
	pk := m.pendingPick()
	if pk == nil || pk.ID != in.pickID {
		m.ensurePickInput()
		return nil, false
	}
	switch k.String() {
	case "enter":
		slug := pk.Slug
		if s := m.current(); slug == "" && s != nil {
			slug = s.Slug
		}
		text := in.String()
		m.pickInput = nil
		return tea.Batch(m.act("pick.send", map[string]any{"id": pk.ID, "slug": slug, "text": text}, fmt.Sprintf("sent pick #%04d to %s", pk.ID, slug)), m.loadPicks()), true
	case "esc":
		m.pickInput = nil
		return tea.Batch(m.act("pick.discard", map[string]any{"id": pk.ID}, "discarded pick"), m.loadPicks()), true
	case "ctrl+s":
		m.modal = newSessionPicker(m, "Send pick to", func(slug string) tea.Cmd {
			return tea.Batch(m.act("pick.send", map[string]any{"id": pk.ID, "slug": slug, "text": in.String()}, "sent pick to "+slug), m.loadPicks())
		})
		return nil, true
	case "ctrl+n":
		nm := newNewModal(m, "")
		nm.pickID, nm.pickText = pk.ID, in.String()
		m.modal = nm
		return nil, true
	}
	if in.key(k) {
		return nil, true
	}
	return nil, false
}

func (m *model) pickCard(cw int) []row {
	pk := m.pendingPick()
	if pk == nil || m.window != engine.WinAgent {
		return nil
	}
	m.ensurePickInput()
	th := m.th
	in := m.pickInput
	focused := m.focus == pAgent && !m.typing
	var comp struct {
		Name      string `json:"name"`
		Framework string `json:"framework"`
		Via       string `json:"via"`
		Source    *struct {
			File string `json:"file"`
			Line int    `json:"line"`
		} `json:"source"`
	}
	_ = jsonUnmarshal(pk.Component, &comp)
	var styles map[string]string
	_ = jsonUnmarshal(pk.Styles, &styles)
	inner := cw - 4
	lines := []string{
		th.FG(theme.Secondary, theme.Prefix(m.ic.Browser)) + th.Bold(theme.Text, fmt.Sprintf("Pick #%04d", pk.ID)) + " " + th.FG(theme.Dim, "from Chrome · "+strings.TrimPrefix(strings.TrimPrefix(pk.URL, "http://"), "https://")+" · "+theme.Ago(time.Since(pk.At))+" ago"),
		"",
		th.FG(theme.Dim, "element   ") + pickElement(pk),
	}
	if comp.Name != "" {
		src := ""
		if comp.Source != nil && comp.Source.File != "" {
			src = fmt.Sprintf("%s:%d", comp.Source.File, comp.Source.Line)
		}
		via := ""
		if comp.Via != "" {
			via = " · via " + comp.Via
		}
		lines = append(lines, th.FG(theme.Dim, "component ")+th.FG(theme.Secondary, "<"+comp.Name+">")+" "+th.FG(theme.Dim, src+via))
	}
	if len(styles) > 0 {
		var parts []string
		for _, k := range []string{"padding", "margin", "font", "color", "background-color", "border-radius"} {
			if v := styles[k]; v != "" && v != "0px" && v != "normal" {
				parts = append(parts, k+" "+v)
			}
		}
		lines = append(lines, th.FG(theme.Dim, "computed  "+strings.Join(parts, " · ")))
	}
	if pk.Image != "" {
		size := pk.ImageSize
		if size == "" {
			size = "?"
		}
		attached := th.FG(theme.Dim, "attached as @…/picks/"+filepath.Base(pk.File))
		if m.graphics && m.sentImages[pk.ID] {
			// Kitty graphics: the screenshot itself replaces the placeholder
			id := imageID(pk.ID)
			for r := 0; r < pickImgRows; r++ {
				side := ""
				switch r {
				case 0:
					side = th.FG(theme.Dim, "clip "+size+" · png")
				case 1:
					side = attached
				}
				lines = append(lines, placeholderRow(id, r, pickImgCols)+"  "+side)
			}
		} else {
			// dashed placeholder: inline images only on terminals that support them
			lines = append(lines, th.FG(theme.Border, "┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄"), th.FG(theme.Border, "┆")+th.FG(theme.Dim, fit("  clip "+size+" · png", 22))+th.FG(theme.Border, "┆")+"  "+attached, th.FG(theme.Border, "┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄"))
		}
	}
	lines = append(lines, th.FG(theme.Secondary, "›")+" "+in.render(th, focused))
	target := pk.Slug
	if target == "" {
		if s := m.current(); s != nil {
			target = s.Slug
		}
	}
	keys := [][2]string{{"↵", "send to " + target}, {"^s", "other session"}, {"^n", "new session"}, {"esc", "discard"}}
	if !focused {
		keys = [][2]string{{"4", "focus card"}}
	}
	lines = append(lines, m.keyHints(keys, inner))
	// frame (border color; icon secondary; never primary — that is focus)
	b := func(s string) string { return th.FG(theme.Border, s) }
	out := []row{{Text: b("╭" + strings.Repeat("─", cw-2) + "╮")}}
	for _, l := range lines {
		out = append(out, row{Text: b("│") + " " + fit(l, cw-4) + " " + b("│")})
	}
	out = append(out, row{Text: b("╰" + strings.Repeat("─", cw-2) + "╯")})
	return out
}

func pickElement(pk *state.Pick) string {
	h := pk.OuterHTML
	if i := strings.Index(h, ">"); i >= 0 {
		h = h[:i+1]
	}
	text := ansi.Strip(strings.TrimSpace(stripTags(pk.OuterHTML)))
	if len([]rune(text)) > 24 {
		text = string([]rune(text)[:24]) + "…"
	}
	if text != "" {
		h += " \"" + text + "\""
	}
	return h
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ---- bottom bar (powerline) ----

func (m *model) modeName() (string, string) {
	if m.modal != nil {
		if n, ic := m.modal.mode(); n != "" {
			return n, ic
		}
	}
	if m.typing {
		if m.window == engine.WinReview {
			return "REVIEW", m.ic.Vim
		}
		return "TYPE", m.ic.Agent
	}
	switch m.focus {
	case pSecond:
		if m.sub2 == 1 {
			return "BROWSER", m.ic.Browser
		}
		return "WORKTREES", m.ic.Worktrees
	case pThird:
		if m.sub3 == 1 {
			return "PICKS", m.ic.Picks
		}
		return "CHANGES", m.ic.Changes
	case pAgent:
		return "AGENT", m.ic.Agent
	}
	return "SESSIONS", m.ic.Sessions
}

func (m *model) hints() [][2]string {
	if m.modal != nil {
		return m.modal.hints()
	}
	if m.typing {
		p := strings.ReplaceAll(m.g.Keys.Prefix, "ctrl+", "^")
		if p == "" {
			p = "^q"
		}
		if m.screen != nil && m.screen.Dead {
			return [][2]string{{"↵", "restart"}, {"x", "close"}, {p, "back"}}
		}
		return [][2]string{{p, "back"}}
	}
	switch m.focus {
	case pSecond:
		if m.sub2 == 1 {
			return [][2]string{{"j/k", "move"}, {"↵", "select"}, {"E", "send errors"}, {"?", "help"}}
		}
		return [][2]string{{"j/k", "move"}, {"↵", "open"}, {"n", "new"}, {"D", "del"}, {"?", "help"}}
	case pThird:
		if m.sub3 == 1 {
			return [][2]string{{"j/k", "move"}, {"↵", "edit"}, {"s", "send"}, {"n", "new"}, {"d", "del"}, {"?", "help"}}
		}
		return [][2]string{{"j/k", "move"}, {"↵", "review"}, {"r", "review"}, {"?", "help"}}
	case pAgent:
		if m.pendingPick() != nil {
			return [][2]string{{"↵", "send"}, {"i", "type"}, {"h", "back"}, {"u", "undo prompt"}, {"?", "help"}}
		}
		if m.screen != nil && m.screen.Dead {
			return [][2]string{{"↵", "restart"}, {"h", "back"}, {"r", "review"}, {"?", "help"}}
		}
		return [][2]string{{"↵", "type"}, {"h", "back"}, {"u", "undo prompt"}, {"r", "review"}, {"d", "dev"}, {"?", "help"}}
	}
	if m.filterOn {
		return [][2]string{{"↵", "apply"}, {"esc", "clear"}}
	}
	return [][2]string{{"j/k", "move"}, {"↵", "open"}, {"n", "new"}, {"r", "review"}, {"]a", "next wait"}, {"?", "help"}}
}

func (m *model) viewBar() string {
	th := m.th
	name, icon := m.modeName()
	segs := []string{m.barTapi(m.width), m.pill(theme.Prefix(icon)+name, theme.Primary, theme.Bg, true)}
	if s := m.current(); s != nil {
		port := ""
		if s.Port > 0 {
			port = " " + th.FG(theme.Dim, fmt.Sprintf(":%d", s.Port))
		}
		segs = append(segs, m.pill(theme.Prefix(m.ic.Worktrees)+s.Branch+port, theme.Select, theme.Text, false))
	}
	// session counts are tapi's speech now (barTapi); picks stay here
	var counts []string
	if pk := m.pendingPick(); pk != nil {
		counts = append(counts, th.FG(theme.Secondary, theme.Prefix(m.ic.Picks))+fmt.Sprintf("pick #%04d", pk.ID))
	}
	if len(counts) > 0 {
		segs = append(segs, m.pill(th.FG(theme.Dim, strings.Join(counts, "  ")), theme.Surface, theme.Dim, false))
	}
	sep := " "
	if th.Mode != theme.Truecolor {
		sep = " " + m.ic.Sep + " "
		if m.ic.Sep == "" {
			sep = " | "
		}
	}
	left := strings.Join(segs, sep)
	rightW := m.width - w(left) - 2
	right := ""
	if m.msg != "" {
		role := theme.Dim
		if m.msgErr {
			role = theme.Err
		}
		right = th.FG(role, clip(strings.SplitN(m.msg, "\n", 2)[0], max(rightW, 0)))
	} else {
		right = m.keyHints(m.hints(), max(rightW, 0))
	}
	gap := max(m.width-w(left)-w(right), 1)
	return left + strings.Repeat(" ", gap) + right
}

// ---- narrow (< 100 columns): left column becomes one tabbed panel ----

func (m *model) narrowTopH() int {
	n := len(m.visible())
	switch m.narrowTab {
	case 1:
		n = len(m.worktrees)
	case 2:
		n = len(m.changes)
	}
	return clampi(n, 1, 6) + 2
}

func (m *model) viewNarrow() []string {
	th := m.th
	topH := m.narrowTopH()
	f := m.focus != pAgent && !m.typing
	tabs := []struct {
		icon, name string
	}{{m.ic.Sessions, fmt.Sprintf("Sessions %d/%d", min(m.sel+1, len(m.visible())), len(m.visible()))}, {m.ic.Worktrees, "Worktrees"}, {m.ic.Changes, fmt.Sprintf("Changes %d", len(m.changes))}}
	var parts []string
	for i, t := range tabs {
		label := theme.Prefix(t.icon) + t.name
		if i == m.narrowTab {
			if f {
				label = th.Bold(theme.Primary, label)
			} else {
				label = th.FG(theme.Text, label)
			}
		} else {
			label = th.FG(theme.Dim, label)
		}
		parts = append(parts, label)
	}
	title := strings.Join(parts, "  ")
	var rows []row
	switch m.narrowTab {
	case 0:
		rows, _ = m.sessionRows(m.width - 4)
	case 1:
		for i, wt := range m.worktrees {
			rows = append(rows, row{Text: fit(wt.Branch, 20) + th.FG(theme.Dim, fmt.Sprintf(":%d", wt.Port)), Sel: f && i == m.wsel})
		}
	case 2:
		peers := filePeers(m.current())
		for i, c := range m.changes {
			rows = append(rows, row{Text: m.changeLine(c, peers[c.Path], m.width-4), Sel: f && i == m.csel})
		}
	}
	if m.narrowTab == 0 && len(rows) > topH-2 {
		start := clampi(m.sel-(topH-3), 0, len(rows)-(topH-2))
		rows = rows[start:]
	}
	top := m.box(m.width, topH, boxOpt{title: title, right: th.Bold(theme.Secondary, "[ ]") + th.FG(theme.Dim, " tab"), rows: rows, focused: f})
	agent := m.viewAgent(m.width, m.height-1-topH)
	return append(top, agent...)
}
