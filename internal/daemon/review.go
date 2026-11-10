package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

type changeEntry struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

func (d *Daemon) hChanges(p slugP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	base, _ := d.baseCommit(s, 0)
	tree, err := gitx.SnapshotTree(s.Path)
	if err != nil {
		return nil, err
	}
	out, err := gitx.Run(s.Path, "diff", "--name-status", "--no-renames", "--end-of-options", base, tree)
	if err != nil {
		return nil, err
	}
	res := []changeEntry{}
	for _, ln := range strings.Split(out, "\n") {
		st, path, ok := strings.Cut(ln, "\t")
		if ok {
			res = append(res, changeEntry{Status: st[:1], Path: path})
		}
	}
	return res, nil
}

func (d *Daemon) hReviewStart(p slugP) (any, error) {
	return d.update(p.Slug, func(s *state.Session) {
		if s.State != state.Waiting && s.State != state.Running {
			s.State = state.Reviewing
		}
	})
}

// hReviewEnd is called when a review surface closes. With nothing left to
// send, the session goes back to done (or approved); unsent comments keep it
// in reviewing so the pending review stays visible.
func (d *Daemon) hReviewEnd(p slugP) (any, error) {
	comments, err := d.DB.Comments(p.Slug, true)
	if err != nil {
		return nil, err
	}
	if len(comments) > 0 {
		return d.snapshot(p.Slug)
	}
	s, err := d.update(p.Slug, func(s *state.Session) {
		if s.State == state.Reviewing {
			s.State = state.Done
		}
	})
	if err != nil {
		return nil, err
	}
	d.checkAllApproved(p.Slug)
	return s, nil
}

type PromptRef struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

type HunkView struct {
	gitx.Hunk
	Approved bool       `json:"approved"`
	Rejected bool       `json:"rejected"`
	Prompt   *PromptRef `json:"prompt,omitempty"`
}

type FileView struct {
	Path     string     `json:"path"`
	OldPath  string     `json:"old_path,omitempty"`
	Status   string     `json:"status"`
	Binary   bool       `json:"binary,omitempty"`
	Hunks    []HunkView `json:"hunks"`
	Total    int        `json:"total"`
	Approved int        `json:"approved"`
	Added    int        `json:"added"`
	Deleted  int        `json:"deleted"`
}

type ReviewView struct {
	Slug      string          `json:"slug"`
	Base      string          `json:"base"`
	BaseLabel string          `json:"base_label"`
	Since     int             `json:"since"`
	Total     int             `json:"total"`
	Approved  int             `json:"approved"`
	Files     []FileView      `json:"files"`
	Comments  []state.Comment `json:"comments"`
	Risks     []state.Risk    `json:"risks"`
	Test      state.Test      `json:"test"`
	Diffstat  state.Stat      `json:"diffstat"`
	Prompts   int             `json:"prompts_since"`
	PR        int             `json:"pr,omitempty"`
}

type filesP struct {
	Slug         string `json:"slug"`
	ShowApproved bool   `json:"show_approved"`
	Since        *int   `json:"since"`
}

func (d *Daemon) hReviewFiles(p filesP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	since := -1
	if p.Since != nil {
		since = *p.Since
	}
	base, label := d.baseCommit(s, since)
	tree, err := gitx.SnapshotTree(s.Path)
	if err != nil {
		return nil, err
	}
	files, err := gitx.Diff(s.Path, base, tree)
	if err != nil {
		return nil, err
	}
	marks, _ := d.DB.Hunks(p.Slug)
	prompts, _ := d.DB.Prompts(p.Slug)
	rv := &ReviewView{Slug: p.Slug, Base: base, BaseLabel: label, Since: s.ReviewN, Test: s.Test, PR: s.PR}
	if since >= 0 {
		rv.Since = since
	}
	st := gitx.DiffStat(files)
	rv.Diffstat = state.Stat{Added: st.Added, Deleted: st.Deleted, Files: st.Files}
	attr := newAttributor(s.Path, prompts, tree)
	for _, f := range files {
		fv := FileView{Path: f.Path, OldPath: f.OldPath, Status: f.Status, Binary: f.Binary, Added: f.Added, Deleted: f.Deleted}
		if f.Binary && len(f.Hunks) == 0 {
			// a binary change is reviewed as one pseudo-hunk
			f.Hunks = []gitx.Hunk{{ID: gitx.HunkID(f.Path, gitx.Hunk{Lines: []gitx.Line{{Kind: "+", Text: "binary " + tree}}}), Header: "binary file"}}
		}
		for _, h := range f.Hunks {
			hv := HunkView{Hunk: h, Approved: marks[h.ID] == "approved", Rejected: marks[h.ID] == "rejected"}
			hv.Prompt = attr.find(f.Path, h)
			fv.Total++
			rv.Total++
			if hv.Approved {
				fv.Approved++
				rv.Approved++
				if !p.ShowApproved {
					// keep the header so the UI can show "approved · hidden"
					hv.Lines = nil
				}
			}
			fv.Hunks = append(fv.Hunks, hv)
		}
		rv.Files = append(rv.Files, fv)
	}
	if rv.Files == nil {
		rv.Files = []FileView{}
	}
	rv.Comments, _ = d.DB.Comments(p.Slug, true)
	if rv.Comments == nil {
		rv.Comments = []state.Comment{}
	}
	rv.Risks, _ = d.DB.Risks(p.Slug)
	if rv.Risks == nil {
		rv.Risks = []state.Risk{}
	}
	reviewAt, hasReview := d.DB.ReviewAt(p.Slug, rv.Since)
	for _, pr := range prompts {
		if pr.N > 0 && (!hasReview || pr.At.After(reviewAt)) {
			rv.Prompts++
		}
	}
	return rv, nil
}

// attributor finds which prompt produced a hunk: the changes between
// snapshot prompt-k and prompt-(k+1) (or the current tree) come from prompt k.
type attributor struct {
	dir     string
	prompts []state.Prompt
	tree    string
	cache   map[string]string
}

func newAttributor(dir string, prompts []state.Prompt, tree string) *attributor {
	return &attributor{dir: dir, prompts: prompts, tree: tree, cache: map[string]string{}}
}

func (a *attributor) content(rev, path string) string {
	k := rev + ":" + path
	if c, ok := a.cache[k]; ok {
		return c
	}
	c := gitx.ShowFile(a.dir, rev, path)
	a.cache[k] = c
	return c
}

func (a *attributor) find(path string, h gitx.Hunk) *PromptRef {
	var added []string
	for _, l := range h.Lines {
		if l.Kind == "+" && strings.TrimSpace(l.Text) != "" {
			added = append(added, l.Text)
		}
	}
	if len(added) == 0 || len(a.prompts) == 0 {
		return nil
	}
	has := func(rev string) bool {
		c := a.content(rev, path)
		for _, ln := range added {
			if !strings.Contains(c, ln) {
				return false
			}
		}
		return true
	}
	// earliest snapshot that already contains the hunk's added lines
	for i, p := range a.prompts {
		if has(p.Ref) {
			if i == 0 || a.prompts[i-1].N == 0 {
				return nil // already there at start: not from an agent prompt
			}
			prev := a.prompts[i-1]
			return &PromptRef{N: prev.N, Text: truncate(oneLine(prev.Text), 80)}
		}
	}
	last := a.prompts[len(a.prompts)-1]
	if last.N == 0 {
		return nil
	}
	return &PromptRef{N: last.N, Text: truncate(oneLine(last.Text), 80)}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

type hunkP struct {
	Slug   string `json:"slug"`
	HunkID string `json:"hunk_id"`
	Text   string `json:"text"`
}

func (d *Daemon) setHunk(p hunkP, status string) (any, error) {
	if _, err := d.get(p.Slug); err != nil {
		return nil, err
	}
	if err := d.DB.SetHunk(p.Slug, p.HunkID, status); err != nil {
		return nil, err
	}
	d.emit("review.updated", map[string]any{"slug": p.Slug})
	if status == "approved" {
		d.checkAllApproved(p.Slug)
	}
	return nil, nil
}

// checkAllApproved moves reviewing → approved when every hunk is approved
// and nothing is waiting to be sent.
func (d *Daemon) checkAllApproved(slug string) {
	res, err := d.hReviewFiles(filesP{Slug: slug})
	if err != nil {
		return
	}
	rv := res.(*ReviewView)
	if rv.Total > 0 && rv.Approved == rv.Total && len(rv.Comments) == 0 {
		d.update(slug, func(s *state.Session) {
			if s.State == state.Reviewing || s.State == state.Done {
				s.State = state.Approved
			}
		})
	}
}

func (d *Daemon) findHunk(slug, id string) (*FileView, *HunkView, error) {
	res, err := d.hReviewFiles(filesP{Slug: slug, ShowApproved: true})
	if err != nil {
		return nil, nil, err
	}
	rv := res.(*ReviewView)
	for i := range rv.Files {
		for j := range rv.Files[i].Hunks {
			if rv.Files[i].Hunks[j].ID == id {
				return &rv.Files[i], &rv.Files[i].Hunks[j], nil
			}
		}
	}
	return nil, nil, fmt.Errorf("no such hunk: %s", id)
}

func (d *Daemon) hReject(p hunkP) (any, error) {
	f, h, err := d.findHunk(p.Slug, p.HunkID)
	if err != nil {
		return nil, err
	}
	if err := d.DB.SetHunk(p.Slug, p.HunkID, "rejected"); err != nil {
		return nil, err
	}
	text := p.Text
	if text == "" {
		text = "この変更は取り消してください"
	}
	id, err := d.DB.AddComment(p.Slug, state.Comment{Path: f.Path, Line: h.NewStart, HunkID: h.ID, Text: text, Kind: "reject"})
	if err != nil {
		return nil, err
	}
	d.emit("review.updated", map[string]any{"slug": p.Slug})
	return map[string]any{"id": id}, nil
}

type commentP struct {
	Slug   string `json:"slug"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	HunkID string `json:"hunk_id"`
	Text   string `json:"text"`
}

func (d *Daemon) hComment(p commentP) (any, error) {
	if _, err := d.get(p.Slug); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, errors.New("empty comment")
	}
	id, err := d.DB.AddComment(p.Slug, state.Comment{Path: p.Path, Line: p.Line, HunkID: p.HunkID, Text: p.Text})
	if err != nil {
		return nil, err
	}
	d.emit("review.updated", map[string]any{"slug": p.Slug})
	return map[string]any{"id": id}, nil
}

type commentDelP struct {
	Slug string `json:"slug"`
	ID   int64  `json:"id"`
}

func (d *Daemon) hCommentDelete(p commentDelP) (any, error) {
	if err := d.DB.DeleteComment(p.Slug, p.ID); err != nil {
		return nil, err
	}
	d.emit("review.updated", map[string]any{"slug": p.Slug})
	return nil, nil
}

func hunkText(h *HunkView) string {
	var b strings.Builder
	b.WriteString(h.Header + "\n")
	for _, l := range h.Lines {
		b.WriteString(l.Kind + l.Text + "\n")
	}
	return b.String()
}

type askP struct {
	Slug     string `json:"slug"`
	HunkID   string `json:"hunk_id"`
	Question string `json:"question"`
}

// hAsk sends "what is the intent here?" to the session's agent; the answer
// is cut out at the next Stop hook and emitted as review.answer.
func (d *Daemon) hAsk(p askP) (any, error) {
	f, h, err := d.findHunk(p.Slug, p.HunkID)
	if err != nil {
		return nil, err
	}
	q := p.Question
	if q == "" {
		q = "ここの意図は?"
	}
	// The answer is whatever the next Stop brings: while the agent is still
	// in a turn, that Stop would end the turn under way (its reply taken as
	// the answer, and the test and gatekeeper skipped).
	if s, err := d.snapshot(p.Slug); err != nil {
		return nil, err
	} else if s.State == state.Running || s.State == state.Waiting {
		return nil, fmt.Errorf("%s is in the middle of a turn: ask once it stops (nothing was sent)", p.Slug)
	}
	end := h.NewStart + h.NewLines - 1
	msg := fmt.Sprintf("%s (%s:L%d-%d)\nコードは変更せず、簡潔に答えてください。\n```diff\n%s```", q, f.Path, h.NewStart, end, hunkText(h))
	d.mu.Lock()
	d.asks[p.Slug] = p.HunkID // before the send: the Stop may come quickly
	d.mu.Unlock()
	if _, err := d.hSend(sendP{Slug: p.Slug, Text: msg, Submit: true}); err != nil {
		// not submitted: the next Stop is not an answer
		d.mu.Lock()
		if d.asks[p.Slug] == p.HunkID {
			delete(d.asks, p.Slug)
		}
		d.mu.Unlock()
		return nil, err
	}
	return nil, nil
}

// hFlush sends all unsent comments as one message and records a review point
// (refs/pekoe/<slug>/review-<n>) so the next review shows only what changed.
func (d *Daemon) hFlush(p slugP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	comments, err := d.DB.Comments(p.Slug, true)
	if err != nil {
		return nil, err
	}
	n := s.ReviewN + 1
	sha, err := gitx.SnapshotCommit(s.Path, fmt.Sprintf("pekoe review #%d", n))
	if err != nil {
		return nil, err
	}
	ref := fmt.Sprintf("refs/pekoe/%s/review-%d", p.Slug, n)
	if err := gitx.UpdateRef(s.Path, ref, sha); err != nil {
		return nil, err
	}
	_ = d.DB.AddReview(p.Slug, n, ref)
	sentTo := ""
	if len(comments) > 0 {
		if s.PRReview && s.PR > 0 {
			if err := d.postGitHubReview(s, comments); err != nil {
				return nil, err
			}
			sentTo = fmt.Sprintf("gh#%d", s.PR)
		} else {
			msg := formatReview(n, comments)
			if _, err := d.hSend(sendP{Slug: p.Slug, Text: msg, Submit: true}); err != nil {
				return nil, err
			}
			sentTo = p.Slug
		}
		_ = d.DB.MarkCommentsSent(p.Slug)
		_ = d.DB.ClearRejections(p.Slug)
	}
	next, _ := d.update(p.Slug, func(x *state.Session) {
		x.ReviewN = n
		switch {
		case len(comments) > 0 && !x.PRReview:
			x.State = state.Running
		case x.State == state.Reviewing || x.State == state.Done:
			x.State = state.Done
		}
	})
	d.emit("review.updated", map[string]any{"slug": p.Slug})
	return map[string]any{"review": n, "sent": len(comments), "to": sentTo, "session": next}, nil
}

func formatReview(n int, cs []state.Comment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "レビュー #%d のコメントです。すべて対応してください。\n", n)
	for _, c := range cs {
		loc := c.Path
		if c.Line > 0 {
			loc = fmt.Sprintf("%s:%d", c.Path, c.Line)
		}
		tag := ""
		if c.Kind == "reject" {
			tag = "[差し戻し] "
		}
		fmt.Fprintf(&b, "\n- %s%s\n  %s", tag, loc, strings.ReplaceAll(c.Text, "\n", "\n  "))
	}
	return b.String()
}

// ---- reviewing someone else's PR with the same UI ----

type prP struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func (d *Daemon) hReviewPR(p prP) (any, error) {
	repo, err := resolveRepo(p.Repo)
	if err != nil {
		return nil, err
	}
	c, err := ghCommand(context.Background(), repo.Dir, "pr", "view", strconv.Itoa(p.Number), "--json", "headRefName,baseRefName,title")
	if err != nil {
		return nil, err
	}
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view %d failed: %v", p.Number, err)
	}
	var pr struct {
		Head  string `json:"headRefName"`
		Base  string `json:"baseRefName"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, err
	}
	local := fmt.Sprintf("pr-%d", p.Number)
	// Into a ref of pekoe's own, forced (the author may have force-pushed):
	// fetching straight into pr-N fails once a review has it checked out,
	// and the old head was then reviewed without a word.
	tmp := fmt.Sprintf("refs/pekoe/pr/%d", p.Number)
	if _, err := gitx.Run(repo.Dir, "fetch", "--end-of-options", "origin", fmt.Sprintf("+pull/%d/head:%s", p.Number, tmp), pr.Base); err != nil {
		return nil, fmt.Errorf("fetching PR #%d: %w", p.Number, err)
	}
	head, err := gitx.RevParse(repo.Dir, tmp)
	_, _ = gitx.Run(repo.Dir, "update-ref", "-d", tmp)
	if err != nil {
		return nil, err
	}
	// this PR is under review already: bring that session to the new head
	// (a new session would replace it and lose its review rounds)
	if old := d.prReviewSession(repo.ID, p.Number); old != nil {
		if err := checkoutHead(old.Path, local, head); err != nil {
			return nil, err
		}
		d.update(old.Slug, func(x *state.Session) { x.LastMessage = pr.Title })
		d.refreshDiffstat(old.Slug)
		return d.hReviewStart(slugP{Slug: old.Slug})
	}
	slug := d.uniqueSlug(local, repo.ID, repo.Name)
	if s, err := d.get(slug); err == nil && s.State != state.Landed {
		return nil, fmt.Errorf("session %s already exists", slug)
	}
	path, err := worktreePath(repo.Name, slug)
	if err != nil {
		return nil, err
	}
	own := false
	if _, err := os.Stat(path); err == nil {
		if err := reusableWorktree(repo.Dir, path); err != nil {
			return nil, err
		}
		if err := checkoutHead(path, local, head); err != nil {
			return nil, err
		}
	} else {
		own = true
		if _, err := gitx.Run(repo.Dir, "branch", "-f", "--end-of-options", local, head); err != nil {
			return nil, err
		}
		if err := gitx.AddWorktree(repo.Dir, path, local, ""); err != nil && !strings.Contains(err.Error(), "already") {
			return nil, err
		}
	}
	port, _ := d.DB.AllocPort(repo.ID, slug, d.Global.Ports.Base, portBusy)
	s := &state.Session{
		Slug: slug, Branch: local, Repo: repo.Name, RepoID: repo.ID, RepoDir: repo.Dir, Path: path,
		Agent: "claude", State: state.Done, StateSince: time.Now(), CreatedAt: time.Now(), Port: port,
		Managed: true, OwnDir: own, Base: pr.Base, PR: p.Number, PRReview: true, Test: state.Test{Status: "none"},
		LastMessage: pr.Title, Stopped: true,
	}
	d.add(s)
	d.refreshDiffstat(slug)
	return d.hReviewStart(slugP{Slug: slug})
}

// prReviewSession is the open review of a repository's PR, if any.
func (d *Daemon) prReviewSession(repoID string, number int) *state.Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		if s.PRReview && s.PR == number && s.RepoID == repoID && s.State != state.Landed {
			return d.copyLocked(s)
		}
	}
	return nil
}

// checkoutHead puts the PR's branch in a review worktree at head. Local
// edits to tracked files are kept (and the update refused), not reset away.
func checkoutHead(path, branch, head string) error {
	if out, err := gitx.Run(path, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return err
	} else if out != "" {
		return fmt.Errorf("%s has uncommitted changes: commit or discard them to review the new head", path)
	}
	_, err := gitx.Run(path, "checkout", "-q", "-B", branch, head)
	return err
}

func (d *Daemon) postGitHubReview(s *state.Session, cs []state.Comment) error {
	type ghComment struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Side string `json:"side"`
		Body string `json:"body"`
	}
	var body struct {
		Event    string      `json:"event"`
		Body     string      `json:"body"`
		Comments []ghComment `json:"comments"`
	}
	body.Event = "COMMENT"
	var general []string
	for _, c := range cs {
		text := c.Text
		if c.Kind == "reject" {
			text = "**差し戻し**: " + text
		}
		if c.Path != "" && c.Line > 0 {
			body.Comments = append(body.Comments, ghComment{Path: c.Path, Line: c.Line, Side: "RIGHT", Body: text})
		} else {
			general = append(general, text)
		}
	}
	body.Body = strings.Join(general, "\n\n")
	b, _ := json.Marshal(body)
	sort.Slice(body.Comments, func(i, j int) bool { return body.Comments[i].Path < body.Comments[j].Path })
	c, err := ghCommand(context.Background(), s.RepoDir, "api", "--method", "POST", fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews", s.PR), "--input", "-")
	if err != nil {
		return err
	}
	c.Stdin = bytes.NewReader(b)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("gh api review: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

var _ = engine.WinAgent
