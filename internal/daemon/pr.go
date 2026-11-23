package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// ---- PR view: one `gh api graphql` call → everything the TUI shows ----

type PRCheck struct {
	Name       string    `json:"name"`
	Workflow   string    `json:"workflow,omitempty"`
	Status     string    `json:"status"`               // QUEUED | IN_PROGRESS | COMPLETED | … (StatusContext: its state)
	Conclusion string    `json:"conclusion,omitempty"` // SUCCESS | FAILURE | …
	Bucket     string    `json:"bucket"`               // pass | fail | pending | skip
	URL        string    `json:"url,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Seconds    int       `json:"seconds,omitempty"` // duration (so far, while running)
	RunID      int64     `json:"run_id,omitempty"`  // GitHub Actions only
	JobID      int64     `json:"job_id,omitempty"`
}

type PRReview struct {
	Author string    `json:"author"`
	State  string    `json:"state"` // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED | PENDING
	Body   string    `json:"body,omitempty"`
	At     time.Time `json:"at"`
	URL    string    `json:"url,omitempty"`
}

type PRComment struct {
	Author string    `json:"author"`
	Body   string    `json:"body"`
	At     time.Time `json:"at"`
	URL    string    `json:"url,omitempty"`
}

type PRThread struct {
	Path     string      `json:"path"`
	Line     int         `json:"line"` // 0 when outdated (see original_line)
	OrigLine int         `json:"original_line,omitempty"`
	Resolved bool        `json:"resolved"`
	Outdated bool        `json:"outdated"`
	Comments []PRComment `json:"comments"`
}

type PRView struct {
	Number         int         `json:"number"`
	Title          string      `json:"title"`
	Body           string      `json:"body,omitempty"`
	URL            string      `json:"url"`
	State          string      `json:"state"` // OPEN | MERGED | CLOSED
	IsDraft        bool        `json:"is_draft"`
	Mergeable      string      `json:"mergeable"`          // MERGEABLE | CONFLICTING | UNKNOWN
	MergeState     string      `json:"merge_state_status"` // CLEAN | BLOCKED | DIRTY | BEHIND | UNSTABLE | …
	ReviewDecision string      `json:"review_decision,omitempty"`
	Base           string      `json:"base"`
	Head           string      `json:"head"`
	HeadSHA        string      `json:"head_sha"` // headRefOid
	Checks         []PRCheck   `json:"checks"`
	Reviews        []PRReview  `json:"reviews"`
	Threads        []PRThread  `json:"threads"`
	Comments       []PRComment `json:"comments"`
	FetchedAt      time.Time   `json:"fetched_at"`
}

const prQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title body url state isDraft mergeable mergeStateStatus reviewDecision baseRefName headRefName headRefOid
      commits(last: 1) { nodes { commit { oid statusCheckRollup { contexts(first: 100) { nodes {
        __typename
        ... on CheckRun { name status conclusion detailsUrl startedAt completedAt databaseId
          checkSuite { workflowRun { databaseId workflow { name } } } }
        ... on StatusContext { context state targetUrl createdAt }
      } } } } } }
      reviews(last: 50) { nodes { author { login } state body submittedAt url } }
      reviewThreads(first: 100) { nodes { isResolved isOutdated path line originalLine
        comments(first: 50) { nodes { author { login } body createdAt url } } } }
      comments(last: 50) { nodes { author { login } body createdAt url } }
    }
  }
}`

type gqlAuthor struct {
	Login string `json:"login"`
}

type gqlComment struct {
	Author    *gqlAuthor `json:"author"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	URL       string     `json:"url"`
}

func (c gqlComment) conv() PRComment {
	return PRComment{Author: login(c.Author), Body: c.Body, At: c.CreatedAt, URL: c.URL}
}

func login(a *gqlAuthor) string {
	if a == nil || a.Login == "" {
		return "ghost" // deleted account
	}
	return a.Login
}

type gqlPR struct {
	Number           int    `json:"number"`
	Title            string `json:"title"`
	Body             string `json:"body"`
	URL              string `json:"url"`
	State            string `json:"state"`
	IsDraft          bool   `json:"isDraft"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
	BaseRefName      string `json:"baseRefName"`
	HeadRefName      string `json:"headRefName"`
	HeadRefOid       string `json:"headRefOid"`
	Commits          struct {
		Nodes []struct {
			Commit struct {
				OID               string `json:"oid"`
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []struct {
							Typename    string    `json:"__typename"`
							Name        string    `json:"name"`
							Status      string    `json:"status"`
							Conclusion  string    `json:"conclusion"`
							DetailsURL  string    `json:"detailsUrl"`
							StartedAt   time.Time `json:"startedAt"`
							CompletedAt time.Time `json:"completedAt"`
							DatabaseID  int64     `json:"databaseId"`
							CheckSuite  *struct {
								WorkflowRun *struct {
									DatabaseID int64 `json:"databaseId"`
									Workflow   struct {
										Name string `json:"name"`
									} `json:"workflow"`
								} `json:"workflowRun"`
							} `json:"checkSuite"`
							Context   string    `json:"context"`
							State     string    `json:"state"`
							TargetURL string    `json:"targetUrl"`
							CreatedAt time.Time `json:"createdAt"`
						} `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		Nodes []struct {
			Author      *gqlAuthor `json:"author"`
			State       string     `json:"state"`
			Body        string     `json:"body"`
			SubmittedAt time.Time  `json:"submittedAt"`
			URL         string     `json:"url"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		Nodes []struct {
			IsResolved   bool   `json:"isResolved"`
			IsOutdated   bool   `json:"isOutdated"`
			Path         string `json:"path"`
			Line         int    `json:"line"`
			OriginalLine int    `json:"originalLine"`
			Comments     struct {
				Nodes []gqlComment `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Comments struct {
		Nodes []gqlComment `json:"nodes"`
	} `json:"comments"`
}

// parsePRView turns the graphql response into a PRView.
func parsePRView(b []byte, now time.Time) (*PRView, error) {
	var res struct {
		Data struct {
			Repository *struct {
				PullRequest *gqlPR `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("gh api graphql: %w", err)
	}
	if res.Data.Repository == nil || res.Data.Repository.PullRequest == nil {
		if len(res.Errors) > 0 {
			return nil, errors.New("gh api graphql: " + res.Errors[0].Message)
		}
		return nil, errors.New("gh api graphql: PR not found")
	}
	g := res.Data.Repository.PullRequest
	v := &PRView{
		Number: g.Number, Title: g.Title, Body: g.Body, URL: g.URL, State: g.State, IsDraft: g.IsDraft,
		Mergeable: g.Mergeable, MergeState: g.MergeStateStatus, ReviewDecision: g.ReviewDecision,
		Base: g.BaseRefName, Head: g.HeadRefName, HeadSHA: strings.ToLower(g.HeadRefOid), FetchedAt: now,
		Checks: []PRCheck{}, Reviews: []PRReview{}, Threads: []PRThread{}, Comments: []PRComment{},
	}
	for _, c := range g.Commits.Nodes {
		if c.Commit.StatusCheckRollup == nil {
			continue
		}
		if oid := strings.ToLower(c.Commit.OID); oid != "" && oid != v.HeadSHA {
			// not the head's commit (the head moved a moment ago): its
			// checks are not this head's result (wait's ci is told per head)
			continue
		}
		for _, n := range c.Commit.StatusCheckRollup.Contexts.Nodes {
			var ck PRCheck
			if n.Typename == "StatusContext" {
				ck = PRCheck{Name: n.Context, Status: n.State, URL: n.TargetURL, StartedAt: n.CreatedAt}
				if n.State != "PENDING" && n.State != "EXPECTED" {
					ck.Conclusion = n.State
				}
			} else {
				ck = PRCheck{Name: n.Name, Status: n.Status, Conclusion: n.Conclusion, URL: n.DetailsURL,
					StartedAt: n.StartedAt, FinishedAt: n.CompletedAt}
				if n.CheckSuite != nil && n.CheckSuite.WorkflowRun != nil {
					ck.RunID, ck.JobID = n.CheckSuite.WorkflowRun.DatabaseID, n.DatabaseID
					ck.Workflow = n.CheckSuite.WorkflowRun.Workflow.Name
				}
				if !ck.StartedAt.IsZero() {
					end := ck.FinishedAt
					if end.IsZero() {
						end = now
					}
					ck.Seconds = max(int(end.Sub(ck.StartedAt).Seconds()), 0) // clock skew
				}
			}
			ck.Bucket = checkBucket(ck)
			v.Checks = append(v.Checks, ck)
		}
	}
	for _, r := range g.Reviews.Nodes {
		v.Reviews = append(v.Reviews, PRReview{Author: login(r.Author), State: r.State, Body: r.Body, At: r.SubmittedAt, URL: r.URL})
	}
	for _, t := range g.ReviewThreads.Nodes {
		th := PRThread{Path: t.Path, Line: t.Line, OrigLine: t.OriginalLine, Resolved: t.IsResolved, Outdated: t.IsOutdated, Comments: []PRComment{}}
		for _, c := range t.Comments.Nodes {
			th.Comments = append(th.Comments, c.conv())
		}
		v.Threads = append(v.Threads, th)
	}
	for _, c := range g.Comments.Nodes {
		v.Comments = append(v.Comments, c.conv())
	}
	return v, nil
}

// checkBucket sorts a check into pass | fail | pending | skip (like `gh pr checks`).
func checkBucket(c PRCheck) string {
	switch c.Conclusion {
	case "SUCCESS":
		return "pass"
	case "FAILURE", "ERROR", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return "fail"
	case "NEUTRAL", "SKIPPED", "CANCELLED", "STALE":
		return "skip"
	}
	return "pending"
}

// failedChecksKept is how many failing checks' names the summary keeps.
const failedChecksKept = 10

// Summary is the small part kept on the session (Session.PRStatus).
func (v *PRView) Summary() *state.PRStatus {
	st := &state.PRStatus{State: v.State, Draft: v.IsDraft, ReviewDecision: v.ReviewDecision, MergeState: v.MergeState, HeadSHA: v.HeadSHA, At: v.FetchedAt}
	for _, c := range v.Checks {
		switch c.Bucket {
		case "pass":
			st.Pass++
		case "fail":
			st.Fail++
			if len(st.FailedChecks) < failedChecksKept {
				st.FailedChecks = append(st.FailedChecks, lineCut(c.Name, 60))
			}
		case "pending":
			st.Pending++
		case "skip":
			st.Skip++
		}
	}
	st.CIResult = ciResult(st, v.Checks)
	later := func(t *time.Time, at time.Time) {
		if at.After(*t) {
			*t = at
		}
	}
	for _, c := range v.Comments {
		later(&st.LastComment, c.At)
	}
	for _, r := range v.Reviews {
		later(&st.LastComment, r.At)
	}
	for _, t := range v.Threads {
		if !t.Resolved {
			st.Unresolved++
		}
		for _, c := range t.Comments {
			later(&st.LastComment, c.At)
			if !t.Resolved {
				later(&st.LastThread, c.At)
			}
		}
	}
	return st
}

// prAttention lists what in next deserves a look compared to prev.
func prAttention(prev, next *state.PRStatus) []string {
	if next == nil || next.State != "OPEN" {
		return nil
	}
	if prev == nil {
		prev = &state.PRStatus{}
	}
	var out []string
	if next.Fail > 0 && prev.Fail == 0 {
		out = append(out, "ci_failed")
	}
	if next.ReviewDecision == "CHANGES_REQUESTED" && prev.ReviewDecision != "CHANGES_REQUESTED" {
		out = append(out, "changes_requested")
	}
	if next.Unresolved > 0 && (next.Unresolved > prev.Unresolved || next.LastThread.After(prev.LastThread)) {
		out = append(out, "new_comment")
	}
	if next.MergeState == "CLEAN" && prev.MergeState != "CLEAN" {
		out = append(out, "mergeable")
	}
	return out
}

// ciSettled reports whether the CI of an open PR's head has a result: no
// check is pending and at least one passed or failed. A PR without checks
// (or with skipped ones only) never settles.
func ciSettled(st *state.PRStatus) bool {
	return st != nil && st.State == "OPEN" && st.HeadSHA != "" && st.Pending == 0 && st.Pass+st.Fail > 0
}

// ciResult is what wait's ci tells about checks (counted in st): green or
// red, the counts, and a hash of which checks passed and which failed
// (workflow, name and bucket; not the run, so a re-run with the same
// outcome is the same result). A check added late or a re-run that turns
// red to green changes it, and the same head is told again (applyPRStatus).
// Summary keeps it in PRStatus.CIResult; applyPRStatus replaces it with
// the result told.
func ciResult(st *state.PRStatus, checks []PRCheck) string {
	var keys []string
	for _, c := range checks {
		if c.Bucket == "pass" || c.Bucket == "fail" {
			keys = append(keys, c.Workflow+"\x00"+c.Name+"\x00"+c.Bucket)
		}
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	res := "green"
	if st.Fail > 0 {
		res = "red"
	}
	return fmt.Sprintf("%s %d/%d %x", res, st.Pass, st.Fail, sum[:6])
}

// ciSummary is the one line wait's ci gives: green or red, the failed
// checks and the head it is about.
func ciSummary(s *state.Session) string {
	st := s.PRStatus
	res := fmt.Sprintf("green: %d passed", st.Pass)
	if st.Fail > 0 {
		names := st.FailedChecks
		if len(names) > 3 {
			names = names[:3]
		}
		list := strings.Join(names, ", ")
		if more := st.Fail - len(names); more > 0 {
			list += fmt.Sprintf(", +%d more", more)
		}
		res = fmt.Sprintf("red: %d failed (%s), %d passed", st.Fail, list, st.Pass)
	}
	return fmt.Sprintf("%s: PR #%d ci %s, head %s", s.Slug, s.PR, res, st.CIHead)
}

// addCIChange tells wait's ci for s's settled CI (s.PRStatus, its CIHead
// and result). The one place a ci change is made: sessionChanges when
// CIHead moves to a new head, applyPRStatus when the same head settles
// with another result.
func (d *Daemon) addCIChange(s *state.Session) {
	brief := "ci green"
	if s.PRStatus.Fail > 0 {
		brief = "ci red"
	}
	d.changes.add(Change{Kind: kindCI, Slug: s.Slug, Summary: ciSummary(s), parent: s.Parent, parentBorn: s.ParentBorn, brief: brief})
}

// ---- gh plumbing ----

func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// ghCommand is the GitHub CLI for repo (host/owner/name, from ghRepo /
// ghRepoOf). gh does not run git: its cwd is an empty directory, and the
// repository goes in by --repo (gh api, which takes none: --hostname and
// the owner and name in the path or fields, ghAPIRepo). gh runs git where
// it starts (git status for pr create, the remotes to find the
// repository), and a worktree's git config and .gitattributes are the
// agent's: filter drivers of any name would run outside the sandbox. So gh
// never starts in a worktree or a checkout, and it gets an environment of
// its own (ghEnv: no GIT_DIR / GIT_WORK_TREE, git discovery stopped above
// the empty directory). What gh would have read from git (the title and
// body of pr create) pekoed reads with gitx and passes in.
//
// done removes the directory; call it once the command has finished.
func ghCommand(ctx context.Context, repo string, args ...string) (c *exec.Cmd, done func(), err error) {
	if repo == "" {
		return nil, nil, fmt.Errorf("gh %s: %w", args[0], errNoGHRepo)
	}
	parent := filepath.Join(paths.DataDir(), "gh")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp(parent, "cwd-")
	if err != nil {
		return nil, nil, err
	}
	c = exec.CommandContext(ctx, "gh", args...)
	c.Dir = dir
	c.Env = ghEnv(parent)
	return c, func() { os.RemoveAll(dir) }, nil
}

var errNoGHRepo = errors.New("no GitHub repository in origin (host/owner/name)")

// ghEnvKeep are what gh needs from pekoed's environment (with GH_* and
// XDG_*): where it finds itself and its config and token (on Linux the
// token may be in the Secret Service, reached over the session D-Bus), the
// locale, proxies and certificates.
var ghEnvKeep = []string{
	"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE",
	"DBUS_SESSION_BUS_ADDRESS",
	"GITHUB_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
}

// ghEnv is gh's environment: ghEnvKeep, GH_* and XDG_*, nothing of git's
// (GIT_DIR, GIT_WORK_TREE, GIT_CONFIG_*, …), and GIT_CEILING_DIRECTORIES
// at ceiling, so a git gh starts anyway finds no repository above its cwd.
func ghEnv(ceiling string) []string {
	if r, err := filepath.EvalSymlinks(ceiling); err == nil {
		ceiling = r
	}
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "GH_") || strings.HasPrefix(k, "XDG_") || slices.Contains(ghEnvKeep, k) {
			env = append(env, e)
		}
	}
	return append(env, "GIT_CEILING_DIRECTORIES="+ceiling)
}

// ghAPIRepo splits repo (host/owner/name) for gh api, which takes no
// --repo: host is the flags that name the host (none for github.com).
func ghAPIRepo(repo string) (host []string, owner, name string) {
	h, rest, _ := strings.Cut(repo, "/")
	owner, name, _ = strings.Cut(rest, "/")
	if h != "github.com" {
		host = []string{"--hostname", h}
	}
	return host, owner, name
}

// gh runs the GitHub CLI for repo; the error carries gh's stderr.
func gh(ctx context.Context, repo string, args ...string) ([]byte, error) {
	c, done, err := ghCommand(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := c.Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return out, fmt.Errorf("gh %s: %s", args[0], msg)
	}
	return out, nil
}

// fetchPRView reads PR number of repo (host/owner/name) in one gh api
// graphql call.
func fetchPRView(ctx context.Context, repo string, number int) (*PRView, error) {
	host, owner, name := ghAPIRepo(repo)
	args := append([]string{"api", "graphql"}, host...)
	args = append(args, "-F", "owner="+owner, "-F", "name="+name, "-F", "number="+strconv.Itoa(number), "-f", "query="+prQuery)
	out, err := gh(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	return parsePRView(out, time.Now())
}

// ---- pr.view ----

type prViewP struct {
	Slug string `json:"slug"`
	Seen bool   `json:"seen"` // the user is looking at it: clear PR attention
}

// hPRView fetches the full PR picture and refreshes the session's summary
// on the way. The TUI calls it every 15 s while its PR view is open, which
// is also what keeps the background poller (60 s) off that session.
func (d *Daemon) hPRView(p prViewP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if !ghAvailable() {
		return nil, errors.New("gh not found: install the GitHub CLI")
	}
	pr, repo := s.PR, d.ghRepo(s)
	if pr == 0 && !s.PRReview && s.Branch != "" && s.Branch != s.Base {
		if pr = prForBranch(d.ghRepoInfoOf(s.RepoDir), s.Branch, s.CreatedAt); pr > 0 {
			d.update(p.Slug, func(x *state.Session) {
				if x.PR == 0 {
					x.PR = pr
				}
			})
		}
	}
	if pr == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, err := fetchPRView(ctx, repo, pr)
	if err != nil {
		return nil, err
	}
	d.applyPRStatus(p.Slug, pr, v.Summary(), p.Seen)
	return v, nil
}

// applyPRStatus stores a fresh summary of PR pr, adds attention for what
// changed and emits session.updated only when something did. A summary
// of a PR that is no longer the session's (land moved it to a new PR
// while gh read the old one) is dropped: it would show the old PR's
// state as the new one's, and a MERGED one would tear the session down.
func (d *Daemon) applyPRStatus(slug string, pr int, next *state.PRStatus, seen bool) {
	d.prMu.Lock()
	defer d.prMu.Unlock()
	d.mu.Lock()
	s, ok := d.sessions[slug]
	if !ok || s.PR != pr {
		d.mu.Unlock()
		return
	}
	prev, landing := s.PRStatus, s.Landing
	d.prFetched[slug] = time.Now()
	d.mu.Unlock()
	ours := true // pr is still the session's when stored (update below)
	if landing && next.State == "MERGED" {
		// pekoe land's PR got merged: land it now rather than at watchMerges'
		// next round (every 2 minutes). Polling stops at MERGED, so this is
		// the one chance; the teardown can take minutes, so not on the
		// poller's goroutine. finishLanding runs once per session even when
		// watchMerges starts it too.
		defer func() {
			if ours {
				go d.landMerged(slug)
			}
		}()
	}
	if prev != nil && !seen {
		next.Attention, next.AttentionAt = append([]string(nil), prev.Attention...), prev.AttentionAt
	}
	for _, a := range prAttention(prev, next) {
		if seen {
			break
		}
		dup := false
		for _, x := range next.Attention {
			dup = dup || x == a
		}
		if !dup {
			next.Attention = append(next.Attention, a)
			next.AttentionAt = time.Now()
		}
	}
	if next.State != "OPEN" { // merged / closed: nothing left to act on
		next.Attention, next.AttentionAt = nil, time.Time{}
	}
	// wait's ci: told when the head's CI settles, once per head and result.
	// A new head is told by sessionChanges (it sees CIHead move); the same
	// head with another settled result (a check that registered after the
	// first ones finished, a re-run) is told here. Kept across restarts with
	// the session.
	result := next.CIResult // Summary's: the checks now
	next.CIHead, next.CIResult = "", ""
	if prev != nil {
		next.CIHead, next.CIResult = prev.CIHead, prev.CIResult
	}
	again := false
	if ciSettled(next) {
		// a record from before ci_result (CIResult "") takes the result
		// without telling the head again
		again = next.CIHead == next.HeadSHA && next.CIResult != "" && next.CIResult != result
		next.CIHead, next.CIResult = next.HeadSHA, result
	}
	if prev.Same(next) {
		return
	}
	c, err := d.update(slug, func(x *state.Session) {
		if x.PR != pr { // land moved it to a new PR since the check above
			ours = false
			return
		}
		x.PRStatus = next
		landedPRMerged(x) // a fetch from before the merge that finished after it
	})
	if again && ours && err == nil && c.PRStatus != nil {
		d.addCIChange(c)
	}
}

// landedPRMerged marks a landed session's PR as merged. A session lands
// only when pekoed saw its PR merged (or, without origin, with no PR
// at all), and polling stops at landed, so the summary would otherwise stay
// at what it was last fetched before the merge: CI running, CI passed.
// It reports whether it changed anything. Called under the session's lock
// (update).
func landedPRMerged(x *state.Session) bool {
	if x.State != state.Landed || x.PR == 0 || (x.PRStatus != nil && x.PRStatus.State == "MERGED") {
		return false
	}
	c := state.PRStatus{}
	if x.PRStatus != nil {
		c = *x.PRStatus
	}
	c.State = "MERGED"
	c.Attention, c.AttentionAt = nil, time.Time{} // nothing left to act on
	x.PRStatus = &c
	return true
}

// hPRSeen clears PR attention without fetching (the PR view was opened on
// data the TUI already had).
func (d *Daemon) hPRSeen(p slugP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.PRStatus == nil || len(s.PRStatus.Attention) == 0 {
		return s, nil
	}
	return d.update(p.Slug, func(x *state.Session) {
		if x.PRStatus != nil {
			c := *x.PRStatus
			c.Attention, c.AttentionAt = nil, time.Time{}
			x.PRStatus = &c
		}
	})
}

// ---- background polling ----

// watchPRStatus refreshes the summary of every open PR at most once per
// [pr] poll_every (a minute by default; sessions whose PR view is open are kept fresh by pr.view). It also
// notices merges and closes of any session, landing or not; a landing
// session's merge starts its teardown right away (applyPRStatus), other
// sessions are left as they are. No gh, or gh without auth: nothing happens.
func (d *Daemon) watchPRStatus() {
	if !d.prPolling.CompareAndSwap(false, true) {
		return
	}
	defer d.prPolling.Store(false)
	if !ghAvailable() {
		return
	}
	now, every := time.Now(), d.cfg().PR.Poll()
	d.mu.Lock()
	var due []state.Session
	for slug, s := range d.sessions {
		if s.PR == 0 || s.State == state.Landed || s.State == state.Creating || s.State == state.Queued {
			continue
		}
		if s.PRStatus != nil && s.PRStatus.State != "OPEN" {
			continue
		}
		if now.Sub(d.prFetched[slug]) < every {
			continue
		}
		due = append(due, *s)
	}
	d.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].Slug < due[j].Slug })
	for _, s := range due {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		v, err := fetchPRView(ctx, d.ghRepo(&s), s.PR)
		cancel()
		if err != nil {
			// unauthenticated, offline, rate limited: try again next round
			d.mu.Lock()
			d.prFetched[s.Slug] = now
			d.mu.Unlock()
			continue
		}
		d.applyPRStatus(s.Slug, s.PR, v.Summary(), false)
	}
}

// ---- pr.failed_log ----

type failedLogP struct {
	Slug  string `json:"slug"`
	Check string `json:"check"`  // check name
	RunID int64  `json:"run_id"` // optional: skips the lookup
	JobID int64  `json:"job_id"`
	Lines int    `json:"lines"` // default 200
}

type FailedLog struct {
	Check     string `json:"check"`
	RunID     int64  `json:"run_id"`
	JobID     int64  `json:"job_id,omitempty"`
	Log       string `json:"log"`
	Truncated bool   `json:"truncated"`
}

var logStampRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z ?`)

// plainLog drops what `gh run view --log` puts before every line ("job\tstep\t
// timestamp ") and escape codes, leaving what the step printed.
func plainLog(s string) string {
	lines := strings.Split(stripANSI(strings.ReplaceAll(s, "\r\n", "\n")), "\n")
	for i, l := range lines {
		if f := strings.SplitN(l, "\t", 3); len(f) == 3 {
			l = f[2]
		}
		lines[i] = logStampRe.ReplaceAllString(l, "")
	}
	return strings.Join(lines, "\n")
}

// hFailedLog returns the tail of `gh run view --log-failed` for a failed
// GitHub Actions check (per job when possible).
func (d *Daemon) hFailedLog(p failedLogP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if !ghAvailable() {
		return nil, errors.New("gh not found: install the GitHub CLI")
	}
	if p.Lines <= 0 {
		p.Lines = 200
	}
	repo := d.ghRepo(s)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if p.RunID == 0 {
		if s.PR == 0 {
			return nil, errors.New("no PR for this session")
		}
		v, err := fetchPRView(ctx, repo, s.PR)
		if err != nil {
			return nil, err
		}
		for _, c := range v.Checks {
			if c.Name == p.Check && (p.RunID == 0 || c.Bucket == "fail") {
				p.RunID, p.JobID = c.RunID, c.JobID
			}
		}
		if p.RunID == 0 {
			return nil, fmt.Errorf("%q is not a GitHub Actions check of PR #%d", p.Check, s.PR)
		}
	}
	var out []byte
	if p.JobID > 0 {
		out, err = gh(ctx, repo, "run", "view", "--job", strconv.FormatInt(p.JobID, 10), "--log-failed", "--repo", repo)
	}
	if p.JobID == 0 || err != nil || len(strings.TrimSpace(string(out))) == 0 {
		out, err = gh(ctx, repo, "run", "view", strconv.FormatInt(p.RunID, 10), "--log-failed", "--repo", repo)
		if err != nil {
			return nil, err
		}
	}
	text := plainLog(string(out))
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	return &FailedLog{Check: p.Check, RunID: p.RunID, JobID: p.JobID, Log: tail(text, p.Lines), Truncated: len(all) > p.Lines}, nil
}
