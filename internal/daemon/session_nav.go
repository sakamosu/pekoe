package daemon

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

type seenP struct {
	Slug  string `json:"slug"`
	Lines int    `json:"lines"` // absolute line offset at which the user left
}

func (d *Daemon) hSeen(p seenP) (any, error) {
	d.mu.Lock()
	if o := d.out[p.Slug]; o != nil {
		o.unread = 0
	}
	d.mu.Unlock()
	return d.update(p.Slug, func(s *state.Session) {
		if p.Lines > 0 {
			s.SeenLines = p.Lines
		}
	})
}

type undoP struct {
	Slug string `json:"slug"`
	N    int    `json:"n"`
}

// hUndo restores the working tree to the snapshot taken when prompt n was
// submitted (default: the last prompt), i.e. throws that prompt's changes away.
func (d *Daemon) hUndo(p undoP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	prompts, _ := d.DB.Prompts(p.Slug)
	if len(prompts) == 0 {
		return nil, errors.New("no snapshots")
	}
	target := prompts[len(prompts)-1]
	if p.N > 0 {
		found := false
		for _, pr := range prompts {
			if pr.N == p.N {
				target, found = pr, true
			}
		}
		if !found {
			return nil, fmt.Errorf("no snapshot #%d", p.N)
		}
	}
	// keep a safety ref of the current state
	if sha, err := gitx.SnapshotCommit(s.Path, "pekoe before undo"); err == nil {
		_ = gitx.UpdateRef(s.Path, fmt.Sprintf("refs/pekoe/%s/undo-%d", p.Slug, time.Now().Unix()), sha)
	}
	if err := gitx.RestoreSnapshot(s.Path, target.Ref); err != nil {
		return nil, err
	}
	d.refreshDiffstat(p.Slug)
	return map[string]any{"restored": target.N, "prompt": target.Text}, nil
}

// ---- attention queue ----

type fromP struct {
	From string `json:"from"`
}

// hAttentionNext: sessions waiting for the user (oldest wait first), then
// sessions whose PR changed in a way that wants a look (PRStatus.Attention:
// CI failed, changes requested, new comment, mergeable; oldest first).
func (d *Daemon) hAttentionNext(p fromP) (any, error) {
	list, _ := d.hList(listP{})
	var waiting, pr []*state.Session
	for _, s := range list.([]*state.Session) {
		switch {
		case s.State == state.Waiting:
			waiting = append(waiting, s)
		case s.PRStatus != nil && len(s.PRStatus.Attention) > 0:
			pr = append(pr, s)
		}
	}
	sort.Slice(waiting, func(i, j int) bool { return waiting[i].StateSince.Before(waiting[j].StateSince) })
	sort.Slice(pr, func(i, j int) bool { return pr[i].PRStatus.AttentionAt.Before(pr[j].PRStatus.AttentionAt) })
	queue := append(waiting, pr...)
	if len(queue) == 0 {
		return nil, nil
	}
	// cycle: the first that is not the one we are on
	for _, s := range queue {
		if s.Slug != p.From {
			return s, nil
		}
	}
	return queue[0], nil
}

func (d *Daemon) hReviewNext(p fromP) (any, error) {
	list, _ := d.hList(listP{})
	var done []*state.Session
	for _, s := range list.([]*state.Session) {
		if s.State == state.Done {
			done = append(done, s)
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].StateSince.Before(done[j].StateSince) })
	for _, s := range done {
		if s.Slug != p.From {
			return s, nil
		}
	}
	if len(done) > 0 {
		return done[0], nil
	}
	return nil, nil
}
