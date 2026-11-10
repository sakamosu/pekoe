package daemon

// wait (docs/orchestration.md "2. 待ち合わせ"): a hub agent (tapi, a main
// session) blocks until something happens to its sessions, instead of
// polling (with Claude Code's run_in_background it spends no tokens while
// it waits). pekoed numbers every change in a ring in memory; wait returns
// the ones after the caller's cursor, or blocks until one comes. Nothing is
// written to disk (U12): after a restart the epoch differs and wait says
// reset, and the caller reads `pekoe ls` again.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"pekoe/internal/state"
	"pekoe/internal/untrusted"
)

// Change kinds.
const (
	kindReport  = "report"
	kindDone    = "done"
	kindWaiting = "waiting"
	kindIdle    = "idle"
	kindFailed  = "failed"
	kindPR      = "pr"
	kindLanded  = "landed"
	kindRemoved = "removed"
	kindInbox   = "inbox" // the inbox (next stage): accepted, not produced yet
)

var waitKinds = map[string]bool{kindReport: true, kindDone: true, kindWaiting: true, kindIdle: true, kindFailed: true, kindPR: true, kindLanded: true, kindRemoved: true, kindInbox: true}

const (
	changeRing     = 2000 // changes kept
	waitsPerCaller = 8    // waits one caller may have open
	waitMaxS       = 3600
	waitDefaultS   = 1800
)

// Change is one thing that happened to a session.
type Change struct {
	Seq     int64     `json:"seq"`
	Kind    string    `json:"kind"`
	Slug    string    `json:"slug"`
	At      time.Time `json:"at"`
	Summary string    `json:"summary"` // one line
	parent  string    // the session's parent then (mine)
}

type changeLog struct {
	mu       sync.Mutex
	epoch    string
	seq      int64
	ring     []Change
	wake     chan struct{} // closed and replaced on every change
	waiting  map[string]int
	promptAt map[string]time.Time // slug -> last UserPromptSubmit (done without a report)
}

func newChangeLog() *changeLog {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return &changeLog{epoch: hex.EncodeToString(b), wake: make(chan struct{}), waiting: map[string]int{}, promptAt: map[string]time.Time{}}
}

func (l *changeLog) cursorLocked() string { return fmt.Sprintf("%s-%d", l.epoch, l.seq) }

func (l *changeLog) add(c Change) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	c.Seq = l.seq
	if c.At.IsZero() {
		c.At = time.Now()
	}
	l.ring = append(l.ring, c)
	if len(l.ring) > 2*changeRing {
		l.ring = append([]Change(nil), l.ring[len(l.ring)-changeRing:]...)
	}
	close(l.wake)
	l.wake = make(chan struct{})
}

// oldestLocked is the first sequence number still in the ring.
func (l *changeLog) oldestLocked() int64 {
	if n := len(l.ring); n > changeRing {
		return l.ring[n-changeRing].Seq
	}
	if len(l.ring) == 0 {
		return l.seq + 1
	}
	return l.ring[0].Seq
}

func (l *changeLog) notePrompt(slug string) {
	l.mu.Lock()
	l.promptAt[slug] = time.Now()
	l.mu.Unlock()
}

func (l *changeLog) lastPrompt(slug string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.promptAt[slug]
}

func (l *changeLog) forget(slug string) {
	l.mu.Lock()
	delete(l.promptAt, slug)
	l.mu.Unlock()
}

// parseCursor reads "<epoch>-<seq>".
func parseCursor(s string) (epoch string, seq int64, err error) {
	e, n, ok := strings.Cut(strings.TrimSpace(s), "-")
	if ok {
		seq, err = strconv.ParseInt(n, 10, 64)
	}
	if !ok || err != nil || e == "" || seq < 0 {
		return "", 0, fmt.Errorf("bad cursor %q: pass the cursor a previous wait returned", s)
	}
	return e, seq, nil
}

// sessionChanges records what changed between a session's copy before an
// update (prev) and after it (s): a state wait knows, or new PR attention.
func (d *Daemon) sessionChanges(prev, s *state.Session) {
	if s.State != prev.State {
		kind := ""
		switch s.State {
		case state.Done:
			kind = kindDone
		case state.Waiting:
			kind = kindWaiting
		case state.Idle:
			kind = kindIdle
		case state.Failed:
			kind = kindFailed
		case state.Landed:
			kind = kindLanded
		}
		if kind != "" {
			sum := s.Slug + ": " + kind
			switch {
			case kind == kindWaiting && s.WaitKind != "":
				sum += " (" + s.WaitKind + ")"
			case kind == kindDone && d.noReport(prev, s):
				// U14: done is the Stop hook; say when it came without a
				// report since the last prompt
				sum += " (no report)"
			case kind == kindFailed && s.Error != "":
				sum += ": " + lineCut(s.Error, 120)
			}
			d.changes.add(Change{Kind: kind, Slug: s.Slug, Summary: sum, parent: s.Parent})
		}
	}
	if s.PRStatus != nil {
		had := map[string]bool{}
		if prev.PRStatus != nil {
			for _, a := range prev.PRStatus.Attention {
				had[a] = true
			}
		}
		for _, a := range s.PRStatus.Attention {
			if !had[a] {
				d.changes.add(Change{Kind: kindPR, Slug: s.Slug, Summary: fmt.Sprintf("%s: PR #%d %s", s.Slug, s.PR, a), parent: s.Parent})
			}
		}
	}
}

// noReport: s went done without a report since its last prompt (or, not
// knowing that after a restart, since its previous state began).
func (d *Daemon) noReport(prev, s *state.Session) bool {
	since := d.changes.lastPrompt(s.Slug)
	if since.IsZero() {
		since = prev.StateSince
	}
	return s.Report == nil || s.Report.At.Before(since)
}

func lineCut(s string, max int) string {
	s = untrusted.Line(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

type waitP struct {
	Slugs    []string `json:"slugs"`
	Mine     bool     `json:"mine"`
	On       []string `json:"on"`
	Since    string   `json:"since"`
	TimeoutS int      `json:"timeout_s"`
}

type waitResult struct {
	Cursor   string   `json:"cursor"`
	Epoch    string   `json:"epoch"`
	Changes  []Change `json:"changes"`
	TimedOut bool     `json:"timed_out,omitempty"`
	Reset    bool     `json:"reset,omitempty"`
}

// hWait returns the changes after since that match, waiting for one when
// there is none yet. No since: from now on.
func (d *Daemon) hWait(who *Caller, p waitP) (any, error) {
	for _, k := range p.On {
		if !waitKinds[k] {
			return nil, fmt.Errorf("unknown kind %q (report, done, waiting, idle, failed, pr, landed, removed, inbox)", k)
		}
	}
	slugs := map[string]bool{}
	for _, s := range p.Slugs {
		if strings.Contains(s, "/") {
			r, err := d.resolveSessionRef(s)
			if err != nil {
				return nil, err
			}
			s = r
		}
		slugs[s] = true
	}
	id := who.ID()
	if p.Mine && id == callerUnknown {
		return nil, errUnknownCaller("wait --mine")
	}
	if p.TimeoutS < 0 || p.TimeoutS > waitMaxS {
		return nil, fmt.Errorf("timeout_s must be 0..%d", waitMaxS)
	}
	timeout := time.Duration(p.TimeoutS) * time.Second
	if p.TimeoutS == 0 {
		timeout = waitDefaultS * time.Second
	}
	on := map[string]bool{}
	for _, k := range p.On {
		on[k] = true
	}
	match := func(c Change) bool {
		return (len(on) == 0 || on[c.Kind]) && (len(slugs) == 0 || slugs[c.Slug]) && (!p.Mine || c.parent == id)
	}

	l := d.changes
	l.mu.Lock()
	var after int64
	if p.Since == "" {
		after = l.seq
	} else {
		epoch, seq, err := parseCursor(p.Since)
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
		if epoch != l.epoch || seq > l.seq || seq+1 < l.oldestLocked() {
			// pekoed restarted, or the changes after the cursor are gone
			res := &waitResult{Cursor: l.cursorLocked(), Epoch: l.epoch, Changes: []Change{}, Reset: true}
			l.mu.Unlock()
			return res, nil
		}
		after = seq
	}
	if !who.Human() {
		if l.waiting[id] >= waitsPerCaller {
			l.mu.Unlock()
			return nil, fmt.Errorf("wait: %s already has %d waits open; wait on one (with --since) instead of several", id, waitsPerCaller)
		}
		l.waiting[id]++
		defer func() {
			l.mu.Lock()
			if l.waiting[id]--; l.waiting[id] <= 0 {
				delete(l.waiting, id)
			}
			l.mu.Unlock()
		}()
	}
	l.mu.Unlock()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		l.mu.Lock()
		got := []Change{}
		for _, c := range l.ring {
			if c.Seq > after && match(c) {
				got = append(got, c)
			}
		}
		res := &waitResult{Cursor: l.cursorLocked(), Epoch: l.epoch, Changes: got}
		if len(got) > 0 {
			l.mu.Unlock()
			return res, nil
		}
		after = l.seq // nothing that matched so far: only newer ones count
		wake := l.wake
		l.mu.Unlock()
		select {
		case <-wake:
		case <-deadline.C:
			res.TimedOut = true
			return res, nil
		case <-who.done:
			return nil, errors.New("wait: connection closed")
		case <-d.stop:
			return nil, errors.New("wait: pekoed is stopping")
		}
	}
}
