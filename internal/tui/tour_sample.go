package tui

// The tutorial's steps and the sample they are shown on (tour.go). The
// sample lives in the model only while the tour runs: a made-up repository
// with five sessions in different states, their changes, a PR, issues, an
// agent's screen and a tapi chat. Every claim in a step's text is about a
// key or a view that exists; check them against the code when keys move.
//
// The text is Japanese, the second exception to English UI text after the
// tips (docs/ui-guidelines.md): long explanations everyone reads on the
// first run, in the polite form (です・ます; the tips are plain form). Keys,
// commands and the names shown on screen (Sessions, New session …) stay as
// they are; put spaces around them, as in tips.toml.

import (
	"fmt"
	"strings"
	"time"

	"pekoe/internal/daemon"
	"pekoe/internal/engine"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// the sample sessions
const (
	tourRun    = "fix-login-redirect" // running, the one selected
	tourWait   = "feat-dark-mode"     // waiting for permission
	tourDone   = "feat-search-api"    // done, with a PR
	tourReview = "docs-readme"        // being reviewed
	tourIdle   = "chore-deps"         // idle
)

var tourRepo = daemon.KnownRepo{ID: "github.com_acme_webapp", Name: "webapp", Dir: "~/src/webapp", Sessions: 5, Cwd: true}

var tourSteps = []tourStep{
	{
		title: "pekoe へようこそ",
		body:  "pekoe はコーディングエージェントを並べて動かします。セッション 1 つに git worktree が 1 つ付きます。このツアーではサンプルのセッションで画面を案内します。ここで押したキーは本物のセッションには届きません。",
	},
	{
		title: "[1] Sessions",
		body:  "エージェントのセッションの一覧です。リポジトリごとにまとまります。{j}/{k} で選ぶと、[4] の画面がそのセッションに切り替わります。",
		setup: func(m *model) { m.focus = pSessions },
		target: func(m *model) (rect, bool) {
			return m.panelRect(pSessions), true
		},
	},
	{
		title: "状態の見かた",
		body:  "{@running} run は作業中、{@waiting} wait はあなた待ち、{@done} done はレビュー待ち、{@idle} idle は待機中です。続く列は、その状態になってからの時間・diff の大きさ・テストの結果です。{]a} で次の待ちセッションへ飛べます。",
		setup: func(m *model) { m.focus = pSessions },
		target: func(m *model) (rect, bool) {
			return m.panelRect(pSessions), true
		},
		cells: func(m *model) []rect {
			x, cw, y, rows, ok := m.sessionRowsAt()
			if !ok {
				return nil
			}
			nameW, _ := sessionCols(cw)
			var out []rect
			for i := range rows {
				out = append(out, rect{x, y + i, 1, 1}, rect{x + nameW + 3, y + i, stateW, 1})
			}
			return out
		},
	},
	{
		title: "[4] Agent",
		body:  "選んだエージェントの端末そのものです。{↵} か {i} で打ち込めます。{^q} で戻るまで、キーはすべてエージェントに届きます。{v} で preview と dev サーバーの画面に切り替わります。",
		setup: func(m *model) { m.focus = pAgent },
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	{
		title: "New session",
		body:  "{n} で開きます。branch・始める issue(gh#123、なくても構いません)・最初のプロンプトを入れると、pekoe が worktree を作ってエージェントを起動します。",
		setup: func(m *model) {
			nm := newNewModal(m, "feat/csv-export")
			p := "Add a CSV export button to the orders page"
			nm.inputs[2] = &input{text: []rune(p), pos: len([]rune(p))}
			nm.field = 2
			m.modal = nm
		},
		target: func(m *model) (rect, bool) { return m.modalRect() },
	},
	{
		title:  "[3] Changes",
		body:   "選んだセッションが変えたファイルです。{@overlap} はほかのセッションも触っているファイルです。{r}(ここでは {↵} でも)でレビューを開き、hunk の承認やコメントをしてエージェントに送り返せます。{[ ]} で Picks(Chrome で拾った要素)に切り替わります。",
		narrow: "選んだセッションが変えたファイルです。{@overlap} はほかのセッションも触っているファイルです。{r}(ここでは {↵} でも)でレビューを開き、hunk の承認やコメントをしてエージェントに送り返せます。",
		setup: func(m *model) {
			m.focus = pThird
			m.narrowTab = 3
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pThird), true
		},
	},
	{
		title: "PR view",
		body:  "{p} で [4] にセッションの GitHub PR を出します。checks・レビュー・未解決のスレッドが並びます。{space} で選んで {s} を押すと、直してほしい項目をエージェントに送れます。",
		setup: func(m *model) {
			m.tourSelect(tourDone)
			m.window = winPR
			m.focus = pAgent
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	{
		title: "land",
		body:  "done になったら {l} で land します。rebase・テスト・push・PR 作成までを流し、PR がマージされたら worktree を消します。実行の前に、衝突の予報を添えて確認します。",
		setup: func(m *model) {
			m.tourSelect(tourDone)
			m.focus = pSessions
		},
		target: func(m *model) (rect, bool) {
			x, cw, y, rows, ok := m.sessionRowsAt()
			if !ok {
				return m.panelRect(pSessions), true
			}
			i := selRow(rows)
			return rect{x - 2, y + max(i, 0), cw + 4, 1}, true
		},
	},
	{
		title:  "[2] Worktrees · Browser · Issues",
		body:   "{[ ]} で切り替わります。Issues は GitHub の issue の一覧で、{n} でその issue からセッションを始められます。Browser は Chrome のタブをポートでセッションに結びつけます。",
		narrow: "{[ ]} で Sessions・Worktrees・Issues・Changes のタブを巡ります。Issues は GitHub の issue の一覧で、{n} でその issue からセッションを始められます。",
		setup: func(m *model) {
			m.focus = pSecond
			m.sub2 = subIssues
			m.narrowTab = 2
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pSecond), true
		},
	},
	{
		title: "tapi chat",
		body:  "どのパネルからでも {t} で、[4] がわたし(tapi)との会話になります。調べものを頼んだり、セッションを作って仕事を任せたりできます。{T} で最初からやり直せます。",
		setup: func(m *model) {
			m.focus = pAgent
			m.chat = chatState{open: true, info: tourTapi(), screen: m.tourChatScreen()}
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	{
		title: "キーとコマンド",
		body:  "{?} で今のパネルのキーが全部並び、選んで {↵} でそのまま実行できます。{:} ではコマンドを名前で打てます。new・send・land・review・tutorial などです。",
		setup: func(m *model) {
			m.focus = pThird
			m.narrowTab = 3
			m.modal = newHelpModal(m)
		},
		target: func(m *model) (rect, bool) { return m.modalRect() },
	},
	{
		title: "ツアーはここまで",
		body:  "{" + tourKey + "} か {:tutorial} でもう一度見られます。閉じると自分のセッションに戻ります。",
	},
}

// tourBase is the state every step starts from: Sessions in focus, the
// running session selected, nothing open.
func (m *model) tourBase() {
	m.focus, m.sub2, m.sub3, m.narrowTab = pSessions, 0, 0, 0
	m.typing, m.prefix, m.prefixType, m.bracket = false, false, false, ""
	m.window = engine.WinAgent
	m.modal = nil
	m.chat = chatState{}
	m.iss.open, m.iss.queryOn = false, false
	m.filter, m.filterOn = "", false
	m.tourSelect(tourRun)
}

// tourSelect selects a sample session with its screen and changes.
func (m *model) tourSelect(slug string) {
	if i, _ := m.find(slug); i >= 0 {
		m.sel = i
	}
	m.lastSel = slug
	m.screen, m.screenOf = m.tourScreen(slug), slug
	m.changes, m.changesOf = tourChanges[slug], slug
	m.csel = 0
}

// loadSample replaces what the panels show with the sample. Everything the
// tour touches is a fresh value, so the real model (kept in tourState)
// shares nothing that the tour writes to.
func (m *model) loadSample() {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	r := tourRepo
	mk := func(slug, branch string, st state.State, since, created time.Duration) *state.Session {
		return &state.Session{
			Slug: slug, Branch: branch, State: st, Repo: r.Name, RepoID: r.ID, RepoDir: r.Dir,
			Path: "~/worktrees/webapp/" + slug, Agent: "claude", Managed: true, Base: "main",
			StateSince: ago(since), CreatedAt: ago(created),
		}
	}
	run := mk(tourRun, "fix/login-redirect", state.Running, 40*time.Second, 50*time.Minute)
	run.Diffstat = state.Stat{Added: 42, Deleted: 8, Files: 4}
	run.Test = state.Test{Status: "running"}
	run.Port, run.Tokens = 3001, 48200
	run.LastTool = "Bash pnpm test src/auth"
	run.Activity = []int{1, 3, 2, 5, 8, 6, 9, 7, 4, 6, 8, 9}
	run.Overlaps = []state.Overlap{{Slug: tourWait, Files: []string{"src/auth/LoginForm.tsx"}}}
	wait := mk(tourWait, "feat/dark-mode", state.Waiting, 3*time.Minute, 40*time.Minute)
	wait.WaitKind = "permission"
	wait.Diffstat = state.Stat{Added: 120, Deleted: 15, Files: 7}
	wait.Port = 3002
	wait.Overlaps = []state.Overlap{{Slug: tourRun, Files: []string{"src/auth/LoginForm.tsx"}}}
	done := mk(tourDone, "feat/search-api", state.Done, 12*time.Minute, 90*time.Minute)
	done.Diffstat = state.Stat{Added: 310, Deleted: 42, Files: 9}
	done.Test = state.Test{Status: "pass", Summary: "Tests  38 passed (38)"}
	done.PR = 128
	done.PRStatus = &state.PRStatus{State: "OPEN", Pass: 2, Fail: 1, ReviewDecision: "CHANGES_REQUESTED", Unresolved: 1}
	done.Port = 3003
	review := mk(tourReview, "docs/readme", state.Reviewing, 5*time.Minute, 30*time.Minute)
	review.Diffstat = state.Stat{Added: 18, Deleted: 3, Files: 1}
	review.Test = state.Test{Status: "pass"}
	idle := mk(tourIdle, "chore/deps", state.Idle, 2*time.Hour, 3*time.Hour)

	m.sessions = []*state.Session{idle, done, run, wait, review}
	m.sortSessions()
	m.repos, m.reposLoaded = []daemon.KnownRepo{r}, true
	m.sessionsIn, m.homePicked, m.chosen = true, true, nil
	m.repoKey, m.repoSynced = r.Dir, true
	m.worktrees = []daemon.WorktreeView{{Path: r.Dir, Branch: "main", Main: true}}
	for _, s := range m.sessions {
		m.worktrees = append(m.worktrees, daemon.WorktreeView{Path: s.Path, Branch: s.Branch, Slug: s.Slug, Port: s.Port, Managed: true, State: string(s.State)})
	}
	m.wsel = 0
	m.tabs, m.tsel = nil, 0
	m.picks, m.psel, m.errs, m.pickInput = nil, 0, nil, nil
	m.unreadAt = map[string]int{}
	m.returnTo = nil
	m.fetching, m.dirty = false, false
	m.layout.Zoom = false // every panel is in view
	m.pr = newPRState()
	m.pr.slug, m.pr.view, m.pr.at = tourDone, tourPR(now), now
	m.iss = issuesState{list: &daemon.IssueList{RepoID: r.ID, Filter: "mine", Issues: tourIssues(now)}, filter: "mine"}
	m.iss.ensure()
}

var tourChanges = map[string][]change{
	tourRun: {
		{Status: "M", Path: "src/auth/session.ts"},
		{Status: "M", Path: "src/auth/LoginForm.tsx"},
		{Status: "A", Path: "src/auth/safeNext.ts"},
		{Status: "A", Path: "src/auth/session.test.ts"},
	},
	tourDone: {
		{Status: "A", Path: "src/api/search.ts"},
		{Status: "A", Path: "src/api/search.test.ts"},
		{Status: "M", Path: "src/pages/Search.tsx"},
		{Status: "M", Path: "src/db/schema.sql"},
		{Status: "D", Path: "src/api/legacySearch.ts"},
	},
}

func tourPR(now time.Time) *daemon.PRView {
	at := now.Add(-40 * time.Minute)
	return &daemon.PRView{
		Number: 128, Title: "Add a full-text search API", State: "OPEN", Mergeable: "MERGEABLE",
		MergeState: "BLOCKED", ReviewDecision: "CHANGES_REQUESTED", Base: "main", Head: "feat/search-api",
		Body: "Adds /api/search backed by the Postgres full-text index, and wires the search page to it.",
		Checks: []daemon.PRCheck{
			{Name: "lint", Workflow: "CI", Status: "COMPLETED", Conclusion: "SUCCESS", Bucket: "pass", Seconds: 41},
			{Name: "unit", Workflow: "CI", Status: "COMPLETED", Conclusion: "SUCCESS", Bucket: "pass", Seconds: 96},
			{Name: "e2e", Workflow: "CI", Status: "COMPLETED", Conclusion: "FAILURE", Bucket: "fail", Seconds: 204},
		},
		Reviews: []daemon.PRReview{{Author: "mika", State: "CHANGES_REQUESTED", Body: "Please paginate the results.", At: at}},
		Threads: []daemon.PRThread{{Path: "src/api/search.ts", Line: 42, Comments: []daemon.PRComment{
			{Author: "mika", Body: "This loads every match at once; add a limit and a cursor.", At: at},
		}}},
		Comments: []daemon.PRComment{{Author: "ren", Body: "Works nicely on staging.", At: at.Add(10 * time.Minute)}},
	}
}

func tourIssues(now time.Time) []daemon.Issue {
	return []daemon.Issue{
		{Number: 214, Title: "Login redirects to / instead of the page you came from", Labels: []string{"bug"}, UpdatedAt: now.Add(-2 * time.Hour), Comments: 3,
			Session: &daemon.IssueSession{Slug: tourRun, Branch: "fix/login-redirect", State: state.Running}},
		{Number: 209, Title: "Dark mode", Labels: []string{"feature"}, UpdatedAt: now.Add(-26 * time.Hour), Comments: 5,
			Session: &daemon.IssueSession{Slug: tourWait, Branch: "feat/dark-mode", State: state.Waiting}},
		{Number: 198, Title: "Export orders as CSV", Labels: []string{"feature"}, UpdatedAt: now.Add(-3 * 24 * time.Hour), Comments: 1},
		{Number: 187, Title: "Flaky e2e test on checkout", Labels: []string{"ci"}, UpdatedAt: now.Add(-6 * 24 * time.Hour)},
	}
}

func tourTapi() *daemon.Tapi {
	return &daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Idle}
}

// tourScreen is a made-up agent screen for a sample session, drawn with the
// theme's roles (so it follows the color mode like everything else).
func (m *model) tourScreen(slug string) *engine.Screen {
	th := m.th
	dot := func(r theme.Role, s string) string { return th.FG(r, "⏺") + " " + s }
	sub := func(s string) string { return "  " + th.FG(theme.Dim, "⎿") + "  " + s }
	add := func(n int, s string) string { return th.FG(theme.Ok, fmt.Sprintf("      %3d + %s", n, s)) }
	box := func(prompt string) []string {
		rule := th.FG(theme.Dim, strings.Repeat("─", 48))
		return []string{"", rule, "> " + prompt, rule, "  " + th.FG(theme.Dim, "? for shortcuts")}
	}
	var lines []string
	switch slug {
	case tourDone:
		lines = []string{
			th.FG(theme.Secondary, "> Add a full-text search API for the search page"), "",
			dot(theme.Ok, "Done. /api/search uses the full-text index."),
			sub("Tests  38 passed (38)"), "",
			dot(theme.Text, "Summary"),
			"  - src/api/search.ts: the endpoint, ranked by ts_rank",
			"  - src/db/schema.sql: a GIN index on products.search",
			"  - removed src/api/legacySearch.ts",
		}
		lines = append(lines, box("")...)
	default:
		lines = []string{
			th.FG(theme.Secondary, "> Login always lands on /. Go back to where they came from."), "",
			dot(theme.Text, "I'll pass the page as ?next= and redirect there."), "",
			dot(theme.Ok, th.Bold(theme.Text, "Read")+"(src/auth/LoginForm.tsx)"),
			sub("Read 96 lines"), "",
			dot(theme.Ok, th.Bold(theme.Text, "Update")+"(src/auth/session.ts)"),
			sub("Updated src/auth/session.ts with 2 additions"),
			add(41, "const next = safeNext(req.query.next)"),
			add(42, `return redirect(next ?? "/")`), "",
			dot(theme.Warn, th.Bold(theme.Text, "Bash")+"(pnpm test src/auth)"),
			sub(th.FG(theme.Dim, "Running…")), "",
			th.FG(theme.Primary, "✶") + " Running tests… " + th.FG(theme.Dim, "(8s · esc to interrupt)"),
		}
		lines = append(lines, box("")...)
	}
	sc := &engine.Screen{Lines: lines, Width: 80, Height: len(lines)}
	sc.Cursor.Y = len(lines) - 3
	sc.Cursor.X = 2
	return sc
}

func (m *model) tourChatScreen() *engine.Screen {
	th := m.th
	lines := []string{
		th.FG(theme.Secondary, "> which sessions need me?"), "",
		th.FG(theme.Ok, "⏺") + " Two of them:",
		"  - feat/dark-mode wants to edit LoginForm.tsx,",
		"    which fix/login-redirect changes too.",
		"  - feat/search-api is done; e2e failed on its PR.", "",
		th.FG(theme.Ok, "⏺") + " Start a session for #198 (CSV export)?",
		"", th.FG(theme.Dim, strings.Repeat("─", 48)), "> ", th.FG(theme.Dim, strings.Repeat("─", 48)),
	}
	sc := &engine.Screen{Lines: lines, Width: 80, Height: len(lines)}
	sc.Cursor.Y = len(lines) - 2
	sc.Cursor.X = 2
	return sc
}
