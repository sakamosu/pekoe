package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"boba/internal/daemon"
	"boba/internal/engine"
	"boba/internal/mascot"
	"boba/internal/state"
	"boba/internal/theme"
)

// The PR view: the main panel's fourth window, next to agent / review / dev.
// It shows the selected session's GitHub PR (checks, reviews, threads,
// comments) and follows the selection in [1] Sessions. Read-only towards
// GitHub; `s` hands items to the agent's pty as a prompt.

const winPR = "pr"

const prRefresh = 15 * time.Second

type prState struct {
	slug    string // whose PR the data below is
	view    *daemon.PRView
	none    bool // the session has no PR
	err     string
	loading bool
	at      time.Time // last fetch done
	sel     int       // selected item
	top     int       // first body row shown
	open    map[string]bool
	marked  map[string]bool
	logs    map[string]*prLog // check key -> failed log
}

type prLog struct {
	text    string
	err     string
	loading bool
}

func newPRState() *prState {
	return &prState{open: map[string]bool{}, marked: map[string]bool{}, logs: map[string]*prLog{}}
}

type prMsg struct {
	slug string
	view *daemon.PRView
	err  error
}

type prLogMsg struct {
	slug, key string
	log       *daemon.FailedLog
	err       error
}

type prSendMsg struct {
	slug, text string
	n          int
	logs       map[string]*prLog
}

// ---- items ----

type prItem struct {
	kind    string // check | review | thread | comment
	key     string
	url     string
	group   string // threads: file path (a header row precedes each new group)
	check   *daemon.PRCheck
	review  *daemon.PRReview
	thread  *daemon.PRThread
	comment *daemon.PRComment
}

var bucketOrder = map[string]int{"fail": 0, "pending": 1, "pass": 2, "skip": 3}

// prItems lists the selectable rows in display order: failed checks first,
// each reviewer's latest review, unresolved threads first (grouped by file),
// then the conversation.
func prItems(v *daemon.PRView) []prItem {
	if v == nil {
		return nil
	}
	var out []prItem
	checks := append([]daemon.PRCheck(nil), v.Checks...)
	sort.SliceStable(checks, func(i, j int) bool { return bucketOrder[checks[i].Bucket] < bucketOrder[checks[j].Bucket] })
	for i := range checks {
		c := &checks[i]
		out = append(out, prItem{kind: "check", key: fmt.Sprintf("c:%s:%d", c.Name, c.JobID), url: c.URL, check: c})
	}
	for _, r := range latestReviews(v.Reviews) {
		r := r
		out = append(out, prItem{kind: "review", key: "r:" + r.Author, url: r.URL, review: &r})
	}
	threads := append([]daemon.PRThread(nil), v.Threads...)
	sort.SliceStable(threads, func(i, j int) bool {
		a, b := threads[i], threads[j]
		if a.Resolved != b.Resolved {
			return !a.Resolved
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return threadLine(a) < threadLine(b)
	})
	for i := range threads {
		t := &threads[i]
		it := prItem{kind: "thread", group: t.Path, thread: t, key: fmt.Sprintf("t:%s:%d", t.Path, threadLine(*t))}
		if len(t.Comments) > 0 {
			it.url = t.Comments[0].URL
			it.key += ":" + t.Comments[0].At.String()
		}
		out = append(out, it)
	}
	for i := range v.Comments {
		c := &v.Comments[i]
		out = append(out, prItem{kind: "comment", key: fmt.Sprintf("m:%d:%s", i, c.At), url: c.URL, comment: c})
	}
	return out
}

func threadLine(t daemon.PRThread) int {
	if t.Line > 0 {
		return t.Line
	}
	return t.OrigLine
}

// latestReviews keeps each reviewer's latest decisive review (approve /
// changes requested / dismissed), or their latest comment-only review when
// that is all they left. Newest first.
func latestReviews(rs []daemon.PRReview) []daemon.PRReview {
	best := map[string]daemon.PRReview{}
	var order []string
	for _, r := range rs {
		cur, ok := best[r.Author]
		if !ok {
			order = append(order, r.Author)
		}
		decisive := r.State != "COMMENTED" && r.State != "PENDING"
		curDecisive := ok && cur.State != "COMMENTED" && cur.State != "PENDING"
		if !ok || (decisive && !r.At.Before(cur.At)) || (!curDecisive && !r.At.Before(cur.At)) {
			best[r.Author] = r
		}
	}
	out := make([]daemon.PRReview, 0, len(order))
	for _, a := range order {
		out = append(out, best[a])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// cleanText makes text from GitHub safe to draw: no escape sequences or
// other control characters, tabs as spaces.
func cleanText(s string) string {
	s = ansi.Strip(strings.ReplaceAll(s, "\r\n", "\n"))
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, s)
}

func firstLine(s string) string {
	s = strings.TrimSpace(cleanText(s))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func wrapText(s string, width int) []string {
	s = strings.TrimRight(cleanText(s), "\n ")
	if width < 4 {
		width = 4
	}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, strings.Split(ansi.Wrap(l, width, ""), "\n")...)
	}
	return out
}

func dur(sec int) string {
	switch {
	case sec <= 0:
		return ""
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh%02dm", sec/3600, sec/60%60)
}

// ---- looks ----

func (m *model) checkLook(c *daemon.PRCheck) theme.StateLook {
	switch c.Bucket {
	case "fail":
		return theme.StateLook{Icon: m.ic.Fail, Role: theme.Err, Word: "fail"}
	case "pass":
		return theme.StateLook{Icon: m.ic.Check, Role: theme.Ok, Word: "pass"}
	case "skip":
		return theme.StateLook{Icon: m.ic.Idle, Role: theme.Dim, Word: strings.ToLower(c.Conclusion)}
	}
	word := "run"
	if c.Status == "QUEUED" || c.Status == "WAITING" || c.Status == "PENDING" || c.Status == "EXPECTED" || c.Status == "REQUESTED" {
		word = "queued"
	}
	return theme.StateLook{Icon: m.ic.Pending, Role: theme.Dim, Word: word}
}

func (m *model) reviewLook(state string) theme.StateLook {
	switch state {
	case "APPROVED":
		return theme.StateLook{Icon: m.ic.Check, Role: theme.Ok, Word: "approved"}
	case "CHANGES_REQUESTED":
		return theme.StateLook{Icon: m.ic.ChangesReq, Role: theme.Warn, Word: "changes"}
	case "DISMISSED":
		return theme.StateLook{Icon: m.ic.Idle, Role: theme.Dim, Word: "dismissed"}
	case "PENDING":
		return theme.StateLook{Icon: m.ic.Pending, Role: theme.Dim, Word: "pending"}
	}
	return theme.StateLook{Icon: m.ic.Comments, Role: theme.Secondary, Word: "commented"}
}

func decisionWord(d string) (string, theme.Role) {
	switch d {
	case "APPROVED":
		return "approved", theme.Ok
	case "CHANGES_REQUESTED":
		return "changes requested", theme.Warn
	case "REVIEW_REQUIRED":
		return "review required", theme.Dim
	}
	return "no review decision", theme.Dim
}

func (m *model) prStateLook(v *daemon.PRView) theme.StateLook {
	switch v.State {
	case "MERGED":
		return theme.StateLook{Icon: m.ic.Merged, Role: theme.Secondary, Word: "merged"}
	case "CLOSED":
		return theme.StateLook{Icon: m.ic.Closed, Role: theme.Dim, Word: "closed"}
	}
	return theme.StateLook{Icon: m.ic.PR, Role: theme.Ok, Word: "open"}
}

// ---- drawing ----

func (m *model) viewPR(s *state.Session, width, height int) []string {
	th := m.th
	p := m.pr
	f := m.focus == pAgent
	name := "PR"
	if s.PR > 0 {
		name = fmt.Sprintf("PR #%d", s.PR)
	}
	var extra []string
	switch {
	case p.loading:
		extra = append(extra, "loading…")
	case !p.at.IsZero() && p.slug == s.Slug:
		extra = append(extra, "updated "+theme.Ago(time.Since(p.at))+" ago")
	}
	if n := len(p.marked); n > 0 {
		extra = append(extra, fmt.Sprintf("%d selected", n))
	}
	ex := ""
	if len(extra) > 0 {
		ex = "· " + strings.Join(extra, " · ")
	}
	title := m.panelTitle(4, m.ic.PR, name+" "+s.Branch, ex, f)
	cw, ch := width-4, height-2
	if p.slug != s.Slug || (p.view == nil && !p.none && p.err == "") {
		return m.box(width, height, boxOpt{title: title, rows: m.bigRows(cw, ch, mascot.Work, "loading PR…", ""), focused: f})
	}
	if p.view == nil {
		if p.err != "" {
			rows := m.bigRows(cw, ch-4, mascot.Error, "could not load the PR", "R retry · esc back")
			for _, l := range wrapText(p.err, cw) {
				rows = append(rows, row{Text: th.FG(theme.Err, l)})
			}
			return m.box(width, height, boxOpt{title: title, rows: rows, focused: f})
		}
		return m.box(width, height, boxOpt{title: title, rows: m.bigRows(cw, ch, mascot.Idle, "no PR", "l land opens one · esc back"), focused: f})
	}
	rows, start, end := m.prRows(cw)
	// keep the selected item (with its expansion) on screen
	if start >= 0 {
		if start < p.top {
			p.top = start
		}
		if end >= p.top+ch {
			p.top = max(min(start, end-ch+1), 0)
		}
	}
	p.top = clampi(p.top, 0, max(len(rows)-ch, 0))
	return m.box(width, height, boxOpt{title: title, rows: rows[p.top:], focused: f})
}

// prRows draws the PR body; start/end are the rows of the selected item.
func (m *model) prRows(cw int) (rows []row, start, end int) {
	th := m.th
	p := m.pr
	v := p.view
	items := prItems(v)
	p.sel = clampi(p.sel, 0, max(len(items)-1, 0))
	start, end = -1, -1
	add := func(text string) { rows = append(rows, row{Text: text}) }
	dimw := func(s string) string { return th.FG(theme.Dim, s) }

	// header
	add(th.FG(theme.Text, firstLine(v.Title)))
	lk := m.prStateLook(v)
	parts := []string{th.FG(lk.Role, theme.Prefix(lk.Icon)+lk.Word)}
	if v.IsDraft {
		parts = append(parts, dimw("draft"))
	}
	if v.State == "OPEN" && v.MergeState != "" {
		role := theme.Dim
		switch v.MergeState {
		case "CLEAN":
			role = theme.Ok
		case "DIRTY":
			role = theme.Err
		}
		parts = append(parts, th.FG(role, strings.ToLower(v.MergeState)))
	}
	parts = append(parts, dimw(v.Base+" ← "+v.Head))
	add(strings.Join(parts, dimw(" · ")))
	if p.err != "" {
		add(th.FG(theme.Err, firstLine(p.err)))
	}

	section := func(name, summary string) {
		add("")
		h := th.FG(theme.Text, name)
		if summary != "" {
			h += "  " + summary
		}
		add(h)
	}
	gutter := func(it prItem) string {
		if p.marked[it.key] {
			return th.FG(theme.Secondary, fit(m.ic.Unsent, 1)) + " "
		}
		return "  "
	}
	kind := ""
	group := ""
	for i, it := range items {
		if it.kind != kind {
			kind = it.kind
			switch kind {
			case "check":
				var fail, pend, pass int
				for _, c := range v.Checks {
					switch c.Bucket {
					case "fail":
						fail++
					case "pending":
						pend++
					case "pass":
						pass++
					}
				}
				var sum []string
				if fail > 0 {
					sum = append(sum, th.FG(theme.Err, fmt.Sprintf("%d fail", fail)))
				}
				if pend > 0 {
					sum = append(sum, dimw(fmt.Sprintf("%d run", pend)))
				}
				if pass > 0 {
					sum = append(sum, th.FG(theme.Ok, fmt.Sprintf("%d pass", pass)))
				}
				section("Checks", strings.Join(sum, dimw(" · ")))
			case "review":
				w, role := decisionWord(v.ReviewDecision)
				section("Reviews", th.FG(role, w))
			case "thread":
				un := 0
				for _, t := range v.Threads {
					if !t.Resolved {
						un++
					}
				}
				section("Threads", dimw(fmt.Sprintf("%d unresolved · %d resolved", un, len(v.Threads)-un)))
			case "comment":
				section("Comments", dimw(fmt.Sprint(len(v.Comments))))
			}
			group = ""
		}
		if it.kind == "thread" && it.group != group {
			group = it.group
			add("  " + dimw(cleanText(group)))
		}
		first := len(rows)
		sel := i == p.sel
		rows = append(rows, row{Text: gutter(it) + m.prItemLine(it, cw-2), Sel: sel})
		if p.open[it.key] {
			for _, l := range m.prExpansion(it, cw-6) {
				add("      " + l)
			}
		}
		if sel {
			start, end = first, len(rows)-1
		}
	}
	if len(items) == 0 {
		add("")
		add(dimw("  no checks, reviews or comments yet"))
	}
	return rows, start, end
}

// prItemLine is one item row (without the gutter), cw cells wide.
func (m *model) prItemLine(it prItem, cw int) string {
	th := m.th
	icon := func(lk theme.StateLook) string { return th.FG(lk.Role, fit(lk.Icon, 1)) + " " }
	ago := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return theme.Ago(time.Since(t))
	}
	switch it.kind {
	case "check":
		c := it.check
		lk := m.checkLook(c)
		wordW, durW := 7, 7
		nameW := cw - 2 - 1 - wordW - 1 - durW
		if nameW < 8 {
			durW = 0
			nameW = cw - 2 - 1 - wordW
		}
		name := cleanText(c.Name)
		if c.Workflow != "" && c.Workflow != c.Name {
			name = th.FG(theme.Dim, cleanText(c.Workflow)+" / ") + name
		}
		line := icon(lk) + fit(name, nameW) + " " + th.FG(lk.Role, fit(lk.Word, wordW))
		if durW > 0 {
			line += " " + th.FG(theme.Dim, fit(dur(c.Seconds), durW))
		}
		return line
	case "review":
		r := it.review
		lk := m.reviewLook(r.State)
		body := ""
		if b := firstLine(r.Body); b != "" {
			body = "  " + th.FG(theme.Dim, b)
		}
		return icon(lk) + fit(fit(cleanText(r.Author), 14)+" "+th.FG(lk.Role, fit(lk.Word, 9))+body, max(cw-2-5, 1)) + " " + th.FG(theme.Dim, fit(ago(r.At), 4))
	case "thread":
		t := it.thread
		lk := theme.StateLook{Icon: m.ic.Comments, Role: theme.Secondary}
		textRole := theme.Text
		if t.Resolved {
			lk = theme.StateLook{Icon: m.ic.Check, Role: theme.Dim}
			textRole = theme.Dim
		}
		if t.Outdated {
			lk.Role, textRole = theme.Dim, theme.Dim
		}
		loc := fmt.Sprintf("L%d", threadLine(*t))
		head := ""
		at := time.Time{}
		if len(t.Comments) > 0 {
			c := t.Comments[0]
			head = cleanText(c.Author) + ": " + firstLine(c.Body)
			at = t.Comments[len(t.Comments)-1].At
		}
		tags := ""
		if n := len(t.Comments) - 1; n > 0 {
			tags += fmt.Sprintf(" +%d", n)
		}
		if t.Outdated {
			tags += " outdated"
		}
		if t.Resolved {
			tags += " resolved"
		}
		right := th.FG(theme.Dim, tags) + " " + th.FG(theme.Dim, fit(ago(at), 4))
		return "  " + icon(lk) + th.FG(theme.Dim, fit(loc, 6)) + th.FG(textRole, fit(head, max(cw-4-6-w(right), 1))) + right
	case "comment":
		c := it.comment
		lk := theme.StateLook{Icon: m.ic.Comments, Role: theme.Secondary}
		right := " " + th.FG(theme.Dim, fit(ago(c.At), 4))
		return icon(lk) + fit(cleanText(c.Author)+": "+firstLine(c.Body), max(cw-2-w(right), 1)) + right
	}
	return ""
}

// prExpansion is what Enter unfolds under an item: the whole thread, the
// review / comment body, or the tail of a failed check's log.
func (m *model) prExpansion(it prItem, width int) []string {
	th := m.th
	dimw := func(s string) string { return th.FG(theme.Dim, s) }
	var out []string
	switch it.kind {
	case "check":
		c := it.check
		if c.URL != "" {
			out = append(out, dimw(c.URL))
		}
		if c.Bucket != "fail" {
			out = append(out, dimw(strings.ToLower(c.Status+" "+c.Conclusion)))
			break
		}
		if c.RunID == 0 {
			out = append(out, dimw("no log here: not a GitHub Actions check · o open"))
			break
		}
		lg := m.pr.logs[it.key]
		switch {
		case lg == nil || lg.loading:
			out = append(out, dimw("loading the failed log…"))
		case lg.err != "":
			out = append(out, th.FG(theme.Err, firstLine(lg.err)))
		default:
			lines := strings.Split(cleanText(lg.text), "\n")
			if len(lines) > 40 {
				out = append(out, dimw(fmt.Sprintf("… %d more lines (s sends the last 200)", len(lines)-40)))
				lines = lines[len(lines)-40:]
			}
			for _, l := range lines {
				out = append(out, clip(l, width))
			}
		}
	case "review":
		r := it.review
		if strings.TrimSpace(r.Body) == "" {
			out = append(out, dimw("(no text)"))
		}
		out = append(out, wrapText(r.Body, width)...)
	case "thread":
		t := it.thread
		out = append(out, dimw(fmt.Sprintf("%s:%d", cleanText(t.Path), threadLine(*t))))
		for _, c := range t.Comments {
			out = append(out, dimw(cleanText(c.Author)+" · "+theme.Ago(time.Since(c.At))+" ago"))
			for _, l := range wrapText(c.Body, width-2) {
				out = append(out, "  "+l)
			}
		}
	case "comment":
		out = append(out, wrapText(it.comment.Body, width)...)
	}
	return out
}

// ---- loading ----

// prFollow points the PR view at the selected session and fetches it.
func (m *model) prFollow() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	if m.pr.slug != s.Slug {
		*m.pr = *newPRState()
		m.pr.slug = s.Slug
	}
	return m.loadPR()
}

func (m *model) loadPR() tea.Cmd {
	slug := m.pr.slug
	if slug == "" || m.pr.loading || m.cl == nil {
		return nil
	}
	m.pr.loading = true
	return func() tea.Msg {
		var v *daemon.PRView
		err := m.cl.Call("pr.view", map[string]any{"slug": slug, "seen": true}, &v)
		if err != nil { // GitHub unreachable: the user still looked
			_ = m.cl.Call("pr.seen", map[string]any{"slug": slug}, nil)
		}
		return prMsg{slug: slug, view: v, err: err}
	}
}

func (m *model) onPRMsg(msg prMsg) tea.Cmd {
	p := m.pr
	if msg.slug != p.slug {
		return nil
	}
	p.loading = false
	p.at = time.Now()
	if msg.err != nil {
		p.err = msg.err.Error()
		return nil
	}
	p.err = ""
	p.view, p.none = msg.view, msg.view == nil
	return nil
}

func (m *model) loadFailedLog(it prItem) tea.Cmd {
	p := m.pr
	if it.check == nil || it.check.RunID == 0 || it.check.Bucket != "fail" || m.cl == nil {
		return nil
	}
	if lg := p.logs[it.key]; lg != nil && (lg.loading || lg.err == "") {
		return nil
	}
	p.logs[it.key] = &prLog{loading: true}
	slug, key, c := p.slug, it.key, *it.check
	return func() tea.Msg {
		var fl daemon.FailedLog
		err := m.cl.Call("pr.failed_log", map[string]any{"slug": slug, "check": c.Name, "run_id": c.RunID, "job_id": c.JobID}, &fl)
		return prLogMsg{slug: slug, key: key, log: &fl, err: err}
	}
}

func (m *model) onPRLogMsg(msg prLogMsg) {
	if msg.slug != m.pr.slug {
		return
	}
	lg := &prLog{}
	if msg.err != nil {
		lg.err = msg.err.Error()
	} else {
		lg.text = msg.log.Log
	}
	m.pr.logs[msg.key] = lg
}

// ---- keys ----

// openPR switches the main panel to the PR view of the selected session.
func (m *model) openPR() tea.Cmd {
	if m.current() == nil {
		return nil
	}
	m.window = winPR
	m.focus = pAgent
	m.typing = false
	m.screen = nil
	return m.prFollow()
}

// closePR goes back to the agent window.
func (m *model) closePR() tea.Cmd {
	m.window = engine.WinAgent
	m.screen = nil
	return m.fetchScreen()
}

func (m *model) selectedPRItem() (prItem, bool) {
	items := prItems(m.pr.view)
	if m.pr.sel >= 0 && m.pr.sel < len(items) {
		return items[m.pr.sel], true
	}
	return prItem{}, false
}

func (m *model) keyPR(key string) tea.Cmd {
	s := m.current()
	p := m.pr
	n := len(prItems(p.view))
	switch key {
	case "j", "down":
		m.move(&p.sel, n, 1)
	case "k", "up":
		m.move(&p.sel, n, -1)
	case "g", "home":
		p.sel, p.top = 0, 0
	case "G", "end":
		p.sel = max(n-1, 0)
	case "enter":
		it, ok := m.selectedPRItem()
		if !ok {
			return nil
		}
		p.open[it.key] = !p.open[it.key]
		if p.open[it.key] {
			return m.loadFailedLog(it)
		}
	case " ", "space":
		if it, ok := m.selectedPRItem(); ok {
			if p.marked[it.key] {
				delete(p.marked, it.key)
			} else {
				p.marked[it.key] = true
			}
			m.move(&p.sel, n, 1)
		}
	case "s":
		return m.prSend()
	case "o":
		u := ""
		if it, ok := m.selectedPRItem(); ok {
			u = it.url
		}
		if u == "" && p.view != nil {
			u = p.view.URL
		}
		if u == "" {
			return nil
		}
		return func() tea.Msg {
			if err := openURL(u); err != nil {
				return errMsg{err}
			}
			return infoMsg("opened " + u)
		}
	case "R":
		return m.loadPR()
	case "p":
		return m.loadPR()
	case "esc":
		return m.closePR()
	case "h", "left":
		m.focus = pSessions
	case "r", "n":
		cmd, _ := m.common(key)
		return cmd
	case "i", "a":
		cmd := m.closePR()
		m.typing = true
		return cmd
	default:
		return m.sessionAction(key, s)
	}
	return nil
}

// ---- sending to the agent ----

func (m *model) prSend() tea.Cmd {
	s := m.current()
	p := m.pr
	if s == nil || p.view == nil {
		return nil
	}
	if s.PRReview {
		m.setMsg("someone else's PR: nothing to send to an agent here", true)
		return nil
	}
	var picked []prItem
	all := prItems(p.view)
	for _, it := range all {
		if p.marked[it.key] {
			picked = append(picked, it)
		}
	}
	if len(picked) == 0 {
		if it, ok := m.selectedPRItem(); ok {
			picked = []prItem{it}
		}
	}
	var items []prItem
	for _, it := range picked {
		if it.kind == "check" && it.check.Bucket != "fail" {
			continue // only failing checks are worth an agent's time
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		m.setMsg("nothing to send: pick a failed check, a review, a thread or a comment", true)
		return nil
	}
	known := map[string]*prLog{}
	for k, v := range p.logs {
		known[k] = v
	}
	slug, view, cl := s.Slug, p.view, m.cl
	return func() tea.Msg {
		logs := map[string]*prLog{}
		for _, it := range items {
			if it.kind != "check" || it.check.RunID == 0 {
				continue
			}
			if lg := known[it.key]; lg != nil && !lg.loading && lg.err == "" {
				logs[it.key] = lg
				continue
			}
			var fl daemon.FailedLog
			if err := cl.Call("pr.failed_log", map[string]any{"slug": slug, "check": it.check.Name, "run_id": it.check.RunID, "job_id": it.check.JobID}, &fl); err != nil {
				logs[it.key] = &prLog{err: err.Error()}
			} else {
				logs[it.key] = &prLog{text: fl.Log}
			}
		}
		return prSendMsg{slug: slug, text: prPrompt(view, items, logs), n: len(items), logs: logs}
	}
}

func (m *model) onPRSendMsg(msg prSendMsg) tea.Cmd {
	for k, v := range msg.logs {
		if msg.slug == m.pr.slug {
			m.pr.logs[k] = v
		}
	}
	_, s := m.find(msg.slug)
	if s == nil {
		return nil
	}
	what := "1 PR item"
	if msg.n > 1 {
		what = fmt.Sprintf("%d PR items", msg.n)
	}
	slug, text := msg.slug, msg.text
	m.modal = newConfirmModal(m, "Send to agent", "send "+what+" to the agent as a prompt", firstLine(text), false, func() tea.Cmd {
		if m.pr.slug == slug {
			m.pr.marked = map[string]bool{}
		}
		return m.act("session.send", map[string]any{"slug": slug, "text": text, "submit": true}, "sent "+what+" to "+slug)
	}).about(s.Branch)
	return nil
}

// prPrompt is the text the agent gets for the chosen PR items.
func prPrompt(v *daemon.PRView, items []prItem, logs map[string]*prLog) string {
	var parts []string
	for _, it := range items {
		var b strings.Builder
		switch it.kind {
		case "check":
			c := it.check
			fmt.Fprintf(&b, "CI の「%s」が失敗しているので直してください。", c.Name)
			if c.URL != "" {
				b.WriteString("\n" + c.URL)
			}
			if lg := logs[it.key]; lg != nil && lg.err == "" && strings.TrimSpace(lg.text) != "" {
				b.WriteString("\n失敗ログ（末尾）:\n```\n" + strings.TrimRight(cleanText(lg.text), "\n") + "\n```")
			} else if c.RunID > 0 {
				fmt.Fprintf(&b, "\nログは `gh run view %d --log-failed` で見られます。", c.RunID)
			}
		case "thread":
			t := it.thread
			fmt.Fprintf(&b, "このレビューコメントに対応してください。\n%s:%d", t.Path, threadLine(*t))
			if t.Outdated {
				b.WriteString("（outdated: コメント後にコードが変わっています）")
			}
			for _, c := range t.Comments {
				fmt.Fprintf(&b, "\n\n%s:\n%s", c.Author, strings.TrimSpace(cleanText(c.Body)))
			}
		case "review":
			r := it.review
			word, _ := decisionWord(r.State)
			if r.State == "COMMENTED" {
				word = "comment"
			}
			fmt.Fprintf(&b, "%s さんのレビュー（%s）に対応してください。", r.Author, word)
			if body := strings.TrimSpace(cleanText(r.Body)); body != "" {
				b.WriteString("\n\n" + body)
			}
		case "comment":
			c := it.comment
			fmt.Fprintf(&b, "PR へのこのコメントに対応してください。\n\n%s:\n%s", c.Author, strings.TrimSpace(cleanText(c.Body)))
		}
		parts = append(parts, b.String())
	}
	head := ""
	if v != nil {
		head = fmt.Sprintf("PR #%d (%s) について:\n\n", v.Number, v.URL)
		if len(parts) > 1 {
			head = fmt.Sprintf("PR #%d (%s) の次の %d 件に対応してください。\n\n", v.Number, v.URL, len(parts))
		}
	}
	return head + strings.Join(parts, "\n\n---\n\n")
}
