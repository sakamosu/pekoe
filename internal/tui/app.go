// Package tui is pekoe's terminal UI: a thin display/input layer over pekoed.
// Layout (docs/ui-guidelines.md): numbered context panels on the left,
// the agent's screen on the right, a powerline bar at the bottom.
package tui

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"pekoe/internal/client"
	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/errcatch"
	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// ---- messages ----

type evMsg client.Event
type disconnectedMsg struct{}
type tickMsg time.Time
type spinMsg time.Time
type screenMsg struct {
	slug, window string
	sc           *engine.Screen
	err          error
}
type sessionsMsg []*state.Session
type worktreesMsg struct {
	dir  string // the repository they belong to
	list []daemon.WorktreeView
}
type changesMsg struct {
	slug string
	list []change
}
type tabsMsg []daemon.Tab
type picksMsg []*state.Pick
type errMsg struct{ err error }
type infoMsg string
type prefixTimeoutMsg struct{ seq int }

type change struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

const (
	pSessions = 1
	pSecond   = 2 // Worktrees | Browser | Issues
	pThird    = 3 // Changes | Picks
	pAgent    = 4
)

type model struct {
	ime    *imeCursor // IME 用に本物のカーソルを置く位置(View が更新)
	cl     *client.Client
	cwd    string
	th     *theme.Theme
	ic     theme.IconSet
	g      *config.Global
	square bool

	width, height int
	frame         int
	fitReq        string // last pty-resize request fitScreen sent (dedupes)

	sessions []*state.Session
	sel      int // index into visible(); repository headings are not rows of it

	// the inbox (inbox.go): open items oldest first, toasted once each;
	// ]a visits the open ones not visited this round first
	inbox        []state.InboxItem
	inboxToasted map[int64]bool
	inboxVisited map[int64]bool
	attnRound    map[string]bool // sessions ]a went to this round

	repos       []daemon.KnownRepo // repo.list: repositories pekoe knows (repos.go)
	reposLoaded bool
	chosen      *daemon.KnownRepo // picked with w; wins over the selection's repository
	repoKey     string            // Dir of the repository Worktrees / Issues were loaded for
	repoSynced  bool
	repoTitle   string // the repository name in the window title (repoSync)
	sessionsIn  bool   // the first session.list answered
	homePicked  bool   // the start-up selection followed the repository pekoe was started in
	filter      string
	filterOn    bool

	worktrees []daemon.WorktreeView
	wsel      int
	changes   []change
	changesOf string
	csel      int
	tabs      []daemon.Tab
	tsel      int
	picks     []*state.Pick
	pickSent  map[int64]bool    // sent from the card, not yet reloaded as sent (sendPick)
	psel      int               // over picks, then errs (Picks panel)
	errs      []errcatch.Report // open errors offered as cards (errcard.go)

	focus     int
	narrowTab int // < 100 columns: index into narrowTabs
	sub2      int // 0 worktrees, 1 browser, 2 issues (subIssues)
	sub3      int // 0 changes, 1 picks
	typing    bool
	guard     mouseGuard    // reassembles mouse reports split across reads
	linkDown  string        // URL under the last left press in the agent panel
	window    string        // agent | review | dev | run | pr | preview shown in the main panel
	pr        *prState      // the PR view's data (pr.go)
	pv        *previewState // the preview window (preview.go)

	screen     *engine.Screen
	screenOf   string
	fetching   bool
	dirty      bool
	unreadAt   map[string]int // slug -> absolute line where the user left
	lastSel    string
	returnTo   []string // attention queue: where ]a jumped from
	dismissed  map[string]time.Time
	progress   map[string][]daemon.Step
	pickInput  *input
	answers    map[string]string
	prefix     bool
	prefixType bool // the prefix also left typing: a layout key goes back to it
	layout     Layout
	layoutPath string // where layout changes are saved; "" in tests
	layoutSeq  int    // bumped per layout change (debounce)
	layoutDone int    // the layoutSeq the ptys and the file have caught up with
	bracket    string // pending ] or [
	bracketSeq int

	modal    modal
	msg      string
	msgErr   bool
	msgAt    time.Time
	quitting bool

	review *reviewModel // built-in review surface when active
	iss    issuesState  // [2] Issues and the issue shown in the main panel (issues.go)

	graphics   bool           // outer terminal draws Kitty graphics
	sentImages map[int64]bool // pick screenshots already transmitted

	tapi  *tapiState // the mascot (tapi.go)
	chat  chatState  // tapi chat in the main panel (chat.go)
	order *ptyOrder  // keys reach the ptys in the order typed (order.go); a pointer so the tour can copy the model

	spinning bool     // the 80 ms spinner tick is scheduled
	tip      tipState // the tip in the empty main pane (tips.go)

	tour     *tourState // the tutorial, while it runs (tour.go)
	tourPath string     // where seeing the tutorial is recorded; "" in tests
	tourAuto bool       // start the tutorial once the screen is up (first run)
	tourLang string     // the language the next tour starts in (tourJa, tourEn); "" goes by the environment
}

func TermSize() (int, int) {
	if w, h, ok := TermSizeOK(); ok {
		return w, h
	}
	return 120, 40
}

// TermSizeOK reports stdout's terminal size; ok is false when stdout is not a
// terminal.
func TermSizeOK() (int, int, bool) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func narrow(total int) bool { return total < 100 }

// AgentWidth/Height compute the pty size of the main panel for a terminal,
// with the saved layout (LoadLayout).
func AgentWidth(total int) int { return LoadLayout().agentWidth(total) }

func AgentHeight(total int) int {
	return max(total-1-2-narrowTop(total), 5)
}

func narrowTop(total int) int { return 0 }

func newModel(cl *client.Client, cwd string) *model {
	g, err := config.LoadGlobal()
	if err != nil {
		g = &config.Global{}
	}
	m := &model{
		cl: cl, cwd: cwd, g: g,
		th:         theme.Load(&g.Theme),
		ic:         theme.Icons(theme.IconsFromEnv()),
		focus:      pSessions,
		window:     engine.WinAgent,
		pr:         newPRState(),
		pv:         newPreviewState(),
		unreadAt:   map[string]int{},
		dismissed:  map[string]time.Time{},
		progress:   map[string][]daemon.Step{},
		answers:    map[string]string{},
		square:     os.Getenv("PEKOE_SQUARE") != "",
		graphics:   graphicsCapable(),
		sentImages: map[int64]bool{},
		ime:        &imeCursor{},
	}
	m.width, m.height = TermSize()
	m.newTapi()
	m.initTips("") // Run keeps them in paths.TipsFile()
	m.initTour("") // Run records it in paths.TutorialFile()
	return m
}

func Run(cl *client.Client, cwd string) error {
	if err := cl.Subscribe("*"); err != nil {
		return err
	}
	m := newModel(cl, cwd)
	m.layout, m.layoutPath = LoadLayout(), paths.LayoutFile()
	m.initTips(paths.TipsFile())
	m.initTour(paths.TutorialFile())
	m.buildTapi(measureTTY()) // measures ● before the TUI owns the terminal
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus(),
		tea.WithOutput(imeWriter{File: os.Stdout, cur: m.ime})}
	if kittyEnabled() {
		opts = append(opts, tea.WithFilter(kittyFilter))
	}
	p := tea.NewProgram(m, opts...)
	go pump(cl, p)
	_, err := p.Run()
	if seq := m.pv.deleteAll(); seq != "" {
		os.Stdout.WriteString(seq) // leave no preview image behind in the terminal
	}
	if m.pv.watching != "" {
		_ = cl.Call("preview.watch", map[string]any{"slug": m.pv.watching, "on": false}, nil)
	}
	if kittyEnabled() {
		os.Stdout.WriteString(kittyPop)
	}
	return err
}

func pump(cl *client.Client, p *tea.Program) {
	for ev := range cl.Events {
		p.Send(evMsg(ev))
	}
	p.Send(disconnectedMsg{})
}

func (m *model) Init() tea.Cmd {
	m.spinning = m.g.AnimateOn()
	cmds := []tea.Cmd{m.loadSessions(), m.loadTapi(), m.loadInbox(), m.loadRepos(), m.loadTabs(), m.loadPicks(), m.loadErrors(), tick(), spin(m.g.AnimateOn()), m.resizeAll(), tea.SetWindowTitle("pekoe")}
	cmds = append(cmds, m.tapiInit()...)
	if kittyEnabled() {
		cmds = append(cmds, writeTerm(kittyPush)) // Kitty keyboard: disambiguate
	}
	g := m.graphics
	cmds = append(cmds, func() tea.Msg { // lets pekoed answer agents' graphics queries
		_ = m.cl.Call("term.caps", map[string]any{"kitty_graphics": g}, nil)
		return nil
	})
	return tea.Batch(cmds...)
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func spin(on bool) tea.Cmd {
	if !on {
		return nil
	}
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg { return spinMsg(t) })
}

// ---- loaders (commands) ----

func (m *model) call(method string, params any, out any) error { return m.cl.Call(method, params, out) }

func (m *model) loadSessions() tea.Cmd {
	return func() tea.Msg {
		var ss []*state.Session
		if err := m.cl.Call("session.list", nil, &ss); err != nil {
			return errMsg{err}
		}
		return sessionsMsg(ss)
	}
}

// loadWorktrees lists the current repository's worktrees.
func (m *model) loadWorktrees() tea.Cmd {
	dir := m.curRepoDir()
	return func() tea.Msg {
		var ws []daemon.WorktreeView
		if dir != "" && m.cl != nil {
			_ = m.cl.Call("worktree.list", map[string]any{"repo": dir}, &ws)
		}
		return worktreesMsg{dir: dir, list: ws}
	}
}

func (m *model) loadChanges(slug string) tea.Cmd {
	if slug == "" {
		return nil
	}
	return func() tea.Msg {
		var cs []change
		if err := m.cl.Call("changes", map[string]any{"slug": slug}, &cs); err != nil {
			return changesMsg{slug: slug}
		}
		return changesMsg{slug: slug, list: cs}
	}
}

func (m *model) loadTabs() tea.Cmd {
	return func() tea.Msg {
		var ts []daemon.Tab
		_ = m.cl.Call("browser.tabs", nil, &ts)
		return tabsMsg(ts)
	}
}

func (m *model) loadPicks() tea.Cmd {
	return func() tea.Msg {
		var ps []*state.Pick
		_ = m.cl.Call("pick.list", nil, &ps)
		return picksMsg(ps)
	}
}

func (m *model) fetchScreen() tea.Cmd {
	s := m.current()
	if s == nil || s.State == state.Creating || m.window == winPR || m.window == winPreview {
		return nil
	}
	if m.fetching {
		m.dirty = true
		return nil
	}
	m.fetching = true
	slug, win := s.Slug, m.window
	return func() tea.Msg {
		var sc engine.Screen
		err := m.cl.Call("session.screen", map[string]any{"slug": slug, "window": win}, &sc)
		return screenMsg{slug: slug, window: win, sc: &sc, err: err}
	}
}

func (m *model) resizeAll() tea.Cmd {
	aw, ah := m.agentSize()
	return func() tea.Msg {
		_ = m.cl.Call("session.resize", map[string]any{"slug": "*", "width": aw, "height": ah}, nil)
		return nil
	}
}

// fitScreen resizes the shown window's pty when it doesn't match the pane.
// resizeAll only runs at start and on terminal resizes, so a window spawned
// later by someone who didn't know the pane size (`pekoe new` run by an
// agent, whose stdout isn't a terminal) would otherwise keep its size: the
// agent wraps narrower than the pane, or paints past its border.
func (m *model) fitScreen(slug, window string, sc *engine.Screen) tea.Cmd {
	aw, ah := m.agentSize()
	if m.layoutPending() {
		return nil // layoutSettled resizes them all
	}
	if sc == nil || sc.Dead || sc.Width <= 0 || sc.Height <= 0 || (sc.Width == aw && sc.Height == ah) {
		return nil
	}
	key := fmt.Sprintf("%s\x00%s\x00%dx%d>%dx%d", slug, window, sc.Width, sc.Height, aw, ah)
	if m.fitReq == key {
		return nil // already asked; wait for tmux to apply it
	}
	m.fitReq = key
	return func() tea.Msg {
		if m.cl == nil {
			return nil
		}
		_ = m.cl.Call("session.resize", map[string]any{"slug": slug, "window": window, "width": aw, "height": ah}, nil)
		return nil
	}
}

func (m *model) agentSize() (int, int) {
	if narrow(m.width) {
		return max(m.width-4, 20), max(m.height-1-m.narrowTopH()-2, 5)
	}
	return m.layout.agentWidth(m.width), max(m.height-1-2, 5)
}

func (m *model) act(method string, params any, okMsg string, then ...tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		if err := m.cl.Call(method, params, nil); err != nil {
			return errMsg{err}
		}
		if okMsg != "" {
			return infoMsg(okMsg)
		}
		return nil
	}
}

// ---- selection helpers ----

func (m *model) visible() []*state.Session {
	if m.filter == "" {
		return m.sessions
	}
	var out []*state.Session
	for _, s := range m.sessions {
		if strings.Contains(strings.ToLower(s.Branch+" "+s.Slug+" "+s.Repo), strings.ToLower(m.filter)) {
			out = append(out, s)
		}
	}
	return out
}

func (m *model) current() *state.Session {
	v := m.visible()
	if m.sel >= 0 && m.sel < len(v) {
		return v[m.sel]
	}
	return nil
}

func (m *model) find(slug string) (int, *state.Session) {
	for i, s := range m.visible() {
		if s.Slug == slug {
			return i, s
		}
	}
	return -1, nil
}

func (m *model) selectSlug(slug string) tea.Cmd {
	if i, _ := m.find(slug); i >= 0 {
		m.sel = i
	} else {
		m.filter = ""
		if i, _ := m.find(slug); i >= 0 {
			m.sel = i
		}
	}
	return m.onSelChange()
}

// onSelChange records the unread line of the session we leave and loads
// the screen + changes of the new one.
func (m *model) onSelChange() tea.Cmd {
	s := m.current()
	cur := ""
	if s != nil {
		cur = s.Slug
	}
	if cur == m.lastSel {
		return nil
	}
	if m.chosen != nil && s != nil && m.lastSel != "" && m.lastSel != "-" {
		m.chosen = nil // moving to a session makes its repository the current one
	}
	var cmds []tea.Cmd
	if m.lastSel != "" && m.screen != nil && m.screenOf == m.lastSel {
		left := m.screen.History + m.screen.Cursor.Y
		m.unreadAt[m.lastSel] = left
		prev := m.lastSel
		cmds = append(cmds, func() tea.Msg {
			_ = m.cl.Call("session.seen", map[string]any{"slug": prev, "lines": left}, nil)
			return nil
		})
	}
	m.lastSel = cur
	m.screen = nil
	m.iss.open = false
	m.typing = false
	m.pickInput = nil
	switch {
	case m.window == winPR && s != nil:
		cmds = append(cmds, m.prFollow()) // the PR view follows the selection
	case m.window == winPreview && s != nil:
		// the preview follows the selection too (previewSync moves the watch)
	default:
		m.window = engine.WinAgent
	}
	cmds = append(cmds, m.fetchScreen(), m.loadChanges(cur))
	return tea.Batch(cmds...)
}

func (m *model) setMsg(s string, isErr bool) {
	m.msg, m.msgErr, m.msgAt = s, isErr, time.Now()
}

// sendPickImages transmits screenshots of unsent picks (once each) so the
// card can show them in place of the dashed placeholder.
func (m *model) sendPickImages() tea.Cmd {
	if !m.graphics {
		return nil
	}
	var b strings.Builder
	for _, pk := range m.picks {
		if pk.Sent || pk.Image == "" || m.sentImages[pk.ID] {
			continue
		}
		m.sentImages[pk.ID] = true
		b.WriteString(transmitImage(imageID(pk.ID), pk.Image, pickImgCols, pickImgRows))
	}
	if b.Len() == 0 {
		return nil
	}
	return writeTerm(b.String())
}

const (
	pickImgCols = 24
	pickImgRows = 3
)

// ---- update ----

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.tour != nil { // the tutorial owns the screen and the keys (tour.go)
		cmd := m.tourUpdate(msg)
		if m.tour != nil {
			return m, cmd
		}
		// it just ended: the real model catches up like after any update
		m.issueSync()
		m.tipSync()
		return m, tea.Batch(cmd, m.previewSync(), m.repoSync(), m.unzoomIfLeft())
	}
	defer m.maybeAutoTour()
	defer m.markSeen()
	defer m.issueSync()
	var cmd tea.Cmd
	// every key passes the mouse guard first, whichever screen handles it
	switch msg := msg.(type) {
	case tea.KeyMsg:
		keys, wait := m.guard.feed(msg)
		cmd = m.routeKeys(keys)
		if wait {
			cmd = tea.Batch(cmd, guardTick(m.guard.gen))
		}
	case guardFlushMsg:
		cmd = m.routeKeys(m.guard.flush(int(msg)))
	default:
		_, cmd = m.route(msg)
	}
	m.tipSync()
	return m, tea.Batch(cmd, m.previewSync(), m.repoSync(), m.unzoomIfLeft())
}

func (m *model) routeKeys(keys []tea.KeyMsg) tea.Cmd {
	var cmds []tea.Cmd
	for _, k := range keys {
		_, c := m.route(k)
		cmds = append(cmds, c)
	}
	if len(cmds) == 1 {
		return cmds[0]
	}
	return tea.Sequence(cmds...)
}

func (m *model) route(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.chatUpdate(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.tapiUpdate(msg); ok {
		return m, cmd
	}
	if m.review != nil {
		switch msg.(type) {
		case tea.KeyMsg, tea.MouseMsg, reviewMsg, reviewAnswerMsg, errMsg, infoMsg, modalDoneMsg:
			cmd, done := m.review.update(msg)
			if done {
				slug := m.review.slug
				m.review = nil
				return m, tea.Batch(cmd, func() tea.Msg {
					_ = m.cl.Call("review.end", map[string]any{"slug": slug}, nil)
					return nil
				}, m.loadSessions(), m.fetchScreen())
			}
			return m, cmd
		case evMsg:
			cmd, _ := m.review.update(msg)
			_, cmd2 := m.updateMain(msg)
			return m, tea.Batch(cmd, cmd2)
		}
	}
	return m.updateMain(msg)
}

func (m *model) updateMain(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case jumpMsg:
		// the session queue came round again: so does the inbox
		if m.attnRound[msg.slug] && len(m.inbox) > 0 {
			m.inboxRoundReset()
			if it := m.nextInbox(); it != nil {
				return m, m.openInbox(*it)
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
			return m, m.selectTapi()
		}
		cmd := m.selectSlug(msg.slug)
		if msg.pr { // in the queue for its PR: show the PR
			return m, tea.Batch(cmd, m.openPR())
		}
		return m, tea.Batch(cmd, m.maybePermission())
	case reviewSurfaceMsg:
		if s := m.current(); s != nil && s.Slug == msg.slug {
			m.chat.open = false
			m.window = engine.WinReview
			m.screen = nil
			m.focus = pAgent
			m.typing = true
			return m, m.fetchScreen()
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.pv.cellW, m.pv.cellH = cellPixels()
		if m.review != nil {
			m.review.width, m.review.height = msg.Width, msg.Height
		}
		return m, tea.Batch(m.resizeAll(), m.fetchScreen(), m.fetchChat())

	case tickMsg:
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
		cmds = append(cmds, m.maybePermission(), m.escalate())
		m.tapiExpire()
		return m, tea.Batch(cmds...)

	case spinMsg:
		if !m.tapi.focused { // animations stop while the window is unfocused
			m.spinning = false
			return m, nil
		}
		m.frame++
		return m, spin(m.g.AnimateOn())

	case disconnectedMsg:
		m.setMsg("lost connection to pekoed", true)
		return m, tea.Quit

	case issuesMsg, issueViewMsg:
		return m, m.onIssuesMsg(msg)

	case errMsg:
		if msg.err != nil {
			m.setMsg(msg.err.Error(), true)
		}
		return m, nil

	case pickSendFailedMsg: // the card comes back to try again
		delete(m.pickSent, msg.id)
		m.setMsg(msg.err.Error(), true)
		return m, nil

	case infoMsg:
		m.setMsg(string(msg), false)
		return m, nil

	case inboxMsg:
		m.onInboxList(msg)
		return m, nil

	case inboxAnswerMsg:
		return m, m.onInboxAnswer(msg)

	case noWaitMsg:
		// nothing else waits: go round the inbox again
		m.inboxRoundReset()
		if it := m.nextInbox(); it != nil {
			return m, m.openInbox(*it)
		}
		m.setMsg("nothing is waiting", false)
		return m, nil

	case reposMsg:
		m.repos, m.reposLoaded = msg, true
		if m.sessionsIn && !m.homePicked {
			m.pickHome()
			return m, m.onSelChange()
		}
		if m.chosen != nil {
			for _, r := range m.repos {
				if r.ID == m.chosen.ID {
					rr := r
					m.chosen = &rr
				}
			}
		}
		return m, nil

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
		return m, tea.Batch(m.onSelChange(), m.maybePermission())

	case worktreesMsg:
		if msg.dir != m.curRepoDir() {
			return m, nil // the current repository changed meanwhile
		}
		m.worktrees = msg.list
		m.wsel = clampi(m.wsel, 0, max(len(m.worktrees)-1, 0))
		return m, nil

	case changesMsg:
		if s := m.current(); s != nil && s.Slug == msg.slug {
			m.changes, m.changesOf = msg.list, msg.slug
			m.csel = clampi(m.csel, 0, max(len(m.changes)-1, 0))
		}
		return m, nil

	case tabsMsg:
		m.tabs = msg
		m.tsel = clampi(m.tsel, 0, max(len(m.tabs)-1, 0))
		if len(m.tabs) > 0 && m.sub2 == 0 && m.focus != pSecond {
			m.sub2 = 1 // linked tabs exist: show Browser in [2] (mock 05)
		}
		return m, nil

	case picksMsg:
		m.picks = msg
		m.prunePickSent()
		m.ensurePickInput()
		return m, m.sendPickImages()

	case errsMsg:
		m.errs = msg
		return m, nil

	case errDetailMsg:
		if em, ok := m.modal.(*errModal); ok && em.r.ID == msg.id {
			em.excerpt = msg.excerpt
		}
		return m, nil

	case screenMsg:
		m.fetching = false
		var cmd tea.Cmd
		if m.dirty {
			m.dirty = false
			cmd = m.fetchScreen()
		}
		s := m.current()
		if s == nil || s.Slug != msg.slug || msg.window != m.window {
			return m, cmd
		}
		if msg.err != nil {
			m.screen = nil
			return m, cmd
		}
		m.screen, m.screenOf = msg.sc, msg.slug
		cmd = tea.Batch(cmd, m.fitScreen(msg.slug, msg.window, msg.sc))
		if msg.window == engine.WinReview && msg.sc.Dead {
			// nvim exited (:PekoeSend → :qa): back to the agent screen
			slug := s.Slug
			m.window = engine.WinAgent
			m.screen = nil
			return m, tea.Batch(cmd, func() tea.Msg {
				_ = m.cl.Call("window.kill", map[string]any{"slug": slug, "window": engine.WinReview}, nil)
				return nil
			}, m.fetchScreen(), m.loadSessions())
		}
		return m, cmd

	case evMsg:
		return m, m.onEvent(client.Event(msg))

	case layoutSettledMsg:
		return m, m.layoutSettled(msg.seq)

	case prefixTimeoutMsg:
		if msg.seq == m.bracketSeq && m.bracket != "" {
			b := m.bracket
			m.bracket = ""
			return m, m.subtab(b == "]")
		}
		return m, nil

	case modalDoneMsg:
		m.modal = nil
		return m, msg.cmd

	case prMsg:
		return m, m.onPRMsg(msg)

	case prLogMsg:
		m.onPRLogMsg(msg)
		return m, nil

	case prSendMsg:
		return m, m.onPRSendMsg(msg)

	case previewMsg:
		return m, m.onPreviewMsg(msg)

	case runPromptMsg:
		return m, m.onRunPrompt(msg)

	case runStartedMsg:
		return m, m.onRunStarted(msg)

	case runClosedMsg:
		return m, m.onRunClosed(msg)

	case reviewOpenMsg:
		rm := newReviewModel(m.cl, msg.slug, m.th, m.ic, m.width, m.height, m.square)
		m.review = rm
		return m, rm.init()

	case kittyKeyMsg:
		// keys with no legacy encoding (Shift+Enter …) only matter to the pty
		if m.typing && m.modal == nil && m.chat.open {
			return m, m.chatKeys(map[string]any{"keys": []string{msg.name}})
		}
		if m.typing && m.modal == nil {
			if s := m.current(); s != nil {
				slug, win, name := s.Slug, m.window, msg.name
				return m, m.inOrder(func() error {
					return m.cl.Call("session.keys", map[string]any{"slug": slug, "window": win, "keys": []string{name}}, nil)
				})
			}
		}
		return m, nil

	case tea.MouseMsg:
		return m, m.onMouse(msg)

	case tea.KeyMsg:
		return m, m.onKey(msg)
	}
	if m.modal != nil {
		if c, ok := m.modal.(interface{ other(tea.Msg) tea.Cmd }); ok {
			return m, c.other(msg)
		}
	}
	return m, nil
}

// sortSessions: creation order, landed sessions sink to the bottom, then
// grouped by repository (a repository sits where its first session would).
func (m *model) sortSessions() {
	sort.SliceStable(m.sessions, func(i, j int) bool {
		li, lj := m.sessions[i].State == state.Landed, m.sessions[j].State == state.Landed
		if li != lj {
			return lj
		}
		return m.sessions[i].CreatedAt.Before(m.sessions[j].CreatedAt)
	})
	groupByRepo(m.sessions)
}

func (m *model) upsert(s *state.Session) {
	for i, x := range m.sessions {
		if x.Slug == s.Slug {
			m.sessions[i] = s
			return
		}
	}
	m.sessions = append(m.sessions, s)
	m.sortSessions()
}

func (m *model) onEvent(ev client.Event) tea.Cmd {
	if cmd, ok := m.chatEvent(ev); ok {
		return cmd
	}
	switch ev.Event {
	case "session.created", "session.updated", "session.waiting", "session.done":
		var s state.Session
		if json.Unmarshal(ev.Data, &s) != nil {
			return nil
		}
		cur := m.current()
		curSlug := ""
		if cur != nil {
			curSlug = cur.Slug
		}
		var prev state.State
		for _, x := range m.sessions {
			if x.Slug == s.Slug {
				prev = x.State
			}
		}
		m.tapiOnSession(prev, &s, curSlug)
		m.upsert(&s)
		m.closeStalePermission()
		if curSlug != "" {
			if i, _ := m.find(curSlug); i >= 0 {
				m.sel = i
			}
		} else if len(m.sessions) == 1 && !m.onTapiRow() {
			m.sel = 0
		}
		var cmds []tea.Cmd
		if ev.Event == "session.created" {
			cmds = append(cmds, m.loadWorktrees(), m.loadRepos())
		}
		if curSlug == "" {
			cmds = append(cmds, m.onSelChange())
		}
		if s.Slug == curSlug {
			if m.changesOf != s.Slug || (cur != nil && cur.Diffstat != s.Diffstat) {
				cmds = append(cmds, m.loadChanges(s.Slug))
			}
			if cur != nil && cur.State == state.Creating && s.State != state.Creating {
				cmds = append(cmds, m.fetchScreen())
			}
		}
		if ev.Event == "session.waiting" {
			cmds = append(cmds, m.maybePermission())
		}
		return tea.Batch(cmds...)
	case "session.removed":
		var s state.Session
		json.Unmarshal(ev.Data, &s)
		m.tapi.lastActive = time.Now()
		curSlug := ""
		if cur := m.current(); cur != nil {
			curSlug = cur.Slug
		}
		for i, x := range m.sessions {
			if x.Slug == s.Slug {
				m.sessions = append(m.sessions[:i], m.sessions[i+1:]...)
				break
			}
		}
		m.closeStalePermission()
		// another session going away keeps the selection (and typing) on
		// the current one; only losing the current one moves it
		if i, _ := m.find(curSlug); curSlug != "" && i >= 0 {
			m.sel = i
			return tea.Batch(m.loadWorktrees(), m.loadRepos())
		}
		m.clampSel()
		m.lastSel = "-"
		return tea.Batch(m.onSelChange(), m.loadWorktrees(), m.loadRepos())
	case "session.progress":
		var p struct {
			Slug  string        `json:"slug"`
			Steps []daemon.Step `json:"steps"`
		}
		json.Unmarshal(ev.Data, &p)
		m.progress[p.Slug] = p.Steps
		return nil
	case "inbox.updated":
		return m.onInboxEvent(ev.Data)
	case "session.output":
		var p struct {
			Slug string `json:"slug"`
		}
		json.Unmarshal(ev.Data, &p)
		if s := m.current(); s != nil && s.Slug == p.Slug {
			return m.fetchScreen()
		}
		return nil
	case "pick.received":
		cmds := []tea.Cmd{m.loadPicks()}
		var pk state.Pick
		if json.Unmarshal(ev.Data, &pk) == nil && pk.File != "" && !pk.Sent {
			m.sub3 = 1
			if pk.Slug != "" {
				cmds = append(cmds, m.selectSlug(pk.Slug))
			}
			m.setMsg(fmt.Sprintf("pick #%04d from Chrome", pk.ID), false)
		}
		return tea.Batch(cmds...)
	case "error.updated":
		return m.onErrorEvent(ev.Data)
	case "browser.updated":
		var ts []daemon.Tab
		if json.Unmarshal(ev.Data, &ts) == nil {
			m.tabs = ts
			m.tsel = clampi(m.tsel, 0, max(len(m.tabs)-1, 0))
			if len(ts) > 0 && m.sub2 == 0 && m.focus != pSecond {
				m.sub2 = 1
			}
		}
		return nil
	case "review.updated":
		if s := m.current(); s != nil {
			return m.loadChanges(s.Slug)
		}
	case "review.answer":
		var a struct {
			Slug string `json:"slug"`
			Text string `json:"text"`
		}
		json.Unmarshal(ev.Data, &a)
		m.answers[a.Slug] = a.Text
	case "preview.updated":
		m.onPreviewEvent(ev)
	case "term.passthrough":
		var p struct {
			Seq string `json:"seq"`
		}
		json.Unmarshal(ev.Data, &p)
		if b, err := base64.StdEncoding.DecodeString(p.Seq); err == nil {
			// OSC 52 / 9 / 777 go straight to Ghostty (nesting tax: pass through)
			os.Stdout.Write(b)
		}
	}
	return nil
}

// maybePermission opens the permission modal for the selected session when
// it waits for a permission and the user is not typing elsewhere.
// closeStalePermission closes the permission dialog once its session no
// longer waits for a permission (or is gone): it would keep every key.
func (m *model) closeStalePermission() {
	if p, ok := m.modal.(*permissionModal); ok && p.stale() {
		m.modal = nil
	}
}

func (m *model) maybePermission() tea.Cmd {
	if m.modal != nil || m.typing || m.review != nil {
		return nil
	}
	s := m.current()
	if s == nil || s.State != state.Waiting || s.WaitKind != "permission" {
		return nil
	}
	if t, ok := m.dismissed[s.Slug]; ok && !s.StateSince.After(t) {
		return nil
	}
	m.modal = newPermissionModal(m, s)
	return nil
}

// ---- keys ----

func (m *model) prefixKey(k tea.KeyMsg) bool {
	p := m.g.Keys.Prefix
	if p == "" {
		p = "ctrl+q"
	}
	return k.String() == p
}

func (m *model) onKey(k tea.KeyMsg) tea.Cmd {
	if m.modal != nil {
		return m.modal.key(k)
	}
	// typing: everything goes to the pty except the prefix, which leaves
	if m.typing && m.chat.open {
		return m.chatTypingKey(k)
	}
	if m.typing {
		if m.prefixKey(k) {
			m.typing = false
			if !m.layout.Zoom || narrow(m.width) { // zoomed, the left column is hidden
				m.focus = pSessions
			}
			m.prefix, m.prefixType = true, true
			return nil
		}
		if m.screen != nil && m.screen.Dead {
			// the process in this window has exited: nothing to type into
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
		return m.forward(k)
	}
	if m.prefix {
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
			return cmd
		}
		// after ^q left typing, any other key is a plain key: l still lands
		if !fromTyping {
			switch k.String() {
			case "l", "right":
				m.focus = pAgent
				return nil
			case "h", "left":
				m.focus = pSessions
				return nil
			}
		}
	}
	if m.filterOn {
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
	if m.iss.queryOn {
		return m.issueQueryKey(k)
	}
	if m.pickInput != nil && m.focus == pAgent && !m.issueShown() && !m.chat.open {
		if cmd, used := m.pickKey(k); used {
			return cmd
		}
	}
	if m.focus == pAgent && m.bracket == "" && !m.issueShown() && !m.chat.open {
		if cmd, used := m.errorKey(k.String()); used {
			return cmd
		}
	}
	key := k.String()
	// ] / [ prefixes: ]a ]r [a, otherwise subtab switch
	if m.bracket != "" {
		b := m.bracket
		m.bracket = ""
		switch b + key {
		case "]a":
			return m.nextWaiting()
		case "[a":
			return m.backFromWaiting()
		case "]r":
			return m.nextReview()
		}
		return tea.Batch(m.subtab(b == "]"), m.onKey(k))
	}
	if key == "]" || key == "[" {
		m.bracket = key
		m.bracketSeq++
		seq := m.bracketSeq
		return tea.Tick(350*time.Millisecond, func(time.Time) tea.Msg { return prefixTimeoutMsg{seq} })
	}
	// global keys
	switch key {
	case "q", "ctrl+c":
		return m.quit()
	case "1":
		m.focus = pSessions
		return nil
	case "2":
		m.focus = pSecond
		return nil
	case "3":
		m.focus = pThird
		return nil
	case "4":
		m.focus = pAgent
		return nil
	case "tab":
		m.focus = m.focus%4 + 1
		return nil
	case "shift+tab":
		m.focus = (m.focus+2)%4 + 1
		return nil
	case "?":
		m.modal = newHelpModal(m)
		return nil
	case ":":
		m.modal = newCommandModal(m)
		return nil
	case "w":
		return m.openRepoPicker(nil)
	case "t": // tapi chat, from any panel (chat.go)
		return m.openChat()
	case "T":
		return m.confirmResetChat()
	case tourKey: // replay the tutorial (tour.go)
		return m.startTour()
	case tipKey: // the next tip, while the empty main pane shows one (tips.go)
		if m.tipSpot() {
			m.nextTip()
			return nil
		}
	}
	if m.prefixKey(k) {
		m.prefix = true
		return nil
	}
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
		if m.chat.open {
			return m.chatKey(key)
		}
		if m.window == winPR && !m.issueShown() {
			return m.keyPR(key)
		}
		if m.window == winPreview && !m.issueShown() {
			return m.keyPreview(key)
		}
		return m.keyAgent(key)
	}
	return nil
}

// narrowTabs are the tabs of the one left panel below 100 columns.
var narrowTabs = []struct{ focus, sub2 int }{{pSessions, 0}, {pSecond, 0}, {pSecond, subIssues}, {pThird, 0}}

// cycle steps i through n values forwards or backwards.
func cycle(i, n int, next bool) int {
	if next {
		return (i + 1) % n
	}
	return (i + n - 1) % n
}

func (m *model) subtab(next bool) tea.Cmd {
	if narrow(m.width) && m.focus != pAgent {
		m.narrowTab = cycle(m.narrowTab, len(narrowTabs), next)
		t := narrowTabs[m.narrowTab]
		m.focus = t.focus
		m.sub2, m.sub3 = t.sub2, 0
		if m.sub2 == subIssues {
			return m.enterIssues()
		}
		return nil
	}
	switch m.focus {
	case pSecond:
		m.sub2 = cycle(m.sub2, 3, next) // worktrees → browser → issues
		switch m.sub2 {
		case 1:
			return m.loadTabs()
		case subIssues:
			return m.enterIssues()
		}
		return m.loadWorktrees()
	case pThird:
		m.sub3 = cycle(m.sub3, 2, next)
		if m.sub3 == 1 {
			return m.loadPicks()
		}
	}
	return nil
}

func (m *model) quit() tea.Cmd {
	running := 0
	for _, s := range m.sessions {
		if s.State == state.Running {
			running++
		}
	}
	// agents live in pekoed, so quitting the TUI never kills them
	if running > 0 {
		m.modal = newConfirmModal(m, "Quit", fmt.Sprintf("%d agents are running. They keep running in pekoed.", running), "", false, func() tea.Cmd { return tea.Quit })
		return nil
	}
	return tea.Quit
}

func (m *model) common(key string) (tea.Cmd, bool) {
	s := m.current()
	switch key {
	case "n":
		return m.openNew(nil), true
	case "r":
		if s == nil {
			return nil, true
		}
		return m.openReview(s.Slug), true
	}
	return nil, false
}

func (m *model) move(idx *int, n int, delta int) {
	if n == 0 {
		*idx = 0
		return
	}
	*idx = clampi(*idx+delta, 0, n-1)
}

// moveWrap is move that wraps around at the ends (bottom → top, top → bottom).
func (m *model) moveWrap(idx *int, n int, delta int) {
	if n == 0 {
		*idx = 0
		return
	}
	*idx = ((*idx+delta)%n + n) % n
}

func (m *model) keySessions(key string) tea.Cmd {
	if m.onTapiRow() {
		return m.tapiRowKey(key)
	}
	s := m.current()
	if cmd, ok := m.common(key); ok {
		return cmd
	}
	switch key {
	case "j", "down":
		m.moveSel(1)
		return m.onSelChange()
	case "k", "up":
		m.moveSel(-1)
		return m.onSelChange()
	case "g", "home": // the top is the tapi row
		return m.selectTapi()
	case "G", "end":
		m.sel = max(len(m.visible())-1, 0)
		return m.onSelChange()
	case "enter", "right": // l is land (sessionAction), as the help says
		if s == nil {
			return nil
		}
		m.focus = pAgent
		if s.State == state.Creating {
			m.modal = newProgressModal(m, s.Slug)
			return nil
		}
		if m.window == winPR {
			m.typing = true
			delete(m.unreadAt, s.Slug)
			return m.closePR()
		}
		m.typing = true
		delete(m.unreadAt, s.Slug)
		if m.window == winPreview { // typing goes to the agent
			return m.closePreview()
		}
		if m.screen != nil && m.screen.Dead && m.window == engine.WinAgent {
			aw, ah := m.agentSize()
			return m.act("session.restart", map[string]any{"slug": s.Slug, "width": aw, "height": ah}, "restarted "+s.Slug)
		}
		return nil
	case "!": // run one command in the worktree (run.go)
		return m.openRun()
	case "x":
		if m.runDone() { // the panel's "x close": the run, not the session
			return m.closeRun(false)
		}
	case "/":
		m.filterOn = true
		m.filter = ""
		return nil
	case "v":
		return m.openPreview()
	case "esc":
		if m.window == winPR {
			return m.closePR()
		}
	}
	return m.sessionAction(key, s)
}

// sessionAction handles keys meaning the same everywhere a session is selected.
func (m *model) sessionAction(key string, s *state.Session) tea.Cmd {
	if s == nil {
		return nil
	}
	switch key {
	case "p":
		return m.openPR()
	case "b":
		if s.Port == 0 {
			return nil
		}
		url := fmt.Sprintf("http://localhost:%d", s.Port)
		return func() tea.Msg {
			if err := openChrome(url); err != nil {
				return errMsg{err}
			}
			return infoMsg("opened " + url)
		}
	case "l":
		m.modal = newConfirmModal(m, "Land", "rebase → test → push → gh pr create", "pekoe land "+s.Slug, false, func() tea.Cmd {
			return func() tea.Msg {
				var res map[string]any
				if err := m.cl.Call("land", map[string]any{"slug": s.Slug}, &res); err != nil {
					return errMsg{err}
				}
				if pr, ok := res["pr"]; ok {
					return infoMsg(fmt.Sprintf("landing %s · PR #%v · teardown after merge", s.Slug, pr))
				}
				return infoMsg("landed " + s.Slug)
			}
		}).about(s.Branch).note(m.landNotes(s))
	case "d":
		return m.act("dev.toggle", map[string]any{"slug": s.Slug}, "dev server toggled · :"+fmt.Sprint(s.Port))
	case "x":
		if s.State == state.Running || s.State == state.Waiting {
			m.modal = newConfirmModal(m, "End session", "the agent is running; end it? (worktree is kept)", "", true, func() tea.Cmd {
				return m.act("session.kill", map[string]any{"slug": s.Slug}, "ended "+s.Slug)
			}).about(s.Branch)
			return nil
		}
		return m.act("session.kill", map[string]any{"slug": s.Slug}, "ended "+s.Slug)
	case "D":
		what := "forget this session (the worktree is outside ~/worktrees and is kept)"
		if s.Managed {
			what = "run teardown and delete the worktree"
		}
		m.modal = newConfirmModal(m, "Delete worktree", what, s.Path, true, func() tea.Cmd {
			return m.act("worktree.rm", map[string]any{"slug": s.Slug, "force": true}, "deleted "+s.Slug)
		}).about(s.Branch)
	case "u":
		m.modal = newConfirmModal(m, "Undo prompt", fmt.Sprintf("restore the worktree to snapshot #%d (before the last prompt)", s.Prompts), fmt.Sprintf("refs/pekoe/%s/prompt-%d", s.Slug, s.Prompts), true, func() tea.Cmd {
			return m.act("session.undo", map[string]any{"slug": s.Slug}, "undid the last prompt")
		}).about(s.Branch)
	case "R":
		aw, ah := m.agentSize()
		return m.act("session.restart", map[string]any{"slug": s.Slug, "width": aw, "height": ah}, "restarted "+s.Slug)
	}
	return nil
}

func (m *model) keyWorktrees(key string) tea.Cmd {
	if cmd, ok := m.common(key); ok {
		return cmd
	}
	switch key {
	case "j", "down":
		m.move(&m.wsel, len(m.worktrees), 1)
	case "k", "up":
		m.move(&m.wsel, len(m.worktrees), -1)
	case "enter":
		if m.wsel >= len(m.worktrees) {
			return nil
		}
		wt := m.worktrees[m.wsel]
		if wt.Slug != "" {
			m.focus = pSessions
			return m.selectSlug(wt.Slug)
		}
		aw, ah := m.agentSize()
		return func() tea.Msg {
			var raw json.RawMessage
			if err := m.cl.Call("session.open", map[string]any{"path": wt.Path, "resume": "resume", "width": aw, "height": ah}, &raw); err != nil {
				return errMsg{err}
			}
			return infoMsg("opened " + wt.Path)
		}
	case "D":
		if m.wsel < len(m.worktrees) && m.worktrees[m.wsel].Slug != "" {
			_, s := m.find(m.worktrees[m.wsel].Slug)
			return m.sessionAction("D", s)
		}
	}
	return nil
}

func (m *model) keyChanges(key string) tea.Cmd {
	if cmd, ok := m.common(key); ok {
		return cmd
	}
	switch key {
	case "j", "down":
		m.move(&m.csel, len(m.changes), 1)
	case "k", "up":
		m.move(&m.csel, len(m.changes), -1)
	case "enter":
		if s := m.current(); s != nil {
			return m.openReview(s.Slug)
		}
	}
	return nil
}

func (m *model) keyBrowser(key string) tea.Cmd {
	switch key {
	case "j", "down":
		m.move(&m.tsel, len(m.tabs), 1)
	case "k", "up":
		m.move(&m.tsel, len(m.tabs), -1)
	case "enter":
		if m.tsel < len(m.tabs) && m.tabs[m.tsel].Slug != "" {
			m.focus = pSessions
			return m.selectSlug(m.tabs[m.tsel].Slug)
		}
	case "E":
		port, errs := 0, 0
		if m.tsel < len(m.tabs) {
			port, errs = m.tabs[m.tsel].Port, m.tabs[m.tsel].Errors
		}
		for _, t := range m.tabs { // first tab with errors when the selected has none
			if port == 0 || (t.Errors > 0 && errs == 0) {
				port = t.Port
			}
		}
		return m.act("browser.send_errors", map[string]any{"port": port}, "sent console errors")
	}
	return nil
}

func (m *model) keyPicks(key string) tea.Cmd {
	if r := m.selectedError(); r != nil {
		switch key {
		case "enter":
			return tea.Batch(m.selectSlug(r.Slug), m.openErrorDetail(*r))
		case "s":
			return m.sendError(*r)
		case "i", "d", "x":
			return m.ignoreError(*r)
		}
	}
	pk := m.selectedPick()
	switch key {
	case "j", "down":
		m.move(&m.psel, m.thirdCount(), 1)
	case "k", "up":
		m.move(&m.psel, m.thirdCount(), -1)
	case "enter":
		if pk != nil && !pk.Sent {
			m.focus = pAgent
			m.ensurePickInput()
		}
	case "s":
		if pk != nil {
			m.modal = newSessionPicker(m, "Send pick to", func(slug string) tea.Cmd {
				return m.act("pick.send", map[string]any{"id": pk.ID, "slug": slug}, "sent pick to "+slug)
			})
		}
	case "n":
		if pk != nil {
			return m.openNew(func(nm *newModal) { nm.pickID = pk.ID })
		}
	case "d", "x":
		if pk != nil {
			return tea.Batch(m.act("pick.discard", map[string]any{"id": pk.ID}, "discarded"), m.loadPicks())
		}
	}
	return nil
}

func (m *model) keyAgent(key string) tea.Cmd {
	if m.issueShown() {
		return m.keyIssueMain(key)
	}
	s := m.current()
	switch key {
	case "i", "enter", "a":
		if s == nil {
			return nil
		}
		if m.screen != nil && m.screen.Dead && m.window == engine.WinAgent {
			aw, ah := m.agentSize()
			return m.act("session.restart", map[string]any{"slug": s.Slug, "width": aw, "height": ah}, "restarted "+s.Slug)
		}
		if m.runDone() && key != "i" {
			return m.finishRun()
		}
		m.typing = true
		delete(m.unreadAt, s.Slug)
		return nil
	case "h", "left", "esc":
		m.focus = pSessions
		return nil
	case "x":
		if m.runDone() { // the panel's "x close": the run, not the session
			return m.closeRun(false)
		}
	case "r", "n":
		cmd, _ := m.common(key)
		return cmd
	case "v": // cycle agent → preview → dev → agent
		if s == nil {
			return nil
		}
		if m.window == engine.WinDev || m.window == engine.WinRun {
			m.window = engine.WinAgent
			m.screen = nil
			return m.fetchScreen()
		}
		return m.openPreview()
	}
	return m.sessionAction(key, s)
}

func (m *model) forward(k tea.KeyMsg) tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	f := toTmux(k)
	slug, win := s.Slug, m.window
	return m.inOrder(func() error { // keys reach the pty in the order typed (order.go)
		switch {
		case f.paste != "":
			return m.cl.Call("session.send", map[string]any{"slug": slug, "window": win, "text": f.paste}, nil)
		case f.literal != "":
			return m.cl.Call("session.keys", map[string]any{"slug": slug, "window": win, "literal": f.literal}, nil)
		case len(f.keys) > 0:
			return m.cl.Call("session.keys", map[string]any{"slug": slug, "window": win, "keys": f.keys}, nil)
		}
		return nil
	})
}

func (m *model) onMouse(ev tea.MouseMsg) tea.Cmd {
	if m.modal != nil {
		return nil
	}
	if m.issueShown() {
		if cmd, ok := m.issueMouse(ev); ok {
			return cmd
		}
	}
	// map to the agent panel's content area
	x0, y0 := m.agentOrigin()
	aw, ah := m.agentSize()
	x, y := ev.X-x0, ev.Y-y0
	inAgent := x >= 0 && y >= 0 && x < aw && y < ah
	if inAgent && m.chat.open {
		return m.chatMouse(ev, x, y)
	}
	if inAgent && m.window == winPR {
		switch {
		case ev.Button == tea.MouseButtonWheelDown:
			m.move(&m.pr.sel, len(prItems(m.pr.view)), 1)
		case ev.Button == tea.MouseButtonWheelUp:
			m.move(&m.pr.sel, len(prItems(m.pr.view)), -1)
		case ev.Button == tea.MouseButtonLeft:
			if ev.Action == tea.MouseActionPress {
				m.focus = pAgent
			}
			return m.linkClick(ev, m.prLinkAt(x, y))
		}
		return nil
	}
	if inAgent && m.window == winPreview {
		if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft {
			m.focus = pAgent
		}
		return nil // nothing to forward: no pty behind the preview
	}
	// a click on a link opens it, like ⌘-click in a bare terminal (mouse
	// reports carry no ⌘, so any left click on a link counts); the click is
	// not forwarded to the agent
	if inAgent && ev.Button == tea.MouseButtonLeft {
		switch ev.Action {
		case tea.MouseActionPress:
			m.linkDown = m.agentLinkAt(x, y)
			if m.linkDown != "" {
				m.focus = pAgent
				return nil
			}
		case tea.MouseActionRelease:
			if u := m.linkDown; u != "" {
				m.linkDown = ""
				if m.agentLinkAt(x, y) != u {
					return nil
				}
				return func() tea.Msg {
					if err := openURL(u); err != nil {
						return errMsg{err}
					}
					return infoMsg("opened " + u)
				}
			}
		}
	}
	if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft && !inAgent {
		if sel, ok := m.sessionRowAt(ev.X, ev.Y); ok { // a click on a row of [1] selects it
			m.focus = pSessions
			m.typing = false
			if sel != tapiSel { // a session's row shows that session
				m.chat.open = false
			}
			m.sel = sel
			return m.onSelChange()
		}
	}
	if ev.Action == tea.MouseActionPress && ev.Button == tea.MouseButtonLeft {
		if inAgent {
			m.focus = pAgent
		} else if !m.zoomed() && (ev.X < x0 || ev.Y < y0) {
			m.focus = pSessions
			m.typing = false
		}
	}
	if !inAgent || !m.typing || m.screen == nil || !m.screen.MouseAny {
		if inAgent && m.screen != nil && !m.screen.MouseAny && (ev.Button == tea.MouseButtonWheelUp || ev.Button == tea.MouseButtonWheelDown) && m.typing {
			k := "Up"
			if ev.Button == tea.MouseButtonWheelDown {
				k = "Down"
			}
			s := m.current()
			if s != nil && m.screen.AltScreen {
				slug, win := s.Slug, m.window
				return m.inOrder(func() error {
					_ = m.cl.Call("session.keys", map[string]any{"slug": slug, "window": win, "keys": []string{k}}, nil)
					return nil
				})
			}
		}
		return nil
	}
	s := m.current()
	hex := sgrMouse(ev, x, y)
	if s == nil || hex == nil {
		return nil
	}
	slug, win := s.Slug, m.window
	return m.inOrder(func() error {
		_ = m.cl.Call("session.keys", map[string]any{"slug": slug, "window": win, "hex": hex}, nil)
		return nil
	})
}

// ---- attention queue / review queue ----

func (m *model) nextWaiting() tea.Cmd {
	// open questions in the inbox first: a person is asked for something
	if it := m.nextInbox(); it != nil && m.modal == nil {
		return m.openInbox(*it)
	}
	from := ""
	if s := m.current(); s != nil {
		from = s.Slug
	}
	onTapi := m.onTapiRow()
	// tapi waiting for the user (permission or a question) is in the queue
	// too, as the oldest waiter it is; pekoed's queue knows only sessions
	var tapiSince *time.Time
	if m.tapiWaits() && !onTapi {
		t := m.chat.info.StateSince
		tapiSince = &t
	}
	back := from
	if onTapi {
		back = daemon.TapiSlug
	}
	return func() tea.Msg {
		var s *state.Session
		if err := m.cl.Call("attention.next", map[string]any{"from": from}, &s); err != nil {
			return errMsg{err}
		}
		if tapiSince != nil && (s == nil || s.State != state.Waiting || !s.StateSince.Before(*tapiSince)) {
			return jumpMsg{slug: daemon.TapiSlug, from: back}
		}
		if s == nil {
			return noWaitMsg{}
		}
		pr := s.State != state.Waiting && s.PRStatus != nil && len(s.PRStatus.Attention) > 0
		return jumpMsg{slug: s.Slug, from: back, pr: pr}
	}
}

// noWaitMsg: attention.next found no session (or tapi) waiting.
type noWaitMsg struct{}

type jumpMsg struct {
	slug, from string
	pr         bool // queued for its PR (CI failed, changes requested, …)
}

func (m *model) backFromWaiting() tea.Cmd {
	if len(m.returnTo) == 0 {
		return nil
	}
	slug := m.returnTo[len(m.returnTo)-1]
	m.returnTo = m.returnTo[:len(m.returnTo)-1]
	m.typing = false
	if slug == daemon.TapiSlug {
		return m.selectTapi()
	}
	return m.selectSlug(slug)
}

func (m *model) nextReview() tea.Cmd {
	from := ""
	if s := m.current(); s != nil {
		from = s.Slug
	}
	return func() tea.Msg {
		var s *state.Session
		if err := m.cl.Call("review.next", map[string]any{"from": from}, &s); err != nil {
			return errMsg{err}
		}
		if s == nil {
			return infoMsg("nothing to review")
		}
		return jumpMsg{slug: s.Slug}
	}
}

type reviewOpenMsg struct{ slug string }

// openReview: nvim as review surface when configured and available
// (right pane swaps to nvim; claude keeps running), else the built-in one.
func (m *model) openReview(slug string) tea.Cmd {
	surface := m.g.Review.Surface
	if _, err := exec.LookPath("nvim"); err != nil {
		surface = "tui"
	}
	if surface == "nvim" {
		aw, ah := m.agentSize()
		return func() tea.Msg {
			if err := m.cl.Call("review.surface", map[string]any{"slug": slug, "width": aw, "height": ah}, nil); err != nil {
				return errMsg{err}
			}
			return reviewSurfaceMsg{slug: slug}
		}
	}
	return func() tea.Msg {
		_ = m.cl.Call("review.start", map[string]any{"slug": slug}, nil)
		return reviewOpenMsg{slug: slug}
	}
}

type reviewSurfaceMsg struct{ slug string }
