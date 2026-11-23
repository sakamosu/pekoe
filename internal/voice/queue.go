package voice

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The queue's timings (8).
const (
	GatherDelay    = 300 * time.Millisecond // Q3
	MaxAge         = 30 * time.Second       // Q6
	RepeatGap      = 10 * time.Second       // Q7
	SynthTimeout   = 3 * time.Second        // Q11
	FailLimit      = 3                      // Q11: failures in a row before the fallback
	PauseAfterFail = 60 * time.Second       // Q11: no synth after the fallback failed too
	MaxSynth       = 2                      // Q10: synth calls at once
	// DefaultMaxSentences is [voice.read] max_sentences when unset (6.2).
	DefaultMaxSentences = 5
	// LogReadRunes is how much of a read sentence the log keeps (14.4).
	LogReadRunes = 80
)

// Result is what became of a pushed item (14.4).
type Result string

const (
	ResultSpoken      Result = "spoken"
	ResultMerged      Result = "merged"
	ResultStale       Result = "stale"
	ResultOld         Result = "old"
	ResultMuted       Result = "muted"
	ResultInterrupted Result = "interrupted"
	ResultFailed      Result = "failed"
)

// LogEntry is one line of voice.log: one pushed item and what became of
// it (14.4).
type LogEntry struct {
	Time   time.Time `json:"time"`
	Slug   string    `json:"slug,omitempty"`
	Kind   Kind      `json:"kind"`
	Text   string    `json:"text"`
	Result Result    `json:"result"`
	Reason string    `json:"reason,omitempty"`
	// LatencyMS is from the push to the start of the sound (spoken and
	// merged): "within 1 second of the state change" is checked with it.
	LatencyMS int64  `json:"latency_ms"`
	Backend   string `json:"backend,omitempty"`
}

// Speaking is the utterance that sounds now.
type Speaking struct {
	Slug string `json:"slug"`
	Kind Kind   `json:"kind"`
	Text string `json:"text"`
}

// Status is the queue's part of VoiceStatus (13).
type Status struct {
	Queue    int       `json:"queue"`
	Speaking *Speaking `json:"speaking"`
	// Error is why nothing is synthesized (Q11), "" when all is well.
	Error       string    `json:"error"`
	PausedUntil time.Time `json:"-"`
	// Synthesizing counts the synth calls running (abandoned ones too).
	Synthesizing int `json:"-"`
}

// Config is what a Queue runs on. Clock, Synth and Player are required.
type Config struct {
	Clock   Clock
	Synth   Synth
	Player  Player
	Phrases Phrases
	// Mode is the session's mode now (Policy.ModeFor); nil is narrate.
	Mode func(slug string) Mode
	// Fresh: a narrate item still holds (Tracker.Fresh, Q5); nil is yes.
	Fresh func(Item) bool
	// Failover switches to the fallback backend after FailLimit synth
	// failures in a row and reports whether it did; false (or nil) pauses
	// synthesis for PauseAfterFail (Q11, 10.5).
	Failover func() bool
	// Fallback is asked after a synth failed with err (10.5): when the
	// backend that failed is not the fallback, it returns that backend's
	// name, the fallback's name and a Synth with the fallback, and the
	// utterance is synthesized once more with it. s nil (no fallback, or
	// the failure was the fallback's own) drops the utterance as before.
	// Called without the lock; it must return quickly (no exec).
	Fallback func(err error) (failed, name string, s Synth)
	// Release is told when the queue is done with a wav Synth returned
	// (played, or dropped unplayed), on its own goroutine; nil is nothing.
	Release func(wav string)
	// Backend is the name of the backend in use, for the log.
	Backend func() string
	// MaxSentences is how many sentences of one message are read (0 is
	// DefaultMaxSentences).
	MaxSentences int
	// Mode, Fresh, Backend, Log and OnUpdate are called with the queue's
	// lock held (Log and OnUpdate in order); they must return quickly and
	// must not call the Queue. Failover is called without the lock.
	Log      func(LogEntry)
	OnUpdate func(Status)
}

type qitem struct {
	Item
	id     uint64
	pushed time.Time
}

type uttState int

const (
	uttNew   uttState = iota // not synthesized
	uttSynth                 // synth running
	uttReady                 // wav ready
)

// utt is one utterance: one or more items said as one sentence.
type utt struct {
	items []*qitem
	kind  Kind
	text  string
	voice string
	lang  string

	state    uttState
	wav      string
	gen      uint64 // the synth or play call that belongs to it
	cancel   context.CancelFunc
	timedOut bool // the synth call hit SynthTimeout
	stopping bool // the player was told to stop
	// retry is why the backend in use failed, while (and after) the
	// fallback via synthesizes the utterance once more (10.5)
	retry, via string
}

func (u *utt) slug() string { return u.items[0].Slug }

// Queue is the speech queue (8): one utterance sounds at a time, narrate
// before read, same narrate kinds gathered into one sentence, stale and old
// items dropped, the next utterance synthesized while one plays.
type Queue struct {
	cfg Config

	mu       sync.Mutex
	closed   bool
	seq      uint64
	pending  []*qitem
	next     *utt
	playing  *utt
	inflight int
	gen      uint64

	spoken  map[string]time.Time // slug|kind → last spoken (Q7)
	turn    map[string]string    // slug → current read turn (Q9)
	message map[string]string    // slug → current read message
	nsent   map[string]int       // slug → sentences pushed of that message

	fails       int
	failingOver bool // Failover runs (without the lock); no synth starts
	pausedUntil time.Time
	errText     string

	timer    Timer
	timerGen uint64
	timerAt  time.Time
}

// NewQueue makes a queue.
func NewQueue(cfg Config) *Queue {
	if cfg.MaxSentences <= 0 {
		cfg.MaxSentences = DefaultMaxSentences
	}
	if cfg.Phrases.m == nil {
		cfg.Phrases = NewPhrases(cfg.Phrases.lang, nil)
	}
	return &Queue{
		cfg:     cfg,
		spoken:  map[string]time.Time{},
		turn:    map[string]string{},
		message: map[string]string{},
		nsent:   map[string]int{},
	}
}

func (q *Queue) mode(slug string) Mode {
	if q.cfg.Mode == nil {
		return ModeNarrate
	}
	return q.cfg.Mode(slug)
}

func (q *Queue) fresh(it *qitem) bool {
	if !it.Kind.Narrate() || q.cfg.Fresh == nil {
		return true
	}
	return q.cfg.Fresh(it.Item)
}

func class(k Kind) int {
	switch {
	case k == KindTest:
		return 3
	case k.Narrate():
		return 2
	}
	return 1
}

// Push puts an item on the queue and reports whether it was taken. An
// item the session's mode does not allow is dropped (logged as muted,
// except read, 6.1); a narrate the session said less than RepeatGap ago is
// not taken (Q7); a read past max_sentences of its message is not taken,
// and a read of a new turn drops the session's reads of the old one (Q9).
func (q *Queue) Push(it Item) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	now := q.cfg.Clock.Now()
	qi := &qitem{Item: it, pushed: now}
	if !q.mode(it.Slug).Allows(it.Kind) {
		if it.Kind != KindRead {
			q.logLocked(qi, "", ResultMuted, "mode "+string(q.mode(it.Slug)), now)
		}
		return false
	}
	if it.Kind.Narrate() {
		for k, t := range q.spoken {
			if now.Sub(t) >= RepeatGap {
				delete(q.spoken, k)
			}
		}
		if _, ok := q.spoken[it.Slug+"|"+string(it.Kind)]; ok {
			return false
		}
	}
	if it.Kind == KindRead {
		if t, ok := q.turn[it.Slug]; !ok || t != it.Turn {
			q.turn[it.Slug] = it.Turn
			q.dropLocked(func(x *qitem) bool { return x.Kind == KindRead && x.Slug == it.Slug }, ResultInterrupted, "new turn", now)
		}
		if q.message[it.Slug] != it.Message {
			q.message[it.Slug] = it.Message
			q.nsent[it.Slug] = 0
		}
		if q.nsent[it.Slug] >= q.cfg.MaxSentences {
			return false
		}
		q.nsent[it.Slug]++
	}
	q.seq++
	qi.id = q.seq
	q.pending = append(q.pending, qi)
	q.pumpLocked()
	q.updateLocked()
	return true
}

// Mute stops the sound and empties the queue (Q8). The caller mutes the
// Policy too, so that nothing new is taken.
func (q *Queue) Mute() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopPlayingLocked(func(*utt) bool { return true })
	q.dropLocked(func(*qitem) bool { return true }, ResultMuted, "muted", q.cfg.Clock.Now())
	q.updateLocked()
}

// Stop stops the sound now and keeps the queue (voice.stop, Q8).
func (q *Queue) Stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopPlayingLocked(func(*utt) bool { return true })
	q.updateLocked()
}

// UserPrompt: the person typed into the session slug. Its read stops and
// leaves the queue; its narrate stays (Q8).
func (q *Queue) UserPrompt(slug string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	isRead := func(k Kind, s string) bool { return k == KindRead && s == slug }
	q.stopPlayingLocked(func(u *utt) bool { return isRead(u.kind, u.slug()) })
	q.dropLocked(func(x *qitem) bool { return isRead(x.Kind, x.Slug) }, ResultInterrupted, "user prompt", q.cfg.Clock.Now())
	q.pumpLocked()
	q.updateLocked()
}

// FocusMoved: the focus went to another session. All read stops and
// leaves the queue; narrate stays (Q8, 6.3).
func (q *Queue) FocusMoved() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopPlayingLocked(func(u *utt) bool { return u.kind == KindRead })
	q.dropLocked(func(x *qitem) bool { return x.Kind == KindRead }, ResultInterrupted, "focus moved", q.cfg.Clock.Now())
	q.pumpLocked()
	q.updateLocked()
}

// Removed drops the items of a removed session (session.removed, 4.2).
func (q *Queue) Removed(slug string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dropLocked(func(x *qitem) bool { return x.Slug == slug && slug != "" }, ResultStale, "session removed", q.cfg.Clock.Now())
	delete(q.turn, slug)
	delete(q.message, slug)
	delete(q.nsent, slug)
	q.pumpLocked()
	q.updateLocked()
}

// Close stops the sound and empties the queue for good (voice turned
// off); later calls do nothing.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.stopPlayingLocked(func(*utt) bool { return true })
	q.dropLocked(func(*qitem) bool { return true }, ResultMuted, "voice off", q.cfg.Clock.Now())
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.closed = true
	q.updateLocked()
}

// Status is the queue's state now.
func (q *Queue) Status() Status {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.statusLocked()
}

func (q *Queue) statusLocked() Status {
	st := Status{Queue: len(q.pending), Error: q.errText, PausedUntil: q.pausedUntil, Synthesizing: q.inflight}
	if q.next != nil {
		st.Queue += len(q.next.items)
	}
	if p := q.playing; p != nil && !p.stopping {
		st.Speaking = &Speaking{Slug: p.slug(), Kind: p.kind, Text: p.text}
	}
	return st
}

func (q *Queue) updateLocked() {
	if q.cfg.OnUpdate != nil {
		q.cfg.OnUpdate(q.statusLocked())
	}
}

// logLocked logs one item; text "" is the item's own sentence.
func (q *Queue) logLocked(it *qitem, text string, r Result, reason string, now time.Time) {
	q.logAsLocked(it, text, r, reason, "", now)
}

// logAsLocked is logLocked naming the backend ("" is the one in use).
func (q *Queue) logAsLocked(it *qitem, text string, r Result, reason, backend string, now time.Time) {
	if q.cfg.Log == nil {
		return
	}
	if text == "" {
		text = q.itemText(it)
	}
	if it.Kind == KindRead {
		text = clipRunes(text, LogReadRunes)
	}
	e := LogEntry{Time: now, Slug: it.Slug, Kind: it.Kind, Text: text, Result: r, Reason: reason}
	if r == ResultSpoken || r == ResultMerged {
		e.LatencyMS = now.Sub(it.pushed).Milliseconds()
	}
	if e.Backend = backend; e.Backend == "" && q.cfg.Backend != nil {
		e.Backend = q.cfg.Backend()
	}
	q.cfg.Log(e)
}

func (q *Queue) itemText(it *qitem) string {
	if it.Kind.Narrate() {
		return q.cfg.Phrases.Render(it.Kind, []Named{{it.Slug, it.Repo}})
	}
	return it.Text
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// firstLine is the reason a synth error leaves in the log.
func firstLine(err error) string {
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return clipRunes(s, 200)
}

// dropLocked removes the matching items from the queue and from the next
// utterance (not the one playing) and logs them with r.
func (q *Queue) dropLocked(match func(*qitem) bool, r Result, reason string, now time.Time) {
	var keep []*qitem
	for _, x := range q.pending {
		if match(x) {
			q.logLocked(x, "", r, reason, now)
			continue
		}
		keep = append(keep, x)
	}
	q.pending = keep
	if u := q.next; u != nil {
		var left []*qitem
		for _, x := range u.items {
			if match(x) {
				q.logLocked(x, "", r, reason, now)
				continue
			}
			left = append(left, x)
		}
		if len(left) != len(u.items) {
			q.abandonLocked(u)
			if len(left) == 0 {
				q.next = nil
			} else {
				u.items = left
			}
		}
	}
}

// abandonLocked forgets the utterance's synth call and wav.
func (q *Queue) abandonLocked(u *utt) {
	if u.cancel != nil {
		u.cancel()
		u.cancel = nil
	}
	u.gen = 0
	u.state = uttNew
	q.releaseLocked(u.wav)
	u.wav = ""
	u.timedOut = false
	u.retry, u.via = "", ""
}

func (q *Queue) stopPlayingLocked(match func(*utt) bool) {
	if p := q.playing; p != nil && !p.stopping && match(p) {
		p.stopping = true
		if p.cancel != nil {
			p.cancel()
		}
	}
}

// pumpLocked moves the queue on: drops old and stale items, picks the next
// utterance, synthesizes it and plays it when nothing plays.
func (q *Queue) pumpLocked() {
	if q.closed {
		return
	}
	if !q.pausedUntil.IsZero() && !q.cfg.Clock.Now().Before(q.pausedUntil) {
		q.pausedUntil, q.errText = time.Time{}, "" // Q11: try again
	}
	now := q.cfg.Clock.Now()
	// Each round settles one step: dropping one utterance's worth of items
	// takes a round, so allow one per pending item and a few more.
	for range len(q.pending) + 8 {
		now = q.cfg.Clock.Now()
		q.expireLocked(now)
		if u := q.next; u != nil {
			if !q.refreshLocked(u, now) {
				q.next = nil
			}
		}
		// Q2: something better than the planned next came up: put it back.
		if u := q.next; u != nil && q.bestReadyLocked(now) > class(u.kind) {
			q.abandonLocked(u)
			q.pending = append(q.pending, u.items...)
			sort.Slice(q.pending, func(i, j int) bool { return q.pending[i].id < q.pending[j].id })
			q.next = nil
		}
		if q.next == nil {
			q.next = q.selectLocked(now)
			if q.next == nil {
				break
			}
			continue // refresh it before anything else
		}
		u := q.next
		if u.state == uttNew {
			if now.Before(q.pausedUntil) {
				// Q11: no synth for a while; nothing can be said till then.
				const why = "synth paused after repeated failures"
				for _, x := range u.items {
					q.logLocked(x, u.text, ResultFailed, why, now)
				}
				q.next = nil
				q.dropLocked(func(*qitem) bool { return true }, ResultFailed, why, now)
				break
			}
			if q.inflight >= MaxSynth || q.failingOver {
				break
			}
			q.synthLocked(u, q.cfg.Synth)
			break
		}
		if u.state == uttReady && q.playing == nil && !q.holdLocked(u, now) {
			q.playLocked(u, now)
			continue // plan and synthesize the one after it (Q10)
		}
		break
	}
	// Arm from the time this round looked at: a deadline that passed since
	// (the clock moved on meanwhile) still gets a timer, at once.
	q.armLocked(now)
}

// expireLocked drops narrate items older than MaxAge (Q6).
func (q *Queue) expireLocked(now time.Time) {
	q.dropLocked(func(x *qitem) bool {
		return x.Kind.Narrate() && now.Sub(x.pushed) >= MaxAge
	}, ResultOld, "", now)
}

// refreshLocked checks the planned utterance again (Q5, mode, Q4) and
// reports whether anything is left of it. When its sentence changed, the
// old synth call is dropped.
func (q *Queue) refreshLocked(u *utt, now time.Time) bool {
	if u.kind == KindTest {
		return true
	}
	items := u.items
	if u.kind.Narrate() {
		// Q4: everything of the same kind on the queue joins it.
		var keep []*qitem
		for _, x := range q.pending {
			if x.Kind == u.kind {
				items = append(items, x)
				continue
			}
			keep = append(keep, x)
		}
		q.pending = keep
	}
	var left []*qitem
	for _, x := range items {
		switch m := q.mode(x.Slug); {
		case !m.Allows(x.Kind):
			q.logLocked(x, "", ResultMuted, "mode "+string(m), now)
		case !q.fresh(x):
			q.logLocked(x, "", ResultStale, "", now)
		default:
			left = append(left, x)
		}
	}
	if len(left) == 0 {
		q.abandonLocked(u)
		return false
	}
	u.items = left
	text := q.uttText(u)
	if text != u.text {
		q.abandonLocked(u)
		u.text = text
	}
	return true
}

func (q *Queue) uttText(u *utt) string {
	if !u.kind.Narrate() {
		return u.items[0].Text
	}
	ns := make([]Named, 0, len(u.items))
	for _, x := range u.items {
		ns = append(ns, Named{x.Slug, x.Repo})
	}
	return q.cfg.Phrases.Render(u.kind, ns)
}

// bestReadyLocked is the class of the best pending item that could be
// said now: test, narrate past its gather delay, read; 0 for none.
func (q *Queue) bestReadyLocked(now time.Time) int {
	best := 0
	for _, x := range q.pending {
		c := class(x.Kind)
		if x.Kind.Narrate() && now.Sub(x.pushed) < GatherDelay {
			continue
		}
		if c > best {
			best = c
		}
	}
	return best
}

// holdLocked: a read waits while a narrate gathers (Q2, Q3), so that the
// narrate is not held up by a whole read sentence.
func (q *Queue) holdLocked(u *utt, now time.Time) bool {
	if u.kind != KindRead {
		return false
	}
	for _, x := range q.pending {
		if x.Kind.Narrate() || x.Kind == KindTest {
			return true
		}
	}
	return false
}

// selectLocked takes the next utterance off the pending items (Q2-Q4):
// a test first, then the narrate kind of the oldest narrate past its
// gather delay with every pending narrate of that kind, then the oldest
// read. A read waits while a narrate gathers.
func (q *Queue) selectLocked(now time.Time) *utt {
	take := func(match func(*qitem) bool) []*qitem {
		var got, keep []*qitem
		for _, x := range q.pending {
			if match(x) {
				got = append(got, x)
				continue
			}
			keep = append(keep, x)
		}
		q.pending = keep
		return got
	}
	mk := func(kind Kind, items []*qitem) *utt {
		u := &utt{items: items, kind: kind, lang: q.cfg.Phrases.Lang()}
		switch {
		case kind == KindTest:
			u.voice = items[0].Voice
			if items[0].Lang != "" {
				u.lang = items[0].Lang
			}
		case kind == KindRead:
			u.voice = VoiceReader
		}
		if u.voice == "" {
			u.voice = VoiceTapi
		}
		u.text = q.uttText(u)
		return u
	}
	for _, x := range q.pending {
		if x.Kind == KindTest {
			first := x
			return mk(KindTest, take(func(y *qitem) bool { return y == first }))
		}
	}
	gathering := false
	for _, x := range q.pending {
		if !x.Kind.Narrate() {
			continue
		}
		if now.Sub(x.pushed) < GatherDelay {
			gathering = true
			continue
		}
		k := x.Kind
		return mk(k, take(func(y *qitem) bool { return y.Kind == k }))
	}
	if gathering {
		return nil
	}
	for _, x := range q.pending {
		if x.Kind == KindRead {
			first := x
			return mk(KindRead, take(func(y *qitem) bool { return y == first }))
		}
	}
	return nil
}

func (q *Queue) synthLocked(u *utt, s Synth) {
	q.gen++
	gen := q.gen
	ctx, cancel := context.WithCancel(context.Background())
	u.gen, u.cancel, u.state, u.timedOut = gen, cancel, uttSynth, false
	q.inflight++
	timeout := q.cfg.Clock.AfterFunc(SynthTimeout, func() {
		q.mu.Lock()
		if u.gen == gen && u.state == uttSynth {
			u.timedOut = true
			cancel()
		}
		q.mu.Unlock()
	})
	text, voice, lang := u.text, u.voice, u.lang
	go func() {
		wav, err := s.Synth(ctx, text, voice, lang)
		timeout.Stop()
		q.synthDone(u, gen, wav, err)
	}()
}

func (q *Queue) synthDone(u *utt, gen uint64, wav string, err error) {
	// what the fallback could do about a failure (10.5): asked before the
	// lock, so that the queue never waits on the backend side
	var failed, fbName string
	var fb Synth
	if err != nil && q.cfg.Fallback != nil {
		failed, fbName, fb = q.cfg.Fallback(err)
	}
	q.mu.Lock()
	q.inflight--
	if q.closed || u.gen != gen || q.next != u {
		// abandoned: its sentence changed, it left the queue, or it was put
		// back for something better
		if err == nil {
			q.releaseLocked(wav)
		}
		q.pumpLocked()
		q.updateLocked()
		q.mu.Unlock()
		return
	}
	now := q.cfg.Clock.Now()
	if err == nil {
		if u.retry == "" {
			// the backend in use works (the fallback's success on a retry
			// says nothing about it)
			q.fails = 0
			q.errText = ""
		}
		u.cancel = nil
		u.state, u.wav = uttReady, wav
		q.pumpLocked()
		q.updateLocked()
		q.mu.Unlock()
		return
	}
	reason := firstLine(err)
	if u.timedOut {
		reason = fmt.Sprintf("synth timed out after %s", SynthTimeout)
	}
	if u.cancel != nil {
		u.cancel()
		u.cancel = nil
	}
	if u.retry != "" {
		// the fallback failed too: the utterance is lost, counted once
		why := u.retry + "; " + u.via + " failed: " + reason
		for _, x := range u.items {
			q.logAsLocked(x, u.text, ResultFailed, why, u.via, now)
		}
		q.next = nil
		q.pumpLocked()
		q.updateLocked()
		q.mu.Unlock()
		return
	}
	if failed != "" {
		reason = failed + " failed: " + reason
	}
	q.fails++
	if q.fails < FailLimit {
		// shown before the switch too: the backend in use is failing
		q.errText = fmt.Sprintf("synth failed (%d in a row): %s", q.fails, reason)
		q.retryLocked(u, reason, fbName, fb, now)
		q.mu.Unlock()
		return
	}
	q.fails = 0
	// Failover may take seconds (it checks the fallback backend): call it
	// without the lock, starting no synth meanwhile. The utterance stays
	// planned, to be tried with the fallback after.
	q.failingOver = true
	q.updateLocked()
	q.mu.Unlock()
	switched := q.cfg.Failover != nil && q.cfg.Failover()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failingOver = false
	now = q.cfg.Clock.Now()
	if switched {
		q.errText = ""
	} else {
		q.pausedUntil = now.Add(PauseAfterFail)
		q.errText = fmt.Sprintf("synth failed %d times in a row (%s); paused until %s", FailLimit, reason, q.pausedUntil.Format(time.RFC3339))
		fb = nil // the fallback cannot be used either (or there is none)
	}
	if q.closed || u.gen != gen || q.next != u {
		// it changed or left the queue meanwhile: what is left of it is
		// planned again (the pause drops it)
		q.pumpLocked()
		q.updateLocked()
		return
	}
	q.retryLocked(u, reason, fbName, fb, now)
}

// retryLocked synthesizes the failed utterance once more with the fallback
// fb (10.5); with none, it is dropped as failed.
func (q *Queue) retryLocked(u *utt, reason, fbName string, fb Synth, now time.Time) {
	if fb == nil {
		for _, x := range u.items {
			q.logLocked(x, u.text, ResultFailed, reason, now)
		}
		q.next = nil
		q.pumpLocked()
		q.updateLocked()
		return
	}
	u.retry, u.via = reason, fbName
	q.synthLocked(u, fb)
	q.updateLocked()
}

// releaseLocked tells the owner of a wav that the queue is done with it.
func (q *Queue) releaseLocked(wav string) {
	if wav != "" && q.cfg.Release != nil {
		go q.cfg.Release(wav)
	}
}

func (q *Queue) playLocked(u *utt, now time.Time) {
	q.next = nil
	q.playing = u
	for i, x := range u.items {
		r := ResultSpoken
		if i > 0 {
			r = ResultMerged
		}
		q.logAsLocked(x, u.text, r, u.retry, u.via, now)
		if x.Kind.Narrate() {
			q.spoken[x.Slug+"|"+string(x.Kind)] = now
		}
	}
	q.gen++
	gen := q.gen
	ctx, cancel := context.WithCancel(context.Background())
	u.gen, u.cancel = gen, cancel
	wav := u.wav
	go func() {
		_ = q.cfg.Player.Play(ctx, wav)
		q.playDone(u, gen)
	}()
}

func (q *Queue) playDone(u *utt, gen uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.playing != u || u.gen != gen {
		return
	}
	if u.cancel != nil {
		u.cancel()
	}
	q.playing = nil
	q.releaseLocked(u.wav)
	q.pumpLocked()
	q.updateLocked()
}

// armLocked sets the one timer to the next moment the queue must look
// again: a gather delay ending (Q3), an item getting old (Q6), the synth
// pause ending (Q11).
func (q *Queue) armLocked(now time.Time) {
	var at time.Time
	consider := func(t time.Time) {
		if t.After(now) && (at.IsZero() || t.Before(at)) {
			at = t
		}
	}
	for _, x := range q.pending {
		if x.Kind.Narrate() {
			consider(x.pushed.Add(GatherDelay))
			consider(x.pushed.Add(MaxAge))
		}
	}
	if u := q.next; u != nil {
		for _, x := range u.items {
			if x.Kind.Narrate() {
				consider(x.pushed.Add(MaxAge))
			}
		}
	}
	consider(q.pausedUntil)
	if at.Equal(q.timerAt) && q.timer != nil {
		return
	}
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.timerAt = at
	if at.IsZero() {
		return
	}
	q.timerGen++
	gen := q.timerGen
	q.timer = q.cfg.Clock.AfterFunc(max(0, at.Sub(q.cfg.Clock.Now())), func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.timerGen != gen {
			return
		}
		q.timer, q.timerAt = nil, time.Time{}
		q.pumpLocked()
		q.updateLocked()
	})
}
