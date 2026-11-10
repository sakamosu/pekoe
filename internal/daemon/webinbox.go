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

	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// webVia is how a web caller is recorded (send log, inbox answer).
func webVia(p *webPeer) string {
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
	writeJSON(w, map[string]any{"items": its, "at": time.Now()})
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
	log.Printf("web: %s answers inbox #%d rev %d, %s, note %d chars (from %s)", p.Login, id, in.Rev, choice, len(in.Note), p.Device)
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
	PRLook     *webLook    `json:"pr_look,omitempty"`
	After      []string    `json:"after,omitempty"`
	Report     string      `json:"report,omitempty"` // the latest report's status (not its words)
	Steps      []webStep   `json:"steps"`
	// Merge is merge.check for a session with its own PR that has not
	// landed: which conditions are missing, with pekoed's detail.
	Merge *state.MergeCheck `json:"merge,omitempty"`
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
	list := res.([]*state.Session)
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
	writeJSON(w, map[string]any{"counts": c, "groups": out, "at": time.Now()})
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
		PR:   s.PR, After: s.After,
	}
	if l, ok := theme.PRLook(theme.WebIcons(), s.PRStatus); ok {
		wl := toWebLook(l)
		f.PRLook = &wl
	}
	if s.Report != nil {
		f.Report = s.Report.Status
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
	// handed over), there is a PR (land pushed it) or it landed
	rv := d.latestReview(s.Slug)
	switch {
	case s.PR > 0 || s.Landing || merged || rv != nil:
		step("impl", stepDone, "")
	case s.State == state.Failed || s.Error != "":
		step("impl", stepFail, firstLine(s.Error))
	case s.Report != nil && s.Report.Status == "blocked":
		step("impl", stepFail, "blocked(報告 #"+strconv.FormatInt(s.Report.ID, 10)+")")
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
		step("review", stepNow, fmt.Sprintf("独立レビューの最中(報告 #%d)", rv.ID))
	case rv != nil && rv.Verdict != "pass":
		step("review", stepFail, fmt.Sprintf("報告 #%d が %s", rv.ID, rv.Verdict))
	case c != nil && rv != nil:
		step("review", stepNow, c.Detail) // passed, but not this HEAD
	case rv != nil:
		step("review", stepDone, fmt.Sprintf("報告 #%d で pass", rv.ID))
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
		step("pr", stepFail, fmt.Sprintf("#%d は閉じられた", s.PR))
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
		step("ci", stepNow, "PR の状態をまだ読んでいない")
	case ps.Fail > 0:
		step("ci", stepFail, fmt.Sprintf("%d 件失敗", ps.Fail))
	case ps.Pending > 0:
		step("ci", stepNow, fmt.Sprintf("%d 件が終わっていない", ps.Pending))
	case ps.Pass > 0:
		step("ci", stepDone, fmt.Sprintf("%d 件すべて成功", ps.Pass))
	default:
		step("ci", stepTodo, "CI のチェックがない")
	}

	// マージ: the person merges (pekoed never does)
	switch {
	case merged:
		step("merge", stepDone, "")
	case f.Merge != nil && f.Merge.Ready:
		step("merge", stepNow, "根拠が揃った: 人のマージ待ち")
	default:
		step("merge", stepTodo, "")
	}
	return f
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return cutRunes(l, 200)
}
