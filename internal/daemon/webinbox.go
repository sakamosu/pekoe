package daemon

// The web UI's inbox and overview (tapi's view is webtapi.go) (docs/orchestration.md
// 「ダッシュボード」→「Web UI」, docs/web.md). Read-only except for the
// inbox answer, which is inbox.answer as the person on the phone: the rev
// they looked at goes with it, and a changed item is refused with 409.
// Everything an agent wrote (the question, its context, options, links)
// is sent as JSON and put into the page with textContent only.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/gitx"
	"pekoe/internal/ids"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// webVia is how a web caller is recorded (send log, inbox answer).
func webVia(p *webPeer) string {
	if p != nil && p.Via != "" {
		return p.Via
	}
	if p != nil && p.Device != "" {
		return viaWeb + ":" + p.Device
	}
	return viaWeb
}

// ---- inbox ----

// webInbox lists the open items, oldest first (the order the TUI's ]a
// goes through them), up to inbox.list's 500 (agents may hold 10 open each). Their facts are read again on the way (inbox.list).
func (d *Daemon) webInbox(w http.ResponseWriter, r *http.Request) {
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "inbox.list", mustJSON(map[string]any{"status": state.InboxOpen, "limit": 500}))
	if err != nil {
		webErr(w, err)
		return
	}
	its := res.([]state.InboxItem)
	sort.SliceStable(its, func(i, j int) bool { return its[i].ID < its[j].ID })
	writeJSON(w, map[string]any{"items": its, "merges": d.recentMerges(time.Now()), "at": time.Now()})
}

const (
	webMergesMax = 5 // the merges under way or through kept in sight
	webMergesFor = 24 * time.Hour
)

// recentMerges are pekoed's merge items a person answered "merge" in the
// last day, newest first: the merge under way, merged, or failed and why
// (the answered item leaves the open list, this keeps it in sight). Every
// failure of the day is kept (Now puts it under NEEDS YOU: it waits on
// the person, so later merges never push it out); the rest, webMergesMax.
func (d *Daemon) recentMerges(now time.Time) []state.InboxItem {
	its, err := d.DB.InboxList(state.InboxFilter{Status: []string{state.InboxAnswered}, From: callerPekoed, Limit: 500})
	out := []state.InboxItem{}
	if err != nil {
		return out
	}
	rest := 0
	for _, it := range its {
		if a := it.Answer; isMergeItem(&it) && a != nil && a.Merge != nil && now.Sub(it.UpdatedAt) < webMergesFor {
			if a.Merge.State != state.MergeFailed {
				if rest == webMergesMax {
					continue
				}
				rest++
			}
			out = append(out, it)
		}
	}
	return out
}

type webInboxAnswerIn struct {
	Choice *int   `json:"choice"`
	Note   string `json:"note"`
	Rev    int64  `json:"rev"`
}

func (d *Daemon) webInboxAnswer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad inbox id", http.StatusBadRequest)
		return
	}
	var in webInboxAnswerIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if in.Rev <= 0 {
		http.Error(w, "rev required (the rev of the item you looked at)", http.StatusBadRequest)
		return
	}
	p := peerFrom(r)
	choice := "no choice"
	if in.Choice != nil {
		choice = fmt.Sprintf("choice %d", *in.Choice+1)
	}
	log.Printf("web: %s answers inbox %s rev %d, %s, note %d chars (from %s)", p.Login, ids.Question(id), in.Rev, choice, len(in.Note), p.Device)
	res, err := d.CallAs(humanCaller(webVia(p)), "inbox.answer", mustJSON(inboxAnswerP{ID: id, Choice: in.Choice, Note: in.Note, Rev: in.Rev}))
	switch {
	case err == nil:
		writeJSON(w, res)
	case errors.Is(err, state.ErrNoInboxItem):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errInboxChanged), errors.Is(err, errInboxClosed):
		http.Error(w, err.Error(), http.StatusConflict)
	case strings.HasPrefix(err.Error(), "inbox.answer:"):
		http.Error(w, err.Error(), http.StatusBadRequest) // a choice out of range, no note…
	default:
		webErr(w, err)
	}
}

// ---- overview: the counts and the flow of every session ----

// Step states in the flow (実装 → レビュー → PR → CI → マージ).
const (
	stepDone = "done" // finished
	stepNow  = "now"  // under way, or waiting on someone
	stepFail = "fail" // something is wrong (CI red, review failed, blocked)
	stepTodo = "todo" // not reached yet
)

type webStep struct {
	Name   string `json:"name"` // impl | pr | ci | review | merge
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// webFlow is one session's row in the flow table.
type webFlow struct {
	Slug       string      `json:"slug"`
	Repo       string      `json:"repo"`
	Branch     string      `json:"branch"`
	State      state.State `json:"state"`
	StateSince time.Time   `json:"state_since"`
	Look       webLook     `json:"look"`
	PR         int         `json:"pr,omitempty"`
	PRURL      string      `json:"pr_url,omitempty"` // the PR on GitHub (prURL)
	PRLook     *webLook    `json:"pr_look,omitempty"`
	After      []string    `json:"after,omitempty"`
	Report     string      `json:"report,omitempty"` // the latest report's status (not its words)
	Steps      []webStep   `json:"steps"`
	// Merge is merge.check for a session with its own PR that has not
	// landed: which conditions are missing, with pekoed's detail.
	Merge *state.MergeCheck `json:"merge,omitempty"`
	// Parent and Origin place the row in the GUI's Pipeline tree: who
	// made the session and the request that made it (0: none). Later are
	// the requests tied to it after that, the one it works on now marked.
	Parent string     `json:"parent"`
	Origin int64      `json:"origin,omitempty"`
	Later  []webLater `json:"later,omitempty"`
	// CI is the PR's checks as counted (none before the PR status is read).
	CI *webCI `json:"ci,omitempty"`
	// Reopened: an instruction after its last report sent the branch,
	// past impl already, back to work, and the agent is at it (impl now).
	Reopened bool `json:"reopened,omitempty"`
}

// webLater is a request tied to a session after it was made. Now: it is
// the request the session works on and it is still open.
type webLater struct {
	ID  int64 `json:"id"`
	Now bool  `json:"now,omitempty"`
}

type webCI struct {
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Pending int `json:"pending"`
}

// webFlowGroup: the sessions one requester (parent) started for one
// request of the ledger.
type webFlowGroup struct {
	Parent  string      `json:"parent"`            // +tapi | a slug | human | unknown | "" (opened directly)
	Request *webRequest `json:"request,omitempty"` // the ledger entry, when the sessions carry one
	Rows    []webFlow   `json:"rows"`
}

type webRequest struct {
	ID     int64  `json:"id"`
	Text   string `json:"text"` // the person's words to tapi, cut to webRequestLen
	Status string `json:"status"`
}

type webCounts struct {
	Running    int `json:"running"`
	Queued     int `json:"queued"` // queue.go: made, waiting for a slot or for --hold
	Waiting    int `json:"waiting"`
	Review     int `json:"review"` // done: waiting for a look
	OpenPRs    int `json:"open_prs"`
	Ready      int `json:"ready"` // PRs with all the merge evidence
	InboxOpen  int `json:"inbox_open"`
	LedgerOpen int `json:"ledger_open"`
}

const webRequestLen = 200

func (d *Daemon) webOverview(w http.ResponseWriter, r *http.Request) {
	res, err := d.Call("session.list", nil)
	if err != nil {
		webErr(w, err)
		return
	}
	c, groups := d.overviewOf(res.([]*state.Session))
	writeJSON(w, map[string]any{"counts": c, "groups": groups, "at": time.Now()})
}

// overviewOf counts list and groups its flows by requester and request
// (GET /api/overview; GET /api/now reads the same). list is sorted in place.
func (d *Daemon) overviewOf(list []*state.Session) (webCounts, []webFlowGroup) {
	checks := map[string]*state.MergeCheck{}
	if res, err := d.Call("merge.check", nil); err == nil {
		for _, c := range res.([]*state.MergeCheck) {
			checks[c.Slug] = c
		}
	}
	var c webCounts
	c.InboxOpen, _ = d.DB.InboxOpenTotal()
	c.LedgerOpen, _ = d.DB.LedgerOpenCount()

	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if ra, rb := webRank(a), webRank(b); ra != rb {
			return ra < rb
		}
		return a.StateSince.After(b.StateSince)
	})
	groups := map[string]*webFlowGroup{}
	var order []*webFlowGroup
	for _, s := range list {
		switch s.State {
		case state.Running, state.Creating:
			c.Running++
		case state.Queued:
			c.Queued++
		case state.Waiting:
			c.Waiting++
		case state.Done:
			c.Review++
		}
		if s.PRReview {
			continue // a review of someone else's PR has no flow of its own
		}
		f := d.webFlowOf(s, checks[s.Slug])
		if f.Merge != nil {
			if ps := s.PRStatus; ps == nil || ps.State == "" || ps.State == "OPEN" {
				c.OpenPRs++
			}
			if f.Merge.Ready {
				c.Ready++
			}
		}
		key := s.Parent + "\x00" + strconv.FormatInt(s.Request, 10)
		g := groups[key]
		if g == nil {
			g = &webFlowGroup{Parent: s.Parent}
			if s.Request > 0 {
				g.Request = &webRequest{ID: s.Request, Status: "gone"}
				if e, err := d.DB.LedgerGet(s.Request); err == nil {
					g.Request.Text, g.Request.Status = cutRunes(e.Text, webRequestLen), e.Status
				}
			}
			groups[key] = g
			order = append(order, g)
		}
		g.Rows = append(g.Rows, f)
	}
	// tapi's requests first, newest request on top; then the other
	// requesters; sessions opened directly last. Inside a group, the
	// order above (waiting, running, the rest).
	parentRank := func(p string) int {
		switch p {
		case TapiSlug:
			return 0
		case "":
			return 3
		case callerHuman, callerUnknown:
			return 2
		}
		return 1
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if ra, rb := parentRank(a.Parent), parentRank(b.Parent); ra != rb {
			return ra < rb
		}
		if a.Parent != b.Parent {
			return a.Parent < b.Parent
		}
		ia, ib := int64(0), int64(0)
		if a.Request != nil {
			ia = a.Request.ID
		}
		if b.Request != nil {
			ib = b.Request.ID
		}
		return ia > ib
	})
	out := make([]webFlowGroup, 0, len(order))
	for _, g := range order {
		out = append(out, *g)
	}
	return c, out
}

func cutRunes(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}

// webFlowOf places s on 実装 → レビュー → PR → CI → マージ, from its
// state, its latest report, its PR and merge.check (mc, nil when it has
// no PR of its own or has landed).
func (d *Daemon) webFlowOf(s *state.Session, mc *state.MergeCheck) webFlow {
	f := webFlow{
		Slug: s.Slug, Repo: s.Repo, Branch: s.Branch, State: s.State, StateSince: s.StateSince,
		Look: toWebLook(theme.Look(theme.WebIcons(), s.State, 0, false)),
		PR:   s.PR, PRURL: d.prURL(s), After: s.After,
	}
	if l, ok := theme.PRLook(theme.WebIcons(), s.PRStatus); ok {
		wl := toWebLook(l)
		f.PRLook = &wl
	}
	if s.Report != nil {
		f.Report = s.Report.Status
	}
	f.Parent, f.Origin = s.Parent, originOf(s)
	for _, id := range s.Later {
		if id == f.Origin {
			continue
		}
		l := webLater{ID: id}
		if id == s.Request {
			if e, err := d.DB.LedgerGet(id); err == nil && e.Status == state.LedgerOpen {
				l.Now = true
			}
		}
		f.Later = append(f.Later, l)
	}
	if ps := s.PRStatus; ps != nil && s.PR > 0 {
		f.CI = &webCI{Pass: ps.Pass, Fail: ps.Fail, Pending: ps.Pending}
	}
	if s.PR > 0 && s.State != state.Landed {
		f.Merge = mc
	}
	ps := s.PRStatus
	merged := s.State == state.Landed || (ps != nil && ps.State == "MERGED")
	cond := func(name string) *state.MergeCond {
		if f.Merge == nil {
			return nil
		}
		for i := range f.Merge.Conds {
			if f.Merge.Conds[i].Name == name {
				return &f.Merge.Conds[i]
			}
		}
		return nil
	}
	step := func(name, status, detail string) {
		f.Steps = append(f.Steps, webStep{Name: name, Status: status, Detail: detail})
	}

	// 実装: done once the independent review was asked for (the change is
	// handed over), there is a PR (land pushed it) or it landed. An
	// instruction after that sends it back (reopenOf).
	rv := d.latestReview(s.Slug)
	ro, head := d.reopenOf(s, merged)
	f.Reopened = ro == reopenWorking
	if ro != reopenNone {
		d.reopenedSteps(&f, s, rv, ro, head)
		return f
	}
	switch {
	case s.PR > 0 || s.Landing || merged || rv != nil:
		step("impl", stepDone, "")
	case s.State == state.Failed || s.Error != "":
		step("impl", stepFail, firstLine(s.Error))
	case s.State == state.Queued:
		step("impl", stepTodo, "queued: "+s.Queue)
	case s.Report != nil && s.Report.Status == "blocked":
		step("impl", stepFail, "blocked (report "+ids.Report(s.Report.ID)+")")
	case s.State == state.Idle || s.State == state.Done:
		// stopped: impl is what the worktree holds (implStopped)
		if st, detail, ok := d.implStopped(s); ok {
			step("impl", st, detail)
		} else {
			step("impl", stepNow, string(s.State))
		}
	default:
		step("impl", stepNow, string(s.State))
	}

	// レビュー: the latest independent review (status review), before the
	// PR as the work goes (pekoe-work). pending is under way. With a PR,
	// a pass counts only for the PR's HEAD or the same change rebased
	// (merge.check's review).
	switch c := cond("review"); {
	case merged:
		step("review", stepDone, "")
	case c != nil && c.OK:
		step("review", stepDone, c.Detail)
	case rv != nil && rv.Verdict == "pending":
		step("review", stepNow, "independent review in progress (report "+ids.Report(rv.ID)+")")
	case rv != nil && rv.Verdict != "pass":
		step("review", stepFail, fmt.Sprintf("report %s is %s", ids.Report(rv.ID), rv.Verdict))
	case c != nil && rv != nil:
		step("review", stepNow, c.Detail) // passed, but not this HEAD
	case rv != nil:
		step("review", stepDone, "pass in report "+ids.Report(rv.ID))
	default:
		step("review", stepTodo, "")
	}

	// PR
	switch {
	case s.PR == 0 && merged:
		step("pr", stepDone, "")
	case s.PR == 0:
		step("pr", stepTodo, "")
	case ps != nil && ps.State == "CLOSED":
		step("pr", stepFail, fmt.Sprintf("#%d was closed", s.PR))
	default:
		step("pr", stepDone, fmt.Sprintf("#%d", s.PR))
	}

	// CI
	switch {
	case s.PR == 0:
		step("ci", stepTodo, "")
	case merged:
		step("ci", stepDone, "")
	case ps == nil:
		step("ci", stepNow, "PR status not read yet")
	case ps.Fail > 0:
		step("ci", stepFail, fmt.Sprintf("%d failed", ps.Fail))
	case ps.Pending > 0:
		step("ci", stepNow, fmt.Sprintf("%d pending", ps.Pending))
	case ps.Pass > 0:
		step("ci", stepDone, fmt.Sprintf("all %d passed", ps.Pass))
	default:
		step("ci", stepTodo, "no CI checks")
	}

	// マージ: the person decides; pekoed merges only on their "merge"
	var run *state.MergeRun
	if f.Merge != nil {
		run = f.Merge.Run
	}
	switch {
	case merged:
		step("merge", stepDone, "")
	case run != nil && run.State == state.MergeRunning && run.Detail != "":
		step("merge", stepNow, run.Detail)
	case run != nil && run.State == state.MergeRunning:
		step("merge", stepNow, "pekoed is merging (checking again first)")
	case run != nil && run.State == state.MergeMerged:
		step("merge", stepNow, "merged by pekoed: landing")
	case run != nil && run.State == state.MergeFailed && (f.Merge == nil || f.Merge.Inbox == 0):
		step("merge", stepFail, run.Detail)
	case f.Merge != nil && f.Merge.Ready:
		step("merge", stepNow, "evidence ready: waiting for a person to merge")
	default:
		step("merge", stepTodo, "")
	}
	return f
}

// How an instruction after the last report moved a branch that was past
// impl (docs/gui-design.md の「Pipeline」).
const (
	reopenNone    = iota // not reopened, or nothing came of it: the steps as they are
	reopenWorking        // the agent is at the instruction: back at impl
	reopenChanged        // it stopped with work the PR lacks: impl done, the rest to do again
)

// reopenOf says whether an instruction (Session.PromptAt) that came when
// the branch was past impl (PromptPast) sent s back to work. While the
// agent runs, waits or starts on it with no report since, that is
// reopenWorking. After that it is the worktree against the PR's head —
// while the head is still the one the instruction found (PromptPRHead; a
// push since puts the PR and its checks in charge again): commits the PR
// lacks or changes not committed are reopenChanged (head: the worktree's
// HEAD), the same is reopenNone (a question answered, nothing changed).
// git runs only for such sessions (status, rev-list and rev-parse, as
// merge.check's settled does); when it cannot tell, the steps stay as
// they are.
func (d *Daemon) reopenOf(s *state.Session, merged bool) (ro int, head string) {
	if merged || !s.PromptPast || s.PRReview {
		return reopenNone, ""
	}
	prompted := s.Report == nil || s.PromptAt.After(s.Report.At)
	if prompted && agentAtWork(s) {
		return reopenWorking, ""
	}
	ps := s.PRStatus
	if s.PR == 0 || ps == nil || s.PromptPRHead == "" || ps.HeadSHA != s.PromptPRHead {
		return reopenNone, ""
	}
	n, err := d.worktreeChanges(s)
	if err != nil {
		return reopenNone, ""
	}
	c, err := d.commitsBeyondPR(s)
	if err != nil || n == 0 && c == 0 {
		return reopenNone, ""
	}
	head, _ = gitx.RevParse(s.Path, "HEAD")
	return reopenChanged, head
}

// agentAtWork: the agent runs, waits (a permission, a question) or is
// starting.
func agentAtWork(s *state.Session) bool {
	switch s.State {
	case state.Running, state.Waiting, state.Creating:
		return true
	}
	return false
}

// reopenedSteps fills f's steps for a reopened branch: impl now
// (reopenWorking) or done (reopenChanged), the PR and its checks to do
// again (the PR's number stays on its box; the old checks are of the
// head before), the merge to come. The review counts only when it was
// asked for after the instruction, and its pass only for head.
func (d *Daemon) reopenedSteps(f *webFlow, s *state.Session, rv *state.Report, ro int, head string) {
	step := func(name, status, detail string) {
		f.Steps = append(f.Steps, webStep{Name: name, Status: status, Detail: detail})
	}
	if ro == reopenWorking {
		step("impl", stepNow, string(s.State)+": back at work after an instruction")
		step("review", stepTodo, "")
	} else {
		step("impl", stepDone, "work the PR lacks (commits or changes not committed)")
		switch {
		case rv == nil || !rv.At.After(s.PromptAt):
			step("review", stepTodo, "to be asked again for the new work")
		case rv.Verdict == "pending":
			step("review", stepNow, "independent review in progress (report "+ids.Report(rv.ID)+")")
		case rv.Verdict != "pass":
			step("review", stepFail, fmt.Sprintf("report %s is %s", ids.Report(rv.ID), rv.Verdict))
		case rv.ReviewedSHA == "" || head == "" || !strings.HasPrefix(head, rv.ReviewedSHA):
			step("review", stepNow, fmt.Sprintf("pass in report %s is of %s, not HEAD %s", ids.Report(rv.ID), shortSHA(rv.ReviewedSHA), shortSHA(head)))
		default:
			step("review", stepDone, "pass in report "+ids.Report(rv.ID))
		}
	}
	switch ps := s.PRStatus; {
	case s.PR == 0:
		step("pr", stepTodo, "")
	case ps != nil && ps.State == "CLOSED":
		step("pr", stepFail, fmt.Sprintf("#%d was closed", s.PR))
	default:
		step("pr", stepTodo, fmt.Sprintf("#%d: to be pushed again (pekoe land)", s.PR))
	}
	ci := ""
	if s.PR > 0 {
		ci = "the checks so far are of the PR's head before"
	}
	step("ci", stepTodo, ci)
	step("merge", stepTodo, "")
}

// implStopped is impl for a branch not past it whose agent has stopped
// (idle or done): done when the worktree has work (changes not committed,
// or commits beyond its base: origin/<base>), todo when it has none (a
// checkout of main left as it was). ok false: git could not tell. The
// answer is kept for a little while (stoppedTTL) and for as long as the
// session's state and diffstat stay the same, so the dashboards' reads do
// not run git each time.
func (d *Daemon) implStopped(s *state.Session) (status, detail string, ok bool) {
	key := stoppedKey{since: s.StateSince, stat: s.Diffstat, head: s.Branch}
	if v, hit := d.stopped.Load(s.Slug); hit {
		e := v.(stoppedEntry)
		if e.key == key && time.Since(e.at) < stoppedTTL {
			return e.status, e.detail, true
		}
	}
	n, err := d.worktreeChanges(s)
	if err != nil {
		return "", "", false
	}
	c, err := d.commitsBeyondBase(s)
	if err != nil {
		return "", "", false
	}
	status, detail = stepTodo, "no changes: nothing beyond the base, nothing uncommitted"
	if n > 0 || c > 0 {
		status, detail = stepDone, fmt.Sprintf("agent %s with work: %d commit%s beyond the base, %d uncommitted or untracked", s.State, c, plural(c), n)
	}
	d.stopped.Store(s.Slug, stoppedEntry{key: key, at: time.Now(), status: status, detail: detail})
	return status, detail, true
}

const stoppedTTL = 15 * time.Second

type stoppedKey struct {
	since time.Time
	stat  state.Stat
	head  string
}

type stoppedEntry struct {
	key            stoppedKey
	at             time.Time
	status, detail string
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return cutRunes(l, 200)
}
