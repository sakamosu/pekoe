package tui

// Issue inbox: [2] Issues sub tab (worktrees / browser / issues) and the
// selected issue read in the main panel. Data comes from bobad's issues.list
// / issues.view (gh issue list / view, cached in the daemon).

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"boba/internal/daemon"
	"boba/internal/state"
	"boba/internal/theme"
)

const subIssues = 2 // m.sub2: 0 worktrees, 1 browser, 2 issues

type issuesMsg struct {
	repo string // the repository it was fetched for
	list *daemon.IssueList
	err  error
}

type issueViewMsg struct {
	repo string
	n    int
	det  *daemon.IssueDetail
	err  error
}

type issuesState struct {
	list     *daemon.IssueList
	loading  bool
	err      string // the last issues.list call failed (not "unavailable")
	sel      int
	filter   string // mine | open | label
	label    string
	query    string // "/" narrows the list locally
	queryOn  bool
	open     bool // the main panel shows the selected issue instead of the agent
	scroll   int
	details  map[int]*daemon.IssueDetail
	viewErr  map[int]string
	fetching map[int]bool
}

// ensure fills the zero value (a model literal in tests has no maps).
func (is *issuesState) ensure() {
	if is.filter == "" {
		is.filter = "mine"
	}
	if is.details == nil {
		is.details, is.viewErr, is.fetching = map[int]*daemon.IssueDetail{}, map[int]string{}, map[int]bool{}
	}
}

// resetRepo forgets what was fetched for another repository (the filter
// and label stay: they mean the same in every repository).
func (is *issuesState) resetRepo() {
	is.list, is.loading, is.err = nil, false, ""
	is.sel, is.scroll = 0, 0
	is.query, is.queryOn = "", false
	is.details, is.viewErr, is.fetching = nil, nil, nil
	is.ensure()
}

// ---- loading ----

func (m *model) loadIssues(refresh bool) tea.Cmd {
	is := &m.iss
	is.ensure()
	repo := m.curRepoDir()
	if repo == "" {
		is.loading = false
		return nil // no repository: issueRows says so
	}
	is.loading = true
	params := map[string]any{"repo": repo, "filter": is.filter, "label": is.label, "refresh": refresh}
	if refresh {
		is.details = map[int]*daemon.IssueDetail{}
		is.viewErr = map[int]string{}
	}
	return func() tea.Msg {
		if m.cl == nil {
			return nil
		}
		var l daemon.IssueList
		if err := m.cl.Call("issues.list", params, &l); err != nil {
			return issuesMsg{repo: repo, err: err}
		}
		return issuesMsg{repo: repo, list: &l}
	}
}

func (m *model) loadIssueView(refresh bool) tea.Cmd {
	cur := m.curIssue()
	if cur == nil || m.cl == nil {
		return nil
	}
	n := cur.Number
	m.iss.ensure()
	if (m.iss.details[n] != nil && !refresh) || m.iss.fetching[n] {
		return nil
	}
	m.iss.fetching[n] = true
	repo := m.curRepoDir()
	return func() tea.Msg {
		var det daemon.IssueDetail
		if err := m.cl.Call("issues.view", map[string]any{"repo": repo, "number": n, "refresh": refresh}, &det); err != nil {
			return issueViewMsg{repo: repo, n: n, err: err}
		}
		return issueViewMsg{repo: repo, n: n, det: &det}
	}
}

func (m *model) onIssuesMsg(msg tea.Msg) tea.Cmd {
	is := &m.iss
	is.ensure()
	switch msg := msg.(type) {
	case issuesMsg:
		if msg.repo != m.curRepoDir() {
			return nil // fetched for the repository we just left
		}
		is.loading = false
		if msg.err != nil {
			is.err = firstLine(msg.err.Error())
			return nil
		}
		is.err = ""
		is.list = msg.list
		is.sel = clampi(is.sel, 0, max(len(m.visibleIssues())-1, 0))
		if is.open {
			return m.loadIssueView(false)
		}
	case issueViewMsg:
		if msg.repo != m.curRepoDir() {
			return nil
		}
		delete(is.fetching, msg.n)
		if msg.err != nil {
			is.viewErr[msg.n] = firstLine(msg.err.Error())
			return nil
		}
		delete(is.viewErr, msg.n)
		is.details[msg.n] = msg.det
	}
	return nil
}

// enterIssues runs when the Issues sub tab is shown.
func (m *model) enterIssues() tea.Cmd {
	m.iss.open = true
	m.iss.scroll = 0
	if m.iss.list == nil || time.Since(m.iss.list.FetchedAt) > time.Minute {
		return m.loadIssues(false) // the daemon answers from its cache when fresh
	}
	return m.loadIssueView(false)
}

// issueShown: the main panel shows the selected issue instead of the agent.
// With nothing to show (loading, an empty list, no GitHub) the agent stays.
func (m *model) issueShown() bool {
	return m.iss.open && !m.typing && m.curIssue() != nil
}

// issueSync keeps the issue view out of the way while typing into the agent.
func (m *model) issueSync() {
	if m.typing {
		m.iss.open = false
	}
}

// ---- selection ----

func (m *model) visibleIssues() []daemon.Issue {
	if m.iss.list == nil {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(m.iss.query))
	if q == "" {
		return m.iss.list.Issues
	}
	var out []daemon.Issue
	for _, is := range m.iss.list.Issues {
		hay := strings.ToLower(fmt.Sprintf("#%d %s %s", is.Number, is.Title, strings.Join(is.Labels, " ")))
		if strings.Contains(hay, q) {
			out = append(out, is)
		}
	}
	return out
}

func (m *model) curIssue() *daemon.Issue {
	v := m.visibleIssues()
	if m.iss.sel >= 0 && m.iss.sel < len(v) {
		return &v[m.iss.sel]
	}
	return nil
}

// issueSession is the live session working on the issue: the daemon's match
// (by --from gh#N or the branch name) looked up in the current session list,
// or a session created since the list was fetched.
func (m *model) issueSession(is *daemon.Issue) *state.Session {
	if is == nil {
		return nil
	}
	if is.Session != nil {
		for _, s := range m.sessions {
			if s.Slug == is.Session.Slug && s.State != state.Landed {
				return s
			}
		}
	}
	repoID := ""
	if m.iss.list != nil {
		repoID = m.iss.list.RepoID
	}
	for _, s := range m.sessions {
		if s.Issue == is.Number && s.State != state.Landed && (repoID == "" || s.RepoID == repoID) {
			return s
		}
	}
	return nil
}

func (m *model) moveIssue(delta int) tea.Cmd {
	n := len(m.visibleIssues())
	prev := m.iss.sel
	switch delta {
	case -1 << 20:
		m.iss.sel = 0
	case 1 << 20:
		m.iss.sel = max(n-1, 0)
	default:
		m.move(&m.iss.sel, n, delta)
	}
	if prev != m.iss.sel || !m.iss.open {
		m.iss.scroll = 0
	}
	m.iss.open = true
	return m.loadIssueView(false)
}

// ---- keys ----

func (m *model) keyIssues(key string) tea.Cmd {
	is := &m.iss
	cur := m.curIssue()
	switch key {
	case "j", "down":
		return m.moveIssue(1)
	case "k", "up":
		return m.moveIssue(-1)
	case "g", "home":
		return m.moveIssue(-1 << 20)
	case "G", "end":
		return m.moveIssue(1 << 20)
	case "enter", "l", "right":
		if s := m.issueSession(cur); s != nil {
			is.open = false
			m.focus = pSessions
			return m.selectSlug(s.Slug)
		}
		if cur != nil {
			is.open = true
			m.focus = pAgent
			return m.loadIssueView(false)
		}
	case "esc":
		is.open = false
	case "n":
		return m.newFromIssue(cur)
	case "f":
		return m.cycleIssueFilter()
	case "/":
		is.queryOn = true
		is.query = ""
		is.sel = 0
	case "o":
		return m.openIssueURL(cur)
	case "R":
		return tea.Batch(m.loadIssues(true), m.loadIssueView(true))
	}
	return nil
}

// keyIssueMain: the issue is shown in the main panel and the panel has focus.
func (m *model) keyIssueMain(key string) tea.Cmd {
	is := &m.iss
	_, ch := m.agentSize()
	switch key {
	case "j", "down":
		is.scroll++
	case "k", "up":
		is.scroll = max(is.scroll-1, 0)
	case "ctrl+d", "pgdown", " ":
		is.scroll += max(ch/2, 1)
	case "ctrl+u", "pgup":
		is.scroll = max(is.scroll-max(ch/2, 1), 0)
	case "g", "home":
		is.scroll = 0
	case "G", "end":
		is.scroll = 1 << 20 // clamped when drawn
	case "esc":
		is.open = false // back to the agent window
	case "h", "left":
		m.focus = pSecond
	case "enter":
		if s := m.issueSession(m.curIssue()); s != nil {
			is.open = false
			m.focus = pSessions
			return m.selectSlug(s.Slug)
		}
	case "n":
		return m.newFromIssue(m.curIssue())
	case "o":
		return m.openIssueURL(m.curIssue())
	case "R":
		return tea.Batch(m.loadIssues(true), m.loadIssueView(true))
	}
	return nil
}

// issueQueryKey edits the "/" filter of the Issues panel.
func (m *model) issueQueryKey(k tea.KeyMsg) tea.Cmd {
	is := &m.iss
	switch k.Type {
	case tea.KeyEnter, tea.KeyEsc:
		is.queryOn = false
		if k.Type == tea.KeyEsc {
			is.query = ""
		}
		is.sel = clampi(is.sel, 0, max(len(m.visibleIssues())-1, 0))
		return m.loadIssueView(false)
	case tea.KeyBackspace:
		if r := []rune(is.query); len(r) > 0 {
			is.query = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		is.query += string(k.Runes)
		if k.Type == tea.KeySpace {
			is.query += " "
		}
	}
	is.sel = 0
	return nil
}

func (m *model) newFromIssue(cur *daemon.Issue) tea.Cmd {
	if cur == nil {
		return nil
	}
	if s := m.issueSession(cur); s != nil {
		m.setMsg(fmt.Sprintf("#%d already has a session: %s · ↵ jump", cur.Number, s.Branch), false)
	}
	branch := daemon.SuggestBranch(cur.Number, cur.Title, cur.Labels)
	from := fmt.Sprintf("gh#%d", cur.Number)
	return m.openNew(func(nm *newModal) {
		nm.inputs[0] = &input{text: []rune(branch), pos: len([]rune(branch))}
		nm.inputs[1] = &input{text: []rune(from), pos: len(from)}
	})
}

func (m *model) openIssueURL(cur *daemon.Issue) tea.Cmd {
	if cur == nil || cur.URL == "" {
		return nil
	}
	u := cur.URL
	return func() tea.Msg {
		if err := openURL(u); err != nil {
			return errMsg{err}
		}
		return infoMsg("opened " + u)
	}
}

// cycleIssueFilter: mine → open → label → mine. Choosing label asks which.
func (m *model) cycleIssueFilter() tea.Cmd {
	is := &m.iss
	is.ensure()
	switch is.filter {
	case "mine":
		is.filter = "open"
	case "open":
		m.modal = newLabelModal(m)
		return nil
	default:
		is.filter, is.label = "mine", ""
	}
	is.sel = 0
	return m.loadIssues(false)
}

func (m *model) issueFilterName() string {
	switch m.iss.filter {
	case "label":
		return "label:" + m.iss.label
	case "":
		return "mine"
	}
	return m.iss.filter
}

// ---- label modal (f → label) ----

type labelModal struct {
	m  *model
	in *input
}

func newLabelModal(m *model) *labelModal {
	l := m.iss.label
	if l == "" {
		if cur := m.curIssue(); cur != nil && len(cur.Labels) > 0 {
			l = cur.Labels[0]
		}
	}
	return &labelModal{m: m, in: &input{text: []rune(l), pos: len([]rune(l))}}
}

func (l *labelModal) mode() (string, string) { return "FILTER", l.m.ic.Issues }
func (l *labelModal) hints() [][2]string {
	return [][2]string{{"↵", "apply"}, {"esc", "cancel"}}
}

func (l *labelModal) view() []string {
	m := l.m
	th := m.th
	lines := []string{"", th.FG(theme.Dim, fit("label", 7)) + l.in.render(th, true)}
	seen := map[string]bool{}
	var known []string
	if m.iss.list != nil {
		for _, is := range m.iss.list.Issues {
			for _, lb := range is.Labels {
				if !seen[lb] {
					seen[lb] = true
					known = append(known, lb)
				}
			}
		}
	}
	if len(known) > 0 {
		lines = append(lines, "", th.FG(theme.Dim, "in this list: "+strings.Join(known, ", ")))
	}
	title := th.FG(theme.Primary, theme.Prefix(m.ic.Issues)) + th.Bold(theme.Primary, "Issues by label")
	return m.modalFrame(m.modalWidth(60), title, lines, l.hints(), false)
}

func (l *labelModal) key(k tea.KeyMsg) tea.Cmd {
	m := l.m
	switch k.String() {
	case "esc":
		return done(nil)
	case "enter":
		lb := strings.TrimSpace(l.in.String())
		if lb == "" {
			m.iss.filter, m.iss.label = "mine", ""
		} else {
			m.iss.filter, m.iss.label = "label", lb
		}
		m.iss.sel = 0
		return done(m.loadIssues(false))
	}
	l.in.key(k)
	return nil
}

// ---- panel ----

func (m *model) issuesTitle(f bool) string {
	n := len(m.visibleIssues())
	extra := m.issueFilterName()
	if r := m.repoScope(); r != "" {
		extra = r + " " + extra // the repository is part of the range
	}
	if m.iss.query != "" || m.iss.queryOn {
		extra += " filter:" + m.iss.query
		if m.iss.queryOn {
			extra += cursorMark + "▏"
		}
	}
	if m.iss.loading {
		extra += " …"
	}
	return m.panelTitle(2, m.ic.Issues, fmt.Sprintf("Issues %d", n), extra, f)
}

func (m *model) issueCount() int {
	return max(len(m.visibleIssues()), 1)
}

func (m *model) issuesPanel(width, h int, f bool) []string {
	rows := m.issueRows(width-4, f)
	// keep the selected row in view
	if avail := h - 2; avail > 0 && len(rows) > avail {
		start := clampi(m.iss.sel-avail+1, 0, len(rows)-avail)
		rows = rows[start:]
	}
	return m.box(width, h, boxOpt{title: m.issuesTitle(f), right: m.subtabHint(f), rows: rows, focused: f})
}

func (m *model) issueRows(cw int, f bool) []row {
	th := m.th
	is := &m.iss
	switch {
	case is.list == nil && m.curRepo() == nil:
		return []row{{Text: th.FG(theme.Dim, "  no repository · w pick one")}}
	case is.list == nil && is.err != "":
		return []row{{Text: th.FG(theme.Err, "  "+is.err)}}
	case is.list == nil:
		return []row{{Text: th.FG(theme.Dim, "  loading issues…")}}
	case is.list.Unavailable != "":
		return []row{{Text: th.FG(theme.Dim, "  "+is.list.Unavailable)}}
	}
	vis := m.visibleIssues()
	if len(vis) == 0 {
		hint := "no open issues"
		switch {
		case is.query != "":
			hint = "no match"
		case is.filter == "mine":
			hint = "no open issues assigned to you · f all"
		case is.filter == "label":
			hint = "no open issues labeled " + is.label
		}
		return []row{{Text: th.FG(theme.Dim, "  "+hint)}}
	}
	now := time.Now()
	rows := make([]row, 0, len(vis))
	for i := range vis {
		rows = append(rows, row{Text: m.issueLine(&vis[i], cw, now), Sel: i == is.sel})
	}
	return rows
}

// issueLine: [state]  #12  title  labels  ✎2  3d
func (m *model) issueLine(is *daemon.Issue, cw int, now time.Time) string {
	th := m.th
	icon := " "
	if s := m.issueSession(is); s != nil {
		lk := theme.Look(m.ic, s.State, m.frame, m.g.AnimateOn())
		icon = th.FG(lk.Role, fit(lk.Icon, 1))
	}
	num := fmt.Sprintf("#%d", is.Number)
	numW := max(w(num), 4)
	cm := ""
	if is.Comments > 0 {
		if m.ic.Name == "ascii" {
			cm = fmt.Sprintf("%dc", is.Comments)
		} else {
			cm = m.ic.Comments + fmt.Sprint(is.Comments)
		}
	}
	cmW := 4
	age := ""
	if !is.UpdatedAt.IsZero() {
		age = theme.Ago(now.Sub(is.UpdatedAt))
	}
	ageW := 4
	labels := strings.Join(is.Labels, ",")
	lw := min(w(labels), 16)
	// icon+sp + num + sp + title + [sp + labels] + sp + comments + sp + age
	titleW := cw - 2 - numW - 1 - 1 - cmW - 1 - ageW
	if lw > 0 {
		titleW -= lw + 1
	}
	if lw > 0 && titleW < 16 {
		titleW += lw + 1 // labels go first when the panel is narrow
		lw = 0
	}
	if titleW < 8 {
		titleW += cmW + 1
		cmW = 0
	}
	line := icon + " " + th.FG(theme.Dim, fit(num, numW)) + " " + th.FG(theme.Text, fit(sanitize(is.Title), max(titleW, 1)))
	if lw > 0 {
		line += " " + th.FG(theme.Dim, fit(labels, lw))
	}
	if cmW > 0 {
		line += " " + th.FG(theme.Dim, fit(cm, cmW))
	}
	return line + " " + th.FG(theme.Dim, fit(age, ageW))
}

// ---- main panel: the selected issue ----

func (m *model) viewIssue(width, height int) []string {
	th := m.th
	f := m.focus == pAgent
	cw, ch := width-4, height-2
	cur := m.curIssue()
	title := m.panelTitle(4, m.ic.Issues, "Issue", "", f)
	if cur == nil {
		msg := "no issue selected"
		if l := m.iss.list; l != nil && l.Unavailable != "" {
			msg = l.Unavailable
		}
		return m.box(width, height, boxOpt{title: title, rows: []row{{Text: th.FG(theme.Dim, msg)}}, focused: f})
	}
	det := m.iss.details[cur.Number]
	extra := ""
	right := ""
	if s := m.issueSession(cur); s != nil {
		lk := theme.Look(m.ic, s.State, m.frame, m.g.AnimateOn())
		right = th.FG(lk.Role, lk.Icon) + " " + th.FG(theme.Dim, s.Branch+" "+lk.Word)
	}
	if det != nil {
		extra = "· " + strings.ToLower(det.State)
		if det.Author != "" {
			extra += " · @" + det.Author
		}
		if !det.CreatedAt.IsZero() {
			extra += " · " + theme.Ago(time.Since(det.CreatedAt)) + " ago"
		}
	}
	title = m.panelTitle(4, m.ic.Issues, fmt.Sprintf("Issue #%d", cur.Number), extra, f)
	rows := m.issueDetailRows(cur, det, cw)
	// scroll: top-aligned, clamped so the last page stays full
	maxScroll := max(len(rows)-ch, 0)
	m.iss.scroll = clampi(m.iss.scroll, 0, maxScroll)
	rows = rows[m.iss.scroll:]
	footer := ""
	if maxScroll > 0 {
		footer = th.FG(theme.Dim, fmt.Sprintf("%d%%", (m.iss.scroll*100+maxScroll/2)/maxScroll))
	}
	return m.box(width, height, boxOpt{title: title, right: right, rows: rows, footer: footer, focused: f})
}

func (m *model) issueDetailRows(cur *daemon.Issue, det *daemon.IssueDetail, cw int) []row {
	th := m.th
	var lines []string
	for _, l := range strings.Split(ansi.Wrap(sanitize(cur.Title), cw, ""), "\n") {
		lines = append(lines, th.FG(theme.Text, l))
	}
	var meta []string
	if len(cur.Labels) > 0 {
		meta = append(meta, strings.Join(cur.Labels, ", "))
	}
	if len(cur.Assignees) > 0 {
		meta = append(meta, "@"+strings.Join(cur.Assignees, " @"))
	}
	if cur.Comments == 1 {
		meta = append(meta, "1 comment")
	} else {
		meta = append(meta, fmt.Sprintf("%d comments", cur.Comments))
	}
	lines = append(lines, th.FG(theme.Dim, clip(strings.Join(meta, " · "), cw)), "")
	switch {
	case m.iss.viewErr[cur.Number] != "":
		lines = append(lines, th.FG(theme.Err, clip(m.iss.viewErr[cur.Number], cw)))
	case det == nil:
		lines = append(lines, th.FG(theme.Dim, "loading…"))
	default:
		if strings.TrimSpace(det.Body) == "" {
			lines = append(lines, th.FG(theme.Dim, "no description"))
		} else {
			lines = append(lines, renderMarkdown(th, m.ic, det.Body, cw)...)
		}
		for _, c := range det.Thread {
			head := " @" + c.Author
			if !c.CreatedAt.IsZero() {
				head += " · " + theme.Ago(time.Since(c.CreatedAt)) + " ago"
			}
			head += " "
			lines = append(lines, "", th.FG(theme.Border, "──")+th.FG(theme.Dim, clip(head, max(cw-3, 0)))+th.FG(theme.Border, strings.Repeat("─", max(cw-2-w(head), 0))))
			lines = append(lines, renderMarkdown(th, m.ic, c.Body, cw)...)
		}
	}
	rows := make([]row, len(lines))
	for i, l := range lines {
		rows[i] = row{Text: l}
	}
	return rows
}

func (m *model) issueMainHints() [][2]string {
	if m.issueSession(m.curIssue()) != nil {
		return [][2]string{{"j/k", "scroll"}, {"↵", "jump"}, {"o", "browse"}, {"esc", "back"}, {"?", "help"}}
	}
	return [][2]string{{"j/k", "scroll"}, {"n", "new"}, {"o", "browse"}, {"esc", "back"}, {"?", "help"}}
}

func (m *model) issuePanelHints() [][2]string {
	if m.iss.queryOn {
		return [][2]string{{"↵", "apply"}, {"esc", "clear"}}
	}
	if l := m.iss.list; l != nil && l.Unavailable != "" {
		return [][2]string{{"R", "reload"}, {"f", "scope"}, {"?", "help"}}
	}
	enter := "read"
	if m.issueSession(m.curIssue()) != nil {
		enter = "jump"
	}
	return [][2]string{{"j/k", "move"}, {"↵", enter}, {"n", "new"}, {"f", "scope"}, {"o", "browse"}, {"?", "help"}}
}

func issuePanelKeysFull() [][3]string {
	return [][3]string{{"j/k", "move (shows the issue)", "j"}, {"↵", "jump to its session / read", "\r"}, {"n", "new session from the issue", "n"},
		{"f", "mine → open → label", "f"}, {"/", "filter", "/"}, {"o", "open in the browser", "o"}, {"R", "refresh from GitHub", "R"}, {"esc", "back to the agent", "esc"}}
}

// issueMouse: wheel scrolls the issue, a click focuses the panel; nothing
// reaches the agent's pty while the issue is shown.
func (m *model) issueMouse(ev tea.MouseMsg) (tea.Cmd, bool) {
	x0, y0 := m.agentOrigin()
	aw, ah := m.agentSize()
	x, y := ev.X-x0, ev.Y-y0
	if x < -2 || y < -1 || x >= aw+2 || y >= ah+1 {
		return nil, false
	}
	switch {
	case ev.Button == tea.MouseButtonWheelDown:
		m.iss.scroll += 3
	case ev.Button == tea.MouseButtonWheelUp:
		m.iss.scroll = max(m.iss.scroll-3, 0)
	case ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft:
		m.focus = pAgent
	}
	return nil, true
}

// ---- light markdown: headings, lists, quotes, code blocks ----

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdList    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	mdRule    = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	mdComment = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// renderMarkdown lays out an issue body for a cw-wide panel. Inline markup
// stays as written; only the block structure is drawn.
func renderMarkdown(th *theme.Theme, ic theme.IconSet, body string, cw int) []string {
	body = mdComment.ReplaceAllString(sanitize(body), "")
	bullet := "•"
	if ic.Name == "ascii" {
		bullet = "-"
	}
	var out []string
	blank := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	wrap := func(text string, width int) []string {
		return strings.Split(ansi.Wrap(text, max(width, 1), ""), "\n")
	}
	inCode := false
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimRight(strings.ReplaceAll(ln, "\t", "    "), " \r")
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			if !inCode {
				blank()
			}
			inCode = !inCode
			if lang := strings.Trim(trim, "`~ "); inCode && lang != "" {
				out = append(out, th.FG(theme.Border, "│ ")+th.FG(theme.Dim, lang))
			}
			continue
		}
		if inCode {
			out = append(out, th.FG(theme.Border, "│ ")+th.FG(theme.Text, clip(ln, max(cw-2, 1))))
			continue
		}
		switch {
		case trim == "":
			blank()
		case mdHeading.MatchString(trim):
			g := mdHeading.FindStringSubmatch(trim)
			blank()
			for i, l := range wrap(g[2], cw-len(g[1])-1) {
				mark := strings.Repeat(" ", len(g[1])+1)
				if i == 0 {
					mark = g[1] + " "
				}
				out = append(out, th.FG(theme.Dim, mark)+th.FG(theme.Text, l))
			}
		case mdRule.MatchString(trim):
			out = append(out, th.FG(theme.Border, strings.Repeat("─", max(cw, 1))))
		case strings.HasPrefix(trim, ">"):
			text := strings.TrimSpace(strings.TrimPrefix(trim, ">"))
			for _, l := range wrap(text, cw-2) {
				out = append(out, th.FG(theme.Border, "▎ ")+th.FG(theme.Dim, l))
			}
		case mdList.MatchString(ln):
			g := mdList.FindStringSubmatch(ln)
			indent := min(len(g[1]), 8)
			mark := g[2]
			if strings.ContainsAny(mark, "-*+") {
				mark = bullet
			}
			pre := strings.Repeat(" ", indent) + mark + " "
			for i, l := range wrap(g[3], cw-w(pre)) {
				if i == 0 {
					out = append(out, strings.Repeat(" ", indent)+th.FG(theme.Dim, mark)+" "+th.FG(theme.Text, l))
				} else {
					out = append(out, strings.Repeat(" ", w(pre))+th.FG(theme.Text, l))
				}
			}
		default:
			for _, l := range wrap(trim, cw) {
				out = append(out, th.FG(theme.Text, l))
			}
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// sanitize drops escape sequences and control characters from text that came
// from GitHub, so an issue can't drive the terminal.
func sanitize(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
