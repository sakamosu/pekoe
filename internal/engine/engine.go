// Package engine is the terminal layer. Phase 1 uses tmux (a dedicated
// server, `tmux -L pekoe`) for pty management, rendering and persistence.
// The daemon only talks to the Engine interface so the tmux engine can be
// replaced by a libghostty-vt mux later without losing features.
package engine

import (
	"strconv"
	"strings"
	"time"
)

// Window kinds inside one pekoe session.
const (
	WinAgent  = "agent"
	WinReview = "review"
	WinDev    = "dev"
	// WinRun runs one command the user typed (TUI `!`) and stays on screen
	// after it exits, so its output and exit status can be read.
	WinRun = "run"
)

type Spawn struct {
	Slug    string
	Window  string
	Dir     string
	Env     []string
	Command string // run through the user's shell
	Width   int
	Height  int
	// Quiet leaves no "Pane is dead" line when the process exits: the
	// client says how it ended. tmux writes that line on the bottom row,
	// scrolling a short output's first line away.
	Quiet bool
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
	// ExitStatus is the exit status of a dead pane's process (-1: unknown,
	// or killed by Signal).
	ExitStatus int `json:"exit_status"`
	// Signal names the signal that killed a dead pane's process ("SIGINT").
	Signal  string `json:"signal,omitempty"`
	History int    `json:"history"` // lines scrolled off the top (for the unread line)
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
	// It is text only: control characters other than newline and tab are
	// dropped (CleanText), never sent as keys.
	SendText(slug, window, text string) error
	// SendKeys sends named keys (tmux key names: Enter, Escape, C-c, Up …).
	SendKeys(slug, window string, keys ...string) error
	// SendLiteral types characters as keys, as given (the key path: what
	// the user typed in the TUI, a one-key answer).
	SendLiteral(slug, window, text string) error
	SendHex(slug, window string, hex []string) error
	Capture(slug, window string) (*Screen, error)
	Resize(slug, window string, w, h int) error
	Kill(slug, window string) error
	// KillWait kills the window and waits until the processes in its
	// pane's process group have exited: after grace, what is left gets
	// SIGKILL (then a short wait more). Closing a window only sends SIGHUP,
	// and an agent keeps writing (its transcript) for a while after it.
	KillWait(slug, window string, grace time.Duration) error
	KillSession(slug string) error
	// KillLeftovers stops what a session's windows started and left
	// running after KillSession closed them (a background job reparented
	// to launchd: leftover.go), and lists what it stopped. Only a session
	// that goes (rm, landed) calls it.
	KillLeftovers(scope LeftoverScope) ([]Leftover, error)
	// Sessions lists pekoe windows currently alive: slug -> windows.
	Sessions() (map[string][]string, error)
	Output() <-chan Output
	Close()
	// Panes lists the process id at the root of every pekoe window (tmux's
	// #{pane_pid}): pekoed walks a socket peer's parents up to one of them
	// to tell which session called (internal/daemon/caller.go). A dead
	// pane (remain-on-exit) still reports the pid its process had, which
	// the system may since have given to another process: Pane.Live tells
	// whether the pid is still the pane's.
	Panes() ([]Pane, error)
	// KillServer stops the terminal server itself, ending every agent.
	// Only `pekoe daemon stop --kill-agents` uses it; a plain stop keeps the
	// agents running for the next daemon.
	KillServer() error
}

// Pane is one window's root process.
type Pane struct {
	Slug   string
	Window string
	PID    int
	// Dead: the pane's process has exited (tmux's #{pane_dead}); PID is
	// then only the pid it had.
	Dead bool
	// Server is the pid of the terminal server that started the pane's
	// process, its parent while it runs (0: not known).
	Server int
}

// Live tells whether PID is still the pane's own process: the pane is not
// dead and the process with that pid is a child of the pane's server. A
// dead pane keeps the pid its process had, and the system may give that
// pid to any process (one that connects to pekoed, or a group that would
// be killed in its place); a reused pid is not the server's child.
func (p Pane) Live() bool {
	if p.Dead || p.PID <= 1 || p.Server <= 1 {
		return false
	}
	pp, err := ParentPID(p.PID)
	return err == nil && pp == p.Server
}

// SignalName turns tmux's pane_dead_signal ("int" in tmux 3.5, a number in
// older ones) into "SIGINT". Empty stays empty.
func SignalName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return ""
	}
	if n, err := strconv.Atoi(raw); err == nil {
		names := map[int]string{1: "HUP", 2: "INT", 3: "QUIT", 4: "ILL", 6: "ABRT", 9: "KILL", 13: "PIPE", 14: "ALRM", 15: "TERM"}
		if name, ok := names[n]; ok {
			return "SIG" + name
		}
		return "signal " + raw
	}
	return "SIG" + strings.TrimPrefix(strings.ToUpper(raw), "SIG")
}

// CleanText is text as SendText types it: line breaks become "\n" (CRLF
// and a lone CR too, so a CR never lands as Enter), tabs stay, and every
// other control character (C0, DEL, C1) is dropped. ESC is what would let
// text end a bracketed paste or press Shift-Tab; ^C would interrupt.
func CleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r':
			return '\n'
		case r == '\n' || r == '\t':
			return r
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		}
		return r
	}, s)
}

// HasControl reports whether s has a control character CleanText would
// drop (line breaks and tabs are fine).
func HasControl(s string) bool {
	for _, r := range s {
		if r != '\n' && r != '\r' && r != '\t' && (r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}
