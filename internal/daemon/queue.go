package daemon

// The queue of new sessions (docs/orchestration.md「G. 負荷の制御」): a
// session.new that may not start yet is kept as state queued — its row,
// slug and port, but no worktree, no setup and no agent — and starts
// (creating → running, as any new session) when it may:
//
//   - [orchestrator] max_running > 0 caps the sessions at work: managed
//     ones (made by pekoe) in creating, running or waiting. A new session
//     beyond it waits, and the queued ones start oldest first as sessions
//     stop working (idle, done, failed, landed, removed).
//   - pekoe new --after X --hold: the session waits until every X has
//     landed (its PR merged), then starts from the base as it is then
//     (origin/<base> fetched, when there is an origin), so it builds on
//     what it waited for. A held session that is ready still waits for a
//     slot. If an X is removed without landing, the held session fails
//     (it would wait forever); an X that failed keeps it waiting (a failed
//     session can be resumed), and the reason says so.
//
// The whole creation is queued, not just the agent: setup (installs,
// migrations) is the heavy part, and a worktree made late starts from the
// newest base. Nothing changes without a cap and without --hold:
// session.new starts at once, as before.
//
// What runNew needs that the row does not keep (the prompt, --from, the
// pane size) is kept in state.db's kv under queued:<slug>, so a queued
// session survives a restart of pekoed and starts after it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// queuedNew is what a queued session.new needs again when it starts.
type queuedNew struct {
	Prompt string `json:"prompt,omitempty"`
	From   string `json:"from,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// Base: the base given to session.new (--base), "" when none was
	// (the repository's base: fetched, see freshStart)
	Base string `json:"base,omitempty"`
}

func queuedKey(slug string) string { return "queued:" + slug }

// working: s counts against max_running. A creation always counts: it
// goes on to start the agent whatever stopped says.
func working(s *state.Session) bool {
	if !s.Managed {
		return false
	}
	switch s.State {
	case state.Creating:
		return true
	case state.Running, state.Waiting:
		return !s.Stopped
	}
	return false
}

// queueNew keeps p for when the queued session slug starts.
func (d *Daemon) queueNew(slug string, p newP) {
	b, _ := json.Marshal(queuedNew{Prompt: p.Prompt, From: p.From, Width: p.Width, Height: p.Height, Base: p.Base})
	if err := d.DB.Set(queuedKey(slug), string(b)); err != nil {
		log.Printf("queue %s: %v", slug, err)
	}
}

// dropQueued forgets what queueNew kept.
func (d *Daemon) dropQueued(slug string) { _ = d.DB.Delete(queuedKey(slug)) }

// kickQueue asks the queue to look again soon (a session stopped working,
// one landed, the config changed). It never blocks.
func (d *Daemon) kickQueue() {
	select {
	case d.queueKick <- struct{}{}:
	default:
	}
}

// queueLoop runs pumpQueue whenever kicked, until pekoed stops.
func (d *Daemon) queueLoop() {
	for {
		select {
		case <-d.stop:
			return
		case <-d.queueKick:
		}
		d.pumpQueue()
	}
}

// pumpQueue starts the queued sessions that may start now, oldest first,
// and brings the others' reasons up to date. One at a time (queueMu).
func (d *Daemon) pumpQueue() {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	limit := d.cfg().Orchestrator.Cap()
	d.mu.Lock()
	busy := 0
	var queued []*state.Session
	states := map[string]state.State{}
	for k, s := range d.sessions {
		states[k] = s.State
		if working(s) {
			busy++
		}
		if s.State == state.Queued {
			queued = append(queued, d.copyLocked(s))
		}
	}
	d.mu.Unlock()
	sort.Slice(queued, func(i, j int) bool {
		if !queued[i].CreatedAt.Equal(queued[j].CreatedAt) {
			return queued[i].CreatedAt.Before(queued[j].CreatedAt)
		}
		return queued[i].Slug < queued[j].Slug
	})
	for _, q := range queued {
		var holding, failed []string
		gone := ""
		for _, dep := range q.Holding {
			st, ok := states[dep]
			if !ok {
				gone = dep
				break
			}
			if st == state.Landed {
				continue
			}
			holding = append(holding, dep)
			if st == state.Failed {
				failed = append(failed, dep)
			}
		}
		if gone != "" {
			// removed while afterGone was not told (it always is while
			// pekoed runs): it can no longer land
			d.failQueued(q.Slug, fmt.Sprintf("--hold: %s was removed before it landed", gone))
			continue
		}
		reason := ""
		switch {
		case len(holding) > 0:
			reason = "--hold: waiting for " + strings.Join(holding, ", ") + " to land"
			if len(failed) > 0 {
				reason += " (" + strings.Join(failed, ", ") + " failed)"
			}
		case limit > 0 && busy >= limit:
			reason = fmt.Sprintf("max_running = %d: waiting for a session to stop working", limit)
		}
		if reason != "" {
			if reason != q.Queue || !slices.Equal(holding, q.Holding) {
				d.update(q.Slug, func(x *state.Session) {
					if x.State == state.Queued {
						x.Queue, x.Holding = reason, holding
					}
				})
			}
			continue
		}
		if d.startQueued(q.Slug) {
			busy++
		}
	}
}

// startQueued takes slug out of the queue and runs its creation.
func (d *Daemon) startQueued(slug string) bool {
	var raw string
	if raw = d.DB.Get(queuedKey(slug)); raw == "" {
		d.failQueued(slug, "queued: what to start it with was lost (pekoe rm it and make it again)")
		return false
	}
	var qn queuedNew
	if err := json.Unmarshal([]byte(raw), &qn); err != nil {
		d.failQueued(slug, "queued: "+err.Error())
		return false
	}
	s, err := d.snapshot(slug)
	if err != nil || s.State != state.Queued {
		return false
	}
	repo, err := resolveRepo(s.RepoDir)
	if err != nil {
		d.failQueued(slug, err.Error())
		return false
	}
	started := false
	if _, err := d.update(slug, func(x *state.Session) {
		if x.State == state.Queued {
			x.State, x.Queue, x.Holding = state.Creating, "", nil
			started = true
		}
	}); err != nil || !started {
		return false
	}
	d.dropQueued(slug)
	c := d.prepareNew(s, repo)
	go d.runNew(c, slug, repo, newP{Prompt: qn.Prompt, From: qn.From, Width: qn.Width, Height: qn.Height, Base: qn.Base})
	return true
}

// failQueued fails a queued session that cannot start.
func (d *Daemon) failQueued(slug, why string) {
	d.dropQueued(slug)
	d.update(slug, func(x *state.Session) {
		if x.State == state.Queued {
			x.State, x.Error, x.Queue = state.Failed, why, ""
		}
	})
}

// afterGone is told that s was removed: a session held on it is now free
// of it if it had landed, and can never start if it had not.
func (d *Daemon) afterGone(s *state.Session) {
	d.mu.Lock()
	var held []string
	for k, x := range d.sessions {
		if x.State == state.Queued && slices.Contains(x.Holding, s.Slug) {
			held = append(held, k)
		}
	}
	d.mu.Unlock()
	for _, k := range held {
		if s.State == state.Landed {
			d.update(k, func(x *state.Session) {
				x.Holding = slices.DeleteFunc(slices.Clone(x.Holding), func(v string) bool { return v == s.Slug })
			})
			continue
		}
		d.failQueued(k, fmt.Sprintf("--hold: %s was removed before it landed (pekoe rm %s, and make it again without --after %s if it should still be done)", s.Slug, k, s.Slug))
	}
	d.kickQueue()
}

// fetchBaseTimeout bounds fetchBase's fetch, both tries (a stalled network).
var fetchBaseTimeout = 60 * time.Second

// fetchBaseRetryDelay is how long fetchBase waits before it fetches again.
var fetchBaseRetryDelay = 200 * time.Millisecond

// fetchBase fetches base's branch from origin into repoDir and answers
// origin/<branch>: base "main" and base "origin/main" both start from
// origin/main as the remote has it now. A fetch that fails is tried once
// more (see below). false when there is no origin, the fetch fails twice
// or takes longer than fetchBaseTimeout, ctx is done (the
// creation was cancelled: the caller looks at ctx), or base is not one of
// origin's branches (a tag, a commit: they do not move, so the local base
// is used, as for any new session).
func fetchBase(ctx context.Context, repoDir, base string) (string, bool) {
	ref, err := fetchBaseRef(ctx, repoDir, base)
	return ref, err == nil && ref != ""
}

// errNoOrigin: fetchBaseRef had nothing to fetch from.
var errNoOrigin = errors.New("no origin")

// fetchBaseRef is fetchBase telling why there is no ref: errNoOrigin
// (no origin), the fetch's error (failed twice, timed out, cancelled), or
// "" with a nil error (fetched, but base is not one of origin's branches).
func fetchBaseRef(ctx context.Context, repoDir, base string) (string, error) {
	if gitx.Origin(repoDir) == "" {
		return "", errNoOrigin
	}
	branch := strings.TrimPrefix(base, "origin/")
	fctx, cancel := context.WithTimeout(ctx, fetchBaseTimeout)
	defer cancel()
	err := fetchOrigin(fctx, repoDir, branch)
	if err != nil && fctx.Err() == nil {
		// once more: the fetch's connectivity check reads every
		// worktree's HEAD, and fails on one that another git in the same
		// repository is adding just then (another session's creation, an
		// agent): "failed to read .git/worktrees/<name>/commondir", then
		// "did not send all necessary objects". That lasts a moment.
		// Not when it timed out or was cancelled.
		log.Printf("new: fetch origin %s: %v (trying once more)", branch, err)
		select {
		case <-fctx.Done():
		case <-time.After(fetchBaseRetryDelay):
			err = fetchOrigin(fctx, repoDir, branch)
		}
	}
	if err != nil {
		log.Printf("new: fetch origin %s: %v (starting from %s)", branch, err, base)
		return "", err
	}
	ref := "origin/" + branch
	if _, err := gitx.Run(repoDir, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/remotes/"+ref+"^{commit}"); err != nil {
		return "", nil // a tag or a commit: no remote-tracking branch of that name
	}
	return ref, nil
}

// fetchOrigin fetches branch from origin into repoDir (a variable so a test
// can run something between the attempts).
var fetchOrigin = func(ctx context.Context, repoDir, branch string) error {
	_, err := gitx.RunRaw(ctx, repoDir, nil, nil, "fetch", "--end-of-options", "origin", branch)
	return err
}

// removed is the session.removed event plus what the queue makes of it.
func (d *Daemon) removed(s *state.Session) {
	d.dropQueued(s.Slug)
	d.emit("session.removed", s)
	d.afterGone(s)
}

// restoreQueued keeps a queued session queued across a restart of pekoed
// when what it starts with is still kept; otherwise it fails, as an
// interrupted creating session does.
func (d *Daemon) restoreQueued(s *state.Session) {
	if d.DB.Get(queuedKey(s.Slug)) == "" {
		d.update(s.Slug, func(x *state.Session) {
			x.State = state.Failed
			x.Error = "interrupted by daemon restart (queued)"
			x.Queue = ""
		})
	}
}
