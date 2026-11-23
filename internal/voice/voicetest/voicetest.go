// Package voicetest has fakes of voice's clock, synthesizer and player for
// tests: time moves only on Advance, a synth call and a play call each wait
// for the test to finish them. Nothing here makes a sound, runs a program
// or touches a file.
package voicetest

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

// Wait is how long Next waits for a call before failing the test.
const Wait = 5 * time.Second

// ---- clock ----

// Clock is a fake voice.Clock: Now stays put until Advance, which runs the
// timers that come due, in order, on the caller's goroutine.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*timer
}

type timer struct {
	c    *Clock
	at   time.Time
	seq  int
	f    func()
	dead bool
}

// NewClock starts at a fixed time.
func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc returns a Timer (voice.Timer).
func (c *Clock) AfterFunc(d time.Duration, f func()) interface{ Stop() bool } {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &timer{c: c, at: c.now.Add(d), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *timer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.dead
	t.dead = true
	return was
}

// Advance moves time forward by d, running each timer due on the way at
// its own time.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var live []*timer
		for _, t := range c.timers {
			if !t.dead {
				live = append(live, t)
			}
		}
		c.timers = live
		sort.Slice(live, func(i, j int) bool {
			if !live[i].at.Equal(live[j].at) {
				return live[i].at.Before(live[j].at)
			}
			return live[i].seq < live[j].seq
		})
		if len(live) == 0 || live[0].at.After(end) {
			c.now = end
			c.mu.Unlock()
			return
		}
		t := live[0]
		t.dead = true
		if t.at.After(c.now) {
			c.now = t.at
		}
		c.mu.Unlock()
		t.f()
	}
}

// ---- synth ----

// SynthCall is one call of Synth waiting for the test.
type SynthCall struct {
	Text, Voice, Lang string
	ctx               context.Context
	reply             chan synthReply
}

type synthReply struct {
	wav string
	err error
}

// OK finishes the call with a wav path.
func (c *SynthCall) OK(wav string) { c.reply <- synthReply{wav: wav} }

// Fail finishes the call with an error.
func (c *SynthCall) Fail(err error) { c.reply <- synthReply{err: err} }

// Cancelled: the caller gave up on the call (its context ended).
func (c *SynthCall) Cancelled() bool { return c.ctx.Err() != nil }

// Synth is a fake voice.Synth. Each call shows up on Next and returns when
// the test finishes it, or with the context's error when it is cancelled.
type Synth struct {
	// Stubborn calls ignore cancellation and return only when the test
	// finishes them (like a backend slow to stop).
	Stubborn bool
	calls    chan *SynthCall
}

func NewSynth() *Synth { return &Synth{calls: make(chan *SynthCall, 64)} }

func (s *Synth) Synth(ctx context.Context, text, voice, lang string) (string, error) {
	c := &SynthCall{Text: text, Voice: voice, Lang: lang, ctx: ctx, reply: make(chan synthReply, 1)}
	s.calls <- c
	done := ctx.Done()
	if s.Stubborn {
		done = nil
	}
	select {
	case r := <-c.reply:
		return r.wav, r.err
	case <-done:
		return "", ctx.Err()
	}
}

// Next is the next call; the test fails if none comes within Wait.
func (s *Synth) Next(t testing.TB) *SynthCall {
	t.Helper()
	select {
	case c := <-s.calls:
		return c
	case <-time.After(Wait):
		t.Fatal("voicetest: no synth call")
		return nil
	}
}

// ---- player ----

// PlayCall is one call of Play: the sound plays until Finish or until the
// caller stops it.
type PlayCall struct {
	Wav     string
	ctx     context.Context
	finish  chan struct{}
	stopped chan struct{}
}

// Finish ends the sound.
func (c *PlayCall) Finish() { close(c.finish) }

// WaitStopped waits until the caller stops the sound; the test fails if it
// does not within Wait.
func (c *PlayCall) WaitStopped(t testing.TB) {
	t.Helper()
	select {
	case <-c.stopped:
	case <-time.After(Wait):
		t.Fatal("voicetest: play not stopped")
	}
}

// Player is a fake voice.Player. Each call shows up on Next.
type Player struct {
	calls chan *PlayCall
}

func NewPlayer() *Player { return &Player{calls: make(chan *PlayCall, 64)} }

func (p *Player) Play(ctx context.Context, wav string) error {
	c := &PlayCall{Wav: wav, ctx: ctx, finish: make(chan struct{}), stopped: make(chan struct{})}
	p.calls <- c
	select {
	case <-c.finish:
		return nil
	case <-ctx.Done():
		close(c.stopped)
		return ctx.Err()
	}
}

// Next is the next call; the test fails if none comes within Wait.
func (p *Player) Next(t testing.TB) *PlayCall {
	t.Helper()
	select {
	case c := <-p.calls:
		return c
	case <-time.After(Wait):
		t.Fatal("voicetest: no play call")
		return nil
	}
}
