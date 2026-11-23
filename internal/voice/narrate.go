package voice

import (
	"sync"
	"time"

	"pekoe/internal/state"
)

// IdleAllDelay is how long nothing must run before tapi says idle_all
// (5.4).
const IdleAllDelay = 5 * time.Second

// Transition is the narrate kind a session's state change is worth (5.1),
// ok false when it says nothing: → waiting is wait, → done from running or
// waiting is done (reviewing → done is the person closing the review), →
// failed is error. before nil (a new session) says nothing.
func Transition(before, after *state.Session) (Kind, bool) {
	if before == nil || after == nil || before.State == after.State {
		return "", false
	}
	switch after.State {
	case state.Waiting:
		return KindWait, true
	case state.Done:
		if before.State == state.Running || before.State == state.Waiting {
			return KindDone, true
		}
	case state.Failed:
		return KindError, true
	}
	return "", false
}

// Active: the state counts as running for idle_all (5.4).
func Active(s state.State) bool {
	return s == state.Running || s == state.Creating || s == state.Queued
}

// HoldsUp: an agent waits on the inbox question (open, not fyi, not asked
// by a person); the TUI's inboxHoldsUp.
func HoldsUp(it state.InboxItem) bool {
	return it.Status == state.InboxOpen && it.Kind != "fyi" && it.From != "human"
}

// Item is one thing to say, as pushed onto the Queue.
type Item struct {
	Kind Kind
	Slug string // "" for idle_all
	Repo string // the repository's display name (to tell same names apart)

	// Narrate: the state and state_since when it was pushed (Q5), the
	// inbox question for ask.
	State state.State
	Since time.Time
	AskID int64

	// Read and test: the sentence. Read: the turn and message it is from
	// (Q9, max_sentences).
	Text    string
	Turn    string
	Message string

	// Test: the voice and language asked for ("" = the default).
	Voice string
	Lang  string
}

type tracked struct {
	state state.State
	since time.Time
	repo  string
}

// Tracker follows the sessions' states and the questions agents wait on,
// pushes narrate items for the changes worth saying (5.1, 5.4) and answers
// whether a pushed item still holds (Q5). Tapi chat is the session
// TapiSlug.
type Tracker struct {
	clock Clock
	push  func(Item)

	mu       sync.Mutex
	sessions map[string]tracked
	asks     map[int64]string // open questions agents wait on → asker
	focus    string
	termFoc  bool

	wasActive bool   // something ran since idle_all was last armed
	idleTimer Timer  // pending idle_all check
	idleGen   uint64 // which arming idleTimer belongs to
	idleSaid  bool   // idle_all was said; quiet until something runs
}

// NewTracker reports the items worth saying to push (the Queue's Push).
// push is called with no lock of the Tracker's held.
func NewTracker(clock Clock, push func(Item)) *Tracker {
	return &Tracker{clock: clock, push: push, sessions: map[string]tracked{}, asks: map[int64]string{}}
}

// Baseline takes the states at startup or when voice is turned on as the
// starting point without saying anything (4.2).
func (t *Tracker) Baseline(sessions []state.Session, inbox []state.InboxItem) {
	t.mu.Lock()
	t.sessions = map[string]tracked{}
	t.asks = map[int64]string{}
	for _, s := range sessions {
		t.sessions[s.Slug] = tracked{s.State, s.StateSince, s.Repo}
	}
	for _, it := range inbox {
		if HoldsUp(it) {
			t.asks[it.ID] = it.From
		}
	}
	if t.idleTimer != nil {
		t.idleTimer.Stop()
		t.idleTimer = nil
	}
	t.wasActive = t.activeLocked() > 0
	t.idleSaid = false
	t.mu.Unlock()
}

// Focus is what the last voice.focus said: the session shown in [4] and
// whether the terminal has focus. A session shown in a focused terminal
// says no wait, done or error (5.1, D2).
func (t *Tracker) Focus(slug string, termFocused bool) {
	t.mu.Lock()
	t.focus, t.termFoc = slug, termFocused
	t.mu.Unlock()
}

// Session takes one state change (Daemon.update, in order; before nil for
// a new session).
func (t *Tracker) Session(before, after *state.Session) {
	if after == nil {
		return
	}
	var out []Item
	t.mu.Lock()
	t.sessions[after.Slug] = tracked{after.State, after.StateSince, after.Repo}
	if k, ok := Transition(before, after); ok && !(t.termFoc && t.focus == after.Slug) {
		out = append(out, Item{Kind: k, Slug: after.Slug, Repo: after.Repo, State: after.State, Since: after.StateSince})
	}
	t.activityLocked()
	t.mu.Unlock()
	for _, it := range out {
		t.push(it)
	}
}

// Removed forgets a session (session.removed). Its queued items go stale.
func (t *Tracker) Removed(slug string) {
	t.mu.Lock()
	delete(t.sessions, slug)
	t.activityLocked()
	t.mu.Unlock()
}

// Inbox takes the inbox after inbox.updated: a question an agent waits on
// that was not there before is an ask (5.1, D3); the ones gone are
// forgotten.
func (t *Tracker) Inbox(items []state.InboxItem) {
	var out []Item
	t.mu.Lock()
	now := map[int64]string{}
	for _, it := range items {
		if !HoldsUp(it) {
			continue
		}
		now[it.ID] = it.From
		if _, ok := t.asks[it.ID]; !ok {
			out = append(out, Item{Kind: KindAsk, Slug: it.From, Repo: t.sessions[it.From].repo, AskID: it.ID})
		}
	}
	t.asks = now
	t.mu.Unlock()
	for _, it := range out {
		t.push(it)
	}
}

// Fresh: the narrate item still holds (Q5). wait, done and error need the
// session to be in the same state since the same time, ask the question
// still open, idle_all 5.4 to still hold. Other kinds are always fresh.
func (t *Tracker) Fresh(it Item) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch it.Kind {
	case KindWait, KindDone, KindError:
		s, ok := t.sessions[it.Slug]
		return ok && s.state == it.State && s.since.Equal(it.Since)
	case KindAsk:
		_, ok := t.asks[it.AskID]
		return ok
	case KindIdleAll:
		return t.idleHoldsLocked()
	}
	return true
}

func (t *Tracker) activeLocked() int {
	n := 0
	for _, s := range t.sessions {
		if Active(s.state) {
			n++
		}
	}
	return n
}

// idleHoldsLocked: nothing runs and nothing waits on the person (no
// waiting session, no question an agent waits on).
func (t *Tracker) idleHoldsLocked() bool {
	if len(t.asks) > 0 {
		return false
	}
	for _, s := range t.sessions {
		if Active(s.state) || s.state == state.Waiting {
			return false
		}
	}
	return true
}

// activityLocked arms idle_all when the running count drops from 1 or
// more to 0 and disarms it when something runs again (5.4).
func (t *Tracker) activityLocked() {
	if t.activeLocked() > 0 {
		t.wasActive = true
		t.idleSaid = false
		if t.idleTimer != nil {
			t.idleTimer.Stop()
			t.idleTimer = nil
		}
		return
	}
	if !t.wasActive {
		return
	}
	t.wasActive = false
	if t.idleTimer != nil {
		t.idleTimer.Stop()
	}
	t.idleGen++
	gen := t.idleGen
	t.idleTimer = t.clock.AfterFunc(IdleAllDelay, func() { t.idleFire(gen) })
}

func (t *Tracker) idleFire(gen uint64) {
	t.mu.Lock()
	if t.idleTimer == nil || t.idleGen != gen {
		t.mu.Unlock()
		return
	}
	t.idleTimer = nil
	say := !t.idleSaid && t.idleHoldsLocked()
	if say {
		t.idleSaid = true
	}
	t.mu.Unlock()
	if say {
		t.push(Item{Kind: KindIdleAll})
	}
}
