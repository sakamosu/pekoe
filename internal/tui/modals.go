package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pekoe/internal/daemon"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

func jsonUnmarshal(b []byte, v any) error {
	if len(b) == 0 {
		return fmt.Errorf("empty")
	}
	return json.Unmarshal(b, v)
}

func userHome() (string, error) { return os.UserHomeDir() }

// A modal is the only focus while open (background borders drop to border).
type modal interface {
	view() []string
	key(tea.KeyMsg) tea.Cmd
	hints() [][2]string
	mode() (name, icon string)
}

type modalDoneMsg struct{ cmd tea.Cmd }

func done(cmd tea.Cmd) tea.Cmd { return func() tea.Msg { return modalDoneMsg{cmd} } }

// frame draws a modal: primary border (err for destructive), surface fill.
func (m *model) modalFrame(width int, title string, lines []string, keys [][2]string, destructive bool) []string {
	th := m.th
	bc := theme.Primary
	if destructive {
		bc = theme.Err
	}
	height := len(lines) + 2
	if len(keys) > 0 {
		height += 2
	}
	b := m.borderPainter(bc, width, height)
	inner := width - 2
	cw := inner - 4
	surf := func(s string) string {
		s = "  " + fit(s, cw) + "  "
		if th.Mode == theme.Truecolor {
			return lipgloss.NewStyle().Background(th.Color(theme.Surface)).Render(strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Surface)))
		}
		return s
	}
	t := clip(title, inner-3)
	tl, tr, bl, br := "╭", "╮", "╰", "╯"
	if m.square {
		tl, tr, bl, br = "┌", "┐", "└", "┘"
	}
	out := []string{b(0, 0, tl+"─") + t + " " + b(3+w(t), 0, strings.Repeat("─", max(inner-w(t)-2, 0))+tr)}
	side := func(s string) string { y := len(out); return b(0, y, "│") + s + b(width-1, y, "│") }
	for _, l := range lines {
		if strings.HasPrefix(l, "\x00sunk") {
			// the thing that will run, sunk into bg: one full-width row, no colored text
			txt := strings.TrimPrefix(l, "\x00sunk")
			s := "  " + fit(txt, cw) + "  "
			if th.Mode == theme.Truecolor {
				s = lipgloss.NewStyle().Background(th.Color(theme.Bg)).Foreground(th.Color(theme.Text)).Render(s)
			}
			out = append(out, side(s))
			continue
		}
		if l == "\x00rule" {
			out = append(out, b(0, len(out), "├"+strings.Repeat("─", inner)+"┤"))
			continue
		}
		out = append(out, side(surf(l)))
	}
	if len(keys) > 0 {
		out = append(out, side(surf("")))
		out = append(out, side(surf(m.keyHints(keys, cw))))
	}
	out = append(out, b(0, len(out), bl+strings.Repeat("─", inner)+br))
	return out
}

func sunk(s string) string { return "\x00sunk" + s }

func (m *model) modalWidth(want int) int { return clampi(want, 30, m.width-4) }

// ---- permission (03 in the mock) ----

type permissionModal struct {
	m    *model
	slug string
}

func newPermissionModal(m *model, s *state.Session) *permissionModal {
	return &permissionModal{m: m, slug: s.Slug}
}

func (p *permissionModal) sess() *state.Session {
	for _, s := range p.m.sessions {
		if s.Slug == p.slug {
			return s
		}
	}
	return nil
}

// stale: the wait was answered elsewhere (web UI, extension, the agent's
// own window), ended, or the session is gone.
func (p *permissionModal) stale() bool {
	s := p.sess()
	return s == nil || s.State != state.Waiting || s.WaitKind != "permission"
}

func (p *permissionModal) mode() (string, string) { return "PERMISSION", p.m.ic.Wait }

func (p *permissionModal) hints() [][2]string {
	return [][2]string{{"y", "allow"}, {"n", "deny"}, {"g", "go to session"}, {"esc", "later"}}
}

func (p *permissionModal) view() []string {
	m := p.m
	th := m.th
	s := p.sess()
	if s == nil {
		return nil
	}
	wait := theme.Ago(time.Since(s.StateSince))
	title := th.FG(theme.Warn, theme.Prefix(m.ic.Wait)) + th.Bold(theme.Primary, "Permission") + " " + th.FG(theme.Dim, "· "+s.Branch+" · waiting "+wait)
	tool := "a tool"
	input := ""
	if s.PendingTool != nil {
		tool, input = s.PendingTool.Name, s.PendingTool.Input
	}
	lines := []string{"", s.Branch + " wants to run", "", sunk(th.Bold(theme.Text, tool) + "  " + input), ""}
	if s.PendingTool != nil && s.PendingTool.Touches != "" {
		lines = append(lines, th.FG(theme.Dim, "  touches  ")+th.FG(theme.Text, s.PendingTool.Touches))
		if also := s.PendingTool.AlsoIn; len(also) > 0 {
			// conflict forecast: another open session already changed this file
			lines = append(lines, "  "+th.FG(theme.Warn, theme.Prefix(m.ic.Warning))+th.FG(theme.Dim, "also changed in ")+th.FG(theme.Text, strings.Join(also, ", ")))
		}
	}
	if s.LastMessage != "" && s.PendingTool == nil {
		lines = append(lines, th.FG(theme.Dim, "  "+s.LastMessage))
	}
	lines = append(lines, th.FG(theme.Dim, "  rule     ")+th.FG(theme.Text, tool+"(…)")+th.FG(theme.Dim, " not in allowlist · ")+th.Bold(theme.Secondary, "A")+th.FG(theme.Dim, " always allow"))
	keys := [][2]string{{"y", "allow"}, {"n", "deny"}, {"g", "go to session"}, {"A", "always"}, {"esc", "later"}}
	return m.modalFrame(m.modalWidth(min(m.width*55/100, 96)), title, lines, keys, false)
}

func (p *permissionModal) key(k tea.KeyMsg) tea.Cmd {
	m := p.m
	answer := func(a string) tea.Cmd {
		// expect: the tool on screen; the daemon refuses the answer when the
		// session has moved on to another prompt meanwhile (as the web UI)
		var tool *state.Tool
		if s := p.sess(); s != nil {
			tool = s.PendingTool
		}
		return done(m.act("session.answer", map[string]any{"slug": p.slug, "answer": a, "expect": map[string]any{"tool": tool}}, ""))
	}
	switch k.String() {
	case "y":
		return answer("allow")
	case "n":
		return answer("deny")
	case "A":
		return answer("always")
	case "g":
		cmd := m.selectSlug(p.slug)
		m.focus, m.typing = pAgent, true
		m.dismissed[p.slug] = time.Now()
		return done(cmd)
	case "esc", "q":
		m.dismissed[p.slug] = time.Now()
		return done(nil)
	}
	return nil
}

// ---- confirm (destructive → err border) ----

type confirmModal struct {
	m           *model
	title, text string
	target      string
	subject     string // what the question is about (pekoe asks: "delete worktree fix-ci?")
	destructive bool
	yes         func() tea.Cmd
	notes       []confirmNote // lines above the question (land: conflict forecast)
}

func (c *confirmModal) about(subject string) *confirmModal { c.subject = subject; return c }

func (c *confirmModal) note(ns []confirmNote) *confirmModal { c.notes = ns; return c }

func newConfirmModal(m *model, title, text, target string, destructive bool, yes func() tea.Cmd) *confirmModal {
	return &confirmModal{m: m, title: title, text: text, target: target, destructive: destructive, yes: yes}
}

func (c *confirmModal) mode() (string, string) { return "CONFIRM", c.m.ic.Warning }
func (c *confirmModal) hints() [][2]string     { return [][2]string{{"y", "confirm"}, {"n", "cancel"}} }

func (c *confirmModal) view() []string {
	th := c.m.th
	role := theme.Primary
	iconRole := theme.Warn
	if c.destructive {
		role, iconRole = theme.Err, theme.Err
	}
	title := th.FG(iconRole, theme.Prefix(c.m.ic.Warning)) + th.Bold(role, c.title)
	lines := []string{"", c.text}
	if c.target != "" {
		lines = append(lines, "", sunk(c.target))
	}
	verb := "confirm"
	if c.destructive {
		verb = strings.ToLower(strings.Fields(c.title)[0])
	}
	return c.m.modalFrame(c.m.modalWidth(min(c.m.width*50/100, 84)), title, lines, [][2]string{{"y", verb}, {"n", "cancel"}}, c.destructive)
}

func (c *confirmModal) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "y", "enter":
		return done(c.yes())
	case "n", "esc", "q":
		return done(nil)
	}
	return nil
}

// ---- new session (04 in the mock) ----

type newModal struct {
	m        *model
	field    int // 0 branch, 1 from, 2 prompt
	inputs   [3]*input
	slug     string // set once created: shows progress
	pickID   int64
	pickText string
	err      string
	repo     *daemon.KnownRepo // where the session is created (the current repository)
}

func newNewModal(m *model, branch string) *newModal {
	n := &newModal{m: m, repo: m.curRepo()}
	for i := range n.inputs {
		n.inputs[i] = &input{}
	}
	n.inputs[0].text = []rune(branch)
	n.inputs[0].pos = len(n.inputs[0].text)
	return n
}

func newProgressModal(m *model, slug string) *newModal {
	n := newNewModal(m, "")
	n.slug = slug
	return n
}

func (n *newModal) mode() (string, string) { return "NEW", n.m.ic.Worktrees }

func (n *newModal) hints() [][2]string {
	if n.slug != "" {
		return [][2]string{{"esc", "cancel & remove worktree"}}
	}
	return [][2]string{{"tab", "field"}, {"↵", "create"}, {"esc", "cancel"}}
}

func (n *newModal) sess() *state.Session {
	for _, s := range n.m.sessions {
		if s.Slug == n.slug {
			return s
		}
	}
	return nil
}

func (n *newModal) view() []string {
	m := n.m
	th := m.th
	repo := "no repository · esc, then w"
	if n.repo != nil {
		repo = m.repoName(n.repo)
	}
	title := theme.Prefix(m.ic.Worktrees) + th.Bold(theme.Primary, "New session") + " " + th.FG(theme.Dim, "· "+repo)
	col := func(icon, label, value string) string {
		return fit(icon, 2) + th.FG(theme.Dim, fit(label, 9)) + value
	}
	var lines []string
	lines = append(lines, "")
	labels := []string{"branch", "from", "prompt"}
	placeholders := []string{"feat/x", "gh#123 (optional)", "initial prompt (optional)"}
	if n.slug == "" {
		for i, in := range n.inputs {
			v := in.render(th, n.field == i)
			if len(in.text) == 0 && n.field != i {
				v = th.FG(theme.Dim, placeholders[i])
			} else if len(in.text) == 0 {
				v = cursorMark + th.FG(theme.Primary, "▏") + th.FG(theme.Dim, placeholders[i])
			}
			lines = append(lines, col("", labels[i], v))
		}
		if n.pickID > 0 {
			lines = append(lines, col("", "pick", fmt.Sprintf("#%04d", n.pickID)))
		}
		if n.err != "" {
			lines = append(lines, "", th.FG(theme.Err, n.err))
		}
		keys := [][2]string{{"tab", "next field"}, {"↵", "create"}, {"esc", "cancel"}}
		return m.modalFrame(m.modalWidth(min(m.width*60/100, 100)), title, lines, keys, false)
	}
	s := n.sess()
	if s != nil {
		lines = append(lines, col("", "branch", s.Branch), col("", "base", s.Base+" "+th.FG(theme.Dim, s.RepoDir)))
	}
	lines = append(lines, "", "\x00rule")
	steps := m.progress[n.slug]
	if len(steps) == 0 {
		var p struct {
			Steps []daemon.Step `json:"steps"`
		}
		if m.cl.Call("session.progress", map[string]any{"slug": n.slug}, &p) == nil {
			m.progress[n.slug] = p.Steps
			steps = p.Steps
		}
	}
	for _, st := range steps {
		icon := th.FG(theme.Dim, " ")
		valRole := theme.Dim
		switch st.Status {
		case "ok":
			icon, valRole = th.FG(theme.Ok, m.ic.Check), theme.Text
		case "running":
			icon, valRole = th.FG(theme.Ok, theme.Look(m.ic, state.Running, m.frame, m.g.AnimateOn()).Icon), theme.Text
		case "fail":
			icon, valRole = th.FG(theme.Err, m.ic.Fail), theme.Err
		}
		v := th.FG(valRole, st.Detail)
		if st.Elapsed > 0 {
			v += " " + th.FG(theme.Dim, fmt.Sprintf("%.1fs", float64(st.Elapsed)/1000))
		}
		if st.Status == "running" && st.Progress > 0 {
			// gauge only for slow steps: ━ value (ok) + ─ rest (border), with the number
			const gw = 15
			fill := gw * st.Progress / 100
			v = th.FG(valRole, truncRunes(st.Detail, 28)) + " " + th.FG(theme.Ok, strings.Repeat("━", fill)) + th.FG(theme.Border, strings.Repeat("─", gw-fill)) +
				" " + fmt.Sprintf("%d%%", st.Progress) + th.FG(theme.Dim, fmt.Sprintf(" · %.0fs of ~%.0fs", float64(st.Elapsed)/1000, float64(st.Expected)/1000))
		}
		lines = append(lines, col(icon, st.Label, v))
	}
	if s != nil && s.State == state.Failed {
		lines = append(lines, "", th.FG(theme.Err, clip(strings.SplitN(s.Error, "\n", 2)[0], 90)))
	}
	keys := [][2]string{{"esc", "cancel & remove worktree"}, {"↵", "open when ready"}, {"b", "background"}}
	if s != nil && s.State != state.Creating && s.State != state.Failed {
		keys = [][2]string{{"↵", "open"}, {"b", "background"}}
	}
	if s != nil && s.State == state.Failed {
		keys = [][2]string{{"esc", "remove worktree"}, {"b", "keep & close"}}
	}
	return m.modalFrame(m.modalWidth(min(m.width*60/100, 100)), title, lines, keys, false)
}

func lastPath(p string) string {
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	return parts[len(parts)-1]
}

func (n *newModal) key(k tea.KeyMsg) tea.Cmd {
	m := n.m
	if n.slug != "" {
		s := n.sess()
		switch k.String() {
		case "esc":
			slug := n.slug
			if s != nil && (s.State == state.Creating || s.State == state.Failed) {
				return done(m.act("session.cancel", map[string]any{"slug": slug}, "cancelled "+slug))
			}
			return done(nil)
		case "b", "q":
			return done(nil)
		case "enter":
			if s != nil && s.State != state.Creating && s.State != state.Failed {
				m.focus, m.typing = pAgent, true
				return done(m.selectSlug(n.slug))
			}
			return nil
		}
		return nil
	}
	switch k.String() {
	case "esc":
		return done(nil)
	case "tab", "down":
		n.field = (n.field + 1) % 3
		return nil
	case "shift+tab", "up":
		n.field = (n.field + 2) % 3
		return nil
	case "enter":
		branch := strings.TrimSpace(n.inputs[0].String())
		if n.repo == nil {
			n.err = "no repository: esc, then w to pick one"
			return nil
		}
		dir := n.repo.Dir
		if branch == "" {
			n.err = "branch is required"
			return nil
		}
		from := strings.TrimSpace(n.inputs[1].String())
		if from != "" && !strings.HasPrefix(from, "gh#") {
			if _, err := fmt.Sscanf(from, "%d", new(int)); err == nil {
				from = "gh#" + from
			}
		}
		prompt := n.inputs[2].String()
		aw, ah := m.agentSize()
		if n.pickID > 0 {
			id, text := n.pickID, n.pickText
			return func() tea.Msg {
				var pk state.Pick
				if err := m.cl.Call("pick.send", map[string]any{"id": id, "new_branch": branch, "text": text, "repo": dir}, &pk); err != nil {
					return newErrMsg{n: n, err: err.Error()}
				}
				return newCreatedMsg{slug: pk.Slug}
			}
		}
		return func() tea.Msg {
			var s state.Session
			err := m.cl.Call("session.new", map[string]any{"repo": dir, "branch": branch, "from": from, "prompt": prompt, "width": aw, "height": ah}, &s)
			if err != nil {
				return newErrMsg{n: n, err: err.Error()}
			}
			return newCreatedMsg{slug: s.Slug}
		}
	}
	n.inputs[n.field].key(k)
	n.err = ""
	return nil
}

type newCreatedMsg struct{ slug string }

// newErrMsg: creating failed; the dialog shows it (set in Update, not in
// the Cmd's goroutine).
type newErrMsg struct {
	n   *newModal
	err string
}

func (n *newModal) other(msg tea.Msg) tea.Cmd {
	switch c := msg.(type) {
	case newCreatedMsg:
		n.slug = c.slug
		return tea.Batch(n.m.loadSessions(), n.m.loadWorktrees(), n.m.selectSlug(c.slug))
	case newErrMsg:
		if c.n == n {
			n.err = c.err
		}
	}
	return nil
}

// ---- session picker ("which session?") ----

type pickerModal struct {
	m     *model
	title string
	sel   int
	then  func(slug string) tea.Cmd
}

func newSessionPicker(m *model, title string, then func(string) tea.Cmd) *pickerModal {
	return &pickerModal{m: m, title: title, then: then, sel: m.sel}
}

func (p *pickerModal) mode() (string, string) { return "SELECT", p.m.ic.Sessions }
func (p *pickerModal) hints() [][2]string {
	return [][2]string{{"j/k", "move"}, {"↵", "send"}, {"esc", "cancel"}}
}

func (p *pickerModal) view() []string {
	m := p.m
	var lines []string
	lines = append(lines, "")
	cw := m.modalWidth(64) - 6
	for i, s := range m.sessions {
		l := m.sessionLine(s, cw, time.Now())
		if i == p.sel {
			l = m.th.SelectStyle().Render(fit(l, cw))
		}
		lines = append(lines, l)
	}
	return m.modalFrame(m.modalWidth(64), theme.Prefix(m.ic.Sessions)+m.th.Bold(theme.Primary, p.title), lines, p.hints(), false)
}

func (p *pickerModal) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "j", "down":
		p.sel = clampi(p.sel+1, 0, max(len(p.m.sessions)-1, 0))
	case "k", "up":
		p.sel = clampi(p.sel-1, 0, max(len(p.m.sessions)-1, 0))
	case "enter":
		if p.sel < len(p.m.sessions) {
			return done(p.then(p.m.sessions[p.sel].Slug))
		}
	case "esc", "q":
		return done(nil)
	}
	return nil
}

// ---- help: a runnable key list (Enter executes, like lazygit's menu) ----

type helpItem struct {
	key, action string
	run         func() tea.Cmd
	global      bool
}

type helpModal struct {
	m     *model
	items []helpItem
	sel   int
}

func newHelpModal(m *model) *helpModal {
	h := &helpModal{m: m}
	send := func(key string) func() tea.Cmd {
		return func() tea.Cmd { return m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}) }
	}
	for _, p := range m.panelKeysFull() {
		h.items = append(h.items, helpItem{key: p[0], action: p[1], run: send(p[2])})
	}
	for _, g := range m.globalHelpKeys() {
		g := g
		it := helpItem{key: g[0], action: g[1], global: true}
		switch g[0] {
		case "t", "T", tourKey:
			it.run = send(g[0])
		}
		h.items = append(h.items, it)
	}
	if m.tipSpot() { // only while the empty main pane shows a tip
		k := tipHelpKey
		h.items = append(h.items, helpItem{key: k[0], action: k[1], run: send(k[0]), global: true})
	}
	return h
}

// tipHelpKey is the help line of the tip key, listed only while a tip shows.
var tipHelpKey = [2]string{tipKey, "next tip"}

// globalHelpKeys are the keys that work from every panel (the lower half
// of the help).
func (m *model) globalHelpKeys() [][2]string {
	p := m.prefixLabel()
	return [][2]string{{"1-4", "focus panel"}, {"tab", "next panel"}, {"[ ]", "sub tab"}, {"]a [a", "next wait or PR news / back"}, {"]r", "next review"}, {p, "leave typing (back to Sessions)"}, {p + " h l", "focus left / main panel"}, {p + " z", "zoom the main panel (toggle)"}, {p + " < >", "narrow / widen the left panels"}, {p + " =", "default panel widths"}, {"t", "talk to tapi (tapi chat)"}, {"T", "reset tapi chat"}, {"w", "switch repository"}, {":", "command"}, {tourKey, "tutorial (replay)"}, {"q", "quit"}}
}

// Names of the panel key lists in helpSets.
const (
	hsSessions  = "sessions"
	hsTapiRow   = "sessions: the tapi row"
	hsWorktrees = "worktrees"
	hsBrowser   = "browser"
	hsIssues    = "issues"
	hsChanges   = "changes"
	hsPicks     = "picks"
	hsErrorList = "picks: an error"
	hsChat      = "tapi chat"
	hsIssueMain = "issue (main panel)"
	hsPreview   = "preview"
	hsPR        = "PR view"
	hsPRReview  = "PR view: someone else's PR"
	hsErrorCard = "agent: an error card"
	hsAgent     = "agent"
	hsDash      = "dashboard"
)

// helpSets is every list of panel keys the help can show (key, action, key
// to send), by name. panelKeysFull picks one; tests walk them all (every
// key has a tip or a reason not to, internal/tips).
func (m *model) helpSets() map[string][][3]string {
	common := [][3]string{{"n", "new session", "n"}, {"r", "review", "r"}, {"p", "PR view", "p"}, {"b", "open port in Chrome", "b"}, {"d", "dev server start/stop", "d"}, {"l", "land", "l"}, {"u", "undo last prompt", "u"}, {"R", "restart agent (resume)", "R"}, {"x", "end session", "x"}, {"D", "delete worktree", "D"}}
	pr := [][3]string{{"j/k", "move", "j"}, {"↵", "expand (thread, failed log)", "\r"}, {"space", "select for send", " "}, {"s", "send to the agent", "s"},
		{"o", "open in browser", "o"}, {"R", "refresh", "R"}, {"esc", "back to the agent", "esc"}, {"h", "back to Sessions", "h"}}
	return map[string][][3]string{
		hsSessions:  append([][3]string{{"j/k", "move (wraps at the ends)", "j"}, {"↵", "type into the agent", "\r"}, {"v", "preview", "v"}, {"!", "run a command in the worktree", "!"}, {"/", "filter", "/"}}, common...),
		hsTapiRow:   tapiRowKeysFull(),
		hsIssues:    issuePanelKeysFull(),
		hsBrowser:   {{"j/k", "move", "j"}, {"↵", "select the tab's session", "\r"}, {"E", "send console errors", "E"}},
		hsWorktrees: {{"j/k", "move", "j"}, {"↵", "open session in worktree", "\r"}, {"n", "new", "n"}, {"D", "delete", "D"}},
		hsErrorList: {{"j/k", "move", "j"}, {"↵", "error detail", "\r"}, {"s", "send error", "s"}, {"i", "ignore error", "i"}},
		hsPicks:     {{"j/k", "move", "j"}, {"↵", "edit & send", "\r"}, {"s", "send to other session", "s"}, {"n", "new session from pick", "n"}, {"d", "discard", "d"}},
		hsChanges:   {{"j/k", "move", "j"}, {"↵", "review", "\r"}, {"r", "review", "r"}},
		hsChat:      chatKeysFull(),
		hsIssueMain: {{"j/k", "scroll", "j"}, {"↵", "jump to its session", "\r"}, {"n", "new session from the issue", "n"},
			{"o", "open in the browser", "o"}, {"R", "refresh from GitHub", "R"}, {"h", "back to Issues", "h"}, {"esc", "back to the agent", "esc"}},
		hsPreview:   m.previewKeysFull(),
		hsPR:        pr,
		hsPRReview:  append(append([][3]string(nil), pr[:2]...), pr[4:]...), // someone else's PR: nothing goes to an agent
		hsErrorCard: append([][3]string{{"s", "send the error card", "s"}, {"i", "ignore the error card", "i"}, {"↵", "error detail", "\r"}, {"a", "type into the agent", "a"}, {"v", "preview / dev window", "v"}}, common...),
		hsDash:      dashKeysFull(),
		hsAgent:     append([][3]string{{"i", "type into the agent", "i"}, {"v", "preview / dev window", "v"}}, common...),
	}
}

// panelKeysFull lists every key of the focused panel: key, action, key to send.
func (m *model) panelKeysFull() [][3]string {
	return m.helpSets()[m.panelKeySet()]
}

// panelKeySet names the helpSets list of the focused panel.
func (m *model) panelKeySet() string {
	switch m.focus {
	case pSessions:
		if m.onTapiRow() {
			return hsTapiRow
		}
		return hsSessions
	case pSecond:
		if m.sub2 == subIssues {
			return hsIssues
		}
		if m.sub2 == 1 {
			return hsBrowser
		}
		return hsWorktrees
	case pThird:
		if m.sub3 == 1 && m.selectedError() != nil {
			return hsErrorList
		}
		if m.sub3 == 1 {
			return hsPicks
		}
		return hsChanges
	}
	switch {
	case m.chat.open:
		return hsChat
	case m.issueShown():
		return hsIssueMain
	case m.dashShown():
		return hsDash
	case m.window == winPreview:
		return hsPreview
	case m.window == winPR:
		if s := m.current(); s != nil && s.PRReview {
			return hsPRReview
		}
		return hsPR
	case m.pendingError() != nil:
		return hsErrorCard
	}
	return hsAgent
}

func (h *helpModal) mode() (string, string) { return "HELP", "" }
func (h *helpModal) hints() [][2]string {
	return [][2]string{{"j/k", "move"}, {"↵", "run"}, {"esc", "close"}}
}

func (h *helpModal) view() []string {
	m := h.m
	th := m.th
	cw := m.modalWidth(60) - 6
	var lines []string
	lines = append(lines, th.FG(theme.Dim, "this panel"))
	shownGlobal := false
	for i, it := range h.items {
		if it.global && !shownGlobal {
			shownGlobal = true
			lines = append(lines, "", th.FG(theme.Dim, "global"))
		}
		l := th.Bold(theme.Secondary, fit(it.key, 10)) + " " + th.FG(theme.Text, it.action)
		if i == h.sel {
			l = th.SelectStyle().Render(fit(l, cw))
		}
		lines = append(lines, l)
	}
	return m.modalFrame(m.modalWidth(60), th.Bold(theme.Primary, "Keys"), lines, h.hints(), false)
}

func (h *helpModal) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "j", "down":
		h.sel = clampi(h.sel+1, 0, len(h.items)-1)
	case "k", "up":
		h.sel = clampi(h.sel-1, 0, len(h.items)-1)
	case "enter":
		it := h.items[h.sel]
		if it.run == nil {
			return nil
		}
		h.m.modal = nil
		if it.key == "↵" {
			return h.m.onKey(tea.KeyMsg{Type: tea.KeyEnter})
		}
		return it.run()
	case "esc", "q", "?":
		return done(nil)
	}
	return nil
}

// ---- ":" command line ----

type commandModal struct {
	m   *model
	in  *input
	err string
}

func newCommandModal(m *model) *commandModal { return &commandModal{m: m, in: &input{}} }

func (c *commandModal) mode() (string, string) { return "COMMAND", "" }
func (c *commandModal) hints() [][2]string {
	return [][2]string{{"↵", "run"}, {"esc", "cancel"}}
}

func (c *commandModal) view() []string {
	th := c.m.th
	lines := []string{"", th.FG(theme.Secondary, ":") + c.in.render(th, true)}
	if c.err != "" {
		lines = append(lines, "", th.FG(theme.Err, c.err))
	}
	lines = append(lines, "", th.FG(theme.Dim, "new <branch> [prompt] · send <text> · land · rm · dev · undo [n] · review · inbox · ext · tutorial · q"))
	return c.m.modalFrame(c.m.modalWidth(80), th.Bold(theme.Primary, "Command"), lines, c.hints(), false)
}

func (c *commandModal) key(k tea.KeyMsg) tea.Cmd {
	m := c.m
	switch k.String() {
	case "esc":
		return done(nil)
	case "enter":
		f := strings.Fields(c.in.String())
		if len(f) == 0 {
			return done(nil)
		}
		s := m.current()
		need := func() bool {
			if s == nil {
				c.err = "no session selected"
				return false
			}
			return true
		}
		arg := strings.TrimSpace(strings.TrimPrefix(c.in.String(), f[0]))
		switch f[0] {
		case "q", "quit":
			return tea.Quit
		case "new":
			m.modal = nil
			return m.openNew(func(nm *newModal) {
				if len(f) > 1 {
					nm.inputs[0] = &input{text: []rune(f[1]), pos: len([]rune(f[1]))}
					if len(f) > 2 {
						p := strings.Join(f[2:], " ")
						nm.inputs[2] = &input{text: []rune(p), pos: len([]rune(p))}
					}
				}
			})
		case "send":
			if need() {
				return done(m.act("session.send", map[string]any{"slug": s.Slug, "text": arg, "submit": true}, "sent"))
			}
		case "land", "rm", "dev", "undo", "review", "restart":
			if need() {
				key := map[string]string{"land": "l", "rm": "D", "dev": "d", "undo": "u", "review": "r", "restart": "R"}[f[0]]
				m.modal = nil
				if f[0] == "undo" && len(f) > 1 {
					return m.act("session.undo", map[string]any{"slug": s.Slug, "n": atoi(f[1])}, "undone")
				}
				if f[0] == "review" {
					return m.openReview(s.Slug)
				}
				return m.sessionAction(key, s)
			}
		case "tutorial":
			m.modal = nil
			return m.startTour()
		case "inbox":
			m.modal = nil
			m.inboxRoundReset()
			if it := m.nextInbox(); it != nil {
				return m.openInbox(*it)
			}
			return done(func() tea.Msg { return infoMsg("the inbox is empty") })
		case "ext":
			return done(func() tea.Msg {
				var info map[string]any
				_ = m.cl.Call("ext.info", nil, &info)
				return infoMsg(fmt.Sprintf("extension: port %v token %v", info["port"], info["token"]))
			})
		default:
			c.err = "unknown command: " + f[0]
		}
		return nil
	}
	c.in.key(k)
	return nil
}

func atoi(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}
