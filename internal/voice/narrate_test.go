package voice

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"pekoe/internal/state"
	"pekoe/internal/voice/voicetest"
)

// Rule 5.1: which state changes are worth saying.
func TestRule5_1Transition(t *testing.T) {
	cases := []struct {
		from, to state.State
		want     Kind // "" says nothing
	}{
		{state.Running, state.Waiting, KindWait},
		{state.Idle, state.Waiting, KindWait},
		{state.Waiting, state.Waiting, ""},
		{state.Running, state.Done, KindDone},
		{state.Waiting, state.Done, KindDone},
		{state.Reviewing, state.Done, ""}, // the person closed the review
		{state.Idle, state.Done, ""},
		{state.Creating, state.Failed, KindError},
		{state.Running, state.Failed, KindError},
		{state.Failed, state.Failed, ""},
		{state.Done, state.Landed, ""},
		{state.Reviewing, state.Approved, ""},
		{state.Running, state.Idle, ""},
		{state.Waiting, state.Running, ""},
		{state.Queued, state.Creating, ""},
	}
	for _, c := range cases {
		got, ok := Transition(&state.Session{State: c.from}, &state.Session{State: c.to})
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s → %s = %q %v, want %q", c.from, c.to, got, ok, c.want)
		}
	}
	if _, ok := Transition(nil, &state.Session{State: state.Waiting}); ok {
		t.Error("a new session spoke")
	}
}

type pushes struct {
	mu    sync.Mutex
	items []Item
}

func (p *pushes) push(it Item) {
	p.mu.Lock()
	p.items = append(p.items, it)
	p.mu.Unlock()
}

func (p *pushes) kinds() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, it := range p.items {
		out = append(out, string(it.Kind)+":"+it.Slug)
	}
	return out
}

func sess(slug string, st state.State, since time.Time) *state.Session {
	return &state.Session{Slug: slug, Repo: "web", State: st, StateSince: since}
}

func newTracker() (*Tracker, *voicetest.Clock, *pushes) {
	clk := voicetest.NewClock()
	p := &pushes{}
	return NewTracker(clk, p.push), clk, p
}

// Rule 5.1: the tracker pushes wait / done / error with the state they were
// pushed in, and nothing for the baseline.
func TestRule5_1TrackerPushes(t *testing.T) {
	tr, clk, p := newTracker()
	t0 := clk.Now()
	tr.Baseline([]state.Session{*sess("feat-a", state.Waiting, t0)}, nil)
	tr.Session(sess("feat-a", state.Waiting, t0), sess("feat-a", state.Running, t0.Add(time.Second)))
	tr.Session(sess("feat-a", state.Running, t0.Add(time.Second)), sess("feat-a", state.Waiting, t0.Add(2*time.Second)))
	tr.Session(sess("fix-b", state.Running, t0), sess("fix-b", state.Done, t0))
	tr.Session(sess("fix-c", state.Creating, t0), sess("fix-c", state.Failed, t0))
	tr.Session(sess("fix-d", state.Reviewing, t0), sess("fix-d", state.Done, t0))
	want := []string{"wait:feat-a", "done:fix-b", "error:fix-c"}
	if got := p.kinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("pushed %q, want %q", got, want)
	}
	if it := p.items[0]; it.State != state.Waiting || !it.Since.Equal(t0.Add(2*time.Second)) || it.Repo != "web" {
		t.Errorf("wait item = %+v", it)
	}
}

// Rule 5.1 (D2): the session shown in [4] of a focused terminal says no
// wait / done / error; unfocused, or another session, it does.
func TestRule5_1SeenSessionSilent(t *testing.T) {
	tr, clk, p := newTracker()
	t0 := clk.Now()
	tr.Focus("feat-a", true)
	tr.Session(sess("feat-a", state.Running, t0), sess("feat-a", state.Waiting, t0))
	tr.Session(sess("feat-a", state.Waiting, t0), sess("feat-a", state.Done, t0))
	tr.Session(sess("feat-b", state.Running, t0), sess("feat-b", state.Waiting, t0))
	tr.Focus("feat-a", false)
	tr.Session(sess("feat-a", state.Done, t0), sess("feat-a", state.Failed, t0))
	want := []string{"wait:feat-b", "error:feat-a"}
	if got := p.kinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("pushed %q, want %q", got, want)
	}
}

func ask(id int64, from, kind, status string) state.InboxItem {
	return state.InboxItem{ID: id, From: from, Kind: kind, Status: status}
}

// Rule 5.1 (ask, D3): a new question an agent waits on is an ask; fyi,
// a person's question, answered ones and ones already seen are not. The ask
// is said even for the seen session.
func TestRule5_1Ask(t *testing.T) {
	tr, _, p := newTracker()
	tr.Baseline(nil, []state.InboxItem{ask(1, "feat-a", "decision", state.InboxOpen)})
	tr.Focus("feat-b", true)
	tr.Inbox([]state.InboxItem{
		ask(1, "feat-a", "decision", state.InboxOpen), // baseline
		ask(2, "feat-b", "login", state.InboxOpen),
		ask(3, "feat-c", "fyi", state.InboxOpen),
		ask(4, "human", "decision", state.InboxOpen),
		ask(5, "feat-d", "approve", state.InboxAnswered),
	})
	tr.Inbox([]state.InboxItem{ask(2, "feat-b", "login", state.InboxOpen), ask(6, TapiSlug, "approve", state.InboxOpen)})
	want := []string{"ask:feat-b", "ask:" + TapiSlug}
	if got := p.kinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("pushed %q, want %q", got, want)
	}
}

// Rule 5.4: idle_all once, 5 seconds after the running count drops to 0.
func TestRule5_4IdleAll(t *testing.T) {
	t0 := voicetest.NewClock().Now()
	run := func(slug string) *state.Session { return sess(slug, state.Running, t0) }
	done := func(slug string) *state.Session { return sess(slug, state.Done, t0) }
	wait := func(slug string) *state.Session { return sess(slug, state.Waiting, t0) }

	type step struct {
		before, after *state.Session
		inbox         []state.InboxItem
		advance       time.Duration
	}
	cases := []struct {
		name  string
		steps []step
		want  int // idle_all pushes
	}{
		{"5.4 drops to 0 and stays 5s", []step{
			{nil, run("a"), nil, 0}, {run("a"), done("a"), nil, 5 * time.Second},
		}, 1},
		{"5.4 not before 5s", []step{
			{nil, run("a"), nil, 0}, {run("a"), done("a"), nil, 4999 * time.Millisecond},
		}, 0},
		{"5.4 runs again within 5s: no", []step{
			{nil, run("a"), nil, 0}, {run("a"), done("a"), nil, 3 * time.Second},
			{done("a"), run("a"), nil, 3 * time.Second},
		}, 0},
		{"5.4 the next session starts within 5s: no", []step{
			{nil, run("a"), nil, 0}, {run("a"), done("a"), nil, time.Second},
			{nil, sess("b", state.Queued, t0), nil, 10 * time.Second},
		}, 0},
		{"5.4 a waiting session at 5s: no", []step{
			{nil, run("a"), nil, 0}, {nil, run("b"), nil, 0},
			{run("a"), wait("a"), nil, 0}, {run("b"), done("b"), nil, 6 * time.Second},
		}, 0},
		{"5.4 a question an agent waits on at 5s: no", []step{
			{nil, run("a"), nil, 0},
			{run("a"), done("a"), []state.InboxItem{ask(1, "a", "decision", state.InboxOpen)}, 6 * time.Second},
		}, 0},
		{"5.4 once until something runs again", []step{
			{nil, run("a"), nil, 0}, {run("a"), done("a"), nil, 6 * time.Second},
			{nil, done("b"), nil, 6 * time.Second}, // still 0 running: no second
			{nil, run("c"), nil, 0}, {run("c"), done("c"), nil, 6 * time.Second},
		}, 2},
		{"5.4 never ran: no", []step{
			{nil, done("a"), nil, 10 * time.Second},
		}, 0},
		{"5.4 running tapi chat counts", []step{
			{nil, sess(TapiSlug, state.Running, t0), nil, 0}, {nil, done("a"), nil, 10 * time.Second},
			{sess(TapiSlug, state.Running, t0), sess(TapiSlug, state.Idle, t0), nil, 5 * time.Second},
		}, 1},
	}
	for _, c := range cases {
		tr, clk, p := newTracker()
		for _, s := range c.steps {
			if s.inbox != nil {
				tr.Inbox(s.inbox)
			}
			tr.Session(s.before, s.after)
			clk.Advance(s.advance)
		}
		n := 0
		for _, k := range p.kinds() {
			if k == "idle_all:" {
				n++
			}
		}
		if n != c.want {
			t.Errorf("%s: idle_all %d times, want %d (%q)", c.name, n, c.want, p.kinds())
		}
	}
}

// Rule Q5 (source): an item is fresh while its session holds the same
// state since the same time, its question is open, idle still holds.
func TestRuleQ5Fresh(t *testing.T) {
	tr, clk, _ := newTracker()
	t0 := clk.Now()
	t1 := t0.Add(time.Second)
	tr.Session(nil, sess("a", state.Waiting, t0))
	tr.Inbox([]state.InboxItem{ask(7, "b", "decision", state.InboxOpen)})
	w := Item{Kind: KindWait, Slug: "a", State: state.Waiting, Since: t0}
	if !tr.Fresh(w) {
		t.Error("wait not fresh")
	}
	tr.Session(sess("a", state.Waiting, t0), sess("a", state.Waiting, t1)) // waiting again, later
	if tr.Fresh(w) {
		t.Error("wait fresh after state_since moved")
	}
	a := Item{Kind: KindAsk, Slug: "b", AskID: 7}
	if !tr.Fresh(a) {
		t.Error("ask not fresh")
	}
	idle := Item{Kind: KindIdleAll}
	if tr.Fresh(idle) {
		t.Error("idle fresh with a waiting session and an open question")
	}
	tr.Inbox(nil)
	if tr.Fresh(a) {
		t.Error("ask fresh after it closed")
	}
	tr.Removed("a")
	if tr.Fresh(Item{Kind: KindWait, Slug: "a", State: state.Waiting, Since: t1}) {
		t.Error("wait fresh after the session was removed")
	}
	if !tr.Fresh(idle) {
		t.Error("idle not fresh with nothing left")
	}
	if !tr.Fresh(Item{Kind: KindTest}) {
		t.Error("test not fresh")
	}
}
