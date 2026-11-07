package mascot

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestPRDiff(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	open := func(p PR) *PR { p.Number, p.State = 42, "OPEN"; return &p }
	cases := []struct {
		name       string
		prev, next *PR
		want       []PREvent
	}{
		{"first sighting is the baseline", nil, open(PR{Fail: 1}), nil},
		{"no summary", open(PR{}), nil, nil},
		{"no change", open(PR{Pass: 3, ReviewDecision: "APPROVED"}), open(PR{Pass: 3, ReviewDecision: "APPROVED"}), nil},
		{"same failure polled again", open(PR{Pass: 1, Fail: 2}), open(PR{Pass: 1, Fail: 2}), nil},
		{"another PR number", open(PR{Pending: 1}), &PR{Number: 43, State: "OPEN", Pass: 3}, nil},
		{"ci passed", open(PR{Pending: 2, Pass: 1}), open(PR{Pass: 3}), []PREvent{PRCIPassed}},
		{"more green checks: still green", open(PR{Pass: 3}), open(PR{Pass: 4}), nil},
		{"ci failed", open(PR{Pending: 2}), open(PR{Pass: 1, Fail: 1}), []PREvent{PRCIFailed}},
		{"one more failure: already red", open(PR{Fail: 1, Pending: 1}), open(PR{Fail: 2}), nil},
		{"ci started", open(PR{Pass: 3}), open(PR{Pending: 3}), []PREvent{PRCIRunning}},
		{"re-run after red", open(PR{Fail: 1}), open(PR{Pending: 3}), []PREvent{PRCIRunning}},
		{"pending while red is not running", open(PR{Fail: 1}), open(PR{Fail: 1, Pending: 1}), nil},
		{"approved", open(PR{}), open(PR{ReviewDecision: "APPROVED"}), []PREvent{PRApproved}},
		{"changes requested", open(PR{ReviewDecision: "APPROVED"}), open(PR{ReviewDecision: "CHANGES_REQUESTED"}), []PREvent{PRChanges}},
		{"new thread", open(PR{}), open(PR{Unresolved: 1, LastThread: t0}), []PREvent{PRComment}},
		{"reply on a thread", open(PR{Unresolved: 1, LastThread: t0}), open(PR{Unresolved: 1, LastThread: t0.Add(time.Minute)}), []PREvent{PRComment}},
		{"thread resolved", open(PR{Unresolved: 2, LastThread: t0}), open(PR{Unresolved: 1, LastThread: t0}), nil},
		{"merged", open(PR{Pass: 3}), &PR{Number: 42, State: "MERGED", Pass: 3}, []PREvent{PRMerged}},
		{"merged polled again", &PR{Number: 42, State: "MERGED"}, &PR{Number: 42, State: "MERGED"}, nil},
		{"merged with green CI says only merged", open(PR{Pending: 1}), &PR{Number: 42, State: "MERGED", Pass: 3, ReviewDecision: "APPROVED"}, []PREvent{PRMerged}},
		{"closed", open(PR{Pass: 3}), &PR{Number: 42, State: "CLOSED", Pass: 3}, nil},
		{"several at once, most important first",
			open(PR{Pending: 3}),
			open(PR{Pass: 2, Fail: 1, ReviewDecision: "CHANGES_REQUESTED", Unresolved: 1, LastThread: t0}),
			[]PREvent{PRCIFailed, PRChanges, PRComment}},
		{"good news together", open(PR{Pending: 3, ReviewDecision: "REVIEW_REQUIRED"}), open(PR{Pass: 3, ReviewDecision: "APPROVED"}), []PREvent{PRApproved, PRCIPassed}},
	}
	for _, c := range cases {
		if got := PRDiff(c.prev, c.next); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestReactPicksTheMostImportant(t *testing.T) {
	prev := &PR{Number: 7, State: "OPEN", Pending: 2}
	next := &PR{Number: 7, State: "OPEN", Fail: 1, ReviewDecision: "APPROVED"}
	r, ok := React("fix-ci", "fix/ci", prev, next)
	if !ok || r.Event != PRCIFailed || r.Name != "fix/ci" || r.PR.Number != 7 {
		t.Fatalf("r=%+v ok=%v", r, ok)
	}
	if _, ok := React("x", "x", next, next); ok {
		t.Fatal("no change, no reaction")
	}
	// the order is the documented one
	for i := 1; i < len(PREvents); i++ {
		if PRPriority(PREvents[i-1]) >= PRPriority(PREvents[i]) {
			t.Fatal("priority order")
		}
	}
	if PRPriority(PRMerged) != 1 || PRPriority(PRCIRunning) != len(PREvents) {
		t.Fatal("merged first, ci running last")
	}
}

// Every reaction names the session and the PR number, in Japanese, with
// no "!" (docs/mascot.md: speech has no exclamation marks).
func TestReactionSpeech(t *testing.T) {
	for _, e := range PREvents {
		d, ok := spec.Reactions[e]
		if !ok || len(d.Lines) < 3 {
			t.Fatalf("%s: needs at least 3 lines in tapi.json", e)
		}
		r := Reaction{Event: e, Slug: "fix-ci", Name: "fix-ci", PR: PR{Number: 42, Fail: 2, Unresolved: 3}}
		for _, l := range r.Lines() {
			if !strings.HasPrefix(l, "fix-ci #42 ") {
				t.Errorf("%s: %q doesn't name the PR", e, l)
			}
			if strings.ContainsAny(l, "!！{}") {
				t.Errorf("%s: %q", e, l)
			}
		}
		sp := r.Speech()
		if sp != r.Speech() {
			t.Errorf("%s: speech must be stable", e)
		}
		found := false
		for _, l := range r.Lines() {
			found = found || l == sp
		}
		if !found {
			t.Errorf("%s: %q is not one of the lines", e, sp)
		}
	}
	r := Reaction{Event: PRCIFailed, Name: "a", PR: PR{Number: 1, Fail: 2}}
	if !strings.Contains(strings.Join(r.Lines(), "\n"), "2 件") {
		t.Error("{fail} filled in")
	}
}

func TestReactionFaces(t *testing.T) {
	uni := plain(Options{Shape: "round"})
	asc := plain(Options{Glyphs: "ascii"})
	moodEyes := map[string]Mood{}
	for _, m := range Moods {
		moodEyes[uni.Eyes(m)] = m
	}
	seen := map[string]PREvent{}
	for _, e := range PREvents {
		for _, frame := range []int{0, 1} {
			for name, r := range map[string]*Renderer{"unicode": uni, "ascii": asc} {
				eyes := r.ReactionEyes(e, frame)
				if n := len([]rune(eyes)); n != 3 {
					t.Errorf("%s %s: eyes %q are %d cells", name, e, eyes, n)
				}
				line := r.Line(eyes)
				if ansi.StringWidth(line) != 5 {
					t.Errorf("%s %s: face %q", name, e, line)
				}
				if name == "ascii" {
					for _, c := range line {
						if c > 127 {
							t.Errorf("ascii %s: %q", e, line)
						}
					}
				}
			}
		}
		eyes := uni.ReactionEyes(e, 0)
		if x, ok := seen[eyes]; ok {
			t.Errorf("%s and %s share %q", e, x, eyes)
		}
		seen[eyes] = e
		// CI failing is sad, not the error face
		if m, ok := moodEyes[eyes]; ok && m != Done {
			t.Errorf("%s looks like %s (%q)", e, m, eyes)
		}
	}
	if uni.ReactionEyes(PRCIFailed, 0) == uni.Eyes(Error) {
		t.Fatal("ci failed must differ from error")
	}
	if uni.ReactionEyes(PRCIRunning, 0) == uni.ReactionEyes(PRCIRunning, 1) {
		t.Fatal("ci running fidgets between two frames")
	}
	if uni.ReactionEyes(PRMerged, 0) != uni.ReactionEyes(PRMerged, 1) {
		t.Fatal("merged has no second frame (the confetti moves)")
	}
	if got := uni.Line(uni.ReactionEyes(PRCIFailed, 0)); got != "▐╥ ╥▌" {
		t.Fatalf("ci failed face: %q", got)
	}
}

func TestCloudySummary(t *testing.T) {
	s := Summary{Mood: Idle, Total: 2}
	if s.IsCloudy() || s.Speech() != "idle" {
		t.Fatal("clear sky")
	}
	s.Cloudy = []string{"fix-ci #42"}
	if !s.IsCloudy() || s.Speech() != "idle · fix-ci #42 ci red" {
		t.Fatalf("%q", s.Speech())
	}
	s.Cloudy = append(s.Cloudy, "b #3")
	if s.Speech() != "idle · 2 prs ci red" {
		t.Fatalf("%q", s.Speech())
	}
	// only idle clouds over: wait / work / sleep keep their own face
	for _, m := range []Mood{Wait, Work, Sleep, Error} {
		if (Summary{Mood: m, Cloudy: s.Cloudy, Names: []string{"a"}}).IsCloudy() {
			t.Errorf("%s clouded", m)
		}
	}
	if plain(Options{}).CloudyEyes() == plain(Options{}).Eyes(Idle) {
		t.Fatal("cloudy eyes differ from idle")
	}
	if (&PR{State: "OPEN", Fail: 1}).Red() != true || (&PR{State: "MERGED", Fail: 1}).Red() || (*PR)(nil).Red() {
		t.Fatal("Red")
	}
}
