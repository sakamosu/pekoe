package daemon

// pekoed merging a PR on a person's answer (merge.go, docs/api.md
// 「マージしてよいかの根拠」): a person answered "merge" to pekoed's merge
// item, and nothing else starts this. Before it merges, pekoed makes sure
// the item is its own latest one for that session (the kv record askMerge
// wrote, which no caller can write), reads the PR from GitHub again and
// checks every merge condition on what it read, and that the PR's head is
// still the head the item asked about. Then it runs
//
//	gh pr merge <n> --repo <host/owner/name> --<method> --match-head-commit <head>
//
// as the person's gh (outside any sandbox), in the main checkout (not the
// agent's worktree), so GitHub itself refuses a head that moved in
// between. --repo comes from the main checkout's origin (plain names
// only), and keeps gh from switching or deleting local branches. A PR
// whose base changed since land is refused. No
// --admin, no --auto: what GitHub would refuse the person is refused.
// How it went is the item's answer.merge (running → merged | failed) and
// merge.check's run; the merge is in the send log (kind merge). The
// landing (teardown, worktree) is the PR poller's, as for a merge on
// GitHub; pekoed reads the PR once more right after, so it starts then.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/config"
	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// ghFound is whether gh is on PATH (tests replace it).
var ghFound = ghAvailable

const (
	mergeReadTimeout = 30 * time.Second
	mergeDetailMax   = 500 // characters of gh's error kept on the item
)

// mergeRunTimeout is how long gh pr merge may take (tests shorten it).
var mergeRunTimeout = 2 * time.Minute

// mergeWaitDelay is how long pekoed waits, after gh pr merge was stopped,
// for its output pipes to close (a child of gh may hold them); then they
// are closed and the merge ends anyway (tests shorten it).
var mergeWaitDelay = 5 * time.Second

// mergeComputeEvery and mergeComputeLimit: how often pekoed reads the PR
// again while GitHub is still computing whether it can merge, and for how
// long in all before the merge fails (tests shorten them).
var (
	mergeComputeEvery = 5 * time.Second
	mergeComputeLimit = time.Minute
)

// mergeComputing is whether the only condition off is merge_state and
// GitHub has not computed it yet: mergeStateStatus UNKNOWN, or none with
// mergeable null or UNKNOWN.
func mergeComputing(v *PRView, c *state.MergeCheck) bool {
	if len(c.Missing) != 1 || c.Missing[0] != "merge_state" {
		return false
	}
	switch strings.ToUpper(v.MergeState) {
	case "UNKNOWN":
		return true
	case "":
		return v.Mergeable == "" || strings.EqualFold(v.Mergeable, "UNKNOWN")
	}
	return false
}

func mergeStateOf(v *PRView) string {
	if v.MergeState == "" {
		return "unknown"
	}
	return v.MergeState
}

// mergeTimedOut is the outcome of a gh pr merge that was stopped: GitHub
// may have merged it, so neither merged nor not merged.
func mergeTimedOut() string {
	return fmt.Sprintf("unknown: gh pr merge took over %s and was stopped; GitHub may have merged it: look at the PR (a merge that went through lands as usual)", mergeRunTimeout)
}

// mergeMethod is how s's repository merges ([merge] method of its
// repos/<id>.toml; merge by default).
func (d *Daemon) mergeMethod(s *state.Session) (string, error) {
	cfg, _, err := config.LoadRepo(s.RepoID)
	if err != nil {
		return "", err
	}
	return cfg.Merge.MergeMethod()
}

// ghRepo is host/owner/name of s's repository as its main checkout's origin
// says ("" when it says none).
// Each part is a plain name: nothing gh would read as a flag or, after
// -F, as "@file".
// "." and ".." are no owner or name either.
func (d *Daemon) ghRepo(s *state.Session) string {
	r := strings.TrimPrefix(d.webBase(s.RepoDir), "https://")
	if !ghRepoRe.MatchString(r) {
		return ""
	}
	for _, p := range strings.Split(r, "/")[1:] {
		if p == "." || p == ".." {
			return ""
		}
	}
	return r
}

var ghRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// prBase is the branch s's PR goes into (what land rebased onto).
func prBase(s *state.Session) string {
	if s.Base != "" {
		return s.Base
	}
	return gitx.DefaultBase(s.RepoDir)
}

func (d *Daemon) lastMergeRun(slug string) *state.MergeRun {
	if v, ok := d.mergeRuns.Load(slug); ok {
		r := v.(state.MergeRun)
		return &r
	}
	return nil
}

// startMerge is called with the answered item (its answer.merge running):
// the merge goes on in the background, the answer returns now.
func (d *Daemon) startMerge(it *state.InboxItem) {
	d.mergeRuns.Store(it.About, *it.Answer.Merge)
	go d.runMerge(*it)
}

// runMerge checks again and merges (see the top of this file). It ends
// with the item's answer.merge merged or failed.
func (d *Daemon) runMerge(it state.InboxItem) {
	run := *it.Answer.Merge
	end := func(st, format string, args ...any) {
		run.State, run.Detail, run.At = st, fmt.Sprintf(format, args...), time.Now()
		d.endMerge(it.ID, it.About, run)
	}
	// the item is still pekoed's latest one, for the session's PR
	current := func() (*state.Session, bool) {
		if rec := d.mergeRecordOf(it.About); rec.ID != it.ID {
			end(state.MergeFailed, "not merged: #%d is not pekoed's latest merge evidence for %s", it.ID, it.About)
			return nil, false
		}
		s, err := d.snapshot(it.About)
		if err != nil {
			end(state.MergeFailed, "not merged: session %s is gone", it.About)
			return nil, false
		}
		if s.PR != run.PR {
			end(state.MergeFailed, "not merged: the session's PR is #%d now, not #%d", s.PR, run.PR)
			return nil, false
		}
		return s, true
	}
	s, ok := current()
	if !ok {
		return
	}
	if !ghFound() {
		end(state.MergeFailed, "not merged: gh not found (install the GitHub CLI)")
		return
	}
	repo := d.ghRepo(s)
	if repo == "" {
		end(state.MergeFailed, "not merged: no GitHub repository in origin of %s", s.RepoDir)
		return
	}
	method, err := d.mergeMethod(s)
	if err != nil {
		end(state.MergeFailed, "not merged: %v", err)
		return
	}
	run.Method = method

	// read the PR again, and every condition on what was read. While
	// GitHub is still computing whether it can merge (UNKNOWN, as right
	// after another PR into the same base merged) and nothing else is
	// off, read it again every mergeComputeEvery, up to mergeComputeLimit.
	waitUntil := time.Now().Add(mergeComputeLimit)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), mergeReadTimeout)
		v, err := fetchPRViewOf(ctx, s.RepoDir, repo, s.PR)
		cancel()
		if err != nil {
			end(state.MergeFailed, "not merged: could not read PR #%d again: %s", s.PR, cutRunes(err.Error(), mergeDetailMax))
			return
		}
		d.applyPRStatus(s.Slug, v.Summary(), false)
		if base := prBase(s); v.Base != base {
			// the base was changed after the person looked: not what they agreed to
			end(state.MergeFailed, "not merged: PR #%d goes into %s now, not %s", s.PR, v.Base, base)
			return
		}
		if s, ok = current(); !ok {
			return
		}
		c := d.mergeCheck(s)
		switch {
		case !strings.EqualFold(c.PRHead, run.Head):
			end(state.MergeFailed, "not merged: PR HEAD moved (%s → %s)", shortSHA(run.Head), shortSHA(c.PRHead))
			return
		case c.Ready:
		case mergeComputing(v, c) && time.Now().Before(waitUntil):
			run.Detail, run.At = fmt.Sprintf("waiting: GitHub is still computing whether PR #%d can merge (merge_state %s); reading it again every %s, up to %s",
				s.PR, mergeStateOf(v), mergeComputeEvery, mergeComputeLimit), time.Now()
			d.endMerge(it.ID, it.About, run)
			time.Sleep(mergeComputeEvery)
			continue
		case mergeComputing(v, c):
			end(state.MergeFailed, "not merged: evidence no longer holds: %s (GitHub was still computing it after %s)", mergeDetails(c), mergeComputeLimit)
			return
		default:
			end(state.MergeFailed, "not merged: evidence no longer holds: %s", mergeDetails(c))
			return
		}
		break
	}
	if run.Detail != "" {
		// no longer waiting on GitHub: the item stops saying so
		run.Detail, run.At = "", time.Now()
		d.endMerge(it.ID, it.About, run)
	}

	args := []string{"pr", "merge", strconv.Itoa(s.PR), "--repo", repo, "--" + method, "--match-head-commit", run.Head}
	ctx, cancel := context.WithTimeout(context.Background(), mergeRunTimeout)
	_, err = ghMerge(ctx, s.RepoDir, args...)
	timedOut := err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
	cancel()
	rec2 := &state.SendRecord{At: time.Now(), From: callerPekoed, To: s.Slug, Kind: "merge", Via: it.Answer.Via, OK: err == nil,
		Text: fmt.Sprintf("gh %s (inbox #%d, answered by %s)", strings.Join(args, " "), it.ID, it.Answer.By)}
	switch {
	case timedOut:
		rec2.Error = mergeTimedOut()
	case err != nil:
		rec2.Error = cutRunes(err.Error(), mergeDetailMax)
	}
	if e := d.DB.AddSend(rec2); e != nil {
		log.Printf("send log: %v", e)
	}
	switch {
	case timedOut:
		// gh was stopped while GitHub may have merged it: not told merged,
		// and not told it did not merge either
		end(state.MergeFailed, "%s", mergeTimedOut())
		return
	case err != nil:
		end(state.MergeFailed, "not merged: %s", cutRunes(cleanInbox(err.Error(), false), mergeDetailMax))
		return
	}
	end(state.MergeMerged, "merged PR #%d (%s) at HEAD %s", s.PR, method, shortSHA(run.Head))
	log.Printf("%s: pekoed merged PR #%d (%s, inbox #%d)", s.Slug, s.PR, method, it.ID)

	// the poller's next round would see it within a minute; read it now so
	// the landing starts right away
	ctx, cancel = context.WithTimeout(context.Background(), mergeReadTimeout)
	if v, err := fetchPRViewOf(ctx, s.RepoDir, repo, s.PR); err == nil {
		d.applyPRStatus(s.Slug, v.Summary(), false)
	}
	cancel()
}

// endMerge writes how the merge went (or, still running, what it waits
// on) onto the item (its rev goes up) and tells the screens.
func (d *Daemon) endMerge(id int64, slug string, run state.MergeRun) {
	d.mergeRuns.Store(slug, run)
	d.inboxMu.Lock()
	it, err := d.DB.InboxGet(id)
	if err == nil && it.Answer != nil {
		it.Answer.Merge = &run
		it.Rev++
		it.UpdatedAt = time.Now()
		err = d.DB.SaveInbox(it)
	} else if err == nil {
		err = errors.New("no answer")
	}
	d.inboxMu.Unlock()
	if err != nil {
		log.Printf("merge inbox #%d: %v", id, err)
		return
	}
	if run.State == state.MergeFailed {
		log.Printf("%s: merge of PR #%d (inbox #%d): %s", slug, run.PR, id, run.Detail)
	}
	d.emit("inbox.updated", it)
}

// failStaleMerges: a merge still running in state.db when pekoed starts
// was cut off by a stop. Whether GitHub merged it is the PR poller's to
// see; the item says to look.
func (d *Daemon) failStaleMerges() {
	its, err := d.DB.InboxList(state.InboxFilter{Status: []string{state.InboxAnswered}, From: callerPekoed})
	if err != nil {
		return
	}
	for i := range its {
		it := &its[i]
		if a := it.Answer; isMergeItem(it) && a != nil && a.Merge != nil && a.Merge.State == state.MergeRunning {
			run := *a.Merge
			run.State, run.At = state.MergeFailed, time.Now()
			run.Detail = "pekoed stopped during the merge: look at the PR (a merge, if it went through, lands as usual)"
			d.endMerge(it.ID, it.About, run)
		}
	}
}

// fetchPRViewOf is fetchPRView for repo (host/owner/name) given, not the
// one gh reads from dir's git.
func fetchPRViewOf(ctx context.Context, dir, repo string, number int) (*PRView, error) {
	host, rest, _ := strings.Cut(repo, "/")
	owner, name, _ := strings.Cut(rest, "/")
	args := []string{"api", "graphql"}
	if host != "github.com" {
		args = append(args, "--hostname", host)
	}
	args = append(args, "-F", "owner="+owner, "-F", "name="+name, "-F", "number="+strconv.Itoa(number), "-f", "query="+prQuery)
	out, err := gh(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	return parsePRView(out, time.Now())
}

// ghMerge is gh without prompts (it never waits on a terminal); the error
// carries gh's stderr. Once ctx is done it returns within mergeWaitDelay,
// even when a child of gh still holds its output.
func ghMerge(ctx context.Context, dir string, args ...string) ([]byte, error) {
	c, err := ghCommand(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	c.Env = append(c.Env, "GH_PROMPT_DISABLED=1")
	c.WaitDelay = mergeWaitDelay
	out, err := c.Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return out, fmt.Errorf("gh pr merge: %s", msg)
	}
	return out, nil
}
