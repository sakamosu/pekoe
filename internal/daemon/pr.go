package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"boba/internal/state"
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
	Checks         []PRCheck   `json:"checks"`
	Reviews        []PRReview  `json:"reviews"`
	Threads        []PRThread  `json:"threads"`
	Comments       []PRComment `json:"comments"`
	FetchedAt      time.Time   `json:"fetched_at"`
}

const prQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title body url state isDraft mergeable mergeStateStatus reviewDecision baseRefName headRefName
      commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { nodes {
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
	Commits          struct {
		Nodes []struct {
			Commit struct {
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
		Base: g.BaseRefName, Head: g.HeadRefName, FetchedAt: now,
		Checks: []PRCheck{}, Reviews: []PRReview{}, Threads: []PRThread{}, Comments: []PRComment{},
	}
	for _, c := range g.Commits.Nodes {
		if c.Commit.StatusCheckRollup == nil {
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

// Summary is the small part kept on the session (Session.PRStatus).
func (v *PRView) Summary() *state.PRStatus {
	st := &state.PRStatus{State: v.State, Draft: v.IsDraft, ReviewDecision: v.ReviewDecision, MergeState: v.MergeState, At: v.FetchedAt}
	for _, c := range v.Checks {
		switch c.Bucket {
		case "pass":
			st.Pass++
		case "fail":
			st.Fail++
		case "pending":
			st.Pending++
		}
	}
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

// ---- gh plumbing ----

func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// gh runs the GitHub CLI in dir; the error carries gh's stderr.
func gh(ctx context.Context, dir string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "gh", args...)
	c.Dir = dir
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

// ghDir is where gh resolves {owner}/{repo}: the worktree, or the main
// worktree once the session's one is gone.
func ghDir(s *state.Session) string {
	if s.Path != "" {
		if _, err := os.Stat(s.Path); err == nil {
			return s.Path
		}
	}
	return s.RepoDir
}

func fetchPRView(ctx context.Context, dir string, number int) (*PRView, error) {
	out, err := gh(ctx, dir, "api", "graphql", "-F", "owner={owner}", "-F", "name={repo}", "-F", "number="+strconv.Itoa(number), "-f", "query="+prQuery)
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
	pr := s.PR
	if pr == 0 && !s.PRReview && s.Branch != "" && s.Branch != s.Base {
		if pr = prForBranch(ghDir(s), s.Branch); pr > 0 {
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
	v, err := fetchPRView(ctx, ghDir(s), pr)
	if err != nil {
		return nil, err
	}
	d.applyPRStatus(p.Slug, v.Summary(), p.Seen)
	return v, nil
}

// applyPRStatus stores a fresh summary, adds attention for what changed and
// emits session.updated only when something did.
func (d *Daemon) applyPRStatus(slug string, next *state.PRStatus, seen bool) {
	d.prMu.Lock()
	defer d.prMu.Unlock()
	d.mu.Lock()
	s, ok := d.sessions[slug]
	if !ok {
		d.mu.Unlock()
		return
	}
	prev := s.PRStatus
	d.prFetched[slug] = time.Now()
	d.mu.Unlock()
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
	if prev.Same(next) {
		return
	}
	d.update(slug, func(x *state.Session) { x.PRStatus = next })
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

const prPollEvery = 60 * time.Second

// watchPRStatus refreshes the summary of every open PR at most once a
// minute (sessions whose PR view is open are kept fresh by pr.view). It also
// notices merges and closes of any session, landing or not; tearing down
// stays with watchMerges (landing sessions only). No gh, or gh without auth:
// nothing happens.
func (d *Daemon) watchPRStatus() {
	if !d.prPolling.CompareAndSwap(false, true) {
		return
	}
	defer d.prPolling.Store(false)
	if !ghAvailable() {
		return
	}
	now := time.Now()
	d.mu.Lock()
	var due []state.Session
	for slug, s := range d.sessions {
		if s.PR == 0 || s.State == state.Landed || s.State == state.Creating {
			continue
		}
		if s.PRStatus != nil && s.PRStatus.State != "OPEN" {
			continue
		}
		if now.Sub(d.prFetched[slug]) < prPollEvery {
			continue
		}
		due = append(due, *s)
	}
	d.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].Slug < due[j].Slug })
	for _, s := range due {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		v, err := fetchPRView(ctx, ghDir(&s), s.PR)
		cancel()
		if err != nil {
			// unauthenticated, offline, rate limited: try again next round
			d.mu.Lock()
			d.prFetched[s.Slug] = now
			d.mu.Unlock()
			continue
		}
		d.applyPRStatus(s.Slug, v.Summary(), false)
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
	dir := ghDir(s)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if p.RunID == 0 {
		if s.PR == 0 {
			return nil, errors.New("no PR for this session")
		}
		v, err := fetchPRView(ctx, dir, s.PR)
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
		out, err = gh(ctx, dir, "run", "view", "--job", strconv.FormatInt(p.JobID, 10), "--log-failed")
	}
	if p.JobID == 0 || err != nil || len(strings.TrimSpace(string(out))) == 0 {
		out, err = gh(ctx, dir, "run", "view", strconv.FormatInt(p.RunID, 10), "--log-failed")
		if err != nil {
			return nil, err
		}
	}
	text := plainLog(string(out))
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	return &FailedLog{Check: p.Check, RunID: p.RunID, JobID: p.JobID, Log: tail(text, p.Lines), Truncated: len(all) > p.Lines}, nil
}
