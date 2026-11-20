package daemon

// Whether a session is still at work (merge.go's settled condition, and
// the landing of a PR merged outside pekoe in finishLanding). A PR that
// passed review and CI can still be a session's work in progress: someone
// sent it more to do after its report, or its agent is running again, or
// its worktree has changes not committed yet. pekoed then neither asks a
// person to merge it nor, when it is merged anyway, closes the agent's
// window and removes the worktree under it.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"pekoe/internal/gitx"
	"pekoe/internal/ids"
	"pekoe/internal/state"
)

// agentBusy says what s's agent is doing when it is at work ("" when it
// is not): running (working), or waiting (a permission prompt, a question).
func agentBusy(s *state.Session) string {
	switch s.State {
	case state.Running:
		return "agent is running"
	case state.Waiting:
		if s.WaitKind != "" {
			return "agent is waiting (" + s.WaitKind + ")"
		}
		return "agent is waiting"
	}
	return ""
}

var errGitBlocked = errors.New("pekoed runs no git in the worktree (git_blocked)")

// worktreeChanges is how many entries git status lists in s's worktree:
// changes not committed and files not tracked. A worktree that is not
// there has none; err: git could not tell.
func (d *Daemon) worktreeChanges(s *state.Session) (int, error) {
	if s.Path == "" {
		return 0, nil
	}
	if _, err := os.Stat(s.Path); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if !d.checkGitDir(s) {
		return 0, errGitBlocked
	}
	out, err := gitx.Run(s.Path, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n, nil
}

// commitsBeyondPR is how many commits s's worktree HEAD has that its PR
// lacks: work committed after pekoe land pushed it, which neither the PR
// nor its merge carries. They are the commits reachable from HEAD and not
// from the PR's head (PRStatus.HeadSHA). When that commit is not here (a
// push to the PR from elsewhere, or no head read yet), it is the commits
// not in origin/<branch>, what land pushed last; with neither, nothing
// tells (0). A worktree that is not there has none; err: git could not
// tell.
func (d *Daemon) commitsBeyondPR(s *state.Session) (int, error) {
	if s.Path == "" {
		return 0, nil
	}
	if _, err := os.Stat(s.Path); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if !d.checkGitDir(s) {
		return 0, errGitBlocked
	}
	if _, err := gitx.RevParse(s.Path, "HEAD"); err != nil {
		return 0, nil // no commit yet
	}
	var not []string
	if ps := s.PRStatus; ps != nil && ps.HeadSHA != "" && gitx.CheckRefArg("PR head", ps.HeadSHA) == nil {
		if _, err := gitx.Run(s.Path, "cat-file", "-e", ps.HeadSHA+"^{commit}"); err == nil {
			not = []string{ps.HeadSHA}
		}
	}
	if not == nil {
		pushed := "refs/remotes/origin/" + s.Branch
		if s.Branch == "" || gitx.CheckRefArg("branch", s.Branch) != nil {
			return 0, nil
		}
		if _, err := gitx.Run(s.Path, "rev-parse", "--verify", "--quiet", pushed+"^{commit}"); err != nil {
			return 0, nil
		}
		not = []string{pushed}
	}
	out, err := gitx.Run(s.Path, append([]string{"rev-list", "--count", "HEAD", "--not"}, not...)...)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// sendKinds are the send log's kinds that hand a session's agent more to
// do: session.send, and an inbox answer typed into its window (a "send
// back" among them). session.new's prompt is where the work began.
var sendKinds = map[string]bool{"send": true, "inbox": true}

// lastWord is the session's newest report that ends a stretch of work
// (done or blocked), nil when there is none.
func (d *Daemon) lastWord(slug string) *state.Report {
	rs, err := d.DB.Reports(slug, 0, 200)
	if err != nil {
		return nil
	}
	for i := range rs {
		if rs[i].Status == "done" || rs[i].Status == "blocked" {
			return &rs[i]
		}
	}
	return nil
}

// sentSince is the newest text that reached slug's agent after its last
// done or blocked report (nil when none did), and that report.
func (d *Daemon) sentSince(slug string) (*state.SendRecord, *state.Report, error) {
	rp := d.lastWord(slug)
	rs, err := d.DB.Sends(state.SendFilter{To: slug, Limit: 200})
	if err != nil {
		return nil, rp, err
	}
	for i := range rs {
		r := &rs[i]
		if !sendKinds[r.Kind] || !r.OK {
			continue
		}
		if rp == nil || r.At.After(rp.At) {
			return r, rp, nil
		}
		break // newest first: the rest came before the report
	}
	return nil, rp, nil
}

// settledCheck is merge.go's settled condition: s's work is over (its
// agent is not running or waiting, its worktree has nothing uncommitted
// and no commit the PR lacks, nothing was sent to it since its last
// report). ok false says why.
func (d *Daemon) settledCheck(s *state.Session) (ok bool, detail string) {
	var why []string
	if b := agentBusy(s); b != "" {
		why = append(why, b)
	}
	if n, err := d.worktreeChanges(s); err != nil {
		why = append(why, "could not read the worktree: "+cutRunes(err.Error(), 200))
	} else if n > 0 {
		why = append(why, fmt.Sprintf("worktree has %d uncommitted or untracked files", n))
	}
	if n, err := d.commitsBeyondPR(s); err != nil {
		why = append(why, "could not tell whether HEAD has commits not in the PR: "+cutRunes(err.Error(), 200))
	} else if n > 0 {
		why = append(why, fmt.Sprintf("HEAD has %d commit%s not in PR #%d (run pekoe land)", n, plural(n), s.PR))
	}
	r, rp, err := d.sentSince(s.Slug)
	switch {
	case err != nil:
		why = append(why, "could not read the send log: "+err.Error())
	case r != nil && rp != nil:
		why = append(why, fmt.Sprintf("%s sent it more (%s, %s) after its last report %s", r.From, r.Kind, r.At.Local().Format("15:04:05"), ids.Report(rp.ID)))
	case r != nil:
		why = append(why, fmt.Sprintf("%s sent it more (%s, %s) and it has not reported done since", r.From, r.Kind, r.At.Local().Format("15:04:05")))
	}
	if len(why) > 0 {
		return false, strings.Join(why, "; ")
	}
	return true, "agent " + string(s.State) + ", worktree clean, nothing sent since its last report"
}

// landHold says why a merged PR's session is not torn down yet ("" when
// it may be): its agent is running or waiting, its worktree has work not
// committed, or commits the merged PR did not carry (or git cannot tell).
func (d *Daemon) landHold(s *state.Session) string {
	var why []string
	if b := agentBusy(s); b != "" {
		why = append(why, b)
	}
	if n, err := d.worktreeChanges(s); err != nil {
		why = append(why, "could not tell whether the worktree has uncommitted work: "+cutRunes(err.Error(), 200))
	} else if n > 0 {
		why = append(why, fmt.Sprintf("worktree has %d uncommitted or untracked files", n))
	}
	if n, err := d.commitsBeyondPR(s); err != nil {
		why = append(why, "could not tell whether HEAD has commits not in the merged PR: "+cutRunes(err.Error(), 200))
	} else if n > 0 {
		why = append(why, fmt.Sprintf("HEAD has %d commit%s not in the merged PR #%d (pekoe land opens a new PR for them)", n, plural(n), s.PR))
	}
	return strings.Join(why, "; ")
}
