package daemon

// Cleanup candidates (docs/orchestration.md 段階 4 の G「後片付け」): the
// sessions a person may want to remove, with what removing them would lose.
// pekoed only lists them; removing is worktree.rm, chosen by a person, with
// the line it already draws: without the human token only a landed session
// whose worktree is gone (hAgentRm), anything else takes the token.
//
//   - landed: pekoed saw the merge, tore it down and removed its worktree;
//     only pekoe's record is left
//   - merged: its PR was merged on GitHub but pekoe did not land it (the
//     PR was made by hand, or by `gh pr create` in the session), so the
//     worktree is still there
//   - stale: stopped (done, idle, failed, reviewing …) and not moved for
//     [cleanup] stale_after (72h by default)
//
// For a candidate whose worktree is there, pekoed reads its uncommitted
// files and the commits on no remote (git status / rev-list, read only), so
// a person sees what rm would throw away before choosing.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pekoe/internal/gitx"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// Kinds of a cleanup candidate, in the order they are listed.
const (
	CleanupLanded = "landed"
	CleanupMerged = "merged"
	CleanupStale  = "stale"
)

var cleanupOrder = map[string]int{CleanupLanded: 0, CleanupMerged: 1, CleanupStale: 2}

// CleanupItem is one candidate of cleanup.list.
type CleanupItem struct {
	Kind   string    `json:"kind"` // landed | merged | stale
	Slug   string    `json:"slug"`
	Repo   string    `json:"repo"`
	RepoID string    `json:"repo_id"`
	Branch string    `json:"branch"`
	State  string    `json:"state"`
	Since  time.Time `json:"since"` // when the session last changed state
	PR     int       `json:"pr,omitempty"`
	Path   string    `json:"path,omitempty"`
	// Worktree: the directory is still there. DeletesWorktree: rm would
	// delete it (pekoe made it under ~/worktrees); otherwise rm only forgets
	// the session.
	Worktree        bool `json:"worktree"`
	DeletesWorktree bool `json:"deletes_worktree"`
	// Dirty: uncommitted files (git status). Unpushed: commits on no remote
	// and not on the base (nor in the merged PR). -1: not read (Unknown).
	Dirty    int    `json:"dirty"`
	Unpushed int    `json:"unpushed"`
	Unknown  string `json:"unknown,omitempty"`
	// Warnings sum up what rm would lose, in words (empty: nothing).
	Warnings []string `json:"warnings,omitempty"`
	// HumanOnly: removing it takes the human token (everything but a
	// landed session whose worktree is gone; hAgentRm).
	HumanOnly bool `json:"human_only"`
}

// CleanupList is cleanup.list's result.
type CleanupList struct {
	StaleAfter string        `json:"stale_after"` // the threshold used, e.g. "72h0m0s"
	Items      []CleanupItem `json:"items"`
}

type cleanupP struct {
	RepoID     string `json:"repo_id"`
	StaleAfter string `json:"stale_after"` // overrides [cleanup] stale_after
}

// cleanupKind says why s is a candidate ("" when it is not).
func cleanupKind(s *state.Session, now time.Time, staleAfter time.Duration) string {
	switch {
	case s.State == state.Creating:
		return ""
	case s.State == state.Landed:
		return CleanupLanded
	case s.Landing:
		// pekoe land made the PR and waits for a person to merge it;
		// watchMerges will tear it down
		return ""
	case s.PRStatus != nil && s.PRStatus.State == "MERGED":
		return CleanupMerged
	case s.RepoDir != "" && s.Path == s.RepoDir:
		// a session in the repository's own checkout is its window, not
		// leftover work
		return ""
	case s.State == state.Running || s.State == state.Waiting || s.Dev:
		return ""
	case s.StateSince.IsZero() || now.Sub(s.StateSince) < staleAfter:
		return ""
	}
	return CleanupStale
}

// cleanupGit is what git said about a candidate's worktree.
type cleanupGit struct {
	at       time.Time
	head     string
	dirty    int
	unpushed int
	unknown  string
}

// agentCleanupEvery: a caller without the token gets the git reading of a
// worktree at most this often (each reading runs git status in it), as
// conflicts.list does.
const agentCleanupEvery = 10 * time.Second

type cleanupCache struct {
	mu  sync.Mutex
	git map[string]cleanupGit // slug -> last reading
}

func (d *Daemon) hCleanup(p cleanupP) (any, error) { return d.cleanupList(p, true) }

// hAgentCleanup is cleanup.list without the human token: the same list,
// git read at most every agentCleanupEvery per worktree.
func (d *Daemon) hAgentCleanup(p cleanupP) (any, error) { return d.cleanupList(p, false) }

func (d *Daemon) cleanupList(p cleanupP, fresh bool) (*CleanupList, error) {
	stale := d.Global.Cleanup.Stale()
	if p.StaleAfter != "" {
		v, err := time.ParseDuration(p.StaleAfter)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("stale_after: a positive duration like 72h (got %q)", p.StaleAfter)
		}
		stale = v
	}
	now := time.Now()
	d.mu.Lock()
	var cand []*state.Session
	kinds := map[string]string{}
	for _, s := range d.sessions {
		if p.RepoID != "" && s.RepoID != p.RepoID {
			continue
		}
		if k := cleanupKind(s, now, stale); k != "" {
			cand = append(cand, d.copyLocked(s))
			kinds[s.Slug] = k
		}
	}
	d.mu.Unlock()

	out := &CleanupList{StaleAfter: stale.String(), Items: []CleanupItem{}}
	for _, s := range cand {
		it := CleanupItem{
			Kind: kinds[s.Slug], Slug: s.Slug, Repo: s.Repo, RepoID: s.RepoID, Branch: s.Branch,
			State: string(s.State), Since: s.StateSince, PR: s.PR, Path: s.Path,
		}
		if s.Path != "" {
			if _, err := os.Stat(s.Path); err == nil {
				it.Worktree = true
				it.DeletesWorktree = s.Managed && paths.UnderWorktrees(s.Path)
			}
		}
		if it.Worktree {
			g := d.cleanupGitOf(s, fresh)
			it.Dirty, it.Unpushed, it.Unknown = g.dirty, g.unpushed, g.unknown
		}
		it.HumanOnly = !(it.Kind == CleanupLanded && !(it.Worktree && s.Managed))
		it.Warnings = cleanupWarnings(&it, s)
		out.Items = append(out.Items, it)
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if cleanupOrder[a.Kind] != cleanupOrder[b.Kind] {
			return cleanupOrder[a.Kind] < cleanupOrder[b.Kind]
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since) // oldest first
		}
		return a.Slug < b.Slug
	})
	return out, nil
}

// cleanupGitOf reads the worktree of s, or (fresh false) reuses a reading
// younger than agentCleanupEvery.
func (d *Daemon) cleanupGitOf(s *state.Session, fresh bool) cleanupGit {
	d.clean.mu.Lock()
	defer d.clean.mu.Unlock()
	if d.clean.git == nil {
		d.clean.git = map[string]cleanupGit{}
	}
	if g, ok := d.clean.git[s.Slug]; ok && !fresh && time.Since(g.at) < agentCleanupEvery {
		return g
	}
	g := d.readCleanupGit(s)
	d.clean.git[s.Slug] = g
	return g
}

func (d *Daemon) readCleanupGit(s *state.Session) cleanupGit {
	g := cleanupGit{at: time.Now(), dirty: -1, unpushed: -1}
	if s.GitBlocked != "" {
		g.unknown = "pekoed runs no git there: " + s.GitBlocked
		return g
	}
	if err := gitx.CheckWorktreeOf(s.RepoDir, s.Path); err != nil {
		g.unknown = err.Error()
		return g
	}
	st, err := gitx.Run(s.Path, "status", "--porcelain")
	if err != nil {
		g.unknown = err.Error()
		return g
	}
	g.dirty = 0
	for _, l := range strings.Split(st, "\n") {
		if strings.TrimSpace(l) != "" {
			g.dirty++
		}
	}
	head, err := gitx.RevParse(s.Path, "HEAD")
	if err != nil {
		g.unpushed = 0 // no commit yet: nothing to push
		return g
	}
	g.head = head
	// commits reachable from HEAD that are on no remote-tracking branch and
	// not on the base (its merge base); a merged PR's head counts as pushed,
	// so a branch squash-merged on GitHub (whose remote branch is gone) does
	// not show its commits as unpushed
	// (without a base to compare with, every commit on no remote counts:
	// better too many than none)
	not := []string{"--remotes"}
	if base, label := d.baseCommit(s, 0); label != "HEAD" && label != "empty" {
		not = append(not, base)
	}
	if ps := s.PRStatus; ps != nil && ps.HeadSHA != "" {
		if _, err := gitx.Run(s.Path, "cat-file", "-e", ps.HeadSHA+"^{commit}"); err == nil {
			not = append(not, ps.HeadSHA)
		}
	}
	args := append([]string{"rev-list", "--count", "HEAD", "--not"}, not...)
	n, err := gitx.Run(s.Path, args...)
	if err != nil {
		g.unknown = err.Error()
		return g
	}
	g.unpushed, _ = strconv.Atoi(strings.TrimSpace(n))
	return g
}

// cleanupWarnings: what removing the candidate would lose, or why it may
// still be wanted.
func cleanupWarnings(it *CleanupItem, s *state.Session) []string {
	var w []string
	if it.Unknown != "" {
		w = append(w, "could not read the worktree: "+it.Unknown)
	}
	if it.Dirty > 0 {
		w = append(w, fmt.Sprintf("%d uncommitted file%s", it.Dirty, plural(it.Dirty)))
	}
	if it.Unpushed > 0 {
		w = append(w, fmt.Sprintf("%d unpushed commit%s", it.Unpushed, plural(it.Unpushed)))
	}
	if it.Kind == CleanupLanded && it.Worktree && s.Managed {
		w = append(w, "landed, but its worktree is still there (rm deletes it)")
	}
	if ps := s.PRStatus; it.Kind == CleanupStale && s.PR > 0 && !s.PRReview && (ps == nil || ps.State == "" || ps.State == "OPEN") {
		w = append(w, fmt.Sprintf("PR #%d is still open", s.PR))
	}
	return w
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
