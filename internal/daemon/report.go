package daemon

// Reports (docs/orchestration.md "1. 報告"): a session leaves word of its
// work — done, blocked, progress, or an independent review's verdict — for
// the hub agent (tapi, a main session) and the person. The session is the
// caller pekoed identified (caller.go), never what the agent says it is.
// The state does not change (done is the Stop hook's); the latest report
// rides on the session (Session.report) and the full one goes out as
// report.posted. pekoed reads no evidence: paths and URLs stay text (it
// would otherwise read any file an agent names), and the person opens them.

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"pekoe/internal/engine"
	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

const (
	reportSummaryMax  = 4000 // characters
	reportEvidenceMax = 20
	reportEvidenceLen = 500 // characters each
)

var reportStatuses = map[string]bool{"done": true, "blocked": true, "progress": true, "review": true}

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

type reportP struct {
	Slug        string   `json:"slug"` // with the human token only: the session reported on
	Summary     string   `json:"summary"`
	Status      string   `json:"status"`
	Evidence    []string `json:"evidence"`
	Verdict     string   `json:"verdict"`
	ReviewedSHA string   `json:"reviewed_sha"`
}

func (d *Daemon) hReportPost(who *Caller, p reportP) (any, error) {
	pane := who.Pane()
	slug := pane
	switch {
	case who.Human() && p.Slug != "":
		slug = p.Slug
	case p.Slug != "" && p.Slug != pane:
		if pane == "" {
			return nil, errUnknownCaller("report.post")
		}
		return nil, fmt.Errorf("report.post: a session reports only on itself (%s, not %s)", pane, p.Slug)
	}
	if slug == "" {
		if who.Human() {
			return nil, fmt.Errorf("report.post: slug required (no pekoe window around the caller)")
		}
		return nil, errUnknownCaller("report.post")
	}
	if slug == TapiSlug {
		return nil, fmt.Errorf("report.post: tapi chat is not a session; reports are a session's word about its work")
	}
	by := pane
	if by == "" {
		by = who.ID()
	}
	r, err := cleanReport(p)
	if err != nil {
		return nil, err
	}
	s, err := d.snapshot(slug)
	if err != nil {
		return nil, err
	}
	r.Slug, r.By, r.Claimed, r.At = slug, by, who.ClaimedDiff(by), time.Now()
	if d.checkGitDir(s) {
		r.Head, _ = gitx.RevParse(s.Path, "HEAD")
		if out, err := gitx.Run(s.Path, "status", "--porcelain", "--untracked-files=normal"); err == nil {
			r.Dirty = strings.TrimSpace(out) != ""
		}
	}
	if err := d.DB.AddReport(r); err != nil {
		return nil, err
	}
	brief, rv := r.Brief(), r.ReviewBrief()
	c, err := d.update(slug, func(x *state.Session) {
		x.Report = brief
		if rv != nil {
			x.Review = rv
		}
	})
	if err != nil {
		return nil, err
	}
	d.changes.add(Change{Kind: kindReport, Slug: slug, Summary: fmt.Sprintf("%s: report %s: %s", slug, r.Status, lineCut(brief.Summary, 120)), parent: c.Parent})
	d.emit("report.posted", r)
	return r, nil
}

// latestReview is the session's newest status-review report (pending, pass or fail), or nil.
func (d *Daemon) latestReview(slug string) *state.Report {
	rs, err := d.DB.Reports(slug, 0, 200)
	if err != nil {
		return nil
	}
	for i := range rs {
		if rs[i].Status == "review" {
			return &rs[i]
		}
	}
	return nil
}

// cleanReport checks p's fields and sizes and drops control characters
// (line breaks and tabs stay in the summary; evidence is one line each).
func cleanReport(p reportP) (*state.Report, error) {
	if !reportStatuses[p.Status] {
		return nil, fmt.Errorf("report.post: status must be done, blocked, progress or review (got %q)", p.Status)
	}
	sum := strings.TrimSpace(engine.CleanText(p.Summary))
	if sum == "" {
		return nil, fmt.Errorf("report.post: summary required")
	}
	if n := utf8.RuneCountInString(sum); n > reportSummaryMax {
		return nil, fmt.Errorf("report.post: summary is %d characters, at most %d (put the details in a file and pass its path as evidence)", n, reportSummaryMax)
	}
	ev, err := cleanEvidence("report.post", p.Evidence)
	if err != nil {
		return nil, err
	}
	r := &state.Report{Status: p.Status, Summary: sum, Evidence: ev}
	if p.Status == "review" {
		// pending marks a review asked for and under way (the dashboard's
		// review stage); merge.check counts only pass
		if p.Verdict != "pass" && p.Verdict != "fail" && p.Verdict != "pending" {
			return nil, fmt.Errorf("report.post: status review needs verdict pending, pass or fail")
		}
		sha := strings.ToLower(strings.TrimSpace(p.ReviewedSHA))
		if sha != "" && !shaRe.MatchString(sha) {
			return nil, fmt.Errorf("report.post: reviewed_sha must be a commit id (hex)")
		}
		r.Verdict, r.ReviewedSHA = p.Verdict, sha
	} else if p.Verdict != "" || p.ReviewedSHA != "" {
		return nil, fmt.Errorf("report.post: verdict and reviewed_sha go with status review")
	}
	return r, nil
}

// cleanEvidence checks evidence (paths and URLs, kept as text) and drops
// every control character: one line each. Empty entries go.
func cleanEvidence(method string, in []string) ([]string, error) {
	if len(in) > reportEvidenceMax {
		return nil, fmt.Errorf("%s: at most %d evidence entries", method, reportEvidenceMax)
	}
	ev := []string{}
	for _, e := range in {
		e = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
				return -1
			}
			return r
		}, e))
		if e == "" {
			continue
		}
		if utf8.RuneCountInString(e) > reportEvidenceLen {
			return nil, fmt.Errorf("%s: an evidence entry is at most %d characters", method, reportEvidenceLen)
		}
		ev = append(ev, e)
	}
	return ev, nil
}

type reportListP struct {
	Slug  string `json:"slug"`
	Since int64  `json:"since"`
	Limit int    `json:"limit"`
}

// hReportList lists reports newest first (as public as session.list).
func (d *Daemon) hReportList(_ *Caller, p reportListP) (any, error) {
	return d.DB.Reports(p.Slug, p.Since, clampLimit(p.Limit, 20, 200))
}
