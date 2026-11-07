package mascot

// tapi reacting to a session's PR (docs/mascot.md「PR と CI への反応」):
// the daemon polls each PR and tapi compares the summary it saw last with
// the new one. Only a change makes a reaction, so the same state polled
// again is silent.

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"
)

// PR is the part of a session's PR summary tapi reacts to. The fields
// mirror state.PRStatus (the daemon's poll, plus Session.PR for Number) so
// the TUI's adapter is a plain copy and this package stays free of the
// fields tapi doesn't look at.
type PR struct {
	Number         int
	State          string // OPEN | MERGED | CLOSED
	Pass           int
	Fail           int
	Pending        int
	ReviewDecision string // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED
	Unresolved     int    // unresolved review threads
	LastThread     time.Time
}

// PREvent is one thing about a PR that tapi reacts to.
type PREvent string

const (
	PRMerged    PREvent = "merged"
	PRCIFailed  PREvent = "ci_failed"
	PRChanges   PREvent = "changes_requested"
	PRComment   PREvent = "new_comment"
	PRApproved  PREvent = "approved"
	PRCIPassed  PREvent = "ci_passed"
	PRCIRunning PREvent = "ci_running"
)

// PREvents in priority order: when several change at once tapi says the
// first. Merged is once per PR; then what needs the user's hands (a red
// CI, a change request, a comment), then the good news, then CI starting.
var PREvents = []PREvent{PRMerged, PRCIFailed, PRChanges, PRComment, PRApproved, PRCIPassed, PRCIRunning}

// PRPriority: smaller is more important.
func PRPriority(e PREvent) int {
	for i, x := range PREvents {
		if x == e {
			return i + 1
		}
	}
	return len(PREvents) + 1
}

// Celebrates: the happy faces, which go back to normal after a few seconds.
func (e PREvent) Celebrates() bool {
	return e == PRMerged || e == PRApproved || e == PRCIPassed
}

func (p *PR) open() bool  { return p.State == "OPEN" }
func (p *PR) green() bool { return p.Pass > 0 && p.Fail == 0 && p.Pending == 0 }

// Red: an open PR with a failing check (the idle face clouds over).
func (p *PR) Red() bool { return p != nil && p.open() && p.Fail > 0 }

// PRDiff lists what changed from prev to next, most important first. The
// first sighting (prev nil, or another PR number) and a missing next say
// nothing: tapi doesn't know what changed, it only learns the baseline.
func PRDiff(prev, next *PR) []PREvent {
	if prev == nil || next == nil || prev.Number != next.Number {
		return nil
	}
	if next.State == "MERGED" {
		if prev.State != "MERGED" {
			return []PREvent{PRMerged}
		}
		return nil
	}
	if !next.open() {
		return nil // closed: nothing to cheer or worry about
	}
	var out []PREvent
	if next.Fail > 0 && prev.Fail == 0 {
		out = append(out, PRCIFailed)
	}
	if next.ReviewDecision == "CHANGES_REQUESTED" && prev.ReviewDecision != "CHANGES_REQUESTED" {
		out = append(out, PRChanges)
	}
	if next.Unresolved > 0 && (next.Unresolved > prev.Unresolved || next.LastThread.After(prev.LastThread)) {
		out = append(out, PRComment)
	}
	if next.ReviewDecision == "APPROVED" && prev.ReviewDecision != "APPROVED" {
		out = append(out, PRApproved)
	}
	if next.green() && !prev.green() {
		out = append(out, PRCIPassed)
	}
	if next.Pending > 0 && next.Fail == 0 && prev.Pending == 0 {
		out = append(out, PRCIRunning)
	}
	return out
}

// Reaction is one PR event about one session, ready to say.
type Reaction struct {
	Event PREvent
	Slug  string
	Name  string // branch (or slug), as the bar names sessions
	PR    PR
}

// React is the most important change from prev to next, if any.
func React(slug, name string, prev, next *PR) (Reaction, bool) {
	ev := PRDiff(prev, next)
	if len(ev) == 0 {
		return Reaction{}, false
	}
	return Reaction{Event: ev[0], Slug: slug, Name: name, PR: *next}, true
}

// Key identifies the reaction for de-duplication: the same event on the
// same PR in the same state.
func (r Reaction) Key() string {
	p := r.PR
	return fmt.Sprintf("%s#%d|%s|%d/%d/%d|%s|%d", r.Slug, p.Number, r.Event, p.Pass, p.Fail, p.Pending, p.ReviewDecision, p.Unresolved)
}

// ReactionDef is a reaction's face and lines (tapi.json "reactions").
type ReactionDef struct {
	Eyes     string   `json:"eyes"`
	Alt      string   `json:"alt"` // second frame while animating (ci_running fidgets)
	ASCII    string   `json:"ascii"`
	ASCIIAlt string   `json:"ascii_alt"`
	Accent   string   `json:"accent"`
	Lines    []string `json:"lines"`
}

// Lines are the event's speech patterns with the placeholders filled in:
// {name} {pr} {pass} {fail} {pending} {unresolved}.
func (r Reaction) Lines() []string {
	p := r.PR
	rep := strings.NewReplacer(
		"{name}", r.Name, "{pr}", strconv.Itoa(p.Number),
		"{pass}", strconv.Itoa(p.Pass), "{fail}", strconv.Itoa(p.Fail),
		"{pending}", strconv.Itoa(p.Pending), "{unresolved}", strconv.Itoa(p.Unresolved),
	)
	var out []string
	for _, l := range spec.Reactions[r.Event].Lines {
		out = append(out, rep.Replace(l))
	}
	return out
}

// Speech is one of the lines, picked by the reaction itself so the same
// reaction always says the same thing (stable in tests and redraws) while
// different PRs and events vary.
func (r Reaction) Speech() string {
	ls := r.Lines()
	if len(ls) == 0 {
		return fmt.Sprintf("%s #%d %s", r.Name, r.PR.Number, r.Event)
	}
	h := fnv.New32a()
	h.Write([]byte(r.Key()))
	return ls[int(h.Sum32()%uint32(len(ls)))]
}

// ReactionEyes is the face for an event. frame alternates the second
// frame of events that have one; pass a fixed 0 when not animating.
func (r *Renderer) ReactionEyes(e PREvent, frame int) string {
	d := spec.Reactions[e]
	eyes, alt := d.Eyes, d.Alt
	if r.ASCII() {
		eyes, alt = d.ASCII, d.ASCIIAlt
	}
	if alt != "" && frame%2 == 1 {
		return alt
	}
	return r.narrowGlyph(eyes)
}

// PaintReaction renders s in the event's accent color.
func (r *Renderer) PaintReaction(e PREvent, s string) string {
	return r.paint(s, spec.Reactions[e].Accent, "")
}

// CloudyEyes: the idle face while an open PR has a failing check. Hollow
// pupils, a shade duller than • • and nowhere near the error face.
func (r *Renderer) CloudyEyes() string {
	if r.ASCII() {
		return asciiEyes[Idle]
	}
	return "◦ ◦"
}

// PRLabel names a PR the way the speech does: "fix-ci #42".
func PRLabel(name string, number int) string {
	if number <= 0 {
		return name
	}
	return name + " #" + strconv.Itoa(number)
}
