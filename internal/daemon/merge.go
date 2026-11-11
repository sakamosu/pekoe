package daemon

// Merge evidence (docs/orchestration.md 段階 3「マージしてよいかの根拠」):
// pekoed looks at the sessions it landed (pushed, PR open, waiting for a
// person to merge) and, once every condition holds, puts one item in the
// inbox, from pekoed, kind approve: "evidence ready to merge #42".
// The conditions are a fixed computation over what pekoed already keeps:
//
//   - land: pekoe land made the PR (Session.landing), and it is open
//   - review: the latest independent review (report.post --status review)
//     passed, and the commit it names is the PR's head on GitHub, or a
//     commit whose change, put onto the head's base, gives the head's tree to
//     the byte (land rebased it: sameChange). A pending review (asked for, under way)
//     does not count
//   - ci: every check of the PR's head passed (none failed, none running,
//     at least one)
//   - merge_state: GitHub's mergeStateStatus is CLEAN
//   - conflicts: the conflict forecast sees no conflict with another session
//   - after: every session this one comes after (pekoe new --after) landed
//
// When one stops holding, or the PR's head moves, the item is withdrawn.
// One PR and head get one item: once a person answered it (or closed it),
// pekoed does not ask again until the head moves; an item pekoed withdrew
// itself is asked again when the conditions come back.
//
// pekoed merges nothing (決まったこと 1): the answers are "merged" (the
// person merged it on GitHub; the PR poller notices), "wait", and "send back"
// with a note, which is typed into the session's window as an inbox answer
// is (inbox.go: idle or done, no permission prompt, checked twice).
//
// What is still missing, per session, is merge.check: the TUI dashboard and
// the Web UI's progress view read it.

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// The answers to a merge item, by index.
const (
	mergeMerged   = 0
	mergeWait     = 1
	mergeSendBack = 2
)

var mergeOptions = []string{"merged", "wait", "send back"}

// mergeable: the session has a PR of its own that has not been merged yet
// (the merge conditions are about it).
func mergeable(s *state.Session) bool {
	return s.PR > 0 && !s.PRReview && s.State != state.Landed
}

// isMergeItem: an item pekoed made itself (v1: only the merge evidence).
func isMergeItem(it *state.InboxItem) bool {
	return it.From == callerPekoed && it.Kind == "approve"
}

// mergeSentBack: a person answered a merge item with "send back"; the note
// goes to the session.
func mergeSentBack(it *state.InboxItem) bool {
	a := it.Answer
	return isMergeItem(it) && it.Status == state.InboxAnswered && a != nil && a.Choice != nil && *a.Choice == mergeSendBack
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// mergeCheck reads s against the merge conditions.
func (d *Daemon) mergeCheck(s *state.Session) *state.MergeCheck {
	c := &state.MergeCheck{Slug: s.Slug, PR: s.PR, Conds: []state.MergeCond{}, Missing: []string{}}
	ps := s.PRStatus
	if ps != nil {
		c.PRHead = ps.HeadSHA
	}
	add := func(name string, ok bool, format string, args ...any) {
		c.Conds = append(c.Conds, state.MergeCond{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)})
		if !ok {
			c.Missing = append(c.Missing, name)
		}
	}

	switch {
	case s.PRReview:
		add("land", false, "reviewing someone else's PR (not this session's)")
	case s.State == state.Landed:
		add("land", false, "landed (merged)")
	case s.PR == 0:
		add("land", false, "no PR (pekoe land not run yet)")
	case !s.Landing:
		add("land", false, "PR #%d exists but pekoe land not run yet", s.PR)
	case ps != nil && ps.State != "" && ps.State != "OPEN":
		add("land", false, "PR #%d is %s", s.PR, ps.State)
	default:
		add("land", true, "pekoe land done (PR #%d)", s.PR)
	}

	rv := d.latestReview(s.Slug)
	switch {
	case rv == nil:
		add("review", false, "no independent review reported")
	case rv.Verdict == "pending":
		add("review", false, "independent review in progress (report #%d · %s)", rv.ID, shortSHA(rv.ReviewedSHA))
	case rv.Verdict != "pass":
		add("review", false, "latest review (report #%d) is %s", rv.ID, rv.Verdict)
	case rv.ReviewedSHA == "":
		add("review", false, "latest review (report #%d) has no reviewed_sha", rv.ID)
	case c.PRHead == "":
		add("review", false, "PR HEAD not read yet (review is of %s)", shortSHA(rv.ReviewedSHA))
	case !strings.HasPrefix(c.PRHead, rv.ReviewedSHA):
		switch d.sameChange(s, rv.ReviewedSHA, c.PRHead) {
		case changeSame:
			add("review", true, "pass in report #%d (%s · %s); PR HEAD %s is a rebase with the same diff", rv.ID, shortSHA(rv.ReviewedSHA), reviewBy(s, rv), shortSHA(c.PRHead))
		case changeDiffers:
			c.ReviewStale = true
			add("review", false, "diff changed from reviewed %s to PR HEAD %s (needs a new review)", shortSHA(rv.ReviewedSHA), shortSHA(c.PRHead))
		default:
			c.ReviewStale = true
			add("review", false, "reviewed %s differs from PR HEAD %s (could not compare the diffs)", shortSHA(rv.ReviewedSHA), shortSHA(c.PRHead))
		}
	default:
		add("review", true, "pass in report #%d (%s · %s)", rv.ID, shortSHA(rv.ReviewedSHA), reviewBy(s, rv))
	}

	switch {
	case ps == nil:
		add("ci", false, "PR status not read yet")
	case ps.Fail > 0:
		add("ci", false, "%d failed", ps.Fail)
	case ps.Pending > 0:
		add("ci", false, "%d pending", ps.Pending)
	case ps.Pass == 0:
		add("ci", false, "no CI checks")
	default:
		add("ci", true, "all %d passed", ps.Pass)
	}

	switch {
	case ps == nil:
		add("merge_state", false, "PR status not read yet")
	case ps.MergeState != "CLEAN":
		ms := ps.MergeState
		if ms == "" {
			ms = "unknown"
		}
		add("merge_state", false, "merge_state is %s", ms)
	default:
		add("merge_state", true, "CLEAN")
	}

	var conflicts, overlaps []string
	for _, o := range s.Overlaps {
		if o.Conflict {
			files := o.ConflictFiles
			if len(files) == 0 {
				files = o.Files
			}
			conflicts = append(conflicts, fmt.Sprintf("%s(%s)", o.Slug, strings.Join(files, ", ")))
		} else {
			overlaps = append(overlaps, o.Slug)
		}
	}
	switch {
	case len(conflicts) > 0:
		add("conflicts", false, "conflicts forecast: %s", strings.Join(conflicts, "; "))
	case len(overlaps) > 0:
		add("conflicts", true, "no conflicts forecast (overlaps %s, merges cleanly)", strings.Join(overlaps, ", "))
	default:
		add("conflicts", true, "no conflicts forecast")
	}

	var notYet []string
	for _, dep := range s.After {
		ds, err := d.snapshot(dep)
		switch {
		case err != nil:
			notYet = append(notYet, dep+" is gone")
		case ds.State != state.Landed:
			notYet = append(notYet, fmt.Sprintf("%s is %s", dep, ds.State))
		}
	}
	switch {
	case len(notYet) > 0:
		add("after", false, "%s", strings.Join(notYet, "; "))
	case len(s.After) > 0:
		add("after", true, "%s landed", strings.Join(s.After, ", "))
	default:
		add("after", true, "no sessions to land first")
	}

	c.Ready = len(c.Missing) == 0
	return c
}

func reviewBy(s *state.Session, rv *state.Report) string {
	if rv.By == s.Slug {
		return "reviewed in the author's session"
	}
	return "reviewed by " + rv.By
}

// What sameChange found.
const (
	changeUnknown = iota // git could not tell (a commit is not here, the worktree is gone)
	changeSame
	changeDiffers
)

// sameChange: does head change the same as reviewed, against the base the
// PR goes into? pekoe land rebases onto origin/<base>, which gives the PR
// a new head though nothing the review read changed; gitx.SameChange
// tells that apart from a head that changes more, less or elsewhere: the
// reviewed change put onto the head's base must give the head's tree. A known answer is kept per pair of commits: neither the
// commits nor their merge-bases with the base move.
func (d *Daemon) sameChange(s *state.Session, reviewed, head string) int {
	key := s.RepoID + " " + reviewed + " " + head
	if v, ok := d.changeSame.Load(key); ok {
		return v.(int)
	}
	if s.Path == "" || !d.checkGitDir(s) {
		return changeUnknown
	}
	base := s.Base
	if base == "" {
		base = gitx.DefaultBase(s.Path)
	}
	if gitx.CheckRefArg("base", base) != nil {
		return changeUnknown
	}
	if _, err := gitx.RevParse(s.Path, "origin/"+base); err == nil {
		base = "origin/" + base
	}
	same, err := gitx.SameChange(s.Path, reviewed, head, base)
	if err != nil {
		return changeUnknown
	}
	r := changeDiffers
	if same {
		r = changeSame
	}
	d.changeSame.Store(key, r)
	return r
}

// mergeDetails is the one line of what is missing in c.
func mergeDetails(c *state.MergeCheck) string {
	var out []string
	for _, x := range c.Conds {
		if !x.OK {
			out = append(out, x.Name+": "+x.Detail)
		}
	}
	return strings.Join(out, " / ")
}

// mergeRecord is what pekoed last asked about for a session (kv): the PR,
// its head and the item. It keeps one PR and head to one item across
// restarts.
type mergeRecord struct {
	PR   int    `json:"pr"`
	Head string `json:"head"`
	ID   int64  `json:"id"`
}

func mergeKey(slug string) string { return "merge-evidence/" + slug }

func (d *Daemon) mergeRecordOf(slug string) mergeRecord {
	var r mergeRecord
	if v := d.DB.Get(mergeKey(slug)); v != "" {
		_ = json.Unmarshal([]byte(v), &r)
	}
	return r
}

// checkMerges brings the merge items in line with the sessions (the tick):
// asks for the sessions whose evidence is all there, withdraws the items
// whose evidence is not any more.
func (d *Daemon) checkMerges() {
	if !d.mergeTick.CompareAndSwap(false, true) {
		return
	}
	defer d.mergeTick.Store(false)
	d.mu.Lock()
	var landing []state.Session
	for _, s := range d.sessions {
		if s.Landing && s.PR > 0 && !s.PRReview {
			landing = append(landing, *s)
		}
	}
	d.mu.Unlock()
	sort.Slice(landing, func(i, j int) bool { return landing[i].Slug < landing[j].Slug })

	its, err := d.DB.InboxList(state.InboxFilter{Status: []string{state.InboxOpen}, From: callerPekoed})
	if err != nil {
		log.Printf("merge evidence: %v", err)
		return
	}
	open := map[string]*state.InboxItem{}
	for i := range its {
		if isMergeItem(&its[i]) {
			open[its[i].About] = &its[i]
		}
	}
	for i := range landing {
		s := &landing[i]
		d.syncMerge(s, open[s.Slug])
		delete(open, s.Slug)
	}
	// items about sessions that are not waiting for a merge any more
	for slug, it := range open {
		note := "no longer after pekoe land"
		if s, err := d.snapshot(slug); err != nil {
			note = "session is gone"
		} else if s.State == state.Landed {
			note = "PR merged (landed)"
		}
		d.withdrawMerge(it.ID, note)
	}
}

// syncMerge asks about s when its evidence is all there, and withdraws
// it, its open item, when not (or when the PR's head moved).
func (d *Daemon) syncMerge(s *state.Session, it *state.InboxItem) {
	c := d.mergeCheck(s)
	rec := d.mergeRecordOf(s.Slug)
	if it != nil {
		switch {
		case c.PR != rec.PR || c.PRHead != rec.Head:
			if c.PRHead != rec.Head {
				d.withdrawMerge(it.ID, fmt.Sprintf("PR HEAD moved (%s → %s)", shortSHA(rec.Head), shortSHA(c.PRHead)))
			} else {
				d.withdrawMerge(it.ID, fmt.Sprintf("PR changed (#%d → #%d)", rec.PR, c.PR))
			}
		case !c.Ready:
			d.withdrawMerge(it.ID, "evidence no longer holds: "+mergeDetails(c))
			return
		default:
			return // still holds
		}
	}
	if !c.Ready {
		return
	}
	if rec.ID != 0 && rec.PR == c.PR && rec.Head == c.PRHead {
		// asked about this PR and head already: ask again only if pekoed
		// withdrew that item itself (a condition broke and came back)
		prev, err := d.DB.InboxGet(rec.ID)
		if err == nil && !(prev.Status == state.InboxWithdrawn && prev.Answer != nil && prev.Answer.By == callerPekoed) {
			return
		}
	}
	d.askMerge(s, c)
}

// askMerge puts the merge item for s in the inbox.
func (d *Daemon) askMerge(s *state.Session, c *state.MergeCheck) {
	now := time.Now()
	it := &state.InboxItem{
		Kind: "approve", From: callerPekoed, About: s.Slug,
		Question: fmt.Sprintf("evidence ready to merge #%d (%s · HEAD %s)", c.PR, s.Slug, shortSHA(c.PRHead)),
		Options:  append([]string(nil), mergeOptions...),
		Links:    []string{},
		Rev:      1, Status: state.InboxOpen, Delivery: state.DeliveryNone, CreatedAt: now, UpdatedAt: now,
	}
	it.Facts = d.inboxFacts(s.Slug)
	if it.Facts.PRURL != "" {
		it.Links = append(it.Links, it.Facts.PRURL)
	}
	var b strings.Builder
	b.WriteString("conditions pekoed checked:\n")
	for _, x := range c.Conds {
		fmt.Fprintf(&b, "- %s: %s\n", x.Name, x.Detail)
	}
	b.WriteString("pekoed does not merge: a person merges on GitHub. \"send back\" sends the note to the session")
	it.Context = b.String()
	d.inboxMu.Lock()
	err := d.DB.AddInbox(it)
	if err == nil {
		v, _ := json.Marshal(mergeRecord{PR: c.PR, Head: c.PRHead, ID: it.ID})
		err = d.DB.Set(mergeKey(s.Slug), string(v))
	}
	d.inboxMu.Unlock()
	if err != nil {
		log.Printf("merge evidence %s: %v", s.Slug, err)
		return
	}
	d.emit("inbox.updated", it)
}

// withdrawMerge closes pekoed's own open item with why.
func (d *Daemon) withdrawMerge(id int64, note string) {
	it, err := d.closeInbox(id, "merge evidence", func(it *state.InboxItem) error {
		it.Status = state.InboxWithdrawn
		it.Answer = &state.InboxAnswer{Note: note, By: callerPekoed, Via: callerPekoed, At: time.Now()}
		return nil
	})
	if err != nil {
		return // answered or closed in the meantime
	}
	d.emit("inbox.updated", it)
}

type mergeCheckP struct {
	Slug string `json:"slug"`
}

// hMergeCheck lists, per session that has not landed, which merge
// conditions hold and which do not (with slug: that session only).
func (d *Daemon) hMergeCheck(p mergeCheckP) (any, error) {
	var ss []state.Session
	if p.Slug != "" {
		s, err := d.snapshot(p.Slug)
		if err != nil {
			return nil, err
		}
		ss = append(ss, *s)
	} else {
		d.mu.Lock()
		for _, s := range d.sessions {
			if s.State != state.Landed && !s.PRReview {
				ss = append(ss, *s)
			}
		}
		d.mu.Unlock()
		sort.Slice(ss, func(i, j int) bool { return ss[i].Slug < ss[j].Slug })
	}
	open := map[string]int64{}
	if its, err := d.DB.InboxList(state.InboxFilter{Status: []string{state.InboxOpen}, From: callerPekoed}); err == nil {
		for i := range its {
			if isMergeItem(&its[i]) {
				open[its[i].About] = its[i].ID
			}
		}
	}
	out := []*state.MergeCheck{}
	for i := range ss {
		c := d.mergeCheck(&ss[i])
		c.Inbox = open[ss[i].Slug]
		out = append(out, c)
	}
	return out, nil
}
