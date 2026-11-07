// Package engine is the terminal layer. Phase 1 uses tmux (a dedicated
// server, `tmux -L boba`) for pty management, rendering and persistence.
// The daemon only talks to the Engine interface so the tmux engine can be
// replaced by a libghostty-vt mux later without losing features.
package engine

import (
	"time"
)

// Window kinds inside one boba session.
const (
	WinAgent  = "agent"
	WinReview = "review"
	WinDev    = "dev"
)

type Spawn struct {
	Slug    string
	Window  string
	Dir     string
	Env     []string
	Command string // run through the user's shell
	Width   int
	Height  int
}

type Screen struct {
	Lines  []string `json:"lines"` // ANSI-coloured lines
	Cursor struct {
		X       int  `json:"x"`
		Y       int  `json:"y"`
		Visible bool `json:"visible"`
	} `json:"cursor"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	AltScreen bool `json:"alt_screen"`
	MouseAny  bool `json:"mouse"`
	Dead      bool `json:"dead"`
	History   int  `json:"history"` // lines scrolled off the top (for the unread line)
}

// Output is a chunk produced by a pane.
type Output struct {
	Slug   string
	Window string
	Data   []byte
	At     time.Time
}

type Engine interface {
	Start() error
	Spawn(s Spawn) error
	Alive(slug, window string) bool
	// SendText writes text to the pty. Multi-line text is wrapped in bracketed paste.
	SendText(slug, window, text string) error
	// SendKeys sends named keys (tmux key names: Enter, Escape, C-c, Up …).
	SendKeys(slug, window string, keys ...string) error
	SendLiteral(slug, window, text string) error
	SendHex(slug, window string, hex []string) error
	Capture(slug, window string) (*Screen, error)
	Resize(slug, window string, w, h int) error
	Kill(slug, window string) error
	KillSession(slug string) error
	// Sessions lists boba windows currently alive: slug -> windows.
	Sessions() (map[string][]string, error)
	Output() <-chan Output
	Close()
}
