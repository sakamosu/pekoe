package daemon

// The local GUI's New session from a GitHub issue (the TUI's [2] Issues n,
// pekoe new --from gh#N) and tapi's Start (the TUI's t). The GUI's mux
// only: issues.list / issues.view read GitHub with the person's gh
// credentials, and tapi.open / tapi.restart start an agent, so all are
// human-only methods (human.go), called as the person (the GUI's Caller).
//
// An issue's title and body are written by anyone who can open an issue:
// the page draws them with textContent and shows the whole prompt they
// become (issueText's fence and note) in the confirm sheet before a
// session is made. pekoed puts in exactly what the sheet showed: the page
// sends back the text's digest, pekoed reads the issue again and refuses
// when it changed since.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// guiRepoDir is the directory of the repository repo.list knows by id, ""
// when it knows none (the page never hands pekoed a path).
func (d *Daemon) guiRepoDir(id string) string {
	if id == "" {
		return ""
	}
	for _, k := range d.knownRepos() {
		if k.ID == id && k.Dir != "" {
			return k.Dir
		}
	}
	return ""
}

// guiIssue is one row of the New session sheet's issue list.
type guiIssue struct {
	Number    int           `json:"number"`
	Title     string        `json:"title"`
	Labels    []string      `json:"labels"`
	URL       string        `json:"url"`
	UpdatedAt time.Time     `json:"updated_at"`
	Branch    string        `json:"branch"` // SuggestBranch, as the TUI's n fills it in
	Session   *IssueSession `json:"session,omitempty"`
}

// guiIssues is GET /api/repos/{repo}/issues?filter=mine|open[&refresh=1]:
// issues.list (the TUI's [2] Issues; mine by default, f cycles).
func (d *Daemon) guiIssues(w http.ResponseWriter, r *http.Request) {
	dir := d.guiRepoDir(r.PathValue("repo"))
	if dir == "" {
		http.Error(w, "repository not known to pekoe: pick one from the list", http.StatusNotFound)
		return
	}
	filter := r.URL.Query().Get("filter")
	if filter != "open" {
		filter = "mine"
	}
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "issues.list",
		mustJSON(issuesP{Repo: dir, Filter: filter, Refresh: r.URL.Query().Get("refresh") == "1"}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	l, _ := res.(*IssueList)
	if l == nil {
		http.Error(w, "issues.list: no answer", http.StatusBadGateway)
		return
	}
	out := []guiIssue{}
	for _, is := range l.Issues {
		out = append(out, guiIssue{Number: is.Number, Title: is.Title, Labels: is.Labels, URL: is.URL, UpdatedAt: is.UpdatedAt,
			Branch: SuggestBranch(is.Number, is.Title, is.Labels), Session: is.Session})
	}
	writeJSON(w, map[string]any{"filter": l.Filter, "issues": out, "unavailable": l.Unavailable, "fetched_at": l.FetchedAt, "cached": l.Cached})
}

// guiIssueRead is issue n of the repository at dir, read from GitHub now
// (issues.view without its cache): the prompt it becomes and its digest.
func (d *Daemon) guiIssueRead(r *http.Request, dir string, n int) (*IssueDetail, string, error) {
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "issues.view", mustJSON(issuesP{Repo: dir, Number: n, Refresh: true}))
	if err != nil {
		return nil, "", err
	}
	det, _ := res.(*IssueDetail)
	if det == nil {
		return nil, "", fmt.Errorf("issues.view: no answer for #%d", n)
	}
	return det, guiIssueText(det), nil
}

// guiIssueText is the prompt of issue det, as pekoe new --from gh#N makes
// it (issueText), with control characters other than newline and tab and
// format characters (Unicode Cf: bidi overrides, zero-width characters,
// tag characters) dropped from the title and the body: they would reach
// the agent unseen or reordered (the sheet cannot show them as they are),
// so the person would not have read what the agent gets. As cleanInbox.
func guiIssueText(det *IssueDetail) string {
	return issueText(det.Number, dropControl(det.Title), dropControl(det.Body))
}

func dropControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func textSig(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// guiIssueView is GET /api/repos/{repo}/issues/{n}: the issue, and the
// whole text it puts at the head of the prompt with its digest (sig): the
// confirm sheet shows the text, POST /api/sessions takes the sig back.
func (d *Daemon) guiIssueView(w http.ResponseWriter, r *http.Request) {
	dir := d.guiRepoDir(r.PathValue("repo"))
	if dir == "" {
		http.Error(w, "repository not known to pekoe: pick one from the list", http.StatusNotFound)
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n <= 0 || !fromIssueRe.MatchString("gh#"+r.PathValue("n")) {
		http.Error(w, "an issue number", http.StatusBadRequest)
		return
	}
	det, text, err := d.guiIssueRead(r, dir, n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"number": det.Number, "title": det.Title, "url": det.URL, "state": det.State, "author": det.Author,
		"labels": det.Labels, "session": det.Session,
		"text": text, "sig": textSig(text),
	})
}

// guiNewIssue reads issue n again for POST /api/sessions and answers the
// prompt to start with (the issue's text, then the person's prompt), or
// why not: the issue changed since the sheet showed it (sig), or gh fails.
func (d *Daemon) guiNewIssue(r *http.Request, dir string, n int, sig, prompt string) (string, int, error) {
	if sig == "" {
		return "", http.StatusBadRequest, fmt.Errorf("issue #%d: issue_sig required (the confirm sheet's)", n)
	}
	_, text, err := d.guiIssueRead(r, dir, n)
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	if textSig(text) != sig {
		return "", http.StatusConflict, fmt.Errorf("issue #%d changed since the sheet showed it: Continue again to read it as it is now", n)
	}
	if prompt != "" {
		return text + "\n\n" + prompt, 0, nil
	}
	return text, 0, nil
}

// noteIssue records that the session slug works on issue n (what
// session.new's from does), so [2] Issues and /api/repos/…/issues show it.
func (d *Daemon) noteIssue(slug string, n int) *state.Session {
	u, err := d.update(slug, func(s *state.Session) { s.Issue = n })
	if err != nil {
		log.Printf("gui: issue #%d of %s: %v", n, slug, err)
	}
	return u
}

// guiTapiStart is POST /api/tapi/start: tapi.open, the TUI's t (no
// confirmation there either: it starts tapi, with the conversation Claude
// Code kept, or a new one after a reset), or tapi.restart when tapi's
// window is there and its agent has exited (the TUI's ↵ on that screen).
// Refused while tapi runs. Two tabs pressing Start at once go one after
// the other (guiTapiStartMu), so the second sees tapi running and does not
// spawn it again.
func (d *Daemon) guiTapiStart(sf webSurface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guiTapiStartMu.Lock()
		defer guiTapiStartMu.Unlock()
		method := "tapi.open"
		if d.tapiStarted() {
			if d.Eng.Alive(TapiSlug, engine.WinAgent) {
				http.Error(w, "tapi is already running", http.StatusConflict)
				return
			}
			method = "tapi.restart"
		}
		p := peerFrom(r)
		log.Printf("gui: %s starts tapi (%s)", p.Login, method)
		if _, err := d.CallAs(humanCaller(webVia(p)), method, mustJSON(tapiSizeP{})); err != nil {
			webErr(w, err)
			return
		}
		writeJSON(w, d.webTapiView(sf.tapiSend, sf.confirmTapi()))
	}
}

var guiTapiStartMu sync.Mutex
