package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"boba/internal/gitx"
	"boba/internal/state"
)

// ---- issue inbox: gh issue list / view, matched against sessions ----

// issueTTL is how long an issues.list / issues.view answer is reused before
// gh is asked again (R in the TUI / --refresh bypasses it).
var issueTTL = 3 * time.Minute

// IssueSession is the session already working on an issue.
type IssueSession struct {
	Slug   string      `json:"slug"`
	Branch string      `json:"branch"`
	State  state.State `json:"state"`
}

type Issue struct {
	Number    int           `json:"number"`
	Title     string        `json:"title"`
	Labels    []string      `json:"labels"`
	Assignees []string      `json:"assignees"`
	UpdatedAt time.Time     `json:"updated_at"`
	Comments  int           `json:"comments"`
	URL       string        `json:"url"`
	Session   *IssueSession `json:"session,omitempty"`
}

type IssueList struct {
	RepoID      string    `json:"repo_id"`
	Filter      string    `json:"filter"` // mine | open | label
	Label       string    `json:"label,omitempty"`
	Issues      []Issue   `json:"issues"`
	FetchedAt   time.Time `json:"fetched_at"`
	Cached      bool      `json:"cached"`
	Unavailable string    `json:"unavailable,omitempty"` // one line for the TUI: "no GitHub remote" …
}

type IssueComment struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type IssueDetail struct {
	Issue
	Body      string         `json:"body"`
	State     string         `json:"state"`
	Author    string         `json:"author"`
	CreatedAt time.Time      `json:"created_at"`
	Thread    []IssueComment `json:"thread"`
}

type issuesP struct {
	Repo    string `json:"repo"`    // a path inside the repository
	RepoID  string `json:"repo_id"` // or the id of a repository with a session
	Filter  string `json:"filter"`  // mine (default) | open | label
	Label   string `json:"label"`
	Number  int    `json:"number"` // issues.view
	Refresh bool   `json:"refresh"`
}

type issueCacheEntry struct {
	at   time.Time
	list *IssueList
	view *IssueDetail
}

// issueRepo resolves the repository directory and id from a path or an id.
func (d *Daemon) issueRepo(p issuesP) (dir, id string, err error) {
	if p.Repo != "" {
		r, err := resolveRepo(p.Repo)
		if err != nil {
			return "", "", err
		}
		return r.Dir, r.ID, nil
	}
	if p.RepoID != "" {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, s := range d.sessions {
			if s.RepoID == p.RepoID && s.RepoDir != "" {
				return s.RepoDir, s.RepoID, nil
			}
		}
		return "", "", fmt.Errorf("unknown repo_id: %s (pass repo: <path>)", p.RepoID)
	}
	return "", "", errors.New("repo or repo_id required")
}

// ghUnavailable turns "gh cannot answer for this repository" into one line.
// It returns "" when gh might work.
func ghUnavailable(dir string) string {
	if _, err := exec.LookPath("gh"); err != nil {
		return "gh not installed"
	}
	if gitx.Origin(dir) == "" {
		return "no GitHub remote"
	}
	return ""
}

// ghError maps gh's stderr to the one line the TUI shows.
func ghError(err error, out []byte) string {
	msg := strings.TrimSpace(string(out))
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		msg = strings.TrimSpace(string(ee.Stderr))
	}
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "auth login"), strings.Contains(l, "not logged"), strings.Contains(l, "authentication"):
		return "gh not authenticated · gh auth login"
	case strings.Contains(l, "none of the git remotes"), strings.Contains(l, "no git remotes"),
		strings.Contains(l, "could not resolve to a repository"), strings.Contains(l, "not a github"):
		return "no GitHub remote"
	case strings.Contains(l, "issues are disabled"), strings.Contains(l, "has disabled issues"):
		return "issues are disabled for this repository"
	}
	if msg == "" {
		msg = err.Error()
	}
	return "gh: " + strings.SplitN(msg, "\n", 2)[0]
}

func runGH(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "gh", args...)
	c.Dir = dir
	return c.Output()
}

func normFilter(p *issuesP) {
	switch p.Filter {
	case "", "mine", "@me":
		p.Filter = "mine"
	case "open", "all":
		p.Filter = "open"
	case "label":
	default:
		if l, ok := strings.CutPrefix(p.Filter, "label:"); ok {
			p.Filter, p.Label = "label", l
		} else {
			p.Filter = "mine"
		}
	}
	if p.Filter == "label" && strings.TrimSpace(p.Label) == "" {
		p.Filter = "open"
	}
	if p.Filter != "label" {
		p.Label = ""
	}
}

// ghIssue is the shape of `gh issue list/view --json …`.
type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	UpdatedAt time.Time       `json:"updatedAt"`
	CreatedAt time.Time       `json:"createdAt"`
	Comments  json.RawMessage `json:"comments"`
}

type ghComment struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

// comments: gh gives an array of comments; older versions / the GraphQL
// shape give {totalCount}.
func (g ghIssue) comments() ([]ghComment, int) {
	var list []ghComment
	if json.Unmarshal(g.Comments, &list) == nil {
		return list, len(list)
	}
	var tc struct {
		TotalCount int `json:"totalCount"`
	}
	if json.Unmarshal(g.Comments, &tc) == nil {
		return nil, tc.TotalCount
	}
	var n int
	_ = json.Unmarshal(g.Comments, &n)
	return nil, n
}

func (g ghIssue) issue() Issue {
	is := Issue{Number: g.Number, Title: g.Title, URL: g.URL, UpdatedAt: g.UpdatedAt, Labels: []string{}, Assignees: []string{}}
	for _, l := range g.Labels {
		is.Labels = append(is.Labels, l.Name)
	}
	for _, a := range g.Assignees {
		is.Assignees = append(is.Assignees, a.Login)
	}
	_, is.Comments = g.comments()
	return is
}

func (d *Daemon) hIssuesList(p issuesP) (any, error) {
	normFilter(&p)
	dir, id, err := d.issueRepo(p)
	if err != nil {
		return nil, err
	}
	key := "list\x00" + dir + "\x00" + p.Filter + "\x00" + p.Label
	var res IssueList
	if c, ok := d.issueCached(key, p.Refresh); ok && c.list != nil {
		res = *c.list
		res.Cached = true
	} else {
		res = IssueList{RepoID: id, Filter: p.Filter, Label: p.Label, Issues: []Issue{}, FetchedAt: time.Now()}
		if u := ghUnavailable(dir); u != "" {
			res.Unavailable = u
		} else {
			args := []string{"issue", "list", "--state", "open", "--limit", "100", "--json", "number,title,labels,assignees,updatedAt,comments,url"}
			switch p.Filter {
			case "mine":
				args = append(args, "--assignee", "@me")
			case "label":
				args = append(args, "--label", p.Label)
			}
			out, err := runGH(dir, args...)
			var raw []ghIssue
			switch {
			case err != nil:
				res.Unavailable = ghError(err, out)
			case json.Unmarshal(out, &raw) != nil:
				res.Unavailable = "gh: unexpected output from gh issue list"
			default:
				for _, g := range raw {
					res.Issues = append(res.Issues, g.issue())
				}
			}
		}
		cp := res
		cp.Issues = append([]Issue(nil), res.Issues...)
		d.issueStore(key, issueCacheEntry{at: res.FetchedAt, list: &cp})
	}
	// sessions change faster than issues: match on every call, not from the cache
	issues := make([]Issue, len(res.Issues))
	copy(issues, res.Issues)
	for i := range issues {
		issues[i].Session = d.sessionForIssue(id, issues[i].Number)
	}
	res.Issues = issues
	return &res, nil
}

func (d *Daemon) hIssuesView(p issuesP) (any, error) {
	if p.Number <= 0 {
		return nil, errors.New("number required")
	}
	dir, id, err := d.issueRepo(p)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("view\x00%s\x00%d", dir, p.Number)
	var det IssueDetail
	if c, ok := d.issueCached(key, p.Refresh); ok && c.view != nil {
		det = *c.view
	} else {
		if u := ghUnavailable(dir); u != "" {
			return nil, errors.New(u)
		}
		out, err := runGH(dir, "issue", "view", strconv.Itoa(p.Number), "--json", "number,title,body,state,author,labels,assignees,comments,url,createdAt,updatedAt")
		if err != nil {
			return nil, errors.New(ghError(err, out))
		}
		var g ghIssue
		if err := json.Unmarshal(out, &g); err != nil {
			return nil, fmt.Errorf("gh issue view: %w", err)
		}
		det = IssueDetail{Issue: g.issue(), Body: g.Body, State: g.State, Author: g.Author.Login, CreatedAt: g.CreatedAt, Thread: []IssueComment{}}
		list, n := g.comments()
		for _, c := range list {
			det.Thread = append(det.Thread, IssueComment{Author: c.Author.Login, Body: c.Body, CreatedAt: c.CreatedAt})
		}
		det.Comments = n
		cp := det
		d.issueStore(key, issueCacheEntry{at: time.Now(), view: &cp})
	}
	det.Session = d.sessionForIssue(id, det.Number)
	return &det, nil
}

func (d *Daemon) issueCached(key string, refresh bool) (issueCacheEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.issueCache[key]
	if !ok || refresh || time.Since(c.at) > issueTTL {
		return issueCacheEntry{}, false
	}
	return c, true
}

func (d *Daemon) issueStore(key string, e issueCacheEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.issueCache == nil {
		d.issueCache = map[string]issueCacheEntry{}
	}
	d.issueCache[key] = e
}

// sessionForIssue finds the live session of this repository that works on
// issue n: one created with --from gh#n first, then one whose branch names
// the number (fix/123-login, feat/issue-123, gh-123).
func (d *Daemon) sessionForIssue(repoID string, n int) *IssueSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	var best *state.Session
	for _, s := range d.sessions {
		if s.RepoID != repoID || s.State == state.Landed || s.PRReview {
			continue
		}
		switch {
		case s.Issue == n:
			if best == nil || best.Issue != n || s.CreatedAt.After(best.CreatedAt) {
				best = s
			}
		case best == nil && s.Issue == 0 && BranchIssue(s.Branch) == n:
			best = s
		}
	}
	if best == nil {
		return nil
	}
	return &IssueSession{Slug: best.Slug, Branch: best.Branch, State: best.State}
}

func issueNumber(from string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(from), "gh#"))
	if err != nil || !strings.HasPrefix(strings.TrimSpace(from), "gh#") || n <= 0 {
		return 0
	}
	return n
}

var branchIssueRe = regexp.MustCompile(`(?i)(?:^|[/_-])(?:issues?|gh)[/_-]?(\d+)(?:[/_-]|$)`)

// BranchIssue reads an issue number from a branch name: the last path part
// starting with the number (fix/123-login, 123) or issue-123 / gh-123
// anywhere. 0 when the branch names no issue.
func BranchIssue(branch string) int {
	if m := branchIssueRe.FindStringSubmatch(branch); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	last := branch[strings.LastIndex(branch, "/")+1:]
	digits := len(last) - len(strings.TrimLeft(last, "0123456789"))
	if digits == 0 || (digits < len(last) && last[digits] != '-' && last[digits] != '_') {
		return 0
	}
	n, _ := strconv.Atoi(last[:digits])
	return n
}

// SuggestBranch proposes a branch for working on an issue: fix/ when it is
// labeled as a bug, feat/ otherwise, plus the title in kebab-case. Titles
// with no ASCII words (日本語のタイトル) become <prefix>issue-<n>.
func SuggestBranch(number int, title string, labels []string) string {
	prefix := "feat/"
	for _, l := range labels {
		// bug, type: bug, kind/bug, 🐛 bug … (any word of the label)
		for _, w := range strings.FieldsFunc(strings.ToLower(l), func(r rune) bool { return !unicode.IsLetter(r) }) {
			if w == "bug" {
				prefix = "fix/"
			}
		}
	}
	kebab := Kebab(title, 40)
	if kebab == "" {
		return fmt.Sprintf("%sissue-%d", prefix, number)
	}
	for _, r := range title {
		if r > unicode.MaxASCII && unicode.IsLetter(r) {
			// most of a mixed title (ログインで CSV が落ちる) is lost: keep the number
			return fmt.Sprintf("%sissue-%d-%s", prefix, number, Kebab(title, 30))
		}
	}
	return prefix + kebab
}

// Kebab lowercases ASCII letters and digits and joins the words with '-',
// dropping everything else; the result is cut at a word boundary to at most
// max bytes.
func Kebab(s string, max int) string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			cur.WriteRune(unicode.ToLower(r))
		case r == '\'' || r == '’':
			// don't → dont
		default:
			flush()
		}
	}
	flush()
	out := ""
	for _, w := range words {
		next := w
		if out != "" {
			next = out + "-" + w
		}
		if len(next) > max {
			if out == "" {
				out = w[:max]
			}
			break
		}
		out = next
	}
	return out
}
