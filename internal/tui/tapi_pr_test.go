package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/config"
	"pekoe/internal/mascot"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

// tapiPRModel pins what the user's config could change (icons, animate), so
// the tests don't depend on ~/.config/pekoe.
func tapiPRModel(t *testing.T, color string, mc config.Mascot) *model {
	t.Helper()
	m := tapiModel(t, color, mc)
	m.g.Animate = nil
	m.ic = theme.Icons("nerd")
	m.buildTapi(nil)
	return m
}

// feedPR stands in for a session.updated carrying a new PR summary.
func feedPR(m *model, s *state.Session, p mascot.PR) {
	m.tapi.demoPR[s.Slug] = &p
	m.tapiOnSession(s.State, s, "")
}

func openPR(p mascot.PR) mascot.PR { p.Number, p.State = 42, "OPEN"; return p }

func TestPRReactionOnlyOnChange(t *testing.T) {
	m := tapiPRModel(t, "none", config.Mascot{})
	s := &state.Session{Slug: "fix-ci", Branch: "fix-ci", State: state.Idle, StateSince: time.Now()}
	feedPR(m, s, openPR(mascot.PR{Pending: 2}))
	if m.tapi.toast != nil {
		t.Fatal("the first summary is only the baseline")
	}
	feedPR(m, s, openPR(mascot.PR{Pending: 2}))
	if m.tapi.toast != nil {
		t.Fatal("the same summary polled again says nothing")
	}
	feedPR(m, s, openPR(mascot.PR{Pass: 3}))
	tt := m.tapi.toast
	if tt == nil || tt.react == nil || tt.react.Event != mascot.PRCIPassed || !strings.HasPrefix(tt.text, "fix-ci #42 ") {
		t.Fatalf("toast=%+v", tt)
	}
	if d := time.Until(tt.until); d > celebrateFor || d < celebrateFor-time.Second {
		t.Fatalf("a happy face lasts a few seconds, got %s", d)
	}
	m.tapi.toast = nil
	feedPR(m, s, openPR(mascot.PR{Pass: 3}))
	if m.tapi.toast != nil {
		t.Fatal("no repeat without a change")
	}
	// flapping back and forth inside the repeat window stays quiet
	feedPR(m, s, openPR(mascot.PR{Pending: 1}))
	m.tapi.toast = nil
	feedPR(m, s, openPR(mascot.PR{Pass: 3}))
	if m.tapi.toast != nil {
		t.Fatal("same reaction on the same state within the window")
	}
	// a real repeat after the window (green → re-run → green) speaks again
	for k := range m.tapi.prToasted {
		m.tapi.prToasted[k] = time.Now().Add(-prRepeatGap)
	}
	feedPR(m, s, openPR(mascot.PR{Pending: 1}))
	m.tapi.toast = nil
	feedPR(m, s, openPR(mascot.PR{Pass: 3}))
	if m.tapi.toast == nil || m.tapi.toast.react.Event != mascot.PRCIPassed {
		t.Fatal("green again after the window should cheer")
	}
	// a session event without a PR summary keeps the baseline
	delete(m.tapi.demoPR, s.Slug)
	m.tapiOnSession(s.State, s, "")
	if m.tapi.prs[s.Slug] == nil {
		t.Fatal("baseline lost")
	}
}

// wait / error come first: a PR toast never covers one, steps aside for
// one and comes back once it is gone.
func TestPRReactionYieldsToWaitAndError(t *testing.T) {
	m := tapiPRModel(t, "none", config.Mascot{})
	pr := &state.Session{Slug: "a", Branch: "a", State: state.Idle, StateSince: time.Now()}
	w8 := &state.Session{Slug: "b", Branch: "b", State: state.Waiting, StateSince: time.Now()}
	feedPR(m, pr, openPR(mascot.PR{Pending: 1}))

	m.tapiOnSession(state.Running, w8, "")
	feedPR(m, pr, openPR(mascot.PR{Fail: 1}))
	if m.tapi.toast.react != nil || m.tapi.toast.text != "b needs you" {
		t.Fatalf("PR toast covered the wait: %+v", m.tapi.toast)
	}
	if len(m.tapi.prQueue) != 1 {
		t.Fatalf("queue=%d", len(m.tapi.prQueue))
	}
	m.tapi.toast.until = time.Now().Add(-time.Second)
	m.tapiExpire()
	if m.tapi.toast == nil || m.tapi.toast.react == nil || m.tapi.toast.react.Event != mascot.PRCIFailed {
		t.Fatalf("queued reaction should follow: %+v", m.tapi.toast)
	}

	// a wait arriving over a PR toast takes the spot; the PR toast waits
	w8.StateSince = time.Now().Add(time.Second)
	m.tapiOnSession(state.Running, w8, "")
	if m.tapi.toast.react != nil || len(m.tapi.prQueue) != 1 {
		t.Fatalf("toast=%+v queue=%d", m.tapi.toast, len(m.tapi.prQueue))
	}

	// stale reactions are dropped instead of shown late
	m.tapi.prQueue[0].at = time.Now().Add(-prQueueFor)
	m.tapi.toast = nil
	m.tapiExpire()
	if m.tapi.toast != nil || len(m.tapi.prQueue) != 0 {
		t.Fatal("stale reaction shown")
	}
}

func TestPRReactionPriority(t *testing.T) {
	m := tapiPRModel(t, "none", config.Mascot{})
	a := &state.Session{Slug: "a", Branch: "a", State: state.Idle}
	b := &state.Session{Slug: "b", Branch: "b", State: state.Idle}
	feedPR(m, a, openPR(mascot.PR{Pending: 1}))
	feedPR(m, b, openPR(mascot.PR{Pending: 1}))
	feedPR(m, a, openPR(mascot.PR{Pass: 1})) // ci passed
	feedPR(m, b, openPR(mascot.PR{Fail: 1})) // ci failed: more important, takes over
	if m.tapi.toast.react.Event != mascot.PRCIFailed || len(m.tapi.prQueue) != 1 {
		t.Fatalf("toast=%v queue=%d", m.tapi.toast.react.Event, len(m.tapi.prQueue))
	}
	feedPR(m, a, openPR(mascot.PR{Pass: 1, ReviewDecision: "APPROVED"})) // less important: queued, replaces a's
	if m.tapi.toast.react.Event != mascot.PRCIFailed || len(m.tapi.prQueue) != 1 || m.tapi.prQueue[0].r.Event != mascot.PRApproved {
		t.Fatalf("queue=%+v", m.tapi.prQueue)
	}
	// several changes in one poll: the most important one speaks
	c := &state.Session{Slug: "c", Branch: "c", State: state.Idle}
	feedPR(m, c, openPR(mascot.PR{Pending: 1}))
	feedPR(m, c, mascot.PR{Number: 42, State: "MERGED", Pass: 2, ReviewDecision: "APPROVED"})
	if m.tapi.toast.react.Event != mascot.PRMerged {
		t.Fatalf("merged wins: %v", m.tapi.toast.react.Event)
	}
}

// The agent pane doesn't show the PR, so looking at the session doesn't
// dismiss its PR toast (it does dismiss wait / error).
func TestPRToastSurvivesMarkSeen(t *testing.T) {
	m := tapiPRModel(t, "none", config.Mascot{})
	s := &state.Session{Slug: "a", Branch: "a", State: state.Idle, StateSince: time.Now()}
	m.sessions = []*state.Session{s}
	m.sel = 0
	feedPR(m, s, openPR(mascot.PR{}))
	feedPR(m, s, openPR(mascot.PR{ReviewDecision: "APPROVED"}))
	m.markSeen()
	if m.tapi.toast == nil {
		t.Fatal("PR toast dismissed by markSeen")
	}
}

func boolp(b bool) *bool { return &b }

func reactionToast(m *model, e mascot.PREvent) []string {
	r := mascot.Reaction{Event: e, Slug: "fix-ci", Name: "fix-ci", PR: mascot.PR{Number: 42, State: "OPEN", Fail: 1, Unresolved: 2}}
	m.tapi.toast = queuedReaction{r: r, at: time.Now(), dur: time.Minute}.toast(time.Now())
	return m.toastBlock()
}

// Faces and speech for every event, nerd and ascii: three rows of one
// width, the PR named, the confetti only for merged and only animated.
func TestPRToastRendering(t *testing.T) {
	for _, c := range []struct {
		name, color, icons string
		mc                 config.Mascot
	}{
		{"nerd/truecolor", "truecolor", "nerd", config.Mascot{Shape: "round"}},
		{"nerd/none", "none", "nerd", config.Mascot{Shape: "round"}},
		{"unicode/ansi", "ansi", "unicode", config.Mascot{Shape: "block"}},
		{"ascii", "none", "ascii", config.Mascot{Glyphs: "ascii"}},
	} {
		m := tapiPRModel(t, c.color, c.mc)
		m.ic = theme.Icons(c.icons)
		m.buildTapi(nil)
		for _, e := range mascot.PREvents {
			tb := reactionToast(m, e)
			if len(tb) != 3 {
				t.Fatalf("%s %s: %d rows", c.name, e, len(tb))
			}
			for i, l := range tb {
				if ansi.StringWidth(l) != ansi.StringWidth(tb[0]) {
					t.Errorf("%s %s row %d: width %d vs %d", c.name, e, i, ansi.StringWidth(l), ansi.StringWidth(tb[0]))
				}
			}
			mid := ansi.Strip(tb[1])
			if !strings.Contains(mid, "fix-ci #42 ") {
				t.Errorf("%s %s: %q", c.name, e, mid)
			}
			if !strings.Contains(mid, ansi.Strip(m.tapi.r.Line(m.tapi.r.ReactionEyes(e, 0)))) {
				t.Errorf("%s %s: face missing in %q", c.name, e, mid)
			}
			border := ansi.Strip(tb[0] + tb[2])
			confetti := strings.Trim(border, "╭╮╰╯┌┐└┘─") != ""
			if confetti != (e == mascot.PRMerged) {
				t.Errorf("%s %s: confetti=%v %q", c.name, e, confetti, border)
			}
			if c.icons == "ascii" {
				for _, l := range []string{ansi.Strip(tb[0]), ansi.Strip(tb[2])} {
					if strings.ContainsAny(strings.Trim(l, "┌┐└┘╭╮╰╯─"), "✦✧⋆⁺") {
						t.Errorf("ascii confetti: %q", l)
					}
				}
				for _, r := range strings.TrimSuffix(strings.SplitN(mid, "fix-ci", 2)[0], " ") {
					if r > 127 && r != '│' {
						t.Errorf("ascii face: %q", mid)
					}
				}
			}
		}
		// animate = false: still face and speech only, no confetti
		m.g.Animate = boolp(false)
		tb := reactionToast(m, mascot.PRMerged)
		if b := ansi.Strip(tb[0] + tb[2]); strings.Trim(b, "╭╮╰╯┌┐└┘─") != "" {
			t.Errorf("%s: confetti with animations off: %q", c.name, b)
		}
		run := reactionToast(m, mascot.PRCIRunning)
		m.tapi.frame = 2
		if ansi.Strip(reactionToast(m, mascot.PRCIRunning)[1]) != ansi.Strip(run[1]) {
			t.Errorf("%s: ci running fidgets with animations off", c.name)
		}
	}
}

// The toast keeps the whole view at the terminal's size.
func TestPRToastViewKeepsWidth(t *testing.T) {
	m := tapiPRModel(t, "truecolor", config.Mascot{Shape: "round"})
	m.sessions = []*state.Session{{Slug: "a", Branch: "a", State: state.Idle, StateSince: time.Now()}}
	reactionToast(m, mascot.PRMerged)
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.height {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		if ansi.StringWidth(l) != m.width {
			t.Errorf("line %d: width %d", i, ansi.StringWidth(l))
		}
	}
	if !strings.Contains(ansi.Strip(m.View()), "fix-ci #42") {
		t.Error("toast not in view")
	}
}

// An open PR with a red CI clouds the idle face a little; nothing else.
func TestCloudyIdleBar(t *testing.T) {
	m := tapiPRModel(t, "none", config.Mascot{})
	s := &state.Session{Slug: "a", Branch: "fix-ci", State: state.Idle, StateSince: time.Now()}
	m.sessions = []*state.Session{s}
	clear := ansi.Strip(m.barTapi(100))
	if clear != "▐• •▌ idle" {
		t.Fatalf("%q", clear)
	}
	m.tapi.demoPR["a"] = &mascot.PR{Number: 42, State: "OPEN", Fail: 1}
	if got := ansi.Strip(m.barTapi(100)); got != "▐◦ ◦▌ idle · fix-ci #42 ci red" {
		t.Fatalf("%q", got)
	}
	// a waiting session keeps the puppy face; the PR doesn't mute it
	s.State = state.Waiting
	if got := ansi.Strip(m.barTapi(100)); got != "▐◕ ◕▌ fix-ci needs you" {
		t.Fatalf("%q", got)
	}
}

// The demo shows each face once, in this order, with the baseline and the
// repeated summary silent.
func TestDemoScript(t *testing.T) {
	m := tapiPRModel(t, "none", config.Mascot{})
	var got []string
	for i := 0; i < len(demoScript(time.Now())); i++ {
		m.tapi.toast, m.tapi.prQueue = nil, nil
		m.demoStep()
		if m.tapi.toast == nil {
			got = append(got, "-")
			continue
		}
		got = append(got, string(m.tapi.toast.react.Event))
	}
	want := "- ci_passed approved new_comment changes_requested ci_running ci_failed - ci_running approved merged"
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
	if m.demoStep() != nil {
		t.Error("demo stops at the end")
	}
}

// The confetti sits unevenly (gaps of 2–6, never next to a corner), comes
// from the PR so it holds still across redraws, and differs between the
// top and bottom borders and between PRs.
func TestConfettiLayout(t *testing.T) {
	set := theme.Confetti(theme.Icons("unicode"))
	places := func(sp []spark) []int {
		var out []int
		for _, s := range sp {
			out = append(out, s.pos)
		}
		return out
	}
	for _, n := range []int{4, 9, 30, 41, 80} {
		for _, slug := range []string{"fix-ci", "feat/demo", "a", "tapi-demo"} {
			for _, pr := range []int{1, 42, 1234} {
				seed := confettiSeed(&mascot.Reaction{Slug: slug, PR: mascot.PR{Number: pr}})
				top, bottom := confettiLayouts(n, seed, set)
				for _, sp := range [][]spark{top, bottom} {
					gaps := map[int]bool{}
					for i, s := range sp {
						if s.pos < 1 || s.pos > n-2 {
							t.Errorf("n=%d %s#%d: sparkle at %d, next to a corner", n, slug, pr, s.pos)
						}
						if i > 0 {
							g := s.pos - sp[i-1].pos
							if g < 2 || g > 6 {
								t.Errorf("n=%d %s#%d: gap %d", n, slug, pr, g)
							}
							gaps[g] = true
						}
					}
					if n >= 30 && len(gaps) < 2 {
						t.Errorf("n=%d %s#%d: evenly spaced %v", n, slug, pr, places(sp))
					}
				}
				if n >= 30 && slices.Equal(places(top), places(bottom)) {
					t.Errorf("n=%d %s#%d: top and bottom alike %v", n, slug, pr, places(top))
				}
				t2, b2 := confettiLayouts(n, seed, set)
				if !slices.Equal(places(top), places(t2)) || !slices.Equal(places(bottom), places(b2)) {
					t.Errorf("n=%d %s#%d: layout moved between draws", n, slug, pr)
				}
			}
		}
	}
	a := confettiSeed(&mascot.Reaction{Slug: "fix-ci", PR: mascot.PR{Number: 42}})
	b := confettiSeed(&mascot.Reaction{Slug: "fix-ci", PR: mascot.PR{Number: 43}})
	ta, _ := confettiLayouts(40, a, set)
	tb, _ := confettiLayouts(40, b, set)
	if slices.Equal(places(ta), places(tb)) {
		t.Errorf("two PRs share a layout: %v", places(ta))
	}
}

// Sparkles burst lit, then twinkle out of step: not all of them change on
// the same frame, few are big, and some go out before the toast does.
func TestConfettiTwinkle(t *testing.T) {
	set := theme.Confetti(theme.Icons("unicode"))
	var all []spark
	for pr := 1; pr <= 20; pr++ {
		top, bottom := confettiLayouts(40, confettiSeed(&mascot.Reaction{Slug: "x", PR: mascot.PR{Number: pr}}), set)
		all = append(append(all, top...), bottom...)
	}
	big, faded := 0, 0
	for _, s := range all {
		if s.glyph(0) != s.ladder[max(s.off-1, 0)] || s.off > len(s.ladder)-2 {
			t.Fatalf("not lit at the burst: %+v", s)
		}
		if s.off == 0 {
			big++
		}
		if s.glyph(9) == "" && s.fade > 0 {
			faded++
		}
	}
	if big*3 > len(all) || big == 0 {
		t.Errorf("big sparkles: %d of %d", big, len(all))
	}
	if faded == 0 || faded*2 > len(all) {
		t.Errorf("faded by the end: %d of %d", faded, len(all))
	}
	for e := burstFrames; e < burstFrames+6; e++ {
		changed := 0
		for _, s := range all {
			if s.glyph(e) != s.glyph(e+1) {
				changed++
			}
		}
		if changed == len(all) {
			t.Errorf("frame %d: every sparkle changes at once", e)
		}
	}
}

// The merged toast: confetti counts from the frame the toast came up and
// holds still while tapi's frame does.
func TestConfettiOnToast(t *testing.T) {
	m := tapiPRModel(t, "none", config.Mascot{Shape: "round"})
	m.ic = theme.Icons("unicode")
	m.buildTapi(nil)
	m.tapi.frame = 7
	m.showReaction(mascot.Reaction{Event: mascot.PRMerged, Slug: "fix-ci", Name: "fix-ci", PR: mascot.PR{Number: 42, State: "MERGED"}}, time.Now())
	first := m.toastBlock()
	if m.tapi.toast.frame0 != 7 {
		t.Fatalf("frame0 %d", m.tapi.toast.frame0)
	}
	if again := m.toastBlock(); ansi.Strip(again[0]) != ansi.Strip(first[0]) {
		t.Errorf("redraw moved the confetti:\n%s\n%s", ansi.Strip(first[0]), ansi.Strip(again[0]))
	}
	differ := false
	for f := 8; f < 14; f++ {
		m.tapi.frame = f
		tb := m.toastBlock()
		if ansi.Strip(tb[0]+tb[2]) != ansi.Strip(first[0]+first[2]) {
			differ = true
		}
		for i, l := range tb {
			if ansi.StringWidth(l) != ansi.StringWidth(first[1]) {
				t.Errorf("frame %d row %d: width %d", f, i, ansi.StringWidth(l))
			}
		}
	}
	if !differ {
		t.Error("confetti never twinkles")
	}
}

// The daemon's Session.PRStatus drives tapi through session.updated.
func TestPRReactionFromSessionPRStatus(t *testing.T) {
	m := tapiPRModel(t, "none", config.Mascot{})
	s := &state.Session{Slug: "fix-ci", Branch: "fix-ci", State: state.Idle, PR: 42,
		PRStatus: &state.PRStatus{State: "OPEN", Pending: 2}}
	m.tapiOnSession(s.State, s, "")
	if m.tapi.toast != nil {
		t.Fatal("baseline")
	}
	s2 := *s
	s2.PRStatus = &state.PRStatus{State: "OPEN", Pass: 1, Fail: 1, Attention: []string{"ci_failed"}}
	m.tapiOnSession(s2.State, &s2, "")
	tt := m.tapi.toast
	if tt == nil || tt.react.Event != mascot.PRCIFailed || !strings.HasPrefix(tt.text, "fix-ci #42 ") {
		t.Fatalf("toast=%+v", tt)
	}
	m.sessions = []*state.Session{&s2}
	if got := m.cloudy(); len(got) != 1 || got[0] != "fix-ci #42" {
		t.Fatalf("cloudy=%v", got)
	}
}
