package tui

// The dashboard (docs/orchestration.md「ダッシュボード」→「TUI」): what [4]
// shows while the tapi row is selected in Sessions, in place of a session's
// windows. It gathers what pekoed holds, top to bottom: an overview, the
// inbox by kind, the pipeline of every session (implement → PR → CI →
// review → merge, grouped by who asked and which request), the open
// requests of the ledger, the send log and what tapi decided (fyi). Nothing
// here spends model tokens: the sessions come from the live list, the rest
// from merge.check / ledger.list / send.log / inbox.list, read again every
// few seconds while the dashboard is on screen.
//
// No new keys (docs/orchestration.md U4): ↵ on the tapi row in [1] still
// opens tapi chat. In [4], j/k move over the rows that can be chosen, ↵
// answers an inbox item (the inbox modal) or unfolds a row, o opens an
// inbox item's link. Every agent's words are drawn as text only (inboxText
// drops escapes, control and invisible format characters), like the inbox
// modal.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// dashRefresh is how often the dashboard reads pekoed again while shown;
// dashSoon how soon after something changed (a ledger or inbox event).
const (
	dashRefresh = 10 * time.Second
	dashSoon    = 2 * time.Second
	dashSends   = 30 // send log records shown
	dashFYI     = 10 // fyi items shown
)

// dashState is the dashboard's data and cursor.
type dashState struct {
	ledger  []state.LedgerEntry
	sends   []state.SendRecord
	fyi     []state.InboxItem
	merge   map[string]*state.MergeCheck
	at      time.Time // the last read came back
	loading bool
	stale   bool // something changed since the last read

	sel  string          // key of the chosen row
	open map[string]bool // unfolded rows
	top  int             // first row drawn
}

type dashMsg struct {
	ledger []state.LedgerEntry
	sends  []state.SendRecord
	fyi    []state.InboxItem
	merge  []state.MergeCheck
}

// dashShown: [4] shows the dashboard (the tapi row is selected and tapi
// chat is not open).
func (m *model) dashShown() bool {
	return m.onTapiRow() && m.current() == nil && !m.chat.open && !m.issueShown()
}

// loadDash reads what the dashboard needs. A method an older pekoed lacks
// leaves its section empty.
func (m *model) loadDash() tea.Cmd {
	if m.dash.loading {
		return nil
	}
	m.dash.loading, m.dash.stale = true, false // a change while reading marks it again
	cl := m.cl
	return func() tea.Msg {
		var out dashMsg
		if cl == nil {
			return out
		}
		_ = cl.Call("ledger.list", map[string]any{"status": "open"}, &out.ledger)
		_ = cl.Call("send.log", map[string]any{"limit": dashSends}, &out.sends)
		var answered []state.InboxItem
		if cl.Call("inbox.list", map[string]any{"status": "answered", "limit": 100}, &answered) == nil {
			for _, it := range answered {
				if it.Kind == "fyi" && len(out.fyi) < dashFYI {
					out.fyi = append(out.fyi, it)
				}
			}
		}
		_ = cl.Call("merge.check", nil, &out.merge)
		return out
	}
}

func (m *model) onDash(msg dashMsg) {
	d := &m.dash
	d.loading, d.at = false, time.Now()
	d.ledger, d.sends, d.fyi = msg.ledger, msg.sends, msg.fyi
	d.merge = map[string]*state.MergeCheck{}
	for i := range msg.merge {
		d.merge[msg.merge[i].Slug] = &msg.merge[i]
	}
}

// dashTick reads again while the dashboard is on screen: soon after a
// change, otherwise every dashRefresh.
func (m *model) dashTick() tea.Cmd {
	if !m.dashShown() || m.dash.loading {
		return nil
	}
	since := time.Since(m.dash.at)
	if m.dash.at.IsZero() || since >= dashRefresh || (m.dash.stale && since >= dashSoon) {
		return m.loadDash()
	}
	return nil
}

// ---- rows ----

// dashItem is a row that can be chosen.
type dashItem struct {
	key   string // "inbox:12", "s:feat-x", "req:3", "send:40", "fyi:15"
	inbox *state.InboxItem
	slug  string
	req   *state.LedgerEntry
	send  *state.SendRecord
	fyi   *state.InboxItem
}

// dashKinds is the inbox's kinds in the order shown: what only the person
// can do first, the merges last.
var dashKinds = []struct{ key, label string }{
	{"login", "login"}, {"approve", "approve"}, {"decision", "decide"}, {"merge", "merge ready"},
}

// inboxKind is the dashboard's kind of an inbox item: pekoed's own approve
// items are the merge evidence.
func inboxKind(it *state.InboxItem) string {
	if it.Kind == "approve" && it.From == "pekoed" {
		return "merge"
	}
	return it.Kind
}

// dashPipeline is the sessions in the pipeline: not someone else's PR,
// and not a repository's own checkout (main): that one has no branch of
// its own to land, so its stages would only ever say "implementing".
func (m *model) dashPipeline() []*state.Session {
	var out []*state.Session
	for _, s := range m.sessions {
		if !s.PRReview && !repoCheckout(s) {
			out = append(out, s)
		}
	}
	return out
}

// repoCheckout: the session runs in the repository's main worktree (the
// main session of a repository), not in a worktree of its own.
func repoCheckout(s *state.Session) bool {
	return s.RepoDir != "" && s.Path == s.RepoDir
}

// dashLanded: the session's work is merged (it only stays as a record).
func dashLanded(s *state.Session) bool {
	return s.State == state.Landed || (s.PRStatus != nil && s.PRStatus.State == "MERGED")
}

// dashGroup is the sessions one requester asked for under one request, or
// (merged) every session whose work is merged.
type dashGroup struct {
	parent   string
	request  int64
	merged   bool
	sessions []*state.Session
}

// dashGroups groups the pipeline by parent and request: tapi first, then
// the person, then the sessions that asked, then the rest; requests in
// order, none last. The merged sessions come last in a group of their own:
// what is still moving is what the eye should find first.
func (m *model) dashGroups() []dashGroup {
	idx := map[[2]string]int{}
	var gs []dashGroup
	var merged []*state.Session
	for _, s := range m.dashPipeline() {
		if dashLanded(s) {
			merged = append(merged, s)
			continue
		}
		k := [2]string{s.Parent, fmt.Sprint(s.Request)}
		i, ok := idx[k]
		if !ok {
			i = len(gs)
			idx[k] = i
			gs = append(gs, dashGroup{parent: s.Parent, request: s.Request})
		}
		gs[i].sessions = append(gs[i].sessions, s)
	}
	rank := func(p string) int {
		switch p {
		case "+tapi":
			return 0
		case "human":
			return 1
		case "", "unknown":
			return 3
		}
		return 2
	}
	sort.SliceStable(gs, func(i, j int) bool {
		a, b := gs[i], gs[j]
		if ra, rb := rank(a.parent), rank(b.parent); ra != rb {
			return ra < rb
		}
		if a.parent != b.parent {
			return a.parent < b.parent
		}
		if (a.request == 0) != (b.request == 0) {
			return b.request == 0
		}
		return a.request < b.request
	})
	if len(merged) > 0 {
		gs = append(gs, dashGroup{merged: true, sessions: merged})
	}
	return gs
}

func (m *model) ledgerEntry(id int64) *state.LedgerEntry {
	for i := range m.dash.ledger {
		if m.dash.ledger[i].ID == id {
			return &m.dash.ledger[i]
		}
	}
	return nil
}

// dashItems lists the rows that can be chosen, top to bottom (the order
// dashRows draws them in).
func (m *model) dashItems() []dashItem {
	var its []dashItem
	for _, k := range dashKinds {
		for i := range m.inbox {
			if it := &m.inbox[i]; it.Kind != "fyi" && inboxKind(it) == k.key {
				its = append(its, dashItem{key: fmt.Sprintf("inbox:%d", it.ID), inbox: it})
			}
		}
	}
	for _, g := range m.dashGroups() {
		for _, s := range g.sessions {
			its = append(its, dashItem{key: "s:" + s.Slug, slug: s.Slug})
		}
	}
	for i := range m.dash.ledger {
		e := &m.dash.ledger[i]
		its = append(its, dashItem{key: fmt.Sprintf("req:%d", e.ID), req: e})
	}
	for i := range m.dash.sends {
		r := &m.dash.sends[i]
		its = append(its, dashItem{key: fmt.Sprintf("send:%d", r.ID), send: r})
	}
	for i := range m.dash.fyi {
		it := &m.dash.fyi[i]
		its = append(its, dashItem{key: fmt.Sprintf("fyi:%d", it.ID), fyi: it})
	}
	return its
}

// dashSelIndex is the chosen row's index in its (the first when the
// chosen one is gone).
func (m *model) dashSelIndex(its []dashItem) int {
	for i, it := range its {
		if it.key == m.dash.sel {
			return i
		}
	}
	return 0
}

// ---- the pipeline of a session ----

// stage marks: done, failing, under way, not yet, ready to merge
const (
	stOK = iota
	stFail
	stRun
	stNone
	stReady
)

func (m *model) stageMark(st int) string {
	th := m.th
	switch st {
	case stOK:
		return th.FG(theme.Ok, fit(m.ic.Check, 1))
	case stFail:
		return th.FG(theme.Err, fit(m.ic.Fail, 1))
	case stRun:
		return th.FG(theme.Warn, fit(m.ic.Pending, 1))
	case stReady:
		return th.FG(theme.Secondary, fit(m.ic.Here, 1))
	}
	if m.ic.Name == "ascii" {
		return th.FG(theme.Dim, ".")
	}
	return th.FG(theme.Dim, "·")
}

// dashStages is a session's 実装 → PR → CI → レビュー → マージ, from its
// state, report, PR summary and pekoed's merge check.
func dashStages(s *state.Session, mc *state.MergeCheck) [5]int {
	ps := s.PRStatus
	landed := s.State == state.Landed || (ps != nil && ps.State == "MERGED")
	var st [5]int
	// implement
	switch {
	case landed || s.PR > 0:
		st[0] = stOK
	case s.State == state.Failed || (s.Report != nil && s.Report.Status == "blocked"):
		st[0] = stFail
	case s.Report != nil && (s.Report.Status == "done" || s.Report.Status == "review"):
		st[0] = stOK
	case s.State == state.Running || s.State == state.Creating || s.State == state.Waiting:
		st[0] = stRun
	default: // idle or done without a report: nothing says it is under way
		st[0] = stNone
	}
	// PR
	switch {
	case landed:
		st[1] = stOK
	case s.PR > 0 && ps != nil && ps.State == "CLOSED":
		st[1] = stFail
	case s.PR > 0:
		st[1] = stOK
	case s.Landing:
		st[1] = stRun
	default:
		st[1] = stNone
	}
	// CI
	switch {
	case landed:
		st[2] = stOK
	case s.PR == 0:
		st[2] = stNone
	case ps == nil:
		st[2] = stRun
	case ps.Fail > 0:
		st[2] = stFail
	case ps.Pending > 0:
		st[2] = stRun
	case ps.Pass > 0:
		st[2] = stOK
	default:
		st[2] = stNone
	}
	// review
	st[3] = stNone
	switch {
	case landed:
		st[3] = stOK
	case ps != nil && ps.ReviewDecision == "CHANGES_REQUESTED":
		st[3] = stFail
	case mc != nil && mergeCond(mc, "review"):
		st[3] = stOK
	}
	// merge
	switch {
	case landed:
		st[4] = stOK
	case mc != nil && mc.Ready:
		st[4] = stReady
	default:
		st[4] = stNone
	}
	return st
}

// dashMoved: what merge.check reads of a session changed (a new report,
// the PR summary, landing or its state), so the dashboard reads it again.
func dashMoved(old, cur *state.Session) bool {
	rid := func(s *state.Session) int64 {
		if s.Report == nil {
			return 0
		}
		return s.Report.ID
	}
	return rid(old) != rid(cur) || old.PR != cur.PR || old.Landing != cur.Landing || old.State != cur.State ||
		!old.PRStatus.Same(cur.PRStatus) || len(old.Overlaps) != len(cur.Overlaps)
}

func mergeCond(mc *state.MergeCheck, name string) bool {
	for _, c := range mc.Conds {
		if c.Name == name {
			return c.OK
		}
	}
	return false
}

var stageNames = [5]string{"impl", "PR", "CI", "review", "merge"}

// stageRole is the color of a stage's mark: done green, failing red,
// under way yellow, ready to merge secondary (the person's turn, like
// done and merged in Sessions), not yet dim.
func stageRole(st int) theme.Role {
	switch st {
	case stOK:
		return theme.Ok
	case stFail:
		return theme.Err
	case stRun:
		return theme.Warn
	case stReady:
		return theme.Secondary
	}
	return theme.Dim
}

// stageBar draws the five stages as a bar, two cells a stage when wide
// (one when not), so how far a session got reads at a glance: ▰ in the
// stage's color for done / under way / failing / ready, ▱ for not yet.
// ▰ ▱ are Neutral width (never East Asian Ambiguous), so fonts that draw
// ambiguous glyphs wide keep the row in line. Without color the bar could
// not tell done from failing, and the ascii icon set (or ascii glyphs)
// asks for ASCII: both get the marks instead ("✓✓✗··", "vvx..").
func (m *model) stageBar(st [5]int, wide bool) string {
	var b strings.Builder
	if m.th.Mode == theme.None || !m.dashGlyphs() {
		for _, x := range st {
			b.WriteString(m.stageMark(x))
		}
		return b.String()
	}
	cell := "▰"
	if wide {
		cell = "▰▰"
	}
	for _, x := range st {
		c := cell
		if x == stNone {
			c = strings.Repeat("▱", w(cell))
		}
		b.WriteString(m.th.FG(stageRole(x), c))
	}
	return b.String()
}

// condNames are the merge conditions (merge.check) in words a person
// reads; pekoed's names (merge_state …) stay in the unfolded detail.
var condNames = map[string]string{
	"land": "land", "review": "review", "ci": "CI", "merge_state": "mergeable", "conflicts": "conflicts", "after": "after",
}

func condName(n string) string {
	if s, ok := condNames[n]; ok {
		return s
	}
	return n
}

// stagePhrase says where a session is in words, in the color of what it
// means: merged, ready to merge, the first failing stage, the first stage
// under way, or what it waits for. The emoji goes before it when
// dashEmoji.
func (m *model) stagePhrase(s *state.Session, st [5]int, mc *state.MergeCheck) string {
	p, _ := m.stagePhraseCond(s, st, mc)
	return p
}

// stagePhraseCond is stagePhrase and the merge condition it names
// ("needs review" → "review"), "" when it names none.
func (m *model) stagePhraseCond(s *state.Session, st [5]int, mc *state.MergeCheck) (string, string) {
	say := func(emoji string, r theme.Role, text string) (string, string) {
		return m.dashEmo(emoji) + m.th.FG(r, text), ""
	}
	if dashLanded(s) {
		return say("🎉", theme.Dim, "merged")
	}
	if st[4] == stReady {
		return say("🚀", theme.Secondary, "ready to merge")
	}
	if s.State == state.Waiting {
		return say("🙋", theme.Warn, "waits for you")
	}
	fail := [5]string{"blocked", "PR closed", "CI failed", "changes requested", ""}
	run := [5]string{"working", "landing", "CI running", "", ""}
	for i, x := range st {
		if x == stFail && fail[i] != "" {
			return say("🚧", theme.Err, fail[i])
		}
	}
	for i, x := range st {
		if x == stRun && run[i] != "" {
			e := "⏳"
			if i == 0 {
				e = "🔨"
			}
			return say(e, theme.Warn, run[i])
		}
	}
	if mc != nil && s.PR > 0 && len(mc.Missing) > 0 {
		p, _ := say("👀", theme.Warn, "needs "+condName(mc.Missing[0]))
		return p, mc.Missing[0]
	}
	if st[0] == stNone {
		return say("💤", theme.Dim, "no report yet")
	}
	if s.PR == 0 {
		return say("📦", theme.Text, "ready to land")
	}
	return say("⏳", theme.Text, "waiting")
}

// stageWhy is pekoed's own words for a missing merge condition: the one
// the phrase names ("needs review" → "独立レビューの報告がない"), or else the
// first one the phrase does not already say (CI running or failing is in
// the phrase), with its name: "review: レビューした 17b962a と PR の HEAD
// ea9697f が違う". It is what tells "no review yet" from "reviewed an
// older commit".
func stageWhy(st [5]int, mc *state.MergeCheck, named string) string {
	if mc == nil || mc.Ready {
		return ""
	}
	for _, n := range mc.Missing {
		if n == "ci" && (st[2] == stRun || st[2] == stFail) {
			continue
		}
		if named != "" && n != named {
			continue
		}
		for _, c := range mc.Conds {
			if c.Name != n || c.OK {
				continue
			}
			d := firstLine(inboxText(c.Detail))
			switch {
			case n == named:
				return d
			case d == "":
				return condName(n)
			}
			return condName(n) + ": " + d
		}
	}
	return ""
}

// ---- drawing ----

func (m *model) viewDash(width, height int) []string {
	f := m.focus == pAgent
	extra := "· tapi the orchestrator"
	switch {
	case m.dash.at.IsZero():
		extra += " · loading…"
	default:
		extra += " · updated " + theme.Ago(time.Since(m.dash.at)) + " ago"
	}
	title := m.panelTitle(4, m.ic.Sessions, "Dashboard", extra, f)
	cw, ch := width-4, height-2
	rows, keys := m.dashRows(cw)
	d := &m.dash
	start, end := -1, -1
	for i, k := range keys {
		if k != "" && k == d.sel {
			if start < 0 {
				start = i
			}
			end = i
		}
	}
	if start >= 0 { // keep the chosen row (and what it unfolds) on screen
		if start < d.top {
			d.top = start
		}
		if end >= d.top+ch {
			d.top = max(min(start, end-ch+1), 0)
		}
	}
	d.top = clampi(d.top, 0, max(len(rows)-ch, 0))
	return m.box(width, height, boxOpt{title: title, rows: rows[d.top:], focused: f})
}

// dashGlyphs: the dashboard may draw its pictures (the stage bar, emoji).
// Not with the ascii icon set, nor with [mascot] glyphs = "ascii" (set,
// or measured: the terminal draws ambiguous glyphs wide), which ask for
// plain characters.
func (m *model) dashGlyphs() bool {
	return m.ic.Name != "ascii" && (m.tapi.r == nil || !m.tapi.r.ASCII())
}

// dashEmo is an emoji and a space before a word, or nothing when the
// dashboard draws no pictures. Only emoji that are East Asian Wide
// (Emoji_Presentation) are used: every terminal and the width code agree
// they take two cells, so the rows stay in line. Text-default symbols
// (⚙ 🛠 ☺ …) are not, as fonts and terminals disagree on their width.
func (m *model) dashEmo(e string) string {
	if !m.dashGlyphs() {
		return ""
	}
	return e + " "
}

// dashRows draws the dashboard body cw cells wide; keys[i] is the item
// drawn on rows[i] (its row or what it unfolds), "" for the rest.
//
// What a person acts on comes first and loudest: the overview's "needs
// you" line, the inbox. Then what is moving (the pipeline, merged ones
// dimmed at its end), then the records (requests, sends, decided), whose
// rows are quieter. Each section opens with an emoji, a bold name and a
// rule across the width, so the eye finds the sections first.
func (m *model) dashRows(cw int) (rows []row, keys []string) {
	th := m.th
	d := &m.dash
	its := m.dashItems()
	cur := m.dashSelIndex(its)
	if len(its) > 0 {
		d.sel = its[cur].key
	}
	focused := m.focus == pAgent
	dim := func(s string) string { return th.FG(theme.Dim, s) }
	add := func(s string) { rows, keys = append(rows, row{Text: s}), append(keys, "") }
	section := func(emoji, name, summary string) {
		if len(rows) > 0 {
			add("")
		}
		h := m.dashEmo(emoji) + th.Bold(theme.Text, name)
		if summary != "" {
			h += "  " + summary
		}
		if gap := cw - w(h) - 1; gap >= 3 {
			h += " " + th.FG(theme.Border, strings.Repeat("─", gap))
		}
		add(fit(h, cw))
	}
	none := func(s string) { add(fit(dim("  "+s), cw)) }
	// item draws a row that can be chosen, the lines that always go with
	// it (sub) and, unfolded, more
	item := func(key, text string, sub, more []string) {
		rows = append(rows, row{Text: fit(text, cw), Sel: focused && key == d.sel})
		keys = append(keys, key)
		for _, l := range sub {
			rows, keys = append(rows, row{Text: fit(l, cw)}), append(keys, key)
		}
		if d.open[key] {
			for _, l := range more {
				rows, keys = append(rows, row{Text: fit("      "+l, cw)}), append(keys, key)
			}
		}
	}
	now := time.Now()
	ago := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return theme.Ago(now.Sub(t))
	}
	wide := cw >= 70 // room for the long stage bar and names

	// overview
	section("🧭", "Overview", "")
	for _, l := range m.dashOverview(cw - 2) {
		add(fit("  "+l, cw))
	}

	// inbox
	open := 0
	for i := range m.inbox {
		if m.inbox[i].Kind != "fyi" {
			open++
		}
	}
	sum := dim("0 open")
	if open > 0 {
		sum = th.Bold(theme.Warn, fmt.Sprintf("%d open", open)) + dim(" · ↵ answer · o link")
	}
	section("📬", "Inbox", sum)
	if open == 0 {
		none(m.dashEmo("🍵") + "nothing to answer · time for a cup of tea")
	}
	kindEmoji := map[string]string{"login": "🔑", "approve": "✋", "decision": "🤔", "merge": "🚀"}
	for _, k := range dashKinds {
		var group []*state.InboxItem
		for i := range m.inbox {
			if it := &m.inbox[i]; it.Kind != "fyi" && inboxKind(it) == k.key {
				group = append(group, it)
			}
		}
		if len(group) == 0 {
			continue
		}
		r := theme.Warn
		if k.key == "merge" {
			r = theme.Secondary
		}
		add(fit("  "+m.dashEmo(kindEmoji[k.key])+th.Bold(r, k.label)+dim(fmt.Sprintf(" %d", len(group))), cw))
		for _, it := range group {
			who := clip(inboxText(it.From), 16)
			if it.About != "" && it.About != it.From {
				who += dim(" · " + clip(inboxText(it.About), 22))
			}
			head := "    " + dim(fmt.Sprintf("#%d ", it.ID)) + who + "  "
			tail := "  " + dim(fit(ago(it.CreatedAt), 4))
			room := max(cw-w(head)-w(tail), 1)
			item(fmt.Sprintf("inbox:%d", it.ID), head+th.FG(theme.Text, fit(firstLine(inboxText(it.Question)), room))+tail, nil, nil)
		}
	}

	// pipeline
	groups := m.dashGroups()
	moving := 0
	for _, g := range groups {
		if !g.merged {
			moving += len(g.sessions)
		}
	}
	section("🚦", "Pipeline", dim(fmt.Sprintf("%d moving · ", moving)+strings.Join(stageNames[:], " › ")))
	if len(groups) == 0 {
		none(m.dashEmo("🌱") + "no sessions yet · ask tapi for something")
	}
	slugW := min(max(cw/3, 8), 30)
	for _, g := range groups {
		add(fit("  "+m.dashGroupHead(g, cw-2), cw))
		for _, s := range g.sessions {
			mc := m.dash.merge[s.Slug]
			key := "s:" + s.Slug
			var more []string
			if mc != nil {
				for _, c := range mc.Conds {
					mark := m.stageMark(stOK)
					if !c.OK {
						mark = m.stageMark(stFail)
					}
					more = append(more, mark+" "+fit(condName(c.Name), 10)+" "+dim(inboxText(c.Detail)))
				}
			}
			if s.Report != nil {
				more = append(more, dim("report #"+fmt.Sprint(s.Report.ID)+" "+s.Report.Status+" · ")+firstLine(inboxText(s.Report.Summary)))
			}
			if len(more) == 0 {
				more = append(more, dim("nothing more yet"))
			}
			if g.merged { // a record: one quiet line
				line := "    " + th.FG(theme.Ok, fit(m.ic.Check, 1)) + " " + dim(fit(s.Slug, slugW)+" merged")
				if s.PR > 0 {
					line += dim(fmt.Sprintf("  #%d", s.PR))
				}
				item(key, line, nil, more)
				continue
			}
			lk := theme.Look(m.ic, s.State, m.frame, m.g.AnimateOn())
			head := "    " + th.FG(lk.Role, fit(lk.Icon, 1)) + " " + th.FG(theme.Text, fit(s.Slug, slugW)) + " "
			st := dashStages(s, mc)
			phrase, named := m.stagePhraseCond(s, st, mc)
			line := m.stageBar(st, wide) + "  " + phrase
			if s.PR > 0 {
				line += dim(fmt.Sprintf("  #%d", s.PR))
			}
			if mc != nil && mc.Inbox > 0 {
				line += dim(fmt.Sprintf("  inbox #%d", mc.Inbox))
			}
			why := stageWhy(st, mc, named)
			if why != "" && w(head)+w(line)+2+w(why) <= cw {
				line, why = line+"  "+dim(why), ""
			}
			var sub []string
			if why != "" { // pekoed's words on a line of their own, under the bar
				ind := w(head) // under the bar, or further left when it would not fit there
				if ind+w(why) > cw {
					ind = 6
				}
				sub = append(sub, strings.Repeat(" ", ind)+dim(why))
			}
			item(key, head+line, sub, more)
		}
	}

	// requests
	section("📝", "Requests", dim(fmt.Sprintf("%d open", len(d.ledger))))
	if len(d.ledger) == 0 {
		none("no open requests")
	}
	for i := range d.ledger {
		e := &d.ledger[i]
		head := "  " + dim(fmt.Sprintf("#%d ", e.ID))
		tail := "  " + dim(fit(ago(e.At), 4))
		room := max(cw-w(head)-w(tail), 1)
		var more []string
		for _, l := range wrapText(inboxText(e.Text), max(cw-8, 4)) {
			more = append(more, l)
		}
		for _, slug := range m.requestSessions(e) {
			if _, s := m.find(slug); s != nil {
				st := dashStages(s, d.merge[slug])
				more = append(more, fit(slug, 20)+" "+m.stageBar(st, false)+" "+m.stagePhrase(s, st, d.merge[slug]))
			} else {
				more = append(more, fit(slug, 20)+" "+dim("gone"))
			}
		}
		text := head + fit(firstLine(inboxText(e.Text)), room) + tail
		item(fmt.Sprintf("req:%d", e.ID), text, nil, more)
		if slugs := m.requestSessions(e); len(slugs) > 0 && !d.open[fmt.Sprintf("req:%d", e.ID)] {
			add(fit("      "+dim("→ "+strings.Join(slugs, ", ")), cw))
		}
	}

	// sends
	section("📨", "Sends", dim("newest first · ↵ full text"))
	if len(d.sends) == 0 {
		none("nothing sent yet")
	}
	for i := range d.sends {
		r := &d.sends[i]
		toW := 18
		if wide {
			toW = 26
		}
		head := "  " + dim(fit(ago(r.At), 4)) + " " + dim(clip(inboxText(r.From), 14)+" → ") + clip(inboxText(r.To), toW) + " " + dim(r.Kind) + "  "
		if !r.OK {
			head += th.FG(theme.Err, fit(m.ic.Fail, 1)+" failed") + " "
		}
		room := max(cw-w(head), 1)
		var more []string
		for _, l := range wrapText(inboxText(r.Text), max(cw-8, 4)) {
			more = append(more, l)
		}
		if r.Error != "" {
			more = append(more, th.FG(theme.Err, inboxText(r.Error)))
		}
		item(fmt.Sprintf("send:%d", r.ID), head+dim(fit(sendGist(inboxText(r.Text)), room)), nil, more)
	}

	// what tapi decided
	section("💡", "Decided", dim("fyi · what the agents chose themselves"))
	if len(d.fyi) == 0 {
		none("nothing yet")
	}
	for i := range d.fyi {
		it := &d.fyi[i]
		head := "  " + dim(fmt.Sprintf("#%d ", it.ID)) + clip(inboxText(it.From), 14) + "  "
		chose := ""
		if a := it.Answer; a != nil && a.Choice != nil && *a.Choice >= 0 && *a.Choice < len(it.Options) {
			chose = dim(" → ") + th.FG(theme.Text, clip(inboxText(it.Options[*a.Choice]), 30))
		}
		tail := "  " + dim(fit(ago(it.CreatedAt), 4))
		room := max(cw-w(head)-w(chose)-w(tail), 1)
		var more []string
		for _, l := range wrapText(inboxText(it.Question), max(cw-8, 4)) {
			more = append(more, l)
		}
		for _, l := range wrapText(inboxText(it.Context), max(cw-8, 4)) {
			more = append(more, dim(l))
		}
		for _, l := range it.Links {
			more = append(more, webLink(l))
		}
		item(fmt.Sprintf("fyi:%d", it.ID), head+dim(fit(firstLine(inboxText(it.Question)), room))+chose+tail, nil, more)
	}
	return rows, keys
}

// sendGist is the line of a send that says what it is about: prompts
// usually open with a markdown heading ("## 何を"), which says nothing, so
// the first line that is not a heading; the first heading when there is
// nothing else.
func sendGist(text string) string {
	first := ""
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if h := strings.TrimLeft(l, "#"); h != l && (h == "" || h[0] == ' ') {
			if first == "" {
				first = strings.TrimSpace(h)
			}
			continue
		}
		return l
	}
	return first
}

// dashOverview: what needs the person first (the inbox's open questions,
// the PRs ready to merge), loud; then running · waiting · to merge · PRs
// open · requests, quiet; on as many lines of room cells as it takes. A
// session reviewing someone else's PR is not counted.
func (m *model) dashOverview(room int) []string {
	th := m.th
	var run, wait, merge, prs, ready int
	for _, s := range m.sessions {
		if s.PRReview {
			continue
		}
		switch s.State {
		case state.Running, state.Creating:
			run++
		case state.Waiting:
			wait++
		}
		if s.Landing && s.State != state.Landed {
			merge++
		}
		if s.PR > 0 && s.State != state.Landed && (s.PRStatus == nil || s.PRStatus.State == "OPEN") {
			prs++
			if mc := m.dash.merge[s.Slug]; mc != nil && mc.Ready {
				ready++
			}
		}
	}
	inbox := 0
	for i := range m.inbox {
		if m.inbox[i].Kind != "fyi" {
			inbox++
		}
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	}
	part := func(n int, word string, r theme.Role) string {
		if n == 0 {
			return th.FG(theme.Dim, fmt.Sprintf("%d %s", n, word))
		}
		return th.FG(r, fmt.Sprint(n)) + " " + th.FG(theme.Dim, word)
	}
	sep := th.FG(theme.Dim, " · ")
	// the person's turn: loud when there is something, a calm line when not
	var loud []string
	if inbox > 0 {
		loud = append(loud, m.dashEmo("🙋")+th.Bold(theme.Warn, fmt.Sprint(inbox))+" "+th.FG(theme.Text, plural(inbox, "question", "questions")+" to answer"))
	}
	if ready > 0 {
		loud = append(loud, m.dashEmo("🚀")+th.Bold(theme.Secondary, fmt.Sprint(ready))+" "+th.FG(theme.Text, plural(ready, "PR", "PRs")+" ready to merge"))
	}
	var parts []string
	if len(loud) == 0 {
		parts = append(parts, m.dashEmo("🍵")+th.FG(theme.Ok, "nothing needs you")+th.FG(theme.Dim, " · all quiet"))
	} else {
		parts = append(parts, strings.Join(loud, "   "))
	}
	lines := []string{}
	flow := func(ps []string) {
		cur := ""
		for _, p := range ps {
			switch {
			case cur == "":
				cur = p
			case w(cur)+w(sep)+w(p) <= room:
				cur += sep + p
			default:
				lines, cur = append(lines, cur), p
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	flow(parts)
	flow([]string{
		part(run, "running", theme.Ok), part(wait, "waiting", theme.Warn), part(merge, "to merge", theme.Secondary),
		part(prs, plural(prs, "PR open", "PRs open"), theme.Text),
		part(len(m.dash.ledger), plural(len(m.dash.ledger), "request", "requests"), theme.Text),
	})
	return lines
}

// dashGroupHead names a group: who asked, and the request (its number and
// the start of its first line; the whole text is in Requests).
func (m *model) dashGroupHead(g dashGroup, room int) string {
	th := m.th
	if g.merged {
		return fit(m.dashEmo("🎉")+th.FG(theme.Dim, fmt.Sprintf("merged · %d", len(g.sessions))), room)
	}
	who := "from " + inboxText(g.parent)
	switch g.parent {
	case "+tapi":
		who = "from tapi"
	case "human":
		who = "from you"
	case "", "unknown":
		who = "other"
	}
	head := th.FG(theme.Text, who)
	if g.request > 0 {
		req := fmt.Sprintf(" · #%d", g.request)
		if e := m.ledgerEntry(g.request); e != nil {
			if t := firstLine(inboxText(e.Text)); t != "" {
				req += " " + fit(t, min(max(room-w(head)-w(req)-1, 1), 32))
			}
		}
		head += th.FG(theme.Dim, strings.TrimRight(req, " "))
	}
	return fit(head, room)
}

// requestSessions are the sessions working on e: the ones it was linked to
// and the ones made with --request.
func (m *model) requestSessions(e *state.LedgerEntry) []string {
	out := append([]string(nil), e.Sessions...)
	for _, s := range m.sessions {
		if s.Request == e.ID && !slices.Contains(out, s.Slug) {
			out = append(out, s.Slug)
		}
	}
	return out
}

// ---- keys and mouse ----

func (m *model) keyDash(key string) tea.Cmd {
	its := m.dashItems()
	cur := m.dashSelIndex(its)
	switch key {
	case "j", "down":
		if len(its) > 0 {
			m.dash.sel = its[min(cur+1, len(its)-1)].key
		}
	case "k", "up":
		if len(its) > 0 {
			m.dash.sel = its[max(cur-1, 0)].key
		}
	case "enter":
		if len(its) == 0 {
			return nil
		}
		it := its[cur]
		if it.inbox != nil {
			return m.openInbox(*it.inbox)
		}
		if m.dash.open == nil {
			m.dash.open = map[string]bool{}
		}
		m.dash.open[it.key] = !m.dash.open[it.key]
	case "o":
		if len(its) == 0 {
			return nil
		}
		it := its[cur]
		var u string
		switch {
		case it.inbox != nil:
			u = inboxLink(it.inbox)
		case it.fyi != nil:
			u = inboxLink(it.fyi)
		}
		if u == "" {
			m.setMsg("no http(s) link to open", false)
			return nil
		}
		return func() tea.Msg {
			if err := openURL(u); err != nil {
				return errMsg{err}
			}
			return infoMsg("opened " + u)
		}
	case "h", "left", "esc":
		m.focus = pSessions
	}
	return nil
}

// inboxLink is the item's PR, or its first http(s) link.
func inboxLink(it *state.InboxItem) string {
	if f := it.Facts; f != nil && f.PRURL != "" {
		return f.PRURL
	}
	for _, l := range it.Links {
		if strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") {
			return l
		}
	}
	return ""
}

// dashMouse: the wheel moves the choice, a click chooses the row (and
// opens a link drawn there).
func (m *model) dashMouse(ev tea.MouseMsg, x, y int) tea.Cmd {
	switch {
	case ev.Button == tea.MouseButtonWheelDown:
		return m.keyDash("j")
	case ev.Button == tea.MouseButtonWheelUp:
		return m.keyDash("k")
	case ev.Button == tea.MouseButtonLeft && ev.Action == tea.MouseActionPress:
		m.focus = pAgent
		cw, _ := m.agentSize()
		rows, keys := m.dashRows(cw)
		i := m.dash.top + y
		if y < 0 || i >= len(rows) {
			return nil
		}
		if u := linkAt(rows[i].Text, x); u != "" {
			return func() tea.Msg {
				if err := openURL(u); err != nil {
					return errMsg{err}
				}
				return infoMsg("opened " + u)
			}
		}
		if keys[i] != "" {
			m.dash.sel = keys[i]
		}
	}
	return nil
}

func dashHints() [][2]string {
	return [][2]string{{"j/k", "move"}, {"↵", "answer / unfold"}, {"o", "open link"}, {"h", "back"}, {"t", "tapi chat"}, {"?", "help"}}
}

func dashKeysFull() [][3]string {
	return [][3]string{{"j/k", "move", "j"}, {"↵", "answer an inbox item / unfold a row", "\r"}, {"o", "open the inbox item's link", "o"},
		{"h", "back to Sessions", "h"}, {"esc", "back to Sessions", "esc"}}
}
