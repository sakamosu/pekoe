package tui

// tapi reacting to PRs and CI (docs/mascot.md「PR と CI への反応」): a
// change in a session's PR summary becomes one toast. PR toasts rank below
// the wait / error toasts: they never cover one, step aside when one
// arrives, and wait in a short queue instead.

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/mascot"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

const (
	celebrateFor = 4 * time.Second  // happy faces go back to normal quickly
	runningFor   = 3 * time.Second  // CI started: a glance, not news
	prQueueFor   = 30 * time.Second // a queued reaction older than this is stale
	prQueueMax   = 3
	// prRepeatGap: the same reaction on the same PR state isn't repeated
	// within this window, so a summary flapping between two polls (60 s)
	// stays quiet. Real repeats (green → re-run → green) take longer.
	prRepeatGap = 90 * time.Second
	demoEvery   = 6 * time.Second
)

// sessionPR adapts a session's PR summary (Session.PRStatus, polled by the
// daemon) for tapi. The demo's fake summary wins while it runs.
func (m *model) sessionPR(s *state.Session) *mascot.PR {
	if p, ok := m.tapi.demoPR[s.Slug]; ok {
		return p
	}
	if p := s.PRStatus; p != nil {
		return &mascot.PR{Number: s.PR, State: p.State, Pass: p.Pass, Fail: p.Fail,
			Pending: p.Pending, ReviewDecision: p.ReviewDecision,
			Unresolved: p.Unresolved, LastThread: p.LastThread}
	}
	return nil
}

func sessionName(s *state.Session) string {
	if s.Branch != "" {
		return s.Branch
	}
	return s.Slug
}

type queuedReaction struct {
	r   mascot.Reaction
	at  time.Time
	dur time.Duration
}

// reactionFor: how long a PR reaction shows; toast is [notify] toast_for.
func reactionFor(e mascot.PREvent, toast time.Duration) time.Duration {
	switch {
	case e == mascot.PRCIRunning:
		return runningFor
	case e.Celebrates():
		return celebrateFor
	}
	return toast
}

// tapiOnPR compares the session's PR summary with the one tapi saw last.
// The first sighting only sets the baseline (startup, a new PR).
func (m *model) tapiOnPR(s *state.Session) {
	t := m.tapi
	next := m.sessionPR(s)
	if next == nil {
		return // no summary in this event: keep the baseline
	}
	prev, known := t.prs[s.Slug]
	c := *next
	t.prs[s.Slug] = &c
	if !known {
		return
	}
	r, ok := mascot.React(s.Slug, sessionName(s), prev, next)
	if !ok {
		return
	}
	now := time.Now()
	key := r.Key()
	if at, ok := t.prToasted[key]; ok && now.Sub(at) < prRepeatGap {
		return
	}
	t.prToasted[key] = now
	m.showReaction(r, now)
}

// showReaction puts a PR toast up, or queues it behind a wait / error
// toast or a more important PR toast.
func (m *model) showReaction(r mascot.Reaction, now time.Time) {
	t := m.tapi
	q := queuedReaction{r: r, at: now, dur: reactionFor(r.Event, m.g.Notify.Toast())}
	cur := t.toast
	switch {
	case cur == nil || now.After(cur.until):
		m.raise(q, now)
	case cur.react == nil: // wait / error: never covered
		m.queueReaction(q)
	case mascot.PRPriority(r.Event) <= mascot.PRPriority(cur.react.Event):
		m.queueReaction(queuedReaction{r: *cur.react, at: now, dur: cur.until.Sub(now)})
		m.raise(q, now)
	default:
		m.queueReaction(q)
	}
}

// raise puts q up as the toast, remembering the frame it came up on (the
// confetti counts from there).
func (m *model) raise(q queuedReaction, now time.Time) {
	m.tapi.toast = q.toast(now)
	m.tapi.toast.frame0 = m.tapi.frame
}

func (q queuedReaction) toast(now time.Time) *toast {
	r := q.r
	return &toast{slug: r.Slug, react: &r, text: r.Speech(), until: now.Add(q.dur)}
}

// queueReaction keeps one reaction per session (the more important one)
// and at most prQueueMax in all, dropping the least important.
func (m *model) queueReaction(q queuedReaction) {
	t := m.tapi
	for i, x := range t.prQueue {
		if x.r.Slug != q.r.Slug {
			continue
		}
		if mascot.PRPriority(q.r.Event) <= mascot.PRPriority(x.r.Event) {
			t.prQueue[i] = q
		}
		return
	}
	t.prQueue = append(t.prQueue, q)
	if len(t.prQueue) > prQueueMax {
		worst := 0
		for i, x := range t.prQueue {
			if mascot.PRPriority(x.r.Event) > mascot.PRPriority(t.prQueue[worst].r.Event) {
				worst = i
			}
		}
		t.prQueue = append(t.prQueue[:worst], t.prQueue[worst+1:]...)
	}
}

// tapiExpire runs on every tick: drops an expired toast and brings up the
// most important queued reaction that is still fresh.
func (m *model) tapiExpire() {
	t := m.tapi
	now := time.Now()
	if t.toast != nil && now.After(t.toast.until) {
		t.toast = nil
	}
	if t.toast != nil || len(t.prQueue) == 0 {
		return
	}
	best := -1
	fresh := t.prQueue[:0]
	for _, x := range t.prQueue {
		if now.Sub(x.at) < prQueueFor {
			fresh = append(fresh, x)
		}
	}
	t.prQueue = fresh
	for i, x := range t.prQueue {
		if best < 0 || mascot.PRPriority(x.r.Event) < mascot.PRPriority(t.prQueue[best].r.Event) {
			best = i
		}
	}
	if best < 0 {
		return
	}
	q := t.prQueue[best]
	t.prQueue = append(t.prQueue[:best], t.prQueue[best+1:]...)
	m.raise(q, now)
}

// cloudy lists open PRs with a failing check for the idle face.
func (m *model) cloudy() []string {
	var out []string
	for _, s := range m.sessions {
		if s.State == state.Landed {
			continue
		}
		if p := m.sessionPR(s); p.Red() {
			out = append(out, mascot.PRLabel(sessionName(s), p.Number))
		}
	}
	return out
}

// ---- drawing ----

// reactionInner is the toast's inside for a PR reaction: face + speech.
func (m *model) reactionInner(t *toast) string {
	r := m.tapi.r
	frame := 0
	if m.animating() {
		frame = m.tapi.frame / 2 // 800 ms: a fidget, not a flicker
	}
	ev := t.react.Event
	return " " + r.Line(r.ReactionEyes(ev, frame)) + "  " + r.PaintReaction(ev, t.text) + " "
}

// ---- confetti ----

// The confetti on a merged PR's toast (docs/mascot.md「PR と CI への反
// 応」): sparkles scattered unevenly on the top and bottom borders, each
// twinkling on its own beat. Where they sit, what they are and how they
// beat all come from the PR, so redrawing never moves them; only the frame
// count moves them along, and it stands still while the window is
// unfocused.
const (
	burstFrames = 2 // the first 800 ms: every sparkle lit, a rung above its usual
	fadeFrom    = 3 // a few go out for good from here on (the toast is 10 frames)
	fadeSpan    = 7
)

type spark struct {
	pos    int        // cell on the border run, never next to a corner
	ladder []string   // brightest → faintest
	off    int        // the rung it starts from: 0 is the big glyph, few get it; never the faintest
	gap    int        // dark frames before it flashes again
	phase  int        // where in its beat it starts
	role   theme.Role // secondary mostly, a little warn and primary
	fade   int        // frame it goes out for good; 0 keeps it to the end
}

// confettiSeed is the PR's own: the session and the PR number.
func confettiSeed(r *mascot.Reaction) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s#%d", r.Slug, r.PR.Number)
	return h.Sum64()
}

// pick draws from weighted choices.
func pick(rng *rand.Rand, weights ...int) int {
	sum := 0
	for _, w := range weights {
		sum += w
	}
	n := rng.IntN(sum)
	for i, w := range weights {
		if n < w {
			return i
		}
		n -= w
	}
	return len(weights) - 1
}

// confettiLayout scatters sparkles over a border run of n cells. Gaps are
// 2–6 cells (never touching, never a long bare stretch) and the cells next
// to the corners stay bare. edge tells the borders apart.
func confettiLayout(n int, seed, edge uint64, set [][]string) []spark {
	rng := rand.New(rand.NewPCG(seed, edge))
	var out []spark
	for p := 1 + rng.IntN(3); p <= n-2; p += 2 + rng.IntN(5) {
		ladder := set[pick(rng, 4, 3, 3)%len(set)]
		s := spark{pos: p, ladder: ladder,
			off:  min(pick(rng, 2, 4, 4), len(ladder)-2),
			gap:  1 + rng.IntN(3),
			role: []theme.Role{theme.Secondary, theme.Warn, theme.Primary}[pick(rng, 6, 2, 2)],
		}
		s.phase = rng.IntN(len(ladder) - s.off + s.gap)
		if rng.IntN(5) < 2 {
			s.fade = fadeFrom + rng.IntN(fadeSpan)
		}
		out = append(out, s)
	}
	return out
}

// confettiLayouts gives the top and bottom borders their own scatter.
func confettiLayouts(n int, seed uint64, set [][]string) (top, bottom []spark) {
	top = confettiLayout(n, seed, 1, set)
	for edge := uint64(2); edge < 10; edge++ {
		bottom = confettiLayout(n, seed, edge, set)
		if !samePlaces(top, bottom) {
			break
		}
	}
	return top, bottom
}

func samePlaces(a, b []spark) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].pos != b[i].pos {
			return false
		}
	}
	return true
}

// glyph is the sparkle e frames after the toast came up; "" is dark.
// It bursts a rung above where it usually starts, then steps down its
// ladder, goes dark for gap frames and flashes back.
func (s spark) glyph(e int) string {
	if s.fade > 0 && e >= s.fade {
		return ""
	}
	if e < burstFrames {
		return s.ladder[max(s.off-1, 0)]
	}
	lit := len(s.ladder) - s.off
	k := (e + s.phase) % (lit + s.gap)
	if k >= lit {
		return ""
	}
	return s.ladder[s.off+k]
}

// confettiBorders are the top and bottom border runs of n cells with the
// confetti on them: a merged PR's toast only, and only with animations on.
func (m *model) confettiBorders(n int, t *toast) (top, bottom string) {
	set := theme.Confetti(m.ic)
	if m.tapi.r.ASCII() {
		set = theme.Confetti(theme.Icons("ascii"))
	}
	e := max(m.tapi.frame-t.frame0, 0)
	up, down := confettiLayouts(n, confettiSeed(t.react), set)
	return m.confettiRun(n, up, e), m.confettiRun(n, down, e)
}

func (m *model) confettiRun(n int, sparks []spark, e int) string {
	var b strings.Builder
	run := 0
	flush := func() {
		if run > 0 {
			b.WriteString(m.th.FG(theme.Border, strings.Repeat("─", run)))
			run = 0
		}
	}
	at := 0
	for _, s := range sparks {
		g := s.glyph(e)
		if g == "" {
			continue
		}
		run += s.pos - at
		flush()
		b.WriteString(m.th.FG(s.role, g))
		at = s.pos + 1
	}
	run += n - at
	flush()
	return b.String()
}

// ---- demo ----

// PEKOE_TAPI_DEMO=pr plays a scripted PR on the selected session (or a
// stand-in) every few seconds, through the same path as a daemon update:
// each face once, a repeat that stays silent, two changes at once, and
// the merge. For checking the faces by eye; nothing reaches the daemon.

type tapiDemoMsg struct{}

func demoTick() tea.Cmd {
	return tea.Tick(demoEvery, func(time.Time) tea.Msg { return tapiDemoMsg{} })
}

func demoScript(now time.Time) []mascot.PR {
	open := func(p mascot.PR) mascot.PR { p.Number, p.State = 42, "OPEN"; return p }
	th := now.Add(-time.Minute)
	return []mascot.PR{
		open(mascot.PR{Pending: 3}),                          // baseline: silent
		open(mascot.PR{Pass: 3}),                             // ci_passed
		open(mascot.PR{Pass: 3, ReviewDecision: "APPROVED"}), // approved
		open(mascot.PR{Pass: 3, ReviewDecision: "APPROVED", Unresolved: 1, LastThread: th}),                   // new_comment
		open(mascot.PR{Pass: 3, ReviewDecision: "CHANGES_REQUESTED", Unresolved: 1, LastThread: th}),          // changes_requested
		open(mascot.PR{Pending: 3, ReviewDecision: "CHANGES_REQUESTED", Unresolved: 1, LastThread: th}),       // ci_running
		open(mascot.PR{Pass: 2, Fail: 1, ReviewDecision: "CHANGES_REQUESTED", Unresolved: 1, LastThread: th}), // ci_failed (idle face clouds)
		open(mascot.PR{Pass: 2, Fail: 1, ReviewDecision: "CHANGES_REQUESTED", Unresolved: 1, LastThread: th}), // same again: silent
		open(mascot.PR{Pending: 3, ReviewDecision: "CHANGES_REQUESTED"}),                                      // ci_running
		open(mascot.PR{Pass: 4, ReviewDecision: "APPROVED"}),                                                  // approved + ci_passed: approved wins
		{Number: 42, State: "MERGED", Pass: 3, ReviewDecision: "APPROVED"},                                    // merged: confetti
	}
}

func demoOn() bool { return os.Getenv("PEKOE_TAPI_DEMO") == "pr" }

func (m *model) demoStep() tea.Cmd {
	t := m.tapi
	if t.demoStep == 0 {
		t.demoSession = &state.Session{Slug: "tapi-demo", Branch: "feat/demo", State: state.Idle}
		if s := m.current(); s != nil {
			t.demoSession = s
		} else if len(m.sessions) > 0 {
			t.demoSession = m.sessions[0]
		}
	}
	if t.demoAt.IsZero() {
		t.demoAt = time.Now()
	}
	script := demoScript(t.demoAt)
	if t.demoStep >= len(script) {
		return nil
	}
	p := script[t.demoStep]
	t.demoStep++
	t.demoPR[t.demoSession.Slug] = &p
	m.tapiOnPR(t.demoSession)
	return demoTick()
}
