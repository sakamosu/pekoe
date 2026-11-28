package daemon

import (
	"sync"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// outputEvents counts session.output per slug.
type outputEvents struct {
	mu sync.Mutex
	n  map[string]int
	at []time.Time
}

func watchOutput(d *Daemon) *outputEvents {
	o := &outputEvents{n: map[string]int{}}
	d.testHook = func(ev string, data any) {
		if ev != "session.output" {
			return
		}
		o.mu.Lock()
		o.n[data.(map[string]any)["slug"].(string)]++
		o.at = append(o.at, time.Now())
		o.mu.Unlock()
	}
	return o
}

func (o *outputEvents) count(slug string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.n[slug]
}

// Right after a person typed into a session (session.keys), its output
// goes out as session.output at once (at most every typedOutputGap), not
// at the next 30 ms flush: the TUI fetches the echo sooner. Untyped
// sessions, and the typed one a second later, keep the 30 ms batching.
func TestTypedOutputGoesOutAtOnce(t *testing.T) {
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	o := watchOutput(d)
	agent := func(b string) bool {
		return d.onOutput(engine.Output{Slug: s.Slug, Window: engine.WinAgent, Data: []byte(b)})
	}

	// nobody typed: pending until a flush 30 ms after the last event
	if agent("tick") {
		t.Fatal("untyped output asked for an early flush")
	}
	if n := o.count(s.Slug); n != 0 {
		t.Fatalf("untyped output went out at once (%d)", n)
	}
	d.flushOutputEvents()
	if n := o.count(s.Slug); n != 1 {
		t.Fatalf("flush sent %d, want 1", n)
	}

	// typed (through the RPC the TUI uses): the echo goes out at once …
	call[any](t, d, "session.keys", map[string]any{"slug": s.Slug, "keys": []string{"a"}})
	d.mu.Lock()
	d.out[s.Slug].lastEmit = time.Now().Add(-time.Second) // the last one long ago
	d.mu.Unlock()
	if agent("a") {
		t.Fatal("the first typed chunk was held back")
	}
	if n := o.count(s.Slug); n != 2 {
		t.Fatalf("typed output: %d session.output, want 2 (at once)", n)
	}
	// … the next chunk within typedOutputGap waits for an early flush …
	if !agent("b") {
		t.Fatal("a chunk right after did not ask for an early flush")
	}
	if n := o.count(s.Slug); n != 2 {
		t.Fatalf("a chunk within %s went out at once", typedOutputGap)
	}
	time.Sleep(typedOutputGap + time.Millisecond)
	d.flushOutputEvents() // the early flush: typedOutputGap, not 30 ms, after the last
	if n := o.count(s.Slug); n != 3 {
		t.Fatalf("early flush: %d, want 3", n)
	}

	// a second after the typing: batched again
	d.mu.Lock()
	d.typedAt[s.Slug] = time.Now().Add(-typedOutputFor - time.Millisecond)
	d.out[s.Slug].lastEmit = time.Now().Add(-10 * time.Millisecond)
	d.mu.Unlock()
	if agent("late") {
		t.Fatal("output long after the typing asked for an early flush")
	}
	d.flushOutputEvents() // 10 ms after the last one: too soon for 30 ms
	if n := o.count(s.Slug); n != 3 {
		t.Fatalf("output long after the typing went out early (%d)", n)
	}
}

// Through outputLoop: a chunk held back right after typing still goes out
// (the loop's early flush), typedOutputGap or more after the first one.
func TestTypedOutputLoopFlushesTheRest(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	o := watchOutput(d)
	done := make(chan struct{})
	go func() { d.outputLoop(); close(done) }()
	t.Cleanup(func() { close(d.stop); <-done })

	call[any](t, d, "session.keys", map[string]any{"slug": s.Slug, "keys": []string{"a"}})
	fe.out <- engine.Output{Slug: s.Slug, Window: engine.WinAgent, Data: []byte("a")}
	fe.out <- engine.Output{Slug: s.Slug, Window: engine.WinAgent, Data: []byte("b")}
	deadline := time.Now().Add(2 * time.Second)
	for o.count(s.Slug) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.at) < 2 {
		t.Fatalf("%d session.output, want 2", len(o.at))
	}
	if gap := o.at[1].Sub(o.at[0]); gap < typedOutputGap {
		t.Fatalf("two session.output %s apart, under %s", gap, typedOutputGap)
	}
}
