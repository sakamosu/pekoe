package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// ---- a stacked session's base, once that base got merged ----
//
// pekoe new --base <another session's branch> stacks a session on another
// one. When the PR of that branch is merged (and GitHub deletes the
// branch), the stacked session's base names a branch whose work is in its
// own base now, maybe not on origin any more: land would fetch it in
// vain, rebase onto the stale local branch (carrying commits of the base's
// base that are not on that branch) and open a PR into a branch GitHub no
// longer has ("No commits between"). So the session goes on from the
// merged branch's base: Session.base becomes that, base_from keeps what
// it was, and land drops the commits that went in with the merged PR.

// baseMove is a session's base moving from a merged branch to that
// branch's own base.
type baseMove struct {
	From, To string // branch names (origin's, no origin/ prefix)
	Tip      string // From's last commit pekoed knows ("" when it knows none)
	Why      string // how pekoed knows From was merged
}

// mergedBaseParent is the session of branch (in s's repository, not s)
// whose PR was merged: landed (only a merged PR, or the fast-forward of
// a repository without origin, lands one), or seen merged and held. nil
// when none; working: a session of branch is still at work with its PR
// not merged, so the branch is not merged whatever else says so (a stale
// local branch whose old commit is in main, a branch gone from origin
// for a moment).
func (d *Daemon) mergedBaseParent(s *state.Session, branch string) (p *state.Session, working bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, x := range d.sessions {
		if x.Slug == s.Slug || x.RepoID != s.RepoID || x.Branch != branch || x.PRReview {
			continue
		}
		if x.State != state.Landed && (x.PRStatus == nil || x.PRStatus.State != "MERGED") {
			return nil, true
		}
		c := *x
		p = &c
	}
	return p, false
}

// defaultBaseOf is the base a session of s's repository gets without
// --base: the repository's [worktree] base, else origin's default branch.
func (d *Daemon) defaultBaseOf(s *state.Session) string {
	if b := d.repoConfig(s).Worktree.Base; b != "" {
		return originBranch(b)
	}
	return originBranch(gitx.DefaultBase(s.RepoDir))
}

// branchTip is branch's commit in dir: the local branch, else origin's as
// last fetched. "" when neither is there.
func branchTip(dir, branch string) string {
	for _, r := range []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch} {
		if sha, err := gitx.Run(dir, "rev-parse", "--verify", "--quiet", "--end-of-options", r+"^{commit}"); err == nil && sha != "" {
			return strings.TrimSpace(sha)
		}
	}
	return ""
}

// remoteTip is branch's commit on origin now (ls-remote): "" and ok when
// origin has no such branch; !ok when git could not ask.
func remoteTip(dir, branch string) (sha string, ok bool) {
	out, err := gitx.Run(dir, "ls-remote", "--heads", "--end-of-options", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", false
	}
	sha, _, _ = strings.Cut(strings.TrimSpace(out), "\t")
	return sha, true
}

// mergedPRHead is the newest merged PR whose head is branch, as GitHub
// has it: its number, the branch it went into and the commit it merged.
// Zero number when there is none or gh cannot tell.
func (d *Daemon) mergedPRHead(s *state.Session, branch string) (n int, into, head string) {
	ri := d.ghRepoInfoOf(s.RepoDir)
	if ri.Base == "" || !ghAvailable() {
		return 0, "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, done, err := ghCommand(ctx, ri.Base, "pr", "list", "--repo", ri.Base, "--head", branch, "--state", "merged", "--limit", "1", "--json", "number,baseRefName,headRefOid")
	if err != nil {
		return 0, "", ""
	}
	defer done()
	out, err := c.Output()
	if err != nil {
		return 0, "", ""
	}
	var v []struct {
		Number      int    `json:"number"`
		BaseRefName string `json:"baseRefName"`
		HeadRefOid  string `json:"headRefOid"`
	}
	if json.Unmarshal(out, &v) != nil || len(v) == 0 {
		return 0, "", ""
	}
	return v[0].Number, v[0].BaseRefName, v[0].HeadRefOid
}

// mergedBase tells whether base (s's, a branch on origin) was merged, and
// where s goes on from then. Known by, in order:
//   - the session of that branch (landed, or its PR seen merged)
//   - GitHub: a merged PR of the branch, and origin no longer has the
//     branch (one still there, such as develop after a release PR into
//     main, stays a base)
//   - git alone: origin no longer has the branch, it was pushed there
//     (its upstream is origin), and its last commit is in the default base
//
// The repository's default base itself is never moved. A base whose
// session is still at work with its PR not merged (pekoed has not seen
// the merge yet) moves only on GitHub's word that the branch's commit
// here now is what a PR merged: not on git's alone (a stale local branch
// whose old commit is in main, the branch gone from origin for a while).
func (d *Daemon) mergedBase(s *state.Session, base string) (baseMove, bool) {
	def := d.defaultBaseOf(s)
	if base == "" || base == def {
		return baseMove{}, false
	}
	rt, asked := remoteTip(s.Path, base)
	if !asked {
		return baseMove{}, false
	}
	p, working := d.mergedBaseParent(s, base)
	if p != nil {
		to := originBranch(p.Base)
		if to == "" {
			to = def
		}
		tip := ""
		if p.PRStatus != nil {
			tip = p.PRStatus.HeadSHA
		}
		if tip == "" {
			tip = branchTip(s.Path, base)
		}
		why := "session " + p.Slug + " landed"
		if p.PR != 0 {
			why = fmt.Sprintf("PR #%d of session %s merged", p.PR, p.Slug)
		}
		// origin's branch moved on since (used again for new work): not
		// the one that landed
		if to != base && (rt == "" || (tip != "" && rt == tip)) {
			return baseMove{From: base, To: to, Tip: tip, Why: why}, true
		}
	}
	if n, into, head := d.mergedPRHead(s, base); n != 0 && into != "" && into != base {
		if working && (head == "" || head != branchTip(s.Path, base)) {
			return baseMove{}, false // an earlier PR of the branch: its session went on
		}
		// only a branch gone from origin: one still there (develop, whose
		// release PR into main merged) goes on being a base
		if rt != "" {
			return baseMove{}, false
		}
		if head == "" {
			head = branchTip(s.Path, base)
		}
		return baseMove{From: base, To: originBranch(into), Tip: head, Why: fmt.Sprintf("PR #%d merged into %s", n, into)}, true
	}
	if rt != "" || working {
		return baseMove{}, false
	}
	if r, err := gitx.Run(s.Path, "config", "--get", "branch."+base+".remote"); err != nil || strings.TrimSpace(r) != "origin" {
		return baseMove{}, false // never on origin (a local branch): nothing says it was merged
	}
	tip := branchTip(s.Path, base)
	if tip == "" {
		return baseMove{}, false
	}
	if _, err := gitx.Run(s.Path, "fetch", "--end-of-options", "origin", def); err != nil {
		return baseMove{}, false
	}
	if _, err := gitx.Run(s.Path, "merge-base", "--is-ancestor", tip, "origin/"+def); err != nil {
		return baseMove{}, false
	}
	return baseMove{From: base, To: def, Tip: tip, Why: fmt.Sprintf("%s is gone from origin and its last commit is in %s", base, def)}, true
}

// resolveMergedBase follows mergedBase while the base it moves to was
// merged too (a stack of three): the move from base to the first base not
// merged, ok false when base itself was not.
func (d *Daemon) resolveMergedBase(s *state.Session, base string) (baseMove, bool) {
	mv, ok := d.mergedBase(s, base)
	if !ok {
		return mv, false
	}
	for range 3 {
		next, ok := d.mergedBase(s, mv.To)
		if !ok || next.To == base {
			break
		}
		mv.To, mv.Why = next.To, mv.Why+"; "+next.Why
	}
	return mv, true
}

// moveBase records mv on session own (slug): base becomes mv.To,
// base_from the base it had first, base_from_sha mv.Tip. false when own
// is no longer the session of slug.
func (d *Daemon) moveBase(slug string, own *state.Session, mv baseMove) bool {
	ours := false
	d.update(slug, func(x *state.Session) {
		if own != nil && x != own {
			return
		}
		ours = true
		if x.BaseFrom == "" {
			x.BaseFrom = x.Base
		}
		if mv.Tip != "" {
			x.BaseFromSHA = mv.Tip
		}
		x.Base = mv.To
	})
	if ours {
		log.Printf("base %s: %s was merged (%s): base is %s now", slug, mv.From, mv.Why, mv.To)
	}
	return ours
}

// moveStackedBases moves the base of the sessions stacked on parent (their
// base is its branch) to parent's own base, now that parent landed: their
// diffs, conflict forecasts and later land go against what parent went
// into, not a branch GitHub may have deleted.
func (d *Daemon) moveStackedBases(parent *state.Session) {
	if parent.Branch == "" || parent.PRReview {
		return
	}
	to := originBranch(parent.Base)
	if to == "" || to == parent.Branch {
		return
	}
	tip := ""
	if parent.PRStatus != nil {
		tip = parent.PRStatus.HeadSHA
	}
	if tip == "" {
		tip = branchTip(parent.RepoDir, parent.Branch)
	}
	d.mu.Lock()
	var kids []*state.Session
	for _, x := range d.sessions {
		if x.Slug != parent.Slug && x.RepoID == parent.RepoID && x.State != state.Landed &&
			!x.PRReview && x.Branch != parent.Branch && originBranch(x.Base) == parent.Branch {
			kids = append(kids, x)
		}
	}
	d.mu.Unlock()
	why := "session " + parent.Slug + " landed"
	if parent.PR != 0 {
		why = fmt.Sprintf("PR #%d of session %s merged", parent.PR, parent.Slug)
	}
	for _, k := range kids {
		d.moveBase(k.Slug, k, baseMove{From: parent.Branch, To: to, Tip: tip, Why: why})
	}
}

// dropPoint is where land's rebase starts for s when its base was moved:
// the merge-base of HEAD and the moved-from branch's last commit, so the
// commits that went in with that branch's PR (merged as they were, or
// squashed) are not replayed. "" when there is none to drop.
func dropPoint(s *state.Session) string {
	if s.BaseFromSHA == "" || gitx.CheckRefArg("base_from_sha", s.BaseFromSHA) != nil {
		return ""
	}
	mb, err := gitx.MergeBase(s.Path, "HEAD", s.BaseFromSHA)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(mb)
}

// prCreateHint is what to do when gh pr create of s's branch into base
// failed with out: the base is not on origin (merged and deleted, with
// nothing that told pekoed so), or the branch has nothing beyond it.
func (d *Daemon) prCreateHint(s *state.Session, base, onto, out string) string {
	def := d.defaultBaseOf(s)
	if rt, ok := remoteTip(s.Path, base); ok && rt == "" {
		return fmt.Sprintf("the base branch %s is not on origin (merged and deleted?), and pekoed found no merged PR or landed session of it to move the base to: %s is pushed; drop the base's commits (git rebase --onto origin/%s %s) and open the PR yourself (gh pr create --base %s --head %s)",
			base, s.Branch, def, onto, def, s.Branch)
	}
	if strings.Contains(out, "No commits between") {
		return fmt.Sprintf("%s has no commits that origin's %s lacks: nothing to put in a PR (already merged?)", s.Branch, base)
	}
	return ""
}

// ghOutput runs gh args for repo (30 s at most) and returns its output;
// err carries what gh said.
func ghOutput(repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, done, err := ghCommand(ctx, repo, args...)
	if err != nil {
		return "", err
	}
	defer done()
	out, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
