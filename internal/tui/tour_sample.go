package tui

// The tutorial's steps and the sample they are shown on (tour.go). The
// sample lives in the model only while the tour runs: a made-up repository
// with five sessions in different states, their changes, a PR, issues, an
// agent's screen and a tapi chat. Every claim in a step's text is about a
// key or a view that exists; check them against the code when keys move.
//
// The text comes in English and Japanese (ja / en, tourLang picks one), the
// second exception to English-only UI text after the tips
// (docs/ui-guidelines.md): long explanations everyone reads on the first
// run. Both say the same thing; change them together. The Japanese is in
// the polite form (です・ます; the tips are plain form). Keys, commands and
// the names shown on screen (Sessions, New session …) stay as they are in
// both; in Japanese, put spaces around them, as in tips.toml.

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
		ja: tourCopy{
			title: "pekoe へようこそ",
			body:  "はじめまして、tapi です。pekoe は、コーディングエージェントを並列で動かすためのツールです。セッションごとに専用の git worktree が作られます。このツアーでは、サンプルのセッションを使って画面をご案内します。ツアー中に押したキーは実際のセッションには送られないので、気軽に触ってみてください。",
		},
		en: tourCopy{
			title: "Welcome to pekoe",
			body:  "Hi, I'm tapi. pekoe is a tool for running coding agents in parallel. Each session gets its own git worktree. In this tour, I'll show you around the screen with sample sessions. Keys you press during the tour never reach your real sessions, so feel free to try things out.",
		},
	},
	{
		ja: tourCopy{
			title: "[1] Sessions",
			body:  "ここには、エージェントのセッションがリポジトリごとにまとめて並びます。{j}/{k} でセッションを選ぶと、[4] の表示もそのセッションに切り替わります。",
		},
		en: tourCopy{
			title: "[1] Sessions",
			body:  "Your agent sessions are listed here, grouped by repository. Pick one with {j}/{k} and [4] switches to that session too.",
		},
		setup: func(m *model) { m.focus = pSessions },
		target: func(m *model) (rect, bool) {
			return m.panelRect(pSessions), true
		},
	},
	{
		ja: tourCopy{
			title: "状態の見かた",
			body:  "{@running} run は作業中、{@waiting} wait は入力待ち、{@done} done はレビュー待ち、{@idle} idle は待機中です。右側には、その状態になってからの経過時間、diff の量、テスト結果が並びます。入力待ちのセッションがあれば、{]a} ですぐに移動できますよ。",
		},
		en: tourCopy{
			title: "Reading states",
			body:  "{@running} run is working, {@waiting} wait needs your input, {@done} done is ready for review, {@idle} idle is on standby. To the right are the time spent in that state, the size of the diff, and the test results. When a session is waiting for you, {]a} takes you straight there.",
		},
		setup: func(m *model) { m.focus = pSessions },
		target: func(m *model) (rect, bool) {
			return m.panelRect(pSessions), true
		},
		cells: func(m *model) []rect {
			x, cw, y, _, sels, ok := m.sessionRowsAt()
			if !ok {
				return nil
			}
			nameW, _ := sessionCols(cw)
			var out []rect
			for i, sel := range sels {
				switch sel {
				case rowNone: // a heading or the empty hint
				case tapiSel: // the tapi row has its word right after the name
					out = append(out, rect{x, y + i, 1, 1}, rect{x + tapiWordX, y + i, stateW, 1})
				default:
					out = append(out, rect{x, y + i, 1, 1}, rect{x + nameW + 3, y + i, stateW, 1})
				}
			}
			return out
		},
	},
	{
		ja: tourCopy{
			title: "[4] Agent",
			body:  "選んだエージェントとのやりとりが、そのまま表示されます。{↵} か {i} で入力を始めると、{^q} で抜けるまで、キー入力はすべてエージェントに送られます。{v} を押すと、preview と dev サーバーの画面に切り替わります。",
		},
		en: tourCopy{
			title: "[4] Agent",
			body:  "Your conversation with the selected agent, shown as is. Start typing with {↵} or {i}: every key goes to the agent until you leave with {^q}. {v} switches to the preview and dev server screens.",
		},
		setup: func(m *model) { m.focus = pAgent },
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	{
		ja: tourCopy{
			title: "New session",
			body:  "{n} で新しいセッションを作れます。ブランチ名、元にする issue(gh#123 など。省略可)、最初のプロンプトを入力してください。worktree の作成とエージェントの起動は、pekoe がやっておきます。",
		},
		en: tourCopy{
			title: "New session",
			body:  "{n} creates a new session. Enter a branch name, an issue to start from (like gh#123; optional) and a first prompt. pekoe takes care of creating the worktree and starting the agent.",
		},
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
		ja: tourCopy{
			title:  "[3] Changes",
			body:   "選んだセッションが変更したファイルの一覧です。{@overlap} が付いたファイルは、ほかのセッションも変更しているので注意してください。{r} または {↵} でレビュー画面を開くと、hunk ごとに承認したりコメントを付けたりして、エージェントに差し戻せます。{[ ]} で Picks(Chrome で選んだ要素)に切り替わります。",
			narrow: "選んだセッションが変更したファイルの一覧です。{@overlap} が付いたファイルは、ほかのセッションも変更しているので注意してください。{r} または {↵} でレビュー画面を開くと、hunk ごとに承認したりコメントを付けたりして、エージェントに差し戻せます。",
		},
		en: tourCopy{
			title:  "[3] Changes",
			body:   "The files the selected session has changed. A file marked {@overlap} is being changed by another session too, so take care. {r} or {↵} opens the review, where you can approve hunks or add comments and send them back to the agent. {[ ]} switches to Picks (elements picked in Chrome).",
			narrow: "The files the selected session has changed. A file marked {@overlap} is being changed by another session too, so take care. {r} or {↵} opens the review, where you can approve hunks or add comments and send them back to the agent.",
		},
		setup: func(m *model) {
			m.focus = pThird
			m.narrowTab = 3
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pThird), true
		},
	},
	{
		ja: tourCopy{
			title: "PR view",
			body:  "{p} を押すと、[4] にこのセッションの GitHub PR が表示されます。checks、レビュー、未解決のスレッドが一覧になります。直してほしい項目を {space} で選んで {s} を押せば、エージェントに伝えられます。",
		},
		en: tourCopy{
			title: "PR view",
			body:  "{p} shows this session's GitHub PR in [4]: checks, reviews and unresolved threads. Select what needs fixing with {space}, then press {s} to pass it on to the agent.",
		},
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
		ja: tourCopy{
			title: "land",
			body:  "セッションが done になったら、{l} で land(取り込み)しましょう。rebase、テスト、push、PR の作成までをまとめて実行し、PR がマージされたら worktree を片付けます。実行前に、コンフリクトが起きそうな箇所をお知らせしてから確認しますね。",
		},
		en: tourCopy{
			title: "land",
			body:  "Once a session is done, land it with {l}. It runs rebase, tests, push and PR creation in one go, and cleans up the worktree once the PR is merged. Before it starts, it shows where conflicts look likely and asks you to confirm.",
		},
		setup: func(m *model) {
			m.tourSelect(tourDone)
			m.focus = pSessions
		},
		target: func(m *model) (rect, bool) {
			x, cw, y, rows, _, ok := m.sessionRowsAt()
			if !ok {
				return m.panelRect(pSessions), true
			}
			i := selRow(rows)
			return rect{x - 2, y + max(i, 0), cw + 4, 1}, true
		},
	},
	{
		ja: tourCopy{
			title:  "[2] Worktrees · Browser · Issues",
			body:   "{[ ]} で表示を切り替えられます。Issues には GitHub の issue が並び、{n} でその issue からセッションを始められます。Browser では、Chrome のタブをポート番号でセッションに紐づけます。",
			narrow: "{[ ]} で Sessions・Worktrees・Issues・Changes のタブを順に切り替えられます。Issues には GitHub の issue が並び、{n} でその issue からセッションを始められます。",
		},
		en: tourCopy{
			title:  "[2] Worktrees · Browser · Issues",
			body:   "{[ ]} switches between them. Issues lists GitHub issues; {n} starts a session from one. Browser ties Chrome tabs to sessions by port number.",
			narrow: "{[ ]} cycles through the Sessions, Worktrees, Issues and Changes tabs. Issues lists GitHub issues; {n} starts a session from one.",
		},
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
		ja: tourCopy{
			title: "tapi chat",
			body:  "どのパネルからでも、{t} を押すと [4] がわたしとのチャットになります。Sessions の一番上の tapi の行で {↵} を押しても開けます。調べものを頼んだり、セッションを作って作業を任せたりしてください。{T} で会話を最初からやり直せます。",
		},
		en: tourCopy{
			title: "tapi chat",
			body:  "From any panel, {t} turns [4] into a chat with me; so does {↵} on the tapi row at the top of Sessions. Ask me to look things up, or to create a session and hand off work. {T} starts the conversation over.",
		},
		setup: func(m *model) {
			m.focus = pAgent
			m.chat = chatState{open: true, info: tourTapi(), screen: m.tourChatScreen()}
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	{
		ja: tourCopy{
			title: "キーとコマンド",
			body:  "キーを忘れたら {?} を押してください。今いるパネルで使えるキーが一覧になり、選んで {↵} を押せばそのまま実行できます。{:} ではコマンド名を入力して実行できます(new、send、land、review、tutorial など)。",
		},
		en: tourCopy{
			title: "Keys and commands",
			body:  "Forgot a key? Press {?}. It lists every key for the current panel; pick one and press {↵} to run it. {:} runs a command by name (new, send, land, review, tutorial, and more).",
		},
		setup: func(m *model) {
			m.focus = pThird
			m.narrowTab = 3
			m.modal = newHelpModal(m)
		},
		target: func(m *model) (rect, bool) { return m.modalRect() },
	},
	{
		ja: tourCopy{
			title: "ツアーはここまで",
			body:  "おつかれさまでした。{" + tourKey + "} か {:tutorial} で、いつでももう一度ご案内します。ツアーを閉じると、自分のセッションに戻ります。",
		},
		en: tourCopy{
			title: "That's the tour",
			body:  "Nice work! I can show you around again any time with {" + tourKey + "} or {:tutorial}. Close the tour to return to your sessions.",
		},
	},
}

// tourBase is the state every step starts from: Sessions in focus, the
// running session selected, nothing open.
func (m *model) tourBase() {
	m.focus, m.sub2, m.sub3, m.narrowTab = pSessions, 0, 0, 0
	m.typing, m.prefix, m.prefixType, m.bracket = false, false, false, ""
	m.window = engine.WinAgent
	m.modal = nil
	m.chat = chatState{info: tourTapi()} // the tapi row shows the sample tapi
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
	return &daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Idle, StateSince: time.Now().Add(-2 * time.Minute),
		LastMessage: "Start a session for #198 (CSV export)?"}
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
