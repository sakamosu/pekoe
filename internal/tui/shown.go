package tui

import (
	"encoding/json"
	"sync/atomic"

	"pekoe/internal/client"
)

// shownSlug is the session whose screen the TUI shows (current()), kept
// for pump, which runs on its own goroutine. pekoed sends session.output
// for every session with new output, up to 33 times a second each; only
// the shown one's leads anywhere (onEvent fetches its screen), yet every
// message makes Bubble Tea draw the whole View again. With 20 sessions
// running that was most of the TUI's CPU, so pump drops the others before
// they reach the program.
//
// A session that becomes the shown one loses nothing: Update stores the
// new slug before the Cmds it returns run, and the move itself fetches
// the screen (onSelChange), which takes in whatever was dropped before.
type shownSlug struct{ v atomic.Value }

func (s *shownSlug) set(slug string) { s.v.Store(slug) }

func (s *shownSlug) get() string {
	v, _ := s.v.Load().(string)
	return v
}

// noteShown records current() for pump (deferred by Update, so it holds
// after every message, the tour's included).
func (m *model) noteShown() {
	if m.shown == nil {
		return
	}
	slug := ""
	if s := m.current(); s != nil {
		slug = s.Slug
	}
	m.shown.set(slug)
}

// dropHidden: ev is a session.output of a session the TUI does not show.
// Every other event goes through, and all of them without a shownSlug
// (the standalone review, which keeps no current session).
func (s *shownSlug) dropHidden(ev client.Event) bool {
	if s == nil || ev.Event != "session.output" {
		return false
	}
	var p struct {
		Slug string `json:"slug"`
	}
	if json.Unmarshal(ev.Data, &p) != nil {
		return false // let onEvent see what it is
	}
	return p.Slug != s.get()
}
