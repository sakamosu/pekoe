package daemon

// Conflict forecast: open sessions of the same repository that touch the
// same files are told so before they collide at land time. What a session
// touches is its change set from base (committed + uncommitted + untracked)
// plus the file its pending Edit/Write is about to change. Pairs whose
// committed contents overlap are test-merged with git merge-tree, which
// touches no worktree, index or ref, to tell "auto-merges" from "conflicts".

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// ConflictPair is one entry of conflicts.list.
type ConflictPair struct {
	RepoID        string   `json:"repo_id"`
	A             string   `json:"a"`
	B             string   `json:"b"`
	Status        string   `json:"status"` // overlap (auto-merges) | conflict
	Files         []string `json:"files"`  // touched by both
	ConflictFiles []string `json:"conflict_files,omitempty"`
}

type mergeResult struct {
	clean     bool
	conflicts []string
}

// conflictScan is the scanner's state. mu serializes scans; changed is read
// by hooks (PreToolUse) and guarded by Daemon.mu.
type conflictScan struct {
	mu       sync.Mutex
	running  atomic.Bool
	lastKick atomic.Int64
	merges   map[string]mergeResult // "<head a> <head b>" -> merge-tree result
	pairs    []ConflictPair
	scanned  time.Time                  // when pairs was computed (under mu)
	changed  map[string]map[string]bool // slug -> changed files at the last scan (under Daemon.mu)
}

type touchSet struct {
	s         *state.Session
	head      string
	changed   map[string]bool // vs base, committed + uncommitted
	committed map[string]bool // base..HEAD
	pending   string          // PendingTool.Touches
}

func (t *touchSet) touches(p string) bool { return t.changed[p] || (t.pending != "" && t.pending == p) }

// forecastable: landed and stopped sessions are out of the race; creating
// ones have no worktree yet.
func forecastable(s *state.Session) bool {
	return s.State != state.Landed && s.State != state.Creating && !s.Stopped && s.Path != ""
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func (d *Daemon) touchSetOf(s *state.Session) *touchSet {
	base, _ := d.baseCommit(s, 0)
	changed, err := gitx.ChangedFiles(s.Path, base)
	if err != nil {
		return nil
	}
	t := &touchSet{s: s, changed: toSet(changed), committed: map[string]bool{}}
	if head, err := gitx.RevParse(s.Path, "HEAD"); err == nil {
		t.head = head
		if names, err := gitx.DiffNames(s.Path, base, head); err == nil {
			t.committed = toSet(names)
		}
	}
	if s.PendingTool != nil && s.PendingTool.Touches != "" && !strings.HasPrefix(s.PendingTool.Touches, "/") {
		t.pending = s.PendingTool.Touches
	}
	return t
}

// merge test-merges two heads, cached by the pair of commits.
func (d *Daemon) merge(dir, a, b string) (mergeResult, bool) {
	if a > b {
		a, b = b, a
	}
	key := a + " " + b
	if r, ok := d.cf.merges[key]; ok {
		return r, true
	}
	clean, files, err := gitx.MergeTree(dir, a, b)
	if err != nil {
		return mergeResult{}, false // old git or broken refs: overlap only
	}
	if d.cf.merges == nil || len(d.cf.merges) > 512 {
		d.cf.merges = map[string]mergeResult{}
	}
	r := mergeResult{clean: clean, conflicts: files}
	d.cf.merges[key] = r
	return r, true
}

func pairOf(a, b *touchSet, d *Daemon) *ConflictPair {
	var files, both []string
	for p := range a.changed {
		if b.touches(p) {
			files = append(files, p)
		}
		if a.committed[p] && b.committed[p] {
			both = append(both, p)
		}
	}
	if a.pending != "" && !a.changed[a.pending] && b.touches(a.pending) {
		files = append(files, a.pending)
	}
	if len(files) == 0 {
		return nil
	}
	sort.Strings(files)
	cp := &ConflictPair{RepoID: a.s.RepoID, A: a.s.Slug, B: b.s.Slug, Status: "overlap", Files: files}
	// only committed contents are test-merged; uncommitted work is overlap only
	if len(both) > 0 && a.head != "" && b.head != "" && a.head != b.head {
		dir := a.s.RepoDir
		if dir == "" {
			dir = a.s.Path
		}
		if r, ok := d.merge(dir, a.head, b.head); ok && !r.clean {
			cp.Status = "conflict"
			cp.ConflictFiles = r.conflicts
		}
	}
	return cp
}

// scanConflicts recomputes every pair, stores the forecast on the sessions
// (session.updated only for those whose forecast changed) and returns the
// pairs of repoID ("" for all).
func (d *Daemon) scanConflicts(repoID string) []ConflictPair {
	d.cf.mu.Lock()
	defer d.cf.mu.Unlock()
	return d.scanConflictsLocked(repoID)
}

// scanConflictsLocked is scanConflicts with d.cf.mu held.
func (d *Daemon) scanConflictsLocked(repoID string) []ConflictPair {
	d.mu.Lock()
	var open []*state.Session
	var stale []string // out of the race but still carrying a forecast
	for _, s := range d.sessions {
		if forecastable(s) {
			c := *s
			open = append(open, &c)
		} else if len(s.Overlaps) > 0 || (s.PendingTool != nil && len(s.PendingTool.AlsoIn) > 0) {
			stale = append(stale, s.Slug)
		}
	}
	d.mu.Unlock()
	sort.Slice(open, func(i, j int) bool { return open[i].Slug < open[j].Slug })

	sets := map[string]*touchSet{}
	changed := map[string]map[string]bool{}
	for _, s := range open {
		if t := d.touchSetOf(s); t != nil {
			sets[s.Slug] = t
			changed[s.Slug] = t.changed
		}
	}
	var pairs []ConflictPair
	for i, a := range open {
		for _, b := range open[i+1:] {
			ta, tb := sets[a.Slug], sets[b.Slug]
			if ta == nil || tb == nil || a.RepoID != b.RepoID {
				continue
			}
			if cp := pairOf(ta, tb, d); cp != nil {
				pairs = append(pairs, *cp)
			}
		}
	}
	d.cf.pairs = pairs
	d.cf.scanned = time.Now()

	per := map[string][]state.Overlap{}
	for _, p := range pairs {
		o := state.Overlap{Files: p.Files, Conflict: p.Status == "conflict", ConflictFiles: p.ConflictFiles}
		oa, ob := o, o
		oa.Slug, ob.Slug = p.B, p.A
		per[p.A] = append(per[p.A], oa)
		per[p.B] = append(per[p.B], ob)
	}
	for _, ovs := range per {
		sort.SliceStable(ovs, func(i, j int) bool {
			if ovs[i].Conflict != ovs[j].Conflict {
				return ovs[i].Conflict
			}
			return ovs[i].Slug < ovs[j].Slug
		})
	}

	d.mu.Lock()
	d.cf.changed = changed
	type change struct {
		slug   string
		ovs    []state.Overlap
		alsoIn []string
	}
	var updates []change
	for _, s := range open {
		cur := d.sessions[s.Slug]
		if cur == nil {
			continue
		}
		var also []string
		if cur.PendingTool != nil {
			also = d.alsoChangedLocked(cur, cur.PendingTool.Touches)
		}
		if !overlapsEqual(cur.Overlaps, per[s.Slug]) || (cur.PendingTool != nil && !slices.Equal(cur.PendingTool.AlsoIn, also)) {
			updates = append(updates, change{s.Slug, per[s.Slug], also})
		}
	}
	d.mu.Unlock()
	for _, c := range updates {
		d.update(c.slug, func(x *state.Session) {
			x.Overlaps = c.ovs
			if x.PendingTool != nil {
				t := *x.PendingTool // copy: snapshots share the pointer
				t.AlsoIn = c.alsoIn
				x.PendingTool = &t
			}
		})
	}
	for _, slug := range stale {
		d.update(slug, func(x *state.Session) {
			x.Overlaps = nil
			if x.PendingTool != nil {
				t := *x.PendingTool
				t.AlsoIn = nil
				x.PendingTool = &t
			}
		})
	}

	return pairsOf(pairs, repoID)
}

// pairsOf filters pairs to repoID ("" for all), never nil.
func pairsOf(pairs []ConflictPair, repoID string) []ConflictPair {
	out := []ConflictPair{}
	for _, p := range pairs {
		if repoID == "" || p.RepoID == repoID {
			out = append(out, p)
		}
	}
	return out
}

func overlapsEqual(a, b []state.Overlap) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Slug != b[i].Slug || a[i].Conflict != b[i].Conflict ||
			!slices.Equal(a[i].Files, b[i].Files) || !slices.Equal(a[i].ConflictFiles, b[i].ConflictFiles) {
			return false
		}
	}
	return true
}

// alsoChangedLocked names the other open sessions of s's repository whose
// change set (as of the last scan) already contains file. Caller holds d.mu.
func (d *Daemon) alsoChangedLocked(s *state.Session, file string) []string {
	if file == "" {
		return nil
	}
	var out []string
	for slug, files := range d.cf.changed {
		o := d.sessions[slug]
		if slug == s.Slug || o == nil || o.RepoID != s.RepoID || !forecastable(o) {
			continue
		}
		if files[file] {
			out = append(out, slug)
		}
	}
	sort.Strings(out)
	return out
}

// kickConflicts rescans in the background, at most every few seconds and
// never two scans at once (tickLoop calls it periodically, edits sooner).
func (d *Daemon) kickConflicts() {
	now := time.Now().UnixNano()
	if now-d.cf.lastKick.Load() < int64(3*time.Second) {
		return
	}
	if !d.cf.running.CompareAndSwap(false, true) {
		return
	}
	d.cf.lastKick.Store(now)
	go func() {
		defer d.cf.running.Store(false)
		d.scanConflicts("")
	}()
}

type conflictsP struct {
	RepoID string `json:"repo_id"`
}

func (d *Daemon) hConflicts(p conflictsP) (any, error) {
	return d.scanConflicts(p.RepoID), nil
}

// agentConflictsEvery: how often a caller without the token may have the
// forecast recomputed. Each scan runs git in every open worktree, so an
// agent polling conflicts.list must not keep pekoed busy; within this the
// last scan (the background one included) is returned.
const agentConflictsEvery = 10 * time.Second

// hAgentConflicts is conflicts.list without the human token: the same
// pairs every session's overlaps already show in session.list, rescanned
// at most every agentConflictsEvery.
func (d *Daemon) hAgentConflicts(p conflictsP) (any, error) {
	d.cf.mu.Lock()
	defer d.cf.mu.Unlock()
	if !d.cf.scanned.IsZero() && time.Since(d.cf.scanned) < agentConflictsEvery {
		return pairsOf(d.cf.pairs, p.RepoID), nil
	}
	return d.scanConflictsLocked(p.RepoID), nil
}
