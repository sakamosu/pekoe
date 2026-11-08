package tui

// The current repository. One pekoed serves every repository; the TUI lists
// every session (grouped by repository in [1]) and acts on one repository at
// a time: n, Issues, Worktrees and picks sent to a new session go to the
// current repository, which is
//
//  1. the one picked with w (until the selection moves to another session),
//  2. else the selected session's repository,
//  3. else the repository pekoe was started in.

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/daemon"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

type reposMsg []daemon.KnownRepo

func (m *model) loadRepos() tea.Cmd {
	cwd := m.cwd
	return func() tea.Msg {
		if m.cl == nil {
			return nil
		}
		var rs []daemon.KnownRepo
		if err := m.cl.Call("repo.list", map[string]any{"cwd": cwd}, &rs); err != nil {
			return errMsg{err}
		}
		return reposMsg(rs)
	}
}

// homeRepo is the repository pekoe was started in (nil outside one).
func (m *model) homeRepo() *daemon.KnownRepo {
	for i := range m.repos {
		if m.repos[i].Cwd {
			return &m.repos[i]
		}
	}
	return nil
}

// sessionRepo is the repository of a session, as repo.list knows it.
func (m *model) sessionRepo(s *state.Session) *daemon.KnownRepo {
	if s == nil || s.RepoDir == "" {
		return nil
	}
	for i := range m.repos {
		if m.repos[i].ID == s.RepoID {
			return &m.repos[i]
		}
	}
	return &daemon.KnownRepo{ID: s.RepoID, Name: s.Repo, Dir: s.RepoDir}
}

// curRepo is the repository n / Issues / Worktrees act on (nil: none yet).
func (m *model) curRepo() *daemon.KnownRepo {
	if m.chosen != nil {
		return m.chosen
	}
	if r := m.sessionRepo(m.current()); r != nil {
		return r
	}
	return m.homeRepo()
}

func (m *model) curRepoDir() string {
	if r := m.curRepo(); r != nil {
		return r.Dir
	}
	return ""
}

// pickHome runs once, when both the sessions and the repositories are in:
// pekoe starts on the repository it was started in — its first session, or
// the repository itself when it has none (so n does not go elsewhere).
func (m *model) pickHome() {
	if m.homePicked || !m.sessionsIn || !m.reposLoaded {
		return
	}
	m.homePicked = true
	home := m.homeRepo()
	if home == nil {
		return
	}
	if r := m.sessionRepo(m.current()); r != nil && r.ID == home.ID {
		return
	}
	for i, s := range m.visible() {
		if s.RepoID == home.ID && s.State != state.Landed {
			m.sel = i
			return
		}
	}
	h := *home
	m.chosen = &h
}

// repoSync reloads what belongs to the current repository when it changes,
// and retitles the window when its name does (a repository with the same
// short name may show up later and make it longer).
func (m *model) repoSync() tea.Cmd {
	r := m.curRepo()
	key, name := "", ""
	if r != nil {
		key, name = r.Dir, m.repoName(r)
	}
	var cmds []tea.Cmd
	if !m.repoSynced || name != m.repoTitle {
		m.repoTitle = name
		title := "pekoe"
		if name != "" {
			title += " · " + name
		}
		cmds = append(cmds, tea.SetWindowTitle(title))
	}
	if !m.repoSynced || key != m.repoKey {
		m.repoSynced, m.repoKey = true, key
		m.worktrees, m.wsel = nil, 0
		m.iss.resetRepo()
		cmds = append(cmds, m.loadWorktrees())
		if m.sub2 == subIssues {
			cmds = append(cmds, m.loadIssues(false))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// repoScope is the state part of a title scoped to the current repository
// ([2] Worktrees, [2] Issues): its name.
func (m *model) repoScope() string {
	if r := m.curRepo(); r != nil {
		return m.repoName(r)
	}
	return ""
}

// ---- display names ----

// repoName is how the TUI names a repository everywhere (headings, the w
// modal, the bar, [2] titles, the window title): its short name, made
// longer only as far as needed when another known repository has the same
// short name (see repoLabels).
func (m *model) repoName(r *daemon.KnownRepo) string {
	if r == nil {
		return ""
	}
	if r.ID != "" {
		if l, ok := repoLabels(m.pickableRepos())[r.ID]; ok {
			return l
		}
	}
	return shortRepoName(r.Name, r.Dir)
}

func shortRepoName(name, dir string) string {
	if name == "" && dir != "" {
		return filepath.Base(dir)
	}
	return name
}

// repoLabels gives each repository (by id) a name that tells it apart from
// the others. A short name nobody else has stays as is (api); repositories
// sharing one get the upper elements of their id added, one level at a
// time until they differ: the owner (a/api, b/api), then the host
// (github.com/a/api, gitlab.com/a/api). A path_ id (no origin) adds its
// parent directories instead (src/api).
func repoLabels(rs []daemon.KnownRepo) map[string]string {
	type item struct {
		id   string
		toks []string // outermost first, the short name last
	}
	groups := map[string][]item{}
	var order []string
	seen := map[string]bool{}
	for _, r := range rs {
		if r.ID == "" || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		name := shortRepoName(r.Name, r.Dir)
		if _, ok := groups[name]; !ok {
			order = append(order, name)
		}
		groups[name] = append(groups[name], item{r.ID, repoTokens(r.ID, name, r.Dir)})
	}
	out := map[string]string{}
	for _, name := range order {
		g := groups[name]
		if len(g) == 1 {
			out[g[0].id] = name
			continue
		}
		deepest := 1
		for _, it := range g {
			deepest = max(deepest, len(it.toks))
		}
		label := func(it item, k int) string {
			return strings.Join(it.toks[max(len(it.toks)-k, 0):], "/")
		}
		k := 2
		for ; k < deepest; k++ {
			distinct := map[string]bool{}
			for _, it := range g {
				distinct[label(it, k)] = true
			}
			if len(distinct) == len(g) {
				break
			}
		}
		for _, it := range g {
			out[it.id] = label(it, k)
		}
		// still the same at full depth (cannot happen for distinct ids
		// unless they only differ in ways the elements hide): use the id
		count := map[string]int{}
		for _, it := range g {
			count[out[it.id]]++
		}
		for _, it := range g {
			if count[out[it.id]] > 1 {
				out[it.id] = it.id
			}
		}
	}
	return out
}

// repoTokens splits a repository's id into its elements, outermost first:
// host_owner_name → [host owner name] (gitlab subgroups give more), and a
// path_ id → the directories of its path (taken from dir when known, since
// "_" in the id is ambiguous). The short name is always the last element.
func repoTokens(id, name, dir string) []string {
	var toks []string
	if rest, ok := strings.CutPrefix(id, "path_"); ok {
		if dir != "" {
			toks = strings.Split(strings.Trim(filepath.ToSlash(filepath.Clean(dir)), "/"), "/")
		} else {
			toks = strings.Split(rest, "_")
		}
	} else if pre, ok := strings.CutSuffix(id, "_"+name); ok && name != "" {
		toks = append(strings.Split(pre, "_"), name)
	} else {
		toks = strings.Split(id, "_")
	}
	if len(toks) == 0 || toks[len(toks)-1] != name {
		toks = append(toks, name)
	}
	return toks
}

// ---- grouping ----

func repoKeyOf(s *state.Session) string {
	if s.RepoID != "" {
		return s.RepoID
	}
	return s.RepoDir
}

// multiRepo: headings only when sessions span more than one repository.
func (m *model) multiRepo() bool {
	first := ""
	for i, s := range m.sessions {
		if i == 0 {
			first = repoKeyOf(s)
		} else if repoKeyOf(s) != first {
			return true
		}
	}
	return false
}

// groupByRepo keeps the order within a repository and orders repositories
// by their first session.
func groupByRepo(ss []*state.Session) {
	rank := map[string]int{}
	for _, s := range ss {
		if _, ok := rank[repoKeyOf(s)]; !ok {
			rank[repoKeyOf(s)] = len(rank)
		}
	}
	grouped := make([]*state.Session, 0, len(ss))
	for k := 0; k < len(rank); k++ {
		for _, s := range ss {
			if rank[repoKeyOf(s)] == k {
				grouped = append(grouped, s)
			}
		}
	}
	copy(ss, grouped)
}

// repoHeading is the row above a repository's sessions in [1]: the current
// marker (text) when it is the current repository, its name, a rule.
func (m *model) repoHeading(s *state.Session, cw int) string {
	th := m.th
	mark, role := " ", theme.Dim
	if cur := m.curRepo(); cur != nil && repoKeyOf(s) != "" && (cur.ID == s.RepoID || cur.Dir == s.RepoDir) {
		mark, role = th.FG(theme.Text, fit(m.ic.Current, 1)), theme.Text
	}
	name := shortRepoName(s.Repo, s.RepoDir)
	if r := m.sessionRepo(s); r != nil {
		name = m.repoName(r)
	}
	// a long name gives way, keeping the marker and at least one rule cell
	if w(name) > cw-4 {
		name = fit(name, max(cw-4, 1))
	}
	head := mark + " " + th.FG(role, name) + " "
	return head + th.FG(theme.Border, strings.Repeat("─", max(cw-w(head), 0)))
}

// selRow is the index of the selected row (-1 when none is).
func selRow(rows []row) int {
	for i, r := range rows {
		if r.Sel {
			return i
		}
	}
	return -1
}

// ---- choosing a repository (w) ----

// chooseRepo makes r the current repository: its first session is selected
// when it has one, otherwise the choice stands on its own.
func (m *model) chooseRepo(r daemon.KnownRepo) tea.Cmd {
	m.chosen = nil
	for _, s := range m.sessions {
		if s.RepoID == r.ID && s.State != state.Landed {
			m.focus = pSessions
			return tea.Batch(m.selectSlug(s.Slug), infoMsgCmd("repository "+m.repoName(&r)))
		}
	}
	rr := r
	m.chosen = &rr
	return infoMsgCmd("repository " + m.repoName(&r) + " · n new session")
}

func infoMsgCmd(s string) tea.Cmd { return func() tea.Msg { return infoMsg(s) } }

// noRepoHint is the one line shown when pekoe runs outside every repository
// and knows none to offer.
const noRepoHint = "not in a git repository · run pekoe inside one (cd <repo> && pekoe)"

// pickableRepos are the repositories w can switch to.
func (m *model) pickableRepos() []daemon.KnownRepo {
	var out []daemon.KnownRepo
	seen := map[string]bool{}
	for _, r := range m.repos {
		out = append(out, r)
		seen[r.ID] = true
	}
	// a session created after the last repo.list still brings its repository
	for _, s := range m.sessions {
		if r := m.sessionRepo(s); r != nil && !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, *r)
		}
	}
	return out
}

// openRepoPicker opens the w modal; then runs after a repository is chosen.
func (m *model) openRepoPicker(then func() tea.Cmd) tea.Cmd {
	rs := m.pickableRepos()
	if len(rs) == 0 {
		m.setMsg(noRepoHint, false)
		return m.loadRepos()
	}
	p := &repoPicker{m: m, then: then}
	if cur := m.curRepo(); cur != nil {
		for i, r := range rs {
			if r.ID == cur.ID {
				p.sel = i
			}
		}
	}
	m.modal = p
	return m.loadRepos() // refresh counts while the modal is open
}

// openNew opens the new-session modal for the current repository, asking
// for one first when there is none.
func (m *model) openNew(setup func(*newModal)) tea.Cmd {
	open := func() tea.Cmd {
		nm := newNewModal(m, "")
		if setup != nil {
			setup(nm)
		}
		m.modal = nm
		return nil
	}
	if m.curRepo() == nil {
		return m.openRepoPicker(open)
	}
	return open()
}

type repoPicker struct {
	m    *model
	sel  int
	then func() tea.Cmd
}

func (p *repoPicker) mode() (string, string) { return "REPO", p.m.ic.Worktrees }

func (p *repoPicker) hints() [][2]string {
	return [][2]string{{"j/k", "move"}, {"↵", "switch"}, {"esc", "cancel"}}
}

func (p *repoPicker) view() []string {
	m := p.m
	th := m.th
	width := m.modalWidth(72)
	cw := width - 6
	rs := m.pickableRepos()
	p.sel = clampi(p.sel, 0, max(len(rs)-1, 0))
	cur := m.curRepo()
	lines := []string{""}
	for i, r := range rs {
		mark := " "
		if cur != nil && cur.ID == r.ID {
			mark = th.FG(theme.Text, fit(m.ic.Current, 1))
		}
		count := "no sessions"
		if r.Sessions == 1 {
			count = "1 session"
		} else if r.Sessions > 1 {
			count = fmt.Sprintf("%d sessions", r.Sessions)
		}
		where := homeTilde(r.Dir)
		nameRole := theme.Text
		if r.Dir == "" {
			where, nameRole = "path unknown · run pekoe in it once", theme.Dim
		}
		nameW := clampi(cw/3, 8, 24)
		countW := 12
		l := mark + " " + th.FG(nameRole, fit(m.repoName(&r), nameW)) + " " + th.FG(theme.Dim, fit(count, countW)) + " " + th.FG(theme.Dim, fit(where, max(cw-nameW-countW-4, 1)))
		if i == p.sel {
			l = th.SelectStyle().Render(fit(l, cw))
		}
		lines = append(lines, l)
	}
	if len(rs) == 0 {
		lines = append(lines, th.FG(theme.Dim, noRepoHint))
	}
	title := theme.Prefix(m.ic.Worktrees) + th.Bold(theme.Primary, "Repository") + " " + th.FG(theme.Dim, fmt.Sprintf("%d", len(rs)))
	return m.modalFrame(width, title, lines, p.hints(), false)
}

func (p *repoPicker) key(k tea.KeyMsg) tea.Cmd {
	m := p.m
	rs := m.pickableRepos()
	switch k.String() {
	case "j", "down":
		p.sel = clampi(p.sel+1, 0, max(len(rs)-1, 0))
	case "k", "up":
		p.sel = clampi(p.sel-1, 0, max(len(rs)-1, 0))
	case "enter":
		if p.sel >= len(rs) {
			return done(nil)
		}
		r := rs[p.sel]
		if r.Dir == "" {
			m.setMsg(m.repoName(&r)+": path unknown · run pekoe inside it once", true)
			return nil
		}
		cmd := m.chooseRepo(r)
		if p.then != nil {
			m.modal = nil
			return tea.Batch(cmd, p.then())
		}
		return done(cmd)
	case "esc", "q":
		return done(nil)
	}
	return nil
}

// sessionsEmptyHint is the dim line [1] shows without sessions.
func (m *model) sessionsEmptyHint() string {
	switch {
	case m.curRepo() != nil:
		return "no sessions · n new"
	case len(m.pickableRepos()) > 0:
		return "not in a git repository · w pick one"
	case !m.reposLoaded:
		return "no sessions"
	}
	return noRepoHint
}
