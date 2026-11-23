package voice

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/state"
	"pekoe/internal/voice/voicetest"
)

// rig is a Queue on the fake clock, synth and player, with the log, modes
// and freshness under the test's control.
type rig struct {
	t   *testing.T
	clk *voicetest.Clock
	syn *voicetest.Synth
	pl  *voicetest.Player
	q   *Queue

	mu       sync.Mutex
	logs     []LogEntry
	modes    map[string]Mode
	stale    map[string]bool // slug → its narrate items are stale
	failover []bool          // answers of Failover, in order
	failN    int
	released []string
}

func newRig(t *testing.T, edit ...func(*Config)) *rig {
	r := &rig{t: t, clk: voicetest.NewClock(), syn: voicetest.NewSynth(), pl: voicetest.NewPlayer(),
		modes: map[string]Mode{}, stale: map[string]bool{}}
	cfg := Config{
		Clock: r.clk, Synth: r.syn, Player: r.pl,
		Phrases: NewPhrases(LangEn, nil),
		Mode: func(slug string) Mode {
			r.mu.Lock()
			defer r.mu.Unlock()
			if m, ok := r.modes[slug]; ok {
				return m
			}
			return ModeNarrate
		},
		Fresh: func(it Item) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return !r.stale[it.Slug]
		},
		Failover: func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.failN++
			if len(r.failover) == 0 {
				return false
			}
			ok := r.failover[0]
			r.failover = r.failover[1:]
			return ok
		},
		Backend: func() string { return "fake" },
		Release: func(wav string) {
			r.mu.Lock()
			r.released = append(r.released, wav)
			r.mu.Unlock()
		},
		Log: func(e LogEntry) {
			r.mu.Lock()
			r.logs = append(r.logs, e)
			r.mu.Unlock()
		},
	}
	for _, e := range edit {
		e(&cfg)
	}
	r.q = NewQueue(cfg)
	t.Cleanup(r.q.Close)
	return r
}

func (r *rig) setMode(slug string, m Mode) {
	r.mu.Lock()
	r.modes[slug] = m
	r.mu.Unlock()
}

func (r *rig) setStale(slug string) {
	r.mu.Lock()
	r.stale[slug] = true
	r.mu.Unlock()
}

func (r *rig) narrate(k Kind, slug string) bool {
	return r.q.Push(Item{Kind: k, Slug: slug, State: state.Waiting, Since: r.clk.Now()})
}

func (r *rig) read(slug, turn, msg, text string) bool {
	return r.q.Push(Item{Kind: KindRead, Slug: slug, Turn: turn, Message: msg, Text: text})
}

// waitFor polls the queue's own state until cond holds (the synth and
// play calls finish on goroutines). Rule timings never come from here:
// they come from the fake clock.
func (r *rig) waitFor(what string, cond func(Status) bool) {
	r.t.Helper()
	deadline := time.Now().Add(voicetest.Wait)
	for {
		if cond(r.q.Status()) {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("waiting for %s; status %+v; log %s", what, r.q.Status(), r.logText())
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *rig) waitSpeaking(text string) {
	r.t.Helper()
	r.waitFor("speaking "+text, func(s Status) bool { return s.Speaking != nil && s.Speaking.Text == text })
}

func (r *rig) waitSilent() {
	r.t.Helper()
	r.waitFor("silence", func(s Status) bool { return s.Speaking == nil })
}

func (r *rig) waitIdle() {
	r.t.Helper()
	r.waitFor("no synth running", func(s Status) bool { return s.Synthesizing == 0 })
}

// synthOK finishes the next synth call, checks its sentence and returns it.
// Calls the queue already gave up on (cancelled) are skipped: they return
// on their own, and their order against the new call is not fixed.
func (r *rig) synthOK(text string) *voicetest.SynthCall {
	r.t.Helper()
	c := r.syn.Next(r.t)
	for c.Cancelled() && c.Text != text {
		c = r.syn.Next(r.t)
	}
	if c.Text != text {
		r.t.Fatalf("synth %q, want %q", c.Text, text)
	}
	c.OK("/data/voice/" + text + ".wav")
	return c
}

// play waits for the next play call and checks its file.
func (r *rig) play(text string) *voicetest.PlayCall {
	r.t.Helper()
	p := r.pl.Next(r.t)
	if want := "/data/voice/" + text + ".wav"; p.Wav != want {
		r.t.Fatalf("play %q, want %q", p.Wav, want)
	}
	return p
}

// say runs one utterance through: synth OK, play, finish.
func (r *rig) say(text string) {
	r.t.Helper()
	r.synthOK(text)
	p := r.play(text)
	r.waitSpeaking(text)
	p.Finish()
	r.waitSilent()
}

func (r *rig) logText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b []string
	for _, e := range r.logs {
		b = append(b, fmt.Sprintf("%s/%s/%s/%q/%s", e.Slug, e.Kind, e.Result, e.Text, e.Reason))
	}
	return strings.Join(b, "; ")
}

// results are the log's "slug:result" in order.
func (r *rig) results() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.logs {
		out = append(out, e.Slug+":"+string(e.Result))
	}
	return out
}

func (r *rig) wantResults(want ...string) {
	r.t.Helper()
	if got := r.results(); !reflect.DeepEqual(got, want) {
		r.t.Fatalf("log %q, want %q (%s)", got, want, r.logText())
	}
}

func (r *rig) noSynthYet() {
	r.t.Helper()
	if s := r.q.Status(); s.Synthesizing != 0 {
		r.t.Fatalf("synth started early: %+v", s)
	}
}

// Q1: one utterance sounds at a time; the next waits for the end even when
// it is synthesized.
func TestRuleQ1Serial(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.narrate(KindDone, "fix-b")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	pa := r.play("a needs you")
	r.waitSpeaking("a needs you")
	r.synthOK("b is done") // prefetched while a plays
	r.waitIdle()
	r.clk.Advance(time.Second)
	if s := r.q.Status(); s.Speaking == nil || s.Speaking.Text != "a needs you" {
		t.Fatalf("speaking %+v while a plays", s.Speaking)
	}
	pa.Finish()
	pb := r.play("b is done")
	r.waitSpeaking("b is done")
	pb.Finish()
	r.waitSilent()
	r.wantResults("fix-a:spoken", "fix-b:spoken")
}

// Q1 with Stop: the next utterance waits until the stopped one has really
// ended.
func TestRuleQ1StopWaitsForEnd(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	pa := r.play("a needs you")
	r.waitSpeaking("a needs you")
	r.narrate(KindDone, "fix-b")
	r.clk.Advance(GatherDelay)
	r.synthOK("b is done")
	r.q.Stop()
	pa.WaitStopped(t)
	r.play("b is done").Finish()
	r.waitSilent()
}

// Q2: narrate goes before read; a read that sounds is finished first and
// the reads go on after the narrate.
func TestRuleQ2Priority(t *testing.T) {
	r := newRig(t)
	r.setMode("feat-r", ModeRead)
	r.read("feat-r", "t1", "m1", "one.")
	r.synthOK("one.")
	p1 := r.play("one.")
	r.waitSpeaking("one.")
	r.narrate(KindWait, "fix-a")
	r.read("feat-r", "t1", "m1", "two.")
	// the read "two." waits while the narrate gathers (Q3)
	r.noSynthYet()
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	r.waitIdle()
	if s := r.q.Status(); s.Speaking == nil || s.Speaking.Text != "one." {
		t.Fatalf("the read was cut: %+v", s.Speaking)
	}
	p1.Finish()
	pa := r.play("a needs you")
	r.synthOK("two.") // prefetched while the narrate plays
	pa.Finish()
	r.play("two.").Finish()
	r.waitSilent()
	r.wantResults("feat-r:spoken", "fix-a:spoken", "feat-r:spoken")
}

// Q2: a narrate past its gather delay goes before a read pushed earlier,
// even one already synthesized.
func TestRuleQ2NarrateBeforePrefetchedRead(t *testing.T) {
	r := newRig(t)
	r.setMode("feat-r", ModeRead)
	r.narrate(KindError, "fix-x")
	r.clk.Advance(GatherDelay)
	r.synthOK("x hit an error")
	px := r.play("x hit an error")
	r.read("feat-r", "t1", "m1", "one.")
	r.synthOK("one.") // planned next
	r.waitIdle()
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you") // the read went back
	px.Finish()
	r.play("a needs you").Finish()
	r.synthOK("one.")
	r.play("one.").Finish()
	r.waitSilent()
}

// Q3: a narrate is not synthesized until 300ms after it was pushed.
func TestRuleQ3Gather(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.noSynthYet()
	r.clk.Advance(GatherDelay - time.Millisecond)
	r.noSynthYet()
	r.clk.Advance(time.Millisecond)
	r.say("a needs you")
}

// Q4: the same narrate kind on the queue becomes one utterance with the
// _many phrase; other kinds stay apart; the log says merged.
func TestRuleQ4Merge(t *testing.T) {
	cases := []struct {
		name   string
		pushes []struct {
			k    Kind
			slug string
		}
		says []string
		log  []string
	}{
		{"Q4 three waits", []struct {
			k    Kind
			slug string
		}{{KindWait, "fix-a"}, {KindWait, "fix-b"}, {KindWait, "fix-c"}},
			[]string{"a, b and c need you"},
			[]string{"fix-a:spoken", "fix-b:merged", "fix-c:merged"}},
		{"Q4 kinds apart, oldest kind first", []struct {
			k    Kind
			slug string
		}{{KindDone, "fix-a"}, {KindWait, "fix-b"}, {KindDone, "fix-c"}},
			[]string{"a and c are done", "b needs you"},
			[]string{"fix-a:spoken", "fix-c:merged", "fix-b:spoken"}},
		{"Q4 the same session once", []struct {
			k    Kind
			slug string
		}{{KindAsk, "fix-a"}, {KindAsk, "fix-a"}},
			[]string{"a has a question"},
			[]string{"fix-a:spoken", "fix-a:merged"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			for _, p := range c.pushes {
				r.q.Push(Item{Kind: p.k, Slug: p.slug, AskID: 1})
			}
			r.clk.Advance(GatherDelay)
			for _, s := range c.says {
				r.say(s)
			}
			r.wantResults(c.log...)
		})
	}
}

// Q4: a narrate of the same kind that comes while the merged one is
// synthesized joins it (the sentence is made again).
func TestRuleQ4JoinsPlanned(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	first := r.syn.Next(t)
	r.narrate(KindWait, "fix-b")
	r.synthOK("a and b need you")
	if !first.Cancelled() {
		t.Error("the old sentence's synth was not cancelled")
	}
	r.play("a and b need you").Finish()
	r.waitSilent()
}

// Q5: a narrate whose session changed state is dropped before synth and
// before play.
func TestRuleQ5Stale(t *testing.T) {
	t.Run("Q5 before synth", func(t *testing.T) {
		r := newRig(t)
		r.narrate(KindWait, "fix-a")
		r.setStale("fix-a")
		r.clk.Advance(GatherDelay)
		r.noSynthYet()
		r.wantResults("fix-a:stale")
	})
	t.Run("Q5 before play", func(t *testing.T) {
		r := newRig(t)
		r.narrate(KindWait, "fix-a")
		r.clk.Advance(GatherDelay)
		r.synthOK("a needs you")
		pa := r.play("a needs you")
		r.narrate(KindDone, "fix-b")
		r.clk.Advance(GatherDelay)
		r.synthOK("b is done") // prefetched
		r.waitIdle()
		r.setStale("fix-b")
		pa.Finish()
		r.waitSilent()
		r.wantResults("fix-a:spoken", "fix-b:stale")
	})
	t.Run("Q5 one of a merged utterance", func(t *testing.T) {
		r := newRig(t)
		r.narrate(KindWait, "fix-x")
		r.clk.Advance(GatherDelay)
		r.synthOK("x needs you")
		px := r.play("x needs you")
		r.narrate(KindDone, "fix-a")
		r.narrate(KindDone, "fix-b")
		r.clk.Advance(GatherDelay)
		r.synthOK("a and b are done")
		r.waitIdle()
		r.setStale("fix-a")
		px.Finish()
		r.synthOK("b is done") // made again without a
		r.play("b is done").Finish()
		r.waitSilent()
		r.wantResults("fix-x:spoken", "fix-a:stale", "fix-b:spoken")
	})
	t.Run("Q5 removed session", func(t *testing.T) {
		r := newRig(t)
		r.narrate(KindWait, "fix-a")
		r.narrate(KindDone, "fix-b")
		r.q.Removed("fix-a")
		r.clk.Advance(GatherDelay)
		r.say("b is done")
		r.wantResults("fix-a:stale", "fix-b:spoken")
	})
}

// Q5 with the Tracker: the queue asks the tracker; waiting → running →
// waiting within the gather delay says the newer wait only.
func TestRuleQ5WithTracker(t *testing.T) {
	var tr *Tracker
	r := newRig(t, func(c *Config) { c.Fresh = func(it Item) bool { return tr.Fresh(it) } })
	tr = NewTracker(r.clk, func(it Item) { r.q.Push(it) })
	t0 := r.clk.Now()
	tr.Session(nil, sess("fix-a", state.Running, t0))
	tr.Session(sess("fix-a", state.Running, t0), sess("fix-a", state.Waiting, t0))
	r.clk.Advance(100 * time.Millisecond)
	t1 := r.clk.Now()
	tr.Session(sess("fix-a", state.Waiting, t0), sess("fix-a", state.Running, t1))
	tr.Session(sess("fix-a", state.Running, t1), sess("fix-a", state.Waiting, t1))
	r.clk.Advance(GatherDelay)
	r.say("a needs you")
	r.wantResults("fix-a:stale", "fix-a:spoken")
}

// Q6: a narrate 30 seconds old is dropped, not said late.
func TestRuleQ6Old(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	pa := r.play("a needs you")
	r.narrate(KindDone, "fix-b")
	r.clk.Advance(GatherDelay)
	r.synthOK("b is done")
	r.waitIdle()
	r.clk.Advance(MaxAge - GatherDelay - time.Millisecond)
	r.wantResults("fix-a:spoken")
	r.clk.Advance(time.Millisecond)
	r.wantResults("fix-a:spoken", "fix-b:old")
	pa.Finish()
	r.waitSilent()
	r.wantResults("fix-a:spoken", "fix-b:old")
}

// Q7: the same session and kind is not taken again within 10 seconds of
// being said; another kind or session is.
func TestRuleQ7Repeat(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.say("a needs you")
	cases := []struct {
		advance time.Duration
		k       Kind
		slug    string
		want    bool
	}{
		{time.Second, KindWait, "fix-a", false},
		{0, KindDone, "fix-a", true},
		{0, KindWait, "fix-b", true},
		{RepeatGap - time.Second - time.Millisecond, KindWait, "fix-a", false},
		{time.Millisecond, KindWait, "fix-a", true},
	}
	for i, c := range cases {
		r.clk.Advance(c.advance)
		if got := r.narrate(c.k, c.slug); got != c.want {
			t.Errorf("step %d (%s %s): taken %v, want %v", i, c.k, c.slug, got, c.want)
		}
	}
}

// Q8: mute stops the sound and empties the queue.
func TestRuleQ8Mute(t *testing.T) {
	r := newRig(t)
	r.setMode("feat-r", ModeRead)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	pa := r.play("a needs you")
	r.narrate(KindDone, "fix-b")
	r.read("feat-r", "t", "m", "hello.")
	r.q.Mute()
	pa.WaitStopped(t)
	r.waitSilent()
	if s := r.q.Status(); s.Queue != 0 {
		t.Fatalf("queue %d after mute", s.Queue)
	}
	r.wantResults("fix-a:spoken", "fix-b:muted", "feat-r:muted")
}

// Q8: the person's prompt stops that session's read and drops its reads;
// its narrate and other sessions' reads stay.
func TestRuleQ8UserPrompt(t *testing.T) {
	r := newRig(t)
	r.setMode("feat-r", ModeRead)
	r.setMode("feat-s", ModeRead)
	r.read("feat-r", "t", "m", "one.")
	r.synthOK("one.")
	p1 := r.play("one.")
	r.read("feat-r", "t", "m", "two.")
	r.read("feat-s", "t", "m", "other.")
	r.q.UserPrompt("feat-r")
	p1.WaitStopped(t)
	r.say("other.")
	r.wantResults("feat-r:spoken", "feat-r:interrupted", "feat-s:spoken")

	r2 := newRig(t)
	r2.setMode("feat-r", ModeRead)
	r2.narrate(KindWait, "feat-r")
	r2.q.UserPrompt("feat-r")
	r2.clk.Advance(GatherDelay)
	r2.say("r needs you") // narrate is kept
}

// Q8: moving the focus stops read and drops all reads; narrate stays.
func TestRuleQ8FocusMoved(t *testing.T) {
	r := newRig(t)
	r.setMode("feat-r", ModeRead)
	r.read("feat-r", "t", "m", "one.")
	r.synthOK("one.")
	p1 := r.play("one.")
	r.read("feat-r", "t", "m", "two.")
	r.narrate(KindWait, "fix-a")
	r.q.FocusMoved()
	p1.WaitStopped(t)
	r.waitSilent()
	r.clk.Advance(GatherDelay)
	r.say("a needs you")
	r.wantResults("feat-r:spoken", "feat-r:interrupted", "fix-a:spoken")
}

// Q8: voice.stop stops the sound and keeps the queue.
func TestRuleQ8Stop(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	pa := r.play("a needs you")
	r.narrate(KindDone, "fix-b")
	r.q.Stop()
	pa.WaitStopped(t)
	r.waitSilent()
	r.clk.Advance(GatherDelay)
	r.say("b is done")
	r.wantResults("fix-a:spoken", "fix-b:spoken")
}

// Q9: a new turn drops the old turn's reads; one message reads up to
// max_sentences.
func TestRuleQ9Reads(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MaxSentences = 2 })
	r.setMode("feat-r", ModeRead)
	r.read("feat-r", "t1", "m1", "a1.")
	r.synthOK("a1.")
	pa := r.play("a1.")
	r.read("feat-r", "t1", "m1", "a2.")
	if r.read("feat-r", "t1", "m1", "a3.") {
		t.Error("a third sentence of the message was taken (max 2)")
	}
	r.read("feat-r", "t1", "m2", "b1.") // a new message counts again
	r.read("feat-r", "t2", "m3", "c1.") // a new turn: a2 and b1 go
	if s := r.q.Status(); s.Queue != 1 {
		t.Fatalf("queue %d, want 1 (c1)", s.Queue)
	}
	r.synthOK("c1.")
	pa.Finish() // the sentence that sounds is not cut
	r.play("c1.").Finish()
	r.waitSilent()
	r.wantResults("feat-r:spoken", "feat-r:interrupted", "feat-r:interrupted", "feat-r:spoken")
}

// Q10: the next utterance is synthesized while one plays, and no more
// than two synth calls run at once (abandoned calls count until they
// return).
func TestRuleQ10Prefetch(t *testing.T) {
	r := newRig(t)
	r.syn.Stubborn = true // calls return only when the test finishes them
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	c1 := r.syn.Next(t)
	r.narrate(KindWait, "fix-b") // changes the sentence: c1 is abandoned
	c2 := r.syn.Next(t)
	if c2.Text != "a and b need you" || !c1.Cancelled() {
		t.Fatalf("second synth %q, first cancelled %v", c2.Text, c1.Cancelled())
	}
	r.narrate(KindWait, "fix-c") // changes it again; two calls still run
	if s := r.q.Status(); s.Synthesizing != 2 || !c2.Cancelled() {
		t.Fatalf("synthesizing %d, second cancelled %v", s.Synthesizing, c2.Cancelled())
	}
	c1.Fail(errors.New("cancelled")) // returns: room for the new sentence
	c3 := r.syn.Next(t)
	if c3.Text != "a, b and c need you" {
		t.Fatalf("third synth %q", c3.Text)
	}
	c2.Fail(errors.New("cancelled"))
	c3.OK("/data/voice/a, b and c need you.wav")
	pa := r.play("a, b and c need you")
	r.narrate(KindDone, "fix-d")
	r.clk.Advance(GatherDelay)
	r.synthOK("d is done") // while a, b and c plays
	r.waitIdle()
	pa.Finish()
	r.play("d is done").Finish()
	r.waitSilent()
	// abandoned calls are not failures
	r.wantResults("fix-a:spoken", "fix-b:merged", "fix-c:merged", "fix-d:spoken")
}

// Q11: a failed synth drops the utterance and logs the backend's first
// line.
func TestRuleQ11Failure(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.syn.Next(t).Fail(errors.New("engine refused\nmore detail"))
	r.waitIdle()
	r.narrate(KindDone, "fix-b")
	r.clk.Advance(GatherDelay)
	r.say("b is done")
	r.wantResults("fix-a:failed", "fix-b:spoken")
	r.mu.Lock()
	reason := r.logs[0].Reason
	r.mu.Unlock()
	if reason != "engine refused" {
		t.Errorf("reason %q", reason)
	}
}

// Q11: a synth call longer than 3 seconds is cancelled and logged as a
// time-out.
func TestRuleQ11Timeout(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	c := r.syn.Next(t)
	r.clk.Advance(SynthTimeout - time.Millisecond)
	if c.Cancelled() {
		t.Fatal("cancelled before the time-out")
	}
	r.clk.Advance(time.Millisecond)
	r.waitIdle()
	if !c.Cancelled() {
		t.Fatal("not cancelled at the time-out")
	}
	r.wantResults("fix-a:failed")
	r.mu.Lock()
	reason := r.logs[0].Reason
	r.mu.Unlock()
	if !strings.Contains(reason, "timed out") {
		t.Errorf("reason %q", reason)
	}
}

// Q11: three failures in a row switch to the fallback; when there is none
// to switch to, synthesis pauses for 60 seconds and the status says why.
func TestRuleQ11Fallback(t *testing.T) {
	r := newRig(t)
	r.failover = []bool{true, false}
	fail := func(slug string) {
		r.narrate(KindWait, slug)
		r.clk.Advance(GatherDelay)
		r.syn.Next(t).Fail(errors.New("down"))
		r.waitIdle()
	}
	fail("fix-a")
	fail("fix-b")
	if r.failN != 0 {
		t.Fatal("failover after two failures")
	}
	fail("fix-c")
	if r.failN != 1 || r.q.Status().Error != "" {
		t.Fatalf("after 3: failover %d, error %q", r.failN, r.q.Status().Error)
	}
	// a success resets the count
	r.narrate(KindWait, "fix-d")
	r.clk.Advance(GatherDelay)
	r.say("d needs you")
	fail("fix-e")
	fail("fix-f")
	fail("fix-g")
	if r.failN != 2 {
		t.Fatalf("failover %d, want 2", r.failN)
	}
	st := r.q.Status()
	if !strings.Contains(st.Error, "paused") {
		t.Fatalf("status error %q", st.Error)
	}
	// paused: nothing is synthesized
	r.narrate(KindDone, "fix-h")
	r.clk.Advance(GatherDelay)
	r.noSynthYet()
	r.clk.Advance(PauseAfterFail)
	if e := r.q.Status().Error; e != "" {
		t.Fatalf("error %q after the pause", e)
	}
	r.narrate(KindDone, "fix-i")
	r.clk.Advance(GatherDelay)
	r.say("i is done")
	got := r.results()
	if got[len(got)-2] != "fix-h:failed" {
		t.Errorf("log %q: fix-h not failed during the pause", got)
	}
}

// Rule 7 in the queue: the mode is decided when an item is pushed and
// again before it plays.
func TestRule7QueueModes(t *testing.T) {
	r := newRig(t)
	r.setMode("fix-m", ModeMute)
	if r.narrate(KindWait, "fix-m") {
		t.Error("a muted session's narrate was taken")
	}
	if r.read("fix-n", "t", "m", "hi.") {
		t.Error("a read was taken in narrate mode")
	}
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	pa := r.play("a needs you")
	r.narrate(KindDone, "fix-b")
	r.clk.Advance(GatherDelay)
	r.synthOK("b is done")
	r.waitIdle()
	r.setMode("fix-b", ModeMute) // muted after it was pushed
	pa.Finish()
	r.waitSilent()
	r.wantResults("fix-m:muted", "fix-a:spoken", "fix-b:muted")
}

// pekoe voice test: plays even when muted, ahead of the queue, with the
// voice and language asked for.
func TestQueueTestItem(t *testing.T) {
	r := newRig(t)
	r.setMode("", ModeMute)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	pa := r.play("a needs you")
	r.narrate(KindDone, "fix-b")
	r.clk.Advance(GatherDelay)
	r.synthOK("b is done")
	r.waitIdle()
	r.q.Push(Item{Kind: KindTest, Text: "テスト", Voice: VoiceReader, Lang: LangJa})
	c := r.syn.Next(t)
	if c.Text != "テスト" || c.Voice != VoiceReader || c.Lang != LangJa {
		t.Fatalf("test synth %+v", c)
	}
	c.OK("/data/voice/テスト.wav")
	r.waitIdle()
	pa.Finish()
	r.play("テスト").Finish()
	r.synthOK("b is done")
	r.play("b is done").Finish()
	r.waitSilent()
}

// 14.4: the log keeps the latency, the backend and only 80 runes of a read
// sentence; narrate goes to the tapi voice, read to the reader voice.
func TestQueueLogAndVoices(t *testing.T) {
	r := newRig(t)
	r.setMode("feat-r", ModeRead)
	long := strings.Repeat("あ", 100)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay + 200*time.Millisecond)
	c := r.syn.Next(t)
	if c.Voice != VoiceTapi || c.Lang != LangEn {
		t.Fatalf("narrate synth voice %q lang %q", c.Voice, c.Lang)
	}
	r.clk.Advance(100 * time.Millisecond)
	c.OK("/data/voice/a needs you.wav")
	r.play("a needs you").Finish()
	r.waitSilent()
	r.read("feat-r", "t", "m", long)
	c = r.syn.Next(t)
	if c.Voice != VoiceReader || c.Text != long {
		t.Fatalf("read synth %+v", c)
	}
	c.OK("/data/voice/long.wav")
	r.pl.Next(t).Finish()
	r.waitSilent()
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.logs[0]; e.LatencyMS != 600 || e.Backend != "fake" || e.Text != "a needs you" || e.Kind != KindWait {
		t.Errorf("narrate log %+v", e)
	}
	if e := r.logs[1]; e.Text != strings.Repeat("あ", LogReadRunes) {
		t.Errorf("read log text %d runes", len([]rune(e.Text)))
	}
}

// Close empties the queue and takes nothing more.
func TestQueueClose(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.q.Close()
	if r.narrate(KindWait, "fix-b") {
		t.Error("taken after Close")
	}
	r.clk.Advance(time.Minute)
	r.noSynthYet()
	r.wantResults("fix-a:muted")
}

// Q11: Failover runs without the queue's lock (it may take seconds); the
// queue answers meanwhile and starts no synth until it returns.
func TestRuleQ11FailoverUnlocked(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan bool)
	var q *Queue
	r := newRig(t, func(c *Config) {
		c.Failover = func() bool {
			_ = q.Status() // would deadlock under the lock
			close(entered)
			return <-release
		}
	})
	q = r.q
	for _, slug := range []string{"fix-a", "fix-b", "fix-c"} {
		r.narrate(KindWait, slug)
		r.clk.Advance(GatherDelay)
		r.syn.Next(t).Fail(errors.New("down"))
		if slug != "fix-c" {
			r.waitIdle()
		}
	}
	<-entered
	if !r.narrate(KindDone, "fix-d") {
		t.Fatal("push refused while failing over")
	}
	r.clk.Advance(GatherDelay)
	r.noSynthYet()
	release <- true
	r.say("d is done")
	if e := r.q.Status().Error; e != "" {
		t.Errorf("error %q after a failover", e)
	}
}

// The queue releases each wav it is done with: played, or synthesized and
// then dropped.
func TestQueueReleasesWav(t *testing.T) {
	r := newRig(t)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.synthOK("a needs you")
	pa := r.play("a needs you")
	r.narrate(KindDone, "fix-b")
	r.clk.Advance(GatherDelay)
	r.synthOK("b is done")
	r.waitIdle()
	r.setStale("fix-b") // its wav is dropped unplayed
	pa.Finish()
	r.waitSilent()
	r.waitFor("two wavs released", func(Status) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.released) == 2
	})
	r.mu.Lock()
	got := append([]string(nil), r.released...)
	r.mu.Unlock()
	sort.Strings(got)
	want := []string{"/data/voice/a needs you.wav", "/data/voice/b is done.wav"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("released %q, want %q", got, want)
	}
}

// fallbackRig is a rig whose backend in use is "vv" with the fallback
// "say" on a second fake synth (10.5): Fallback offers it for a failure of
// "vv" only. none: no fallback to offer at all.
func fallbackRig(t *testing.T, none bool) (*rig, *voicetest.Synth) {
	fb := voicetest.NewSynth()
	using := "vv"
	var mu sync.Mutex
	r := newRig(t, func(c *Config) {
		c.Backend = func() string {
			mu.Lock()
			defer mu.Unlock()
			return using
		}
		inner := c.Failover
		c.Failover = func() bool {
			ok := inner()
			if ok {
				mu.Lock()
				using = "say"
				mu.Unlock()
			}
			return ok
		}
		c.Fallback = func(err error) (string, string, Synth) {
			if none || strings.HasPrefix(err.Error(), "say:") {
				return "", "", nil
			}
			return "vv", "say", fb
		}
	})
	return r, fb
}

// Q11 and 10.5: a failed synth of the backend in use is tried once more
// with the fallback, and the same utterance sounds; the log says which
// backend made it and why. The failure still counts: the third in a row
// switches to the fallback as before.
func TestRuleQ11RetryWithFallback(t *testing.T) {
	r, fb := fallbackRig(t, false)
	r.failover = []bool{true}
	for i, slug := range []string{"fix-a", "fix-b", "fix-c"} {
		r.narrate(KindWait, slug)
		r.clk.Advance(GatherDelay)
		c := r.syn.Next(t)
		c.Fail(errors.New("vv: connection refused\ndetail"))
		f := fb.Next(t)
		if f.Text != c.Text || f.Voice != c.Voice || f.Lang != c.Lang {
			t.Fatalf("fallback synth %+v, want the same utterance %+v", f, c)
		}
		if i == 0 {
			if e := r.q.Status().Error; !strings.Contains(e, "connection refused") {
				t.Errorf("status error %q after the first failure", e)
			}
		}
		f.OK("/data/voice/" + f.Text + ".wav")
		p := r.play(f.Text)
		p.Finish()
		r.waitSilent()
		r.waitIdle()
	}
	r.wantResults("fix-a:spoken", "fix-b:spoken", "fix-c:spoken")
	r.mu.Lock()
	failN := r.failN
	e := r.logs[0]
	r.mu.Unlock()
	if failN != 1 {
		t.Fatalf("failover %d times, want 1", failN)
	}
	if e.Backend != "say" || e.Reason != "vv failed: vv: connection refused" {
		t.Errorf("log entry %+v", e)
	}
	if st := r.q.Status(); st.Error != "" {
		t.Errorf("status error %q after the switch", st.Error)
	}
	// switched: the backend in use (the fake's "say") failing is not tried
	// again, and a success of the backend in use is logged plainly
	r.narrate(KindDone, "fix-d")
	r.clk.Advance(GatherDelay)
	r.say("d is done")
	r.mu.Lock()
	e = r.logs[len(r.logs)-1]
	r.mu.Unlock()
	if e.Backend != "say" || e.Reason != "" {
		t.Errorf("log entry %+v", e)
	}
}

// Q11: the fallback failing too leaves the utterance failed with both
// reasons; the failure is counted once.
func TestRuleQ11RetryFails(t *testing.T) {
	r, fb := fallbackRig(t, false)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.syn.Next(t).Fail(errors.New("vv: down"))
	fb.Next(t).Fail(errors.New("say: no voice"))
	r.waitIdle()
	r.wantResults("fix-a:failed")
	r.mu.Lock()
	e := r.logs[0]
	r.mu.Unlock()
	if e.Reason != "vv failed: vv: down; say failed: say: no voice" {
		t.Errorf("reason %q", e.Reason)
	}
	// counted once: two more failures switch
	r.failover = []bool{true}
	for _, slug := range []string{"fix-b", "fix-c"} {
		r.narrate(KindWait, slug)
		r.clk.Advance(GatherDelay)
		r.syn.Next(t).Fail(errors.New("vv: down"))
		fb.Next(t).Fail(errors.New("say: no voice"))
		r.waitIdle()
	}
	r.mu.Lock()
	failN := r.failN
	r.mu.Unlock()
	if failN != 1 {
		t.Fatalf("failover %d times, want 1", failN)
	}
}

// Q11: a time-out of the backend in use is tried with the fallback too,
// with its own time-out.
func TestRuleQ11RetryAfterTimeout(t *testing.T) {
	r, fb := fallbackRig(t, false)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	c := r.syn.Next(t)
	r.clk.Advance(SynthTimeout)
	f := fb.Next(t)
	if !c.Cancelled() || f.Cancelled() {
		t.Fatal("the first call was not cancelled, or the retry was")
	}
	r.clk.Advance(SynthTimeout - time.Millisecond)
	if f.Cancelled() {
		t.Fatal("the retry got less than its own time-out")
	}
	f.OK("/data/voice/a needs you.wav")
	r.play("a needs you").Finish()
	r.waitSilent()
	r.wantResults("fix-a:spoken")
}

// Q11: with no fallback to offer (none configured, or the failure was the
// fallback's own), the utterance fails as before.
func TestRuleQ11NoRetry(t *testing.T) {
	r, fb := fallbackRig(t, true)
	r.narrate(KindWait, "fix-a")
	r.clk.Advance(GatherDelay)
	r.syn.Next(t).Fail(errors.New("vv: down"))
	r.waitIdle()
	r.wantResults("fix-a:failed")
	if n := fb.Pending(); n != 0 {
		t.Fatalf("the fallback was called %d times", n)
	}
	r.mu.Lock()
	reason := r.logs[0].Reason
	r.mu.Unlock()
	if reason != "vv: down" {
		t.Errorf("reason %q", reason)
	}
}
