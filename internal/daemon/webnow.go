package daemon

// The phone's Now screen and the Sessions headings (docs/web.md 「Now」
// 「Sessions」). Read only: what a person should look at, gathered from
// the same reads as the overview and the inbox, so the page only draws.
//   - needs: what waits on the person, one card each: a merge pekoed could
//     not do, a session waiting on them, an open inbox item
//   - counts: the rest, as numbers (a 0 is one dim line on the page)
//   - requests: the ledger's requests with their sessions, newest first
//   - merged: pekoed's merges that went through (the failed ones are needs)

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"pekoe/internal/state"
)

// Sessions headings (webSession.Group).
const (
	groupActive = "active" // running, waiting, starting, queued, under review
	groupReview = "review" // done (or failed, approved): a person looks next
	groupIdle   = "idle"
	groupLanded = "landed"
)

// webGroup is the heading a session in s goes under on the phone's
// Sessions screen.
func webGroup(s state.State) string {
	switch s {
	case state.Done, state.Failed, state.Approved:
		return groupReview
	case state.Idle:
		return groupIdle
	case state.Landed:
		return groupLanded
	}
	return groupActive
}

// Kinds of a need (webNeed.Kind).
const (
	needMergeFailed = "merge_failed" // pekoed answered "merge" and it did not go through
	needWaiting     = "waiting"      // a session waits on a permission or a question
	needInbox       = "inbox"        // an open inbox item (a question, a merge to approve…)
)

// webNeed is one card under NEEDS YOU.
type webNeed struct {
	Kind  string    `json:"kind"`
	Since time.Time `json:"since"`
	// Item: the inbox item (merge_failed: the answered merge item, its
	// answer.merge says why; inbox: the open item, as GET /api/inbox).
	Item *state.InboxItem `json:"item,omitempty"`
	// Session: the waiting session; for merge_failed the session the PR
	// is from, when it is still there (its branch).
	Session *webSession `json:"session,omitempty"`
}

// webNowCounts are the numbers under the cards.
type webNowCounts struct {
	Questions int `json:"questions"` // open inbox items other than pekoed's merge approvals
	Ready     int `json:"ready"`     // pekoed asks whether to merge (open merge items)
	Running   int `json:"running"`
	Queued    int `json:"queued"`
	Waiting   int `json:"waiting"`
	Review    int `json:"review"` // the TO REVIEW heading of Sessions
}

// webNowRequest is one request card: the ledger entry and the session
// working on it (the first in the overview's order), with its flow.
type webNowRequest struct {
	ID         int64       `json:"id"`
	Text       string      `json:"text"`
	Status     string      `json:"status"`
	Slug       string      `json:"slug"`
	Branch     string      `json:"branch"`
	Repo       string      `json:"repo"`
	State      state.State `json:"state"`
	StateSince time.Time   `json:"state_since"`
	Look       webLook     `json:"look"`
	Sessions   int         `json:"sessions"` // how many sessions work on it
	Steps      []webStep   `json:"steps"`
}

// webMerged is one merge pekoed did on a person's "merge".
type webMerged struct {
	PR    int       `json:"pr"`
	Item  int64     `json:"item"`
	About string    `json:"about,omitempty"`
	At    time.Time `json:"at"`
}

type webNow struct {
	Needs    []webNeed       `json:"needs"`
	Counts   webNowCounts    `json:"counts"`
	Requests []webNowRequest `json:"requests"`
	// RequestsOpen: the ledger's open requests (the "5 open" on the page)
	RequestsOpen int         `json:"requests_open"`
	Merged       []webMerged `json:"merged"`
	At           time.Time   `json:"at"`
}

const webNowRequestsMax = 20

func (d *Daemon) webNow(w http.ResponseWriter, r *http.Request) {
	res, err := d.Call("session.list", nil)
	if err != nil {
		webErr(w, err)
		return
	}
	list := res.([]*state.Session)
	ires, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "inbox.list", mustJSON(map[string]any{"status": state.InboxOpen, "limit": 500}))
	if err != nil {
		webErr(w, err)
		return
	}
	now := time.Now()
	n := d.nowOf(list, ires.([]state.InboxItem), d.recentMerges(now))
	n.RequestsOpen, _ = d.DB.LedgerOpenCount()
	n.At = now
	writeJSON(w, n)
}

// nowOf gathers the Now screen from the sessions, the open inbox items and
// pekoed's recent merges (newest first, recentMerges).
func (d *Daemon) nowOf(list []*state.Session, open []state.InboxItem, merges []state.InboxItem) webNow {
	n := webNow{Needs: []webNeed{}, Requests: []webNowRequest{}, Merged: []webMerged{}}
	bySlug := map[string]*state.Session{}
	for _, s := range list {
		bySlug[s.Slug] = s
	}

	// 1. merges that did not go through, newest first
	for i := range merges {
		it := merges[i]
		m := it.Answer.Merge
		switch {
		case m.State == state.MergeFailed && !mergedAfterAll(m, bySlug[it.About]):
			need := webNeed{Kind: needMergeFailed, Since: m.At, Item: &it}
			if s := bySlug[it.About]; s != nil {
				ws := toWebSession(s)
				need.Session = &ws
			}
			n.Needs = append(n.Needs, need)
		case m.State == state.MergeMerged, m.State == state.MergeFailed:
			n.Merged = append(n.Merged, webMerged{PR: m.PR, Item: it.ID, About: it.About, At: m.At})
		}
	}

	sort.SliceStable(n.Needs, func(i, j int) bool { return n.Needs[i].Since.After(n.Needs[j].Since) })
	sort.SliceStable(n.Merged, func(i, j int) bool { return n.Merged[i].At.After(n.Merged[j].At) })

	// 2. sessions waiting on the person, the oldest wait first
	var waiting []*state.Session
	for _, s := range list {
		switch s.State {
		case state.Waiting:
			if !s.Stopped {
				waiting = append(waiting, s)
			}
			n.Counts.Waiting++
		case state.Running, state.Creating:
			n.Counts.Running++
		case state.Queued:
			n.Counts.Queued++
		}
		if webGroup(s.State) == groupReview {
			n.Counts.Review++
		}
	}
	sort.SliceStable(waiting, func(i, j int) bool { return waiting[i].StateSince.Before(waiting[j].StateSince) })
	for _, s := range waiting {
		ws := toWebSession(s)
		n.Needs = append(n.Needs, webNeed{Kind: needWaiting, Since: s.StateSince, Session: &ws})
	}

	// 3. the open inbox, oldest first (the order ]a goes through them)
	sort.SliceStable(open, func(i, j int) bool { return open[i].ID < open[j].ID })
	for i := range open {
		it := open[i]
		if isMergeItem(&it) {
			n.Counts.Ready++
		} else {
			n.Counts.Questions++
		}
		n.Needs = append(n.Needs, webNeed{Kind: needInbox, Since: it.CreatedAt, Item: &it})
	}

	// requests: the overview's groups that carry one, newest request first
	_, groups := d.overviewOf(list)
	for _, g := range groups {
		if g.Request == nil || len(g.Rows) == 0 {
			continue
		}
		f := g.Rows[0]
		n.Requests = append(n.Requests, webNowRequest{
			ID: g.Request.ID, Text: g.Request.Text, Status: g.Request.Status,
			Slug: f.Slug, Branch: f.Branch, Repo: f.Repo, State: f.State, StateSince: f.StateSince,
			Look: f.Look, Sessions: len(g.Rows), Steps: f.Steps,
		})
	}
	// the same request can come from two requesters (a group each): one card
	sort.SliceStable(n.Requests, func(i, j int) bool { return n.Requests[i].ID > n.Requests[j].ID })
	out := n.Requests[:0]
	for i, rq := range n.Requests {
		if i > 0 && out[len(out)-1].ID == rq.ID {
			out[len(out)-1].Sessions += rq.Sessions
			continue
		}
		out = append(out, rq)
	}
	n.Requests = out
	if len(n.Requests) > webNowRequestsMax {
		n.Requests = n.Requests[:webNowRequestsMax]
	}
	return n
}

// mergedAfterAll: pekoed's merge of m.PR failed, but the PR is merged
// anyway (a person merged it on GitHub first, or it landed): nothing waits
// on the person, so it goes with the merges, not under NEEDS YOU. Told by
// pekoed's own words for it (the land condition read "PR #n is MERGED"
// when it checked again, merge.go) or by the session (s, nil when gone):
// landed, or the same PR read as MERGED.
func mergedAfterAll(m *state.MergeRun, s *state.Session) bool {
	if strings.Contains(m.Detail, fmt.Sprintf("PR #%d is MERGED", m.PR)) {
		return true
	}
	if s == nil || s.PR != m.PR {
		return false
	}
	return s.State == state.Landed || (s.PRStatus != nil && s.PRStatus.State == "MERGED")
}
