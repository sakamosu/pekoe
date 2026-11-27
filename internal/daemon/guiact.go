package daemon

// The local GUI's operations beyond the phone's /api/* (docs/gui.md 段階 2,
// docs/web.md の API の表): a new session in a repository pekoe knows, tapi
// chat's reset, and interrupting an agent. The GUI's mux only (the phone's
// Web UI has none of them): each calls the method the TUI calls, as the
// person (the GUI's Caller). The page puts every one through a confirm sheet
// first; pekoed's checks are the method's own.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// guiMaxPrompt: runes in a new session's prompt (the body cap, webMaxBody,
// holds this many three-byte runes)
const guiMaxPrompt = 16000

// guiRepo is a repository the New session sheet offers: repo.list's, only
// those pekoe knows the directory of (a session is made there).
type guiRepo struct {
	ID       string `json:"repo_id"`
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Sessions int    `json:"sessions"`
	// Watch: issue watch mode is on ([watch] enable; watch.go)
	Watch bool `json:"watch,omitempty"`
}

// guiRepos is GET /api/repos: repo.list, as the TUI's w lists them (the
// ones with a directory, each directory once: guiRepoList).
func (d *Daemon) guiRepos(w http.ResponseWriter, r *http.Request) {
	ks, err := d.guiRepoList(humanCaller(webVia(peerFrom(r))))
	if err != nil {
		webErr(w, err)
		return
	}
	out := []guiRepo{}
	for _, k := range ks {
		out = append(out, guiRepo{ID: k.ID, Name: k.Name, Dir: k.Dir, Sessions: k.Sessions, Watch: k.Watch})
	}
	writeJSON(w, map[string]any{"repos": out})
}

type guiNewIn struct {
	RepoID string `json:"repo_id"`
	Branch string `json:"branch"`
	Prompt string `json:"prompt"`
	// Issue: start from GitHub issue N of the repository (guiissue.go);
	// IssueSig is the digest of its text the confirm sheet showed
	Issue    int    `json:"issue"`
	IssueSig string `json:"issue_sig"`
}

// guiNew is POST /api/sessions: session.new as the TUI's n makes it (a
// branch and a prompt in a repository; the agent is the repository's, as
// in the TUI). The repository is named by its id and must be one repo.list
// knows: the page never hands pekoed a path. A prompt that starts with '!'
// is the person's (the sheet says what it does): only a caller without the
// human token is refused that (hAgentNew).
func (d *Daemon) guiNew(w http.ResponseWriter, r *http.Request) {
	var in guiNewIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	in.Branch = strings.TrimSpace(in.Branch)
	if in.Branch == "" {
		http.Error(w, "branch required", http.StatusBadRequest)
		return
	}
	if !utf8.ValidString(in.Prompt) || utf8.RuneCountInString(in.Prompt) > guiMaxPrompt {
		http.Error(w, fmt.Sprintf("prompt must be at most %d characters", guiMaxPrompt), http.StatusBadRequest)
		return
	}
	if engine.HasControl(in.Prompt) || engine.HasControl(in.Branch) {
		http.Error(w, "text must not contain control characters", http.StatusBadRequest)
		return
	}
	dir := d.guiRepoDir(in.RepoID)
	if dir == "" {
		http.Error(w, "repository not known to pekoe: pick one from the list", http.StatusBadRequest)
		return
	}
	if in.Issue < 0 || in.Issue > 0 && !fromIssueRe.MatchString(fmt.Sprintf("gh#%d", in.Issue)) {
		http.Error(w, "issue: an issue number", http.StatusBadRequest)
		return
	}
	prompt := in.Prompt
	if in.Issue > 0 {
		pr, code, err := d.guiNewIssue(r, dir, in.Issue, in.IssueSig, in.Prompt)
		if err != nil {
			http.Error(w, err.Error(), code)
			return
		}
		prompt = pr
	}
	p := peerFrom(r)
	log.Printf("gui: %s creates %s in %s (issue %d, %d chars of prompt)", p.Login, in.Branch, in.RepoID, in.Issue, utf8.RuneCountInString(prompt))
	res, err := d.CallAs(humanCaller(webVia(p)), "session.new", mustJSON(newP{Repo: dir, Branch: in.Branch, Prompt: prompt}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	var s *state.Session
	switch v := res.(type) {
	case *newResult:
		s = v.Session
	case *state.Session:
		s = v
	}
	if s == nil {
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if in.Issue > 0 {
		if u := d.noteIssue(s.Slug, in.Issue); u != nil {
			s = u
		}
	}
	writeJSON(w, d.toWebSession(s))
}

// guiInterrupt is POST /api/sessions/{slug}/interrupt: session.interrupt
// (one Escape while the agent works; interrupt.go says when it refuses).
func (d *Daemon) guiInterrupt(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p := peerFrom(r)
	log.Printf("gui: %s interrupts %s", p.Login, slug)
	res, err := d.CallAs(humanCaller(webVia(p)), "session.interrupt", mustJSON(slugP{Slug: slug}))
	if err != nil {
		webErr(w, err)
		return
	}
	if s, ok := res.(*state.Session); ok && s != nil {
		writeJSON(w, d.toWebSession(s))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// guiTapiInterrupt is POST /api/tapi/interrupt: tapi.interrupt.
func (d *Daemon) guiTapiInterrupt(sf webSurface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.webTapiReady(w) {
			return
		}
		p := peerFrom(r)
		log.Printf("gui: %s interrupts tapi", p.Login)
		if _, err := d.CallAs(humanCaller(webVia(p)), "tapi.interrupt", nil); err != nil {
			webErr(w, err)
			return
		}
		writeJSON(w, d.webTapiView(sf.tapiSend, sf.confirmTapi()))
	}
}

// guiTapiReset is POST /api/tapi/reset: tapi.reset, the TUI's T. tapi's
// window closes and its Claude Code conversation is deleted; the requests
// (ledger), the inbox, the reports and the send log stay (they are in
// state.db). tapi then is not started: the TUI's t starts it afresh.
func (d *Daemon) guiTapiReset(sf webSurface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := peerFrom(r)
		log.Printf("gui: %s resets tapi chat", p.Login)
		if _, err := d.CallAs(humanCaller(webVia(p)), "tapi.reset", nil); err != nil {
			webErr(w, err)
			return
		}
		writeJSON(w, d.webTapiView(sf.tapiSend, sf.confirmTapi()))
	}
}
