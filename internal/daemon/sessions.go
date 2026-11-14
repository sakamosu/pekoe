package daemon

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// ---- session bookkeeping ----

func (d *Daemon) get(slug string) (*state.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[slug]
	if !ok {
		return nil, fmt.Errorf("no such session: %s", slug)
	}
	return s, nil
}

// snapshot returns a copy safe to marshal outside the lock.
func (d *Daemon) snapshot(slug string) (*state.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[slug]
	if !ok {
		return nil, fmt.Errorf("no such session: %s", slug)
	}
	return d.copyLocked(s), nil
}

// copyLocked copies s with its live counters filled in (under mu).
func (d *Daemon) copyLocked(s *state.Session) *state.Session {
	c := *s
	c.Activity = d.activityLocked(s.Slug)
	if o := d.out[s.Slug]; o != nil {
		c.Unread = o.unread
	}
	return &c
}

// persist writes the session to state.db, but only while it is still the one
// in memory. The write happens outside mu (no disk I/O under the daemon-wide
// lock), so without this check a save that copied the session just before
// session removal could land after DeleteSession and bring a removed
// session back on the next restart. saveMu orders persist against forget:
// either the save finishes before the delete, or it sees the session gone.
// It also makes concurrent saves of one slug write the newest copy last.
func (d *Daemon) persist(s *state.Session) {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	if d.sessions[s.Slug] != s { // removed (or replaced by a new session of the same slug)
		d.mu.Unlock()
		return
	}
	c := d.copyLocked(s)
	d.mu.Unlock()
	if err := d.DB.SaveSession(c); err != nil {
		log.Printf("save %s: %v", s.Slug, err)
	}
}

// forget drops a session from memory and from state.db as one step with
// respect to persist (see there).
func (d *Daemon) forget(slug string) { d.forgetIf(slug, nil) }

// forgetIf is forget, but only while ok (called under mu) holds for the
// session in memory; it reports whether it forgot it. A check made before
// cannot go stale: nothing changes the session between ok and the delete.
func (d *Daemon) forgetIf(slug string, ok func(*state.Session) bool) bool {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	s := d.sessions[slug]
	if ok != nil && (s == nil || !ok(s)) {
		d.mu.Unlock()
		return false
	}
	if s != nil && !d.pathUsedLocked(s.Path, slug) {
		gitx.Unpin(s.Path)
	}
	delete(d.sessions, slug)
	delete(d.out, slug)
	delete(d.progress, slug)
	delete(d.prFetched, slug)
	d.mu.Unlock()
	if err := d.DB.DeleteSession(slug); err != nil {
		log.Printf("delete %s: %v", slug, err)
	}
	return true
}

// update mutates a session under the lock, persists it and emits events.
func (d *Daemon) update(slug string, f func(s *state.Session)) (*state.Session, error) {
	// evMu: two updates racing must emit in the order they changed the
	// session, or a client is left showing the older copy
	d.evMu.Lock()
	d.mu.Lock()
	s, ok := d.sessions[slug]
	if !ok {
		d.mu.Unlock()
		d.evMu.Unlock()
		return nil, fmt.Errorf("no such session: %s", slug)
	}
	before := *s
	prev := s.State
	f(s)
	if s.State != prev {
		s.StateSince = time.Now()
	}
	c := d.copyLocked(s)
	d.mu.Unlock()
	d.sessionChanges(&before, c) // under evMu: in change order
	d.emit("session.updated", c)
	if c.State != prev {
		switch c.State {
		case state.Waiting:
			d.emit("session.waiting", c)
		case state.Done:
			d.emit("session.done", c)
		}
	}
	d.evMu.Unlock()
	d.persist(s)
	if c.State != prev {
		d.previewOnState(c, prev)
	}
	return c, nil
}

func (d *Daemon) setState(slug string, st state.State, waitKind string) {
	d.update(slug, func(s *state.Session) {
		s.State = st
		if st == state.Waiting {
			s.WaitKind = waitKind
		} else {
			s.WaitKind = ""
			if st != state.Waiting {
				s.PendingTool = nil
			}
		}
	})
}

// pathUsedLocked: another session than slug has its worktree at path.
func (d *Daemon) pathUsedLocked(path, slug string) bool {
	for sl, s := range d.sessions {
		if sl != slug && s.Path == path {
			return true
		}
	}
	return false
}

func (d *Daemon) add(s *state.Session) {
	// before anything runs git in it: its .git file is the agent's to write
	gitx.Pin(s.Path, s.RepoDir)
	d.mu.Lock()
	d.sessions[s.Slug] = s
	if d.out[s.Slug] == nil {
		d.out[s.Slug] = &outState{}
	}
	c := *s
	d.mu.Unlock()
	d.persist(s)
	d.emit("session.created", &c)
}

type listP struct {
	RepoID string `json:"repo_id"`
}

func (d *Daemon) hList(p listP) (any, error) {
	d.mu.Lock()
	slugs := make([]string, 0, len(d.sessions))
	for k, s := range d.sessions {
		if p.RepoID == "" || s.RepoID == p.RepoID {
			slugs = append(slugs, k)
		}
	}
	d.mu.Unlock()
	out := make([]*state.Session, 0, len(slugs))
	for _, k := range slugs {
		if s, err := d.snapshot(k); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// ---- restore after daemon restart ----

// worktreeMissing starts the error restore gives a session whose worktree
// is gone (it resumes the session if the directory comes back).
const worktreeMissing = "worktree missing: "

func (d *Daemon) restore() {
	list, err := d.DB.Sessions()
	if err != nil {
		log.Printf("restore: %v", err)
		return
	}
	alive, _ := d.Eng.Sessions()
	d.mu.Lock()
	for _, s := range list {
		d.sessions[s.Slug] = s
		d.out[s.Slug] = &outState{}
		gitx.Pin(s.Path, s.RepoDir)
	}
	d.mu.Unlock()
	// before anything reads per-repo config or files by repo-id
	d.realignRepoIDs()
	for _, s := range list {
		// sessions saved before Session.review existed: their latest
		// review is in the reports
		if s.Review == nil && s.State != state.Landed {
			if rv := d.latestReview(s.Slug).ReviewBrief(); rv != nil {
				d.update(s.Slug, func(x *state.Session) { x.Review = rv })
			}
		}
	}
	for _, s := range list {
		if s.State == state.Landed {
			// landed before landedPRMerged existed: the summary still
			// shows the last fetch before the merge
			if s.PR > 0 && (s.PRStatus == nil || s.PRStatus.State != "MERGED") {
				d.update(s.Slug, func(x *state.Session) { landedPRMerged(x) })
			}
			continue
		}
		if s.State == state.Creating {
			d.update(s.Slug, func(x *state.Session) { x.State = state.Failed; x.Error = "interrupted by daemon restart" })
			continue
		}
		// Kept (as failed) rather than dropped even for managed worktrees: the
		// directory may be only temporarily unreachable (unmounted volume,
		// renamed parent), and dropping it would lose prompts and review
		// history. Since persist/forget, rm no longer leaves such rows behind.
		if _, err := os.Stat(s.Path); err != nil {
			d.update(s.Slug, func(x *state.Session) {
				x.State = state.Failed
				x.Error = worktreeMissing + s.Path + " (pekoe rm " + s.Slug + " to forget it)"
			})
			continue
		}
		// its .git may have been changed while pekoed was away
		if c, err := d.snapshot(s.Slug); err == nil {
			d.checkGitDir(c)
		}
		// A session whose creation failed (setup hook, a worktree that was
		// not ours) never got its agent: starting one now would run it in a
		// half-made or foreign directory and wipe the error. Only the
		// failure above (the worktree was away) is undone once it is back.
		if s.State == state.Failed && !strings.HasPrefix(s.Error, worktreeMissing) {
			continue
		}
		d.refreshBranch(s.Slug)
		hasAgent, hasRun := false, false
		for _, w := range alive[s.Slug] {
			if w == engine.WinAgent {
				hasAgent = true
			}
			// a dead window is a hook that ended while pekoed was away:
			// Dev stays as saved, and checkDevExits records the end
			if w == engine.WinDev && !s.Dev && d.Eng.Alive(s.Slug, engine.WinDev) {
				s.Dev = true
			}
			if w == engine.WinRun {
				hasRun = true
			}
		}
		if s.Run != "" && !hasRun { // the tmux server went away with it
			d.update(s.Slug, func(x *state.Session) { x.Run = "" })
		}
		if hasAgent || s.Stopped {
			continue
		}
		// the pty is gone (tmux server restarted): resume the agent session
		resume := s.ClaudeSessionID
		if err := d.spawnAgent(s.Slug, "", resume, 0, 0); err != nil {
			log.Printf("resume %s: %v", s.Slug, err)
			continue
		}
		d.update(s.Slug, func(x *state.Session) {
			if x.State == state.Running || x.State == state.Waiting {
				x.State = state.Idle
			}
		})
	}
}
