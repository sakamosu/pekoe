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

// dashPipeline is the sessions in the pipeline (not someone else's PR).
func (m *model) dashPipeline() []*state.Session {
	var out []*state.Session
	for _, s := range m.sessions {
		if !s.PRReview {
			out = append(out, s)
		}
	}
	return out
}

// dashGroup is the sessions one requester asked for under one request.
type dashGroup struct {
	parent   string
	request  int64
	sessions []*state.Session
}

// dashGroups groups the pipeline by parent and request: tapi first, then
// the person, then the sessions that asked, then the rest; requests in
// order, none last.
func (m *model) dashGroups() []dashGroup {
	idx := map[[2]string]int{}
	var gs []dashGroup
	for _, s := range m.dashPipeline() {
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
		return th.FG(theme.Ok, fit(m.ic.Here, 1))
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
	default:
		st[0] = stRun
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

// stagesLine is "impl ✓ › PR ✓ › CI ✗ › review · › merge ·", or the
// marks alone ("✓ ✓ ✗ · ·") when that does not fit in room.
func (m *model) stagesLine(st [5]int, room int) string {
	dim := func(s string) string { return m.th.FG(theme.Dim, s) }
	var long, short []string
	for i, x := range st {
		long = append(long, dim(stageNames[i]+" ")+m.stageMark(x))
		short = append(short, m.stageMark(x))
	}
	if l := strings.Join(long, dim(" › ")); w(l) <= room {
		return l
	}
	return strings.Join(short, " ")
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

// dashRows draws the dashboard body cw cells wide; keys[i] is the item
// drawn on rows[i] (its row or what it unfolds), "" for the rest.
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
	section := func(name, summary string) {
		if len(rows) > 0 {
			add("")
		}
		h := th.Bold(theme.Text, name)
		if summary != "" {
			h += "  " + summary
		}
		add(fit(h, cw))
	}
	none := func(s string) { add(dim("  " + s)) }
	item := func(key, text string, more []string) {
		rows = append(rows, row{Text: fit(text, cw), Sel: focused && key == d.sel})
		keys = append(keys, key)
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

	// overview
	section("Overview", "")
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
	section("Inbox", dim(fmt.Sprintf("%d open · ↵ answer · o link", open)))
	if open == 0 {
		none("nothing to answer")
	}
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
		add(fit("  "+th.FG(theme.Warn, k.label)+dim(fmt.Sprintf(" %d", len(group))), cw))
		for _, it := range group {
			who := clip(inboxText(it.From), 16)
			if it.About != "" && it.About != it.From {
				who += dim(" · " + clip(inboxText(it.About), 16))
			}
			head := fmt.Sprintf("    #%d ", it.ID) + who + "  "
			tail := "  " + dim(fit(ago(it.CreatedAt), 4))
			room := max(cw-w(head)-w(tail), 1)
			item(fmt.Sprintf("inbox:%d", it.ID), head+fit(firstLine(inboxText(it.Question)), room)+tail, nil)
		}
	}

	// pipeline
	section("Pipeline", dim("impl › PR › CI › review › merge"))
	groups := m.dashGroups()
	if len(groups) == 0 {
		none("no sessions")
	}
	for _, g := range groups {
		add(fit("  "+m.dashGroupHead(g, cw-2), cw))
		for _, s := range g.sessions {
			mc := m.dash.merge[s.Slug]
			key := "s:" + s.Slug
			lk := theme.Look(m.ic, s.State, m.frame, m.g.AnimateOn())
			slugW := min(max(cw/4, 8), 24)
			head := "    " + th.FG(lk.Role, fit(lk.Icon, 1)) + " " + fit(s.Slug, slugW) + " "
			room := max(cw-w(head), 1)
			tail := "" // what is missing comes before the stage names
			if s.PR > 0 {
				tail += dim(fmt.Sprintf("  #%d", s.PR))
			}
			if mc != nil && s.PR > 0 && !mc.Ready && len(mc.Missing) > 0 {
				tail += "  " + th.FG(theme.Warn, "needs "+strings.Join(mc.Missing, ", "))
			}
			if mc != nil && mc.Inbox > 0 {
				tail += dim(fmt.Sprintf("  inbox #%d", mc.Inbox))
			}
			line := m.stagesLine(dashStages(s, mc), room-w(tail)) + tail
			var more []string
			if mc != nil {
				for _, c := range mc.Conds {
					mark := m.stageMark(stOK)
					if !c.OK {
						mark = m.stageMark(stFail)
					}
					more = append(more, mark+" "+fit(c.Name, 11)+" "+dim(inboxText(c.Detail)))
				}
			}
			if s.Report != nil {
				more = append(more, dim("report #"+fmt.Sprint(s.Report.ID)+" "+s.Report.Status+" · ")+firstLine(inboxText(s.Report.Summary)))
			}
			if len(more) == 0 {
				more = append(more, dim("nothing more yet"))
			}
			item(key, head+fit(line, room), more)
		}
	}

	// requests
	section("Requests", dim(fmt.Sprintf("%d open", len(d.ledger))))
	if len(d.ledger) == 0 {
		none("no open requests")
	}
	for i := range d.ledger {
		e := &d.ledger[i]
		head := fmt.Sprintf("  #%d ", e.ID)
		tail := "  " + dim(fit(ago(e.At), 4))
		room := max(cw-w(head)-w(tail), 1)
		var more []string
		for _, l := range wrapText(inboxText(e.Text), max(cw-8, 4)) {
			more = append(more, l)
		}
		for _, slug := range m.requestSessions(e) {
			if _, s := m.find(slug); s != nil {
				more = append(more, fit(slug, 20)+" "+m.stagesLine(dashStages(s, d.merge[slug]), max(cw-30, 1)))
			} else {
				more = append(more, fit(slug, 20)+" "+dim("gone"))
			}
		}
		text := head + fit(firstLine(inboxText(e.Text)), room) + tail
		item(fmt.Sprintf("req:%d", e.ID), text, more)
		if slugs := m.requestSessions(e); len(slugs) > 0 && !d.open[fmt.Sprintf("req:%d", e.ID)] {
			add(fit("      "+dim("→ "+strings.Join(slugs, ", ")), cw))
		}
	}

	// sends
	section("Sends", dim("newest first · ↵ full text"))
	if len(d.sends) == 0 {
		none("nothing sent yet")
	}
	for i := range d.sends {
		r := &d.sends[i]
		head := "  " + dim(fit(ago(r.At), 4)) + " " + clip(inboxText(r.From), 14) + dim(" → ") + clip(inboxText(r.To), 18) + " " + dim(r.Kind) + "  "
		if !r.OK {
			head += th.FG(theme.Err, "failed") + " "
		}
		room := max(cw-w(head), 1)
		var more []string
		for _, l := range wrapText(inboxText(r.Text), max(cw-8, 4)) {
			more = append(more, l)
		}
		if r.Error != "" {
			more = append(more, th.FG(theme.Err, inboxText(r.Error)))
		}
		item(fmt.Sprintf("send:%d", r.ID), head+fit(firstLine(inboxText(r.Text)), room), more)
	}

	// what tapi decided
	section("Decided", dim("fyi · what the agents chose themselves"))
	if len(d.fyi) == 0 {
		none("nothing yet")
	}
	for i := range d.fyi {
		it := &d.fyi[i]
		head := fmt.Sprintf("  #%d ", it.ID) + clip(inboxText(it.From), 14) + "  "
		chose := ""
		if a := it.Answer; a != nil && a.Choice != nil && *a.Choice >= 0 && *a.Choice < len(it.Options) {
			chose = dim(" → ") + clip(inboxText(it.Options[*a.Choice]), 30)
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
		item(fmt.Sprintf("fyi:%d", it.ID), head+fit(firstLine(inboxText(it.Question)), room)+chose+tail, more)
	}
	return rows, keys
}

// dashOverview: running · waiting · to merge · PRs open · inbox · requests,
// on as many lines of room cells as it takes.
func (m *model) dashOverview(room int) []string {
	th := m.th
	var run, wait, merge, prs int
	for _, s := range m.dashPipeline() {
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
		}
	}
	inbox := 0
	for i := range m.inbox {
		if m.inbox[i].Kind != "fyi" {
			inbox++
		}
	}
	part := func(n int, word string, r theme.Role) string {
		if n != 1 {
			word = strings.Replace(word, "PR ", "PRs ", 1)
			if word == "request" {
				word = "requests"
			}
		}
		if n == 0 {
			return th.FG(theme.Dim, fmt.Sprintf("%d %s", n, word))
		}
		return th.FG(r, fmt.Sprint(n)) + " " + word
	}
	sep := th.FG(theme.Dim, " · ")
	var lines []string
	cur := ""
	for _, p := range []string{
		part(run, "running", theme.Text), part(wait, "waiting", theme.Warn), part(merge, "to merge", theme.Ok),
		part(prs, "PR open", theme.Text), part(inbox, "in the inbox", theme.Warn), part(len(m.dash.ledger), "request", theme.Text),
	} {
		switch {
		case cur == "":
			cur = p
		case w(cur)+w(sep)+w(p) <= room:
			cur += sep + p
		default:
			lines, cur = append(lines, cur), p
		}
	}
	return append(lines, cur)
}

// dashGroupHead names a group: who asked, and the request.
func (m *model) dashGroupHead(g dashGroup, room int) string {
	th := m.th
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
		req := fmt.Sprintf(" · request #%d", g.request)
		if e := m.ledgerEntry(g.request); e != nil {
			req += " " + firstLine(inboxText(e.Text))
		}
		head += th.FG(theme.Dim, req)
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
