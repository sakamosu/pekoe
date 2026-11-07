package tui

// tapi reacting to PRs and CI (docs/mascot.md「PR と CI への反応」): a
// change in a session's PR summary becomes one toast. PR toasts rank below
// the wait / error toasts: they never cover one, step aside when one
// arrives, and wait in a short queue instead.

import (
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"boba/internal/mascot"
	"boba/internal/state"
	"boba/internal/theme"
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

// sessionPR adapts a session's PR summary for tapi.
//
// This branch has no Session.PRStatus yet (feat/pr-view adds it), so only
// the demo feeds tapi. After merging feat/pr-view, add below the demo:
//
//	if p := s.PRStatus; p != nil {
//		return &mascot.PR{Number: s.PR, State: p.State, Pass: p.Pass, Fail: p.Fail,
//			Pending: p.Pending, ReviewDecision: p.ReviewDecision,
//			Unresolved: p.Unresolved, LastThread: p.LastThread}
//	}
func (m *model) sessionPR(s *state.Session) *mascot.PR {
	if p, ok := m.tapi.demoPR[s.Slug]; ok {
		return p
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

func reactionFor(e mascot.PREvent) time.Duration {
	switch {
	case e == mascot.PRCIRunning:
		return runningFor
	case e.Celebrates():
		return celebrateFor
	}
	return toastFor
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
	q := queuedReaction{r: r, at: now, dur: reactionFor(r.Event)}
	cur := t.toast
	switch {
	case cur == nil || now.After(cur.until):
		t.toast = q.toast(now)
	case cur.react == nil: // wait / error: never covered
		m.queueReaction(q)
	case mascot.PRPriority(r.Event) <= mascot.PRPriority(cur.react.Event):
		m.queueReaction(queuedReaction{r: *cur.react, at: now, dur: cur.until.Sub(now)})
		t.toast = q.toast(now)
	default:
		m.queueReaction(q)
	}
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
	t.toast = q.toast(now)
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

// confettiBorder is a border run of n cells with confetti thrown on it:
// a merged PR's toast only, and only with animations on. The glyphs
// twinkle frame by frame while focused and hold still otherwise.
func (m *model) confettiBorder(n, seed int) string {
	set := theme.Confetti(m.ic)
	if m.tapi.r.ASCII() {
		set = theme.Confetti(theme.Icons("ascii"))
	}
	frame := 0
	if m.animating() {
		frame = m.tapi.frame
	}
	var b strings.Builder
	run := 0
	flush := func() {
		if run > 0 {
			b.WriteString(m.th.FG(theme.Border, strings.Repeat("─", run)))
			run = 0
		}
	}
	for i := 0; i < n; i++ {
		if (i+seed)%5 == 2 {
			flush()
			b.WriteString(m.th.FG(theme.Secondary, set[(i/5+seed+frame)%len(set)]))
			continue
		}
		run++
	}
	flush()
	return b.String()
}

// ---- demo ----

// BOBA_TAPI_DEMO=pr plays a scripted PR on the selected session (or a
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

func demoOn() bool { return os.Getenv("BOBA_TAPI_DEMO") == "pr" }

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
