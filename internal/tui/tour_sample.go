package tui

// The tutorial's steps and the sample they are shown on (tour.go). The
// steps tell two stories, tapi first: asking tapi (the tapi row, tapi chat,
// the dashboard, the inbox, the pipeline up to a merge the person does),
// then driving sessions yourself (Sessions, the agent, a new session,
// Changes, the PR view and land, the keys). The sample lives in the model
// only while the tour runs: a made-up repository with six sessions in
// different states (three of them tapi's, under requests in the ledger),
// their changes, a PR, issues, an agent's screen, a tapi chat and what the
// dashboard reads (the inbox, merge checks, the ledger, the send log, fyi).
// Every claim in a step's text is about a key or a view that exists; check
// them against the code when keys move.
//
// The text comes in English and Japanese (ja / en, tourLang picks one), the
// second exception to English-only UI text after the tips
// (docs/ui-guidelines.md): long explanations everyone reads on the first
// run. Both say the same thing; change them together. tapi tells the tour,
// in its own voice (docs/mascot.md「tapi の人物像」): ぼく and きみ, casual
// talk (〜だよ, 〜してね), no faces or emoji in the text; the English is as
// relaxed. Keys, commands and the names shown on screen (Sessions, New
// session …) stay as they are in both; in Japanese, put spaces around them,
// as in tips.toml.

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
	tourRun   = "fix-login-redirect" // running, the one selected
	tourWait  = "feat-dark-mode"     // waiting for permission
	tourDone  = "feat-search-api"    // done, with a PR
	tourIdle  = "chore-deps"         // idle
	tourCSV   = "feat-csv-export"    // tapi's, running, asks in the inbox
	tourReady = "fix-cart-total"     // tapi's, landed as a PR, ready to merge
)

var tourRepo = daemon.KnownRepo{ID: "github.com_acme_webapp", Name: "webapp", Dir: "~/src/webapp", Sessions: 6, Cwd: true}

var tourSteps = []tourStep{
	{
		ja: tourCopy{
			title: "pekoe へようこそ",
			body:  "やあ、ぼくは tapi。pekoe はコーディングエージェントを並べて走らせる道具で、セッションごとに専用の git worktree ができるよ。ツアーは 2 本立て。前半はぼくに頼んで任せるやり方、後半はきみが自分でセッションを動かすやり方。中身はサンプルで、押したキーは本物のセッションに届かないから、気楽に触ってみて。",
		},
		en: tourCopy{
			title: "Welcome to pekoe",
			body:  "Hi, I'm tapi. pekoe runs coding agents side by side, each session in its own git worktree. The tour has two parts: first, asking me and letting me hand the work out; then, driving sessions yourself. It's all sample data, and no key you press reaches a real session, so go ahead and poke around.",
		},
	},
	// part 1: ask tapi
	{
		ja: tourCopy{
			title: "[1] tapi の行",
			body:  "Sessions のいちばん上にいるのがぼく。ぼくは統括役で、頼まれた変更は自分では書かずに、セッションを作って任せて報告を待つ。きみに回すのは判断と承認だけ。この行を選んでいる間、[4] はダッシュボードになるよ。",
		},
		en: tourCopy{
			title: "[1] The tapi row",
			body:  "That's me at the top of Sessions. I'm the orchestrator: I don't write the changes you ask for myself. I start sessions, hand the work out and wait for their reports, and only decisions and approvals come back to you. While this row is selected, [4] shows the dashboard.",
		},
		setup: func(m *model) {
			m.focus = pSessions
			m.sel = tapiSel
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pSessions), true
		},
	},
	{
		ja: tourCopy{
			title: "tapi chat",
			body:  "頼みごとは {t}(どのパネルからでも)か、tapi の行で {↵}。[4] がぼくとのチャットになる。「注文ページに CSV エクスポートを足して」みたいに言ってくれたら、完了条件を決めてセッションに任せるよ。頼んだことは台帳に残るから、{T} で会話をやり直しても続きから始められる。",
		},
		en: tourCopy{
			title: "tapi chat",
			body:  "To ask me something, press {t} from any panel, or {↵} on the tapi row: [4] becomes a chat with me. Say something like \"add a CSV export to the orders page\", and I'll decide what done means and hand it to a session. What you ask goes into the ledger, so even after {T} starts the chat over, I pick up where we left off.",
		},
		setup: func(m *model) {
			m.sel = tapiSel
			m.focus = pAgent
			m.chat = chatState{open: true, info: tourTapi(), screen: m.tourChatScreen()}
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	{
		ja: tourCopy{
			title: "ダッシュボード",
			body:  "任せた仕事の様子はここで見る。上から概要、受信箱、工程表、依頼(台帳)、送ったものの記録、エージェントが自分で決めたこと(Decided)。{4} で入って {j}/{k} で選び、{↵} で開く。{o} でリンク、{h} か {esc} で Sessions に戻るよ。",
		},
		en: tourCopy{
			title: "Dashboard",
			body:  "This is where you watch the work you've handed out. Top to bottom: an overview, the inbox, the pipeline, the requests (the ledger), what was sent, and what the agents decided themselves (Decided). Get in with {4}, pick a row with {j}/{k} and unfold it with {↵}; {o} opens a link, {h} or {esc} goes back to Sessions.",
		},
		setup: func(m *model) {
			m.sel = tapiSel
			m.focus = pAgent
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	{
		ja: tourCopy{
			title: "受信箱",
			body:  "セッションが迷ったら、ここできみに聞いてくる。pekoe が自分で読んだ事実(ブランチ・差分・PR・CI)が先で、エージェントの言葉が後。推奨はエージェントの意見で、最初から選ばれてはいないよ。数字で選んで {↵} で答えると、そのセッションに届く。{]a} でも次の問いに回れる。答えを待たせていると最下行に残って、ターミナルから離れていれば 2 分で OS の通知が来るよ。",
		},
		en: tourCopy{
			title: "Inbox",
			body:  "When a session is stuck on a choice, it asks you here. What pekoe read itself (branch, diff, PR, CI) comes first, the agent's words after; its recommendation is only its opinion and is never picked for you. Choose with a number and answer with {↵}: the session gets it. {]a} also takes you to the next question, and one left waiting stays on the bottom line, and turns into an OS notification after 2 minutes if you are away from the terminal.",
		},
		setup: func(m *model) {
			m.sel = tapiSel
			m.focus = pAgent
			if len(m.inbox) > 0 {
				m.modal = &inboxModal{m: m, it: m.inbox[0], sel: -1, note: &input{}}
			}
		},
		target: func(m *model) (rect, bool) { return m.modalRect() },
	},
	{
		ja: tourCopy{
			title: "工程表とマージ",
			body:  "工程表は、セッションごとの impl › review › PR › CI › merge(作業の順: 実装 → 独立レビュー → PR → CI → マージ)のバーと、今の段階のひとこと。足りないものは needs … とその理由に出る。fix-cart-total は全部そろって ready to merge になってるね。そろうと pekoed が受信箱に merge ready を 1 件出すけど、マージするのはきみ。GitHub でマージしてから、その項目に答えてね。",
		},
		en: tourCopy{
			title: "Pipeline and merging",
			body:  "The pipeline shows a bar of impl › review › PR › CI › merge (the order the work goes in: implement, independent review, PR, CI, merge) for each session and a word for where it is; what is still missing shows as needs …, with the reason. fix-cart-total has it all and says ready to merge. Then pekoed puts a merge ready item in the inbox, but the merge is yours: merge it on GitHub, then answer the item.",
		},
		setup: func(m *model) {
			m.sel = tapiSel
			m.focus = pAgent
			m.dash.sel = "s:" + tourReady
		},
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	// part 2: drive sessions yourself
	{
		ja: tourCopy{
			title: "[1] Sessions",
			body:  "ここからは、きみが自分でセッションを動かすやり方。セッションはリポジトリごとに並んで、{j}/{k} で選ぶと [4] もそれに切り替わる。{@running} run は作業中、{@waiting} wait は入力待ち、{@done} done はレビュー待ち、{@idle} idle は待機中。右は経過時間・diff・テスト結果で、待っているセッションには {]a} で飛べるよ。",
		},
		en: tourCopy{
			title: "[1] Sessions",
			body:  "Now for driving sessions yourself. Sessions are listed by repository; pick one with {j}/{k} and [4] follows. {@running} run is working, {@waiting} wait needs you, {@done} done is ready for review, {@idle} idle is on standby. On the right: time in that state, diff size and tests. {]a} jumps to a session that's waiting.",
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
			body:  "選んだエージェントとのやりとりが、そのまま見える。{↵} か {i} で打ち始めると、{^q} で抜けるまでキーは全部エージェントに届く。{v} で preview や dev サーバーの画面に切り替わるよ。",
		},
		en: tourCopy{
			title: "[4] Agent",
			body:  "Your conversation with the selected agent, as is. Start typing with {↵} or {i}: every key goes to the agent until you leave with {^q}. {v} switches to the preview and dev server screens.",
		},
		setup: func(m *model) { m.focus = pAgent },
		target: func(m *model) (rect, bool) {
			return m.panelRect(pAgent), true
		},
	},
	{
		ja: tourCopy{
			title: "New session",
			body:  "{n} で新しいセッション。ブランチ名、元にする issue(gh#123 など。なくてもいい)、最初のプロンプトを書けば、worktree を作ってエージェントを起こすところまで pekoe がやるよ。",
		},
		en: tourCopy{
			title: "New session",
			body:  "{n} starts a new session. Write a branch name, an issue to start from (like gh#123; optional) and a first prompt, and pekoe creates the worktree and starts the agent.",
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
			body:   "選んだセッションが変えたファイル。{@overlap} は、ほかのセッションも同じファイルを触っているしるし。{r} か {↵} でレビューを開くと、hunk ごとに承認したりコメントを付けたりして、エージェントに差し戻せる。{[ ]} で Picks(Chrome で選んだ要素)に切り替わるよ。",
			narrow: "選んだセッションが変えたファイル。{@overlap} は、ほかのセッションも同じファイルを触っているしるし。{r} か {↵} でレビューを開くと、hunk ごとに承認したりコメントを付けたりして、エージェントに差し戻せるよ。",
		},
		en: tourCopy{
			title:  "[3] Changes",
			body:   "The files the selected session has changed. {@overlap} means another session is touching the same file. {r} or {↵} opens the review, where you approve hunks or comment and send it back to the agent. {[ ]} switches to Picks (elements picked in Chrome).",
			narrow: "The files the selected session has changed. {@overlap} means another session is touching the same file. {r} or {↵} opens the review, where you approve hunks or comment and send it back to the agent.",
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
			title: "PR view · land",
			body:  "{p} で [4] にこのセッションの GitHub PR。checks・レビュー・未解決のスレッドが並ぶから、直してほしいものを {space} で選んで {s} でエージェントに渡そう。done になったセッションは Sessions で {l} を押して land。rebase・テスト・push・PR 作成までやって、マージされたら worktree を片付けるよ。",
		},
		en: tourCopy{
			title: "PR view · land",
			body:  "{p} shows this session's GitHub PR in [4]: checks, reviews and unresolved threads. Pick what needs fixing with {space} and pass it to the agent with {s}. Once a session is done, {l} in Sessions lands it: rebase, tests, push and a PR in one go, and the worktree is cleaned up after the merge.",
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
			title:  "キーとコマンド",
			body:   "キーを忘れたら {?}。今のパネルで使えるキーが並んで、選んで {↵} でそのまま実行できる。{:} はコマンド名で実行(new、send、land、review、tutorial など)。[2] は {[ ]} で Worktrees・Browser・Issues を切り替えられて、Issues からは {n} でセッションを始められるよ。",
			narrow: "キーを忘れたら {?}。今のパネルで使えるキーが並んで、選んで {↵} でそのまま実行できる。{:} はコマンド名で実行(new、send、land、review、tutorial など)。上のタブは {[ ]} で切り替えられて、Issues からは {n} でセッションを始められるよ。",
		},
		en: tourCopy{
			title:  "Keys and commands",
			body:   "Forgot a key? Press {?}: it lists every key for the current panel, and {↵} runs the one you pick. {:} runs a command by name (new, send, land, review, tutorial, and more). In [2], {[ ]} switches between Worktrees, Browser and Issues, and {n} starts a session from an issue.",
			narrow: "Forgot a key? Press {?}: it lists every key for the current panel, and {↵} runs the one you pick. {:} runs a command by name (new, send, land, review, tutorial, and more). {[ ]} cycles the tabs at the top, and {n} starts a session from an issue.",
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
			body:  "おつかれさま。{" + tourKey + "} か {:tutorial} で、いつでももう一回案内するよ。閉じたら、きみのセッションに戻るね。困ったら {t} で声をかけて。",
		},
		en: tourCopy{
			title: "That's the tour",
			body:  "Nice work. {" + tourKey + "} or {:tutorial} brings me back to show you around any time. Close the tour to go back to your sessions, and if you get stuck, press {t} and ask me.",
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
	m.dash.sel, m.dash.top, m.dash.open = "", 0, nil
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
			StateSince: ago(since), CreatedAt: ago(created), Parent: "human",
		}
	}
	// tapi's: made for a request in the ledger
	byTapi := func(s *state.Session, req int64) *state.Session {
		s.Parent, s.Request = "+tapi", req
		return s
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
	done := byTapi(mk(tourDone, "feat/search-api", state.Done, 12*time.Minute, 90*time.Minute), 6)
	done.Diffstat = state.Stat{Added: 310, Deleted: 42, Files: 9}
	done.Test = state.Test{Status: "pass", Summary: "Tests  38 passed (38)"}
	done.PR = 128
	done.PRStatus = &state.PRStatus{State: "OPEN", Pass: 2, Fail: 1, ReviewDecision: "CHANGES_REQUESTED", Unresolved: 1}
	done.Port = 3003
	done.Report = &state.ReportSummary{ID: 24, Status: "done", Summary: "Search API in, PR #128; paginating next", At: ago(14 * time.Minute)}
	csv := byTapi(mk(tourCSV, "feat/csv-export", state.Running, 2*time.Minute, 6*time.Minute), 7)
	csv.Diffstat = state.Stat{Added: 64, Deleted: 2, Files: 3}
	csv.Port = 3004
	csv.LastTool = "Bash pekoe ask-human"
	ready := byTapi(mk(tourReady, "fix/cart-total", state.Done, 20*time.Minute, 2*time.Hour), 5)
	ready.Diffstat = state.Stat{Added: 23, Deleted: 6, Files: 2}
	ready.Test = state.Test{Status: "pass", Summary: "Tests  12 passed (12)"}
	ready.PR, ready.Landing = 131, true
	ready.PRStatus = &state.PRStatus{State: "OPEN", Pass: 3, MergeState: "CLEAN"}
	ready.Report = &state.ReportSummary{ID: 31, Status: "review", Summary: "Independent review: pass", At: ago(22 * time.Minute)}
	ready.Review = &state.ReviewSummary{ID: 31, Verdict: "pass", ReviewedSHA: "3f9c2e1", At: ago(22 * time.Minute)}
	idle := mk(tourIdle, "chore/deps", state.Idle, 2*time.Hour, 3*time.Hour)

	m.sessions = []*state.Session{idle, done, run, wait, csv, ready}
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
	m.inbox = tourInbox(now)
	m.dash = tourDash(now)
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

// tourInbox is the open inbox: a session asking for a decision, and
// pekoed's own item saying a PR is ready to merge.
func tourInbox(now time.Time) []state.InboxItem {
	rec := 0
	return []state.InboxItem{
		{
			ID: 12, Rev: 1, Kind: "decision", From: tourCSV, About: tourCSV, Status: state.InboxOpen, CreatedAt: now.Add(-2 * time.Minute),
			Question:  "Dates in the CSV: ISO 8601 in UTC, or the shop's local time?",
			Options:   []string{"ISO 8601, UTC", "Local time, zone in the header"},
			Recommend: &rec,
			Context:   "Accounting opens the file in a spreadsheet; the orders page shows local time.",
			Facts: &state.InboxFacts{Slug: tourCSV, State: state.Running, Branch: "feat/csv-export", Base: "main",
				Head: "a41c09e7d2", Diffstat: state.Stat{Added: 64, Deleted: 2, Files: 3}},
		},
		{
			ID: 13, Rev: 1, Kind: "approve", From: "pekoed", About: tourReady, Status: state.InboxOpen, CreatedAt: now.Add(-15 * time.Minute),
			Question: "#131 is ready to merge (fix-cart-total · HEAD 3f9c2e1)",
			Options:  []string{"merged", "wait", "send back"},
			Links:    []string{"https://github.com/acme/webapp/pull/131"},
			Facts: &state.InboxFacts{Slug: tourReady, State: state.Done, Branch: "fix/cart-total", Base: "main",
				Head: "3f9c2e1b88", Diffstat: state.Stat{Added: 23, Deleted: 6, Files: 2}, PR: 131,
				PRURL: "https://github.com/acme/webapp/pull/131"},
		},
	}
}

// tourDash is what the dashboard reads from pekoed: the open requests, the
// send log, what the agents decided, and the merge checks.
func tourDash(now time.Time) dashState {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	chose := 0
	ok := func(name, detail string) state.MergeCond {
		return state.MergeCond{Name: name, OK: true, Detail: detail}
	}
	no := func(name, detail string) state.MergeCond { return state.MergeCond{Name: name, Detail: detail} }
	return dashState{
		at: now,
		ledger: []state.LedgerEntry{
			{ID: 5, At: ago(2 * time.Hour), Text: "The cart total ignores the discount code", Status: "open", Sessions: []string{tourReady}},
			{ID: 6, At: ago(95 * time.Minute), Text: "Add a full-text search API for the search page", Status: "open", Sessions: []string{tourDone}},
			{ID: 7, At: ago(7 * time.Minute), Text: "Add a CSV export button to the orders page", Status: "open", Sessions: []string{tourCSV}},
		},
		sends: []state.SendRecord{
			{ID: 41, At: ago(6 * time.Minute), From: "+tapi", To: tourCSV, Kind: "new", OK: true,
				Text: "Add a CSV export button to the orders page. Done when the button downloads the filtered orders, with a test."},
			{ID: 38, At: ago(40 * time.Minute), From: "+tapi", To: tourDone, Kind: "send", OK: true,
				Text: "mika asked for pagination on PR #128: add a limit and a cursor."},
		},
		fyi: []state.InboxItem{
			{ID: 11, Kind: "fyi", From: tourDone, Status: state.InboxAnswered, CreatedAt: ago(30 * time.Minute),
				Question: "Paginate the search results with a cursor or an offset?", Options: []string{"cursor", "offset"},
				Answer: &state.InboxAnswer{Choice: &chose, By: tourDone, At: ago(30 * time.Minute)}},
		},
		merge: map[string]*state.MergeCheck{
			tourReady: {Slug: tourReady, PR: 131, PRHead: "3f9c2e1b88", Ready: true, Inbox: 13, Missing: []string{}, Conds: []state.MergeCond{
				ok("land", "pekoe land done (PR #131)"), ok("review", "pass in report #31 (3f9c2e1)"), ok("ci", "all 3 passed"),
				ok("merge_state", "CLEAN"), ok("conflicts", "no conflicts forecast"), ok("after", "nothing to wait for"),
			}},
			tourDone: {Slug: tourDone, PR: 128, Missing: []string{"review", "ci", "merge_state"}, Conds: []state.MergeCond{
				ok("land", "pekoe land done (PR #128)"), no("review", "no independent review reported"), no("ci", "1 failed"),
				no("merge_state", "merge_state is BLOCKED"), ok("conflicts", "no conflicts forecast"), ok("after", "nothing to wait for"),
			}},
		},
	}
}

func tourTapi() *daemon.Tapi {
	return &daemon.Tapi{Agent: "claude", Started: true, Alive: true, State: state.Idle, StateSince: time.Now().Add(-2 * time.Minute),
		LastMessage: "Handed request #7 to feat-csv-export"}
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
		th.FG(theme.Secondary, "> add a CSV export button to the orders page"), "",
		th.FG(theme.Ok, "⏺") + " Leave it to me, I'll get someone on it.", "",
		th.FG(theme.Ok, "⏺") + " " + th.Bold(theme.Text, "Bash") + "(pekoe new feat/csv-export --request 7 …)",
		"  " + th.FG(theme.Dim, "⎿") + "  started feat-csv-export", "",
		th.FG(theme.Ok, "⏺") + " Handed request #7 to feat-csv-export.",
		"  Done when the button downloads the filtered",
		"  orders, with a test. I'll tell you when it",
		"  needs you; the rest is on the dashboard.",
		"", th.FG(theme.Dim, strings.Repeat("─", 48)), "> ", th.FG(theme.Dim, strings.Repeat("─", 48)),
	}
	sc := &engine.Screen{Lines: lines, Width: 80, Height: len(lines)}
	sc.Cursor.Y = len(lines) - 2
	sc.Cursor.X = 2
	return sc
}
