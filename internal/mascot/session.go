package mascot

import (
	"fmt"
	"time"

	"boba/internal/state"
)

const (
	// DoneFade: an unseen done returns to idle after this long (in the bar).
	DoneFade = 10 * time.Second
	// SleepAfter: every session idle (and no input) for this long → sleep.
	SleepAfter = 3 * time.Minute
	// WaitBlinkAfter / WaitNotifyAfter: wait escalation.
	WaitBlinkAfter  = 30 * time.Second
	WaitNotifyAfter = 2 * time.Minute
)

// seenSince: the user looked at the session after its state last changed.
func seenSince(s *state.Session, seen time.Time) bool {
	return !seen.IsZero() && !seen.Before(s.StateSince)
}

// RowMood is the mood of one session's 1-cell dot in the session list.
// A done dot stays green until the session is shown.
func RowMood(s *state.Session, seen time.Time) Mood {
	switch s.State {
	case state.Failed:
		return Error
	case state.Waiting:
		return Wait
	case state.Running, state.Creating:
		return Work
	case state.Done:
		if seenSince(s, seen) {
			return Idle
		}
		return Done
	}
	return Idle
}

// BarMood is the session's contribution to the aggregated face:
// done fades after DoneFade or once seen, error holds until seen.
func BarMood(s *state.Session, seen, now time.Time) Mood {
	m := RowMood(s, seen)
	switch m {
	case Error:
		if seenSince(s, seen) {
			return Idle
		}
	case Done:
		if now.Sub(s.StateSince) >= DoneFade {
			return Idle
		}
	}
	return m
}

// Summary is the aggregated state the status bar shows.
type Summary struct {
	Mood  Mood
	Names []string // sessions in that mood (branch names)
	Total int
	// Since: oldest StateSince among the sessions in Mood (wait escalation)
	Since time.Time
}

// Aggregate picks the most urgent mood across all sessions, regardless of
// which one is shown. lastActive is the last key press or bobad event.
func Aggregate(ss []*state.Session, seen map[string]time.Time, now, lastActive time.Time) Summary {
	sum := Summary{Mood: Idle, Total: len(ss)}
	latest := time.Time{}
	for _, s := range ss {
		if s.State == state.Landed {
			sum.Total--
			continue
		}
		m := BarMood(s, seen[s.Slug], now)
		if s.StateSince.After(latest) {
			latest = s.StateSince
		}
		switch {
		case Priority(m) < Priority(sum.Mood):
			sum.Mood, sum.Names, sum.Since = m, nil, s.StateSince
		case m != sum.Mood:
			continue
		}
		sum.Names = append(sum.Names, name(s))
		if s.StateSince.Before(sum.Since) {
			sum.Since = s.StateSince
		}
	}
	if sum.Mood == Idle && now.Sub(lastActive) >= SleepAfter && now.Sub(latest) >= SleepAfter {
		sum.Mood = Sleep
	}
	return sum
}

func name(s *state.Session) string {
	if s.Branch != "" {
		return s.Branch
	}
	return s.Slug
}

// Speech is what the pearl says next to its face: lowercase, no "!".
// One session is named; several are counted.
func (s Summary) Speech() string {
	n := len(s.Names)
	one := func(single, plural string) string {
		if n == 1 {
			return s.Names[0] + " " + single
		}
		return fmt.Sprintf("%d %s", n, plural)
	}
	switch s.Mood {
	case Error:
		return one("failed", "failed")
	case Wait:
		return one("needs you", "need you")
	case Work:
		return one("working", "working")
	case Done:
		return one("done", "done")
	case Sleep:
		return "all quiet"
	}
	if s.Total == 0 {
		return "no sessions"
	}
	return "idle"
}

// SessionSpeech is the toast line for one session.
func SessionSpeech(s *state.Session) string {
	switch s.State {
	case state.Failed:
		return name(s) + " failed"
	case state.Waiting:
		return name(s) + " needs you"
	case state.Done:
		return name(s) + " done"
	}
	return name(s) + " " + string(s.State)
}
