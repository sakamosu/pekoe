package daemon

// Who is calling (docs/orchestration.md "呼び出し元の識別"). Reports, the
// send log and wait's mine all record or filter by caller, and an agent
// must not be able to pass itself off as another session: $PEKOE_SESSION
// and the cwd are the agent's own word. pekoed asks the socket for the
// connecting process (LOCAL_PEERPID / SO_PEERCRED, peer_*.go) and walks its
// parents up to the root process of a pekoe window (tmux's #{pane_pid},
// Engine.Panes): the window's slug is the caller ("+tapi" for tapi chat).
// An agent runs in Claude Code's sandbox, which reaches pekoed's socket but
// not tmux's, so it cannot start a process inside another session's window;
// a process that left its window's tree (double fork, reparented to
// launchd) is "unknown", never another session. A request with the human
// token is "human", unless it comes from a session's agent window (tapi
// chat's included): with sandbox off an agent reads the token too, and what
// it does is still its own (ID). Only the record changes; the token still
// opens the human-only methods. $PEKOE_SESSION is kept as Claimed, only to
// show when it differs.

import (
	"net"
	"sync"

	"pekoe/internal/engine"
)

const (
	callerHuman   = "human"
	callerUnknown = "unknown"
	viaSocket     = "socket"
	viaWeb        = "web"
)

// Caller is who sent one request.
type Caller struct {
	human   bool
	Claimed string // $PEKOE_SESSION as the client sent it
	Via     string // socket | web
	peer    *peer  // nil: in process (tests, the Web UI)
	// done closes when the caller's connection goes (wait stops waiting);
	// nil never closes.
	done <-chan struct{}
}

// humanCaller is a person calling in process: Call, the Web UI.
func humanCaller(via string) *Caller { return &Caller{human: true, Via: via} }

// Human reports whether the request carried the human token.
func (c *Caller) Human() bool { return c.human }

// Pane is the session whose window the caller's process runs in, token or
// not ("" when none): the hooks run outside the agent's sandbox and read
// the token, yet come from the agent's window.
func (c *Caller) Pane() string {
	if c.peer == nil {
		return ""
	}
	return c.peer.slug()
}

// ID is the caller as recorded and matched (a session's parent, the send
// log, wait's mine, a report's by): the window's slug ("+tapi" included)
// when the request has no token, or has it but comes from an agent window;
// else "human" with the token, else "unknown". An agent whose sandbox is
// off reads the token (the hooks read it too), yet the sessions it starts
// are its own, not a person's. The token from a session's other windows is
// a person's: the run window (TUI `!`), the review window (nvim); the dev
// window runs in the hooks sandbox, which cannot read the token.
func (c *Caller) ID() string {
	p := c.Pane()
	switch {
	case c.human && (p == "" || c.peer.window() != engine.WinAgent):
		return callerHuman
	case p != "":
		return p
	}
	return callerUnknown
}

// reservedWindow is the reserved name (session_new.go reservedSlug) the
// caller's ID came from when that ID is its window's slug, not the token
// ("" otherwise): a session named human, unknown or pekoed made before the
// names were reserved, which would read as a person, an unknown caller or
// pekoed itself.
func (c *Caller) reservedWindow() string {
	id := c.ID()
	if id == c.Pane() && reservedSlug(id) && !(c.human && c.peer.window() != engine.WinAgent) {
		return id
	}
	return ""
}

// ClaimedDiff is Claimed when it names someone else than by ("" when it
// agrees or is missing): what a report or an inbox item shows as a warning.
func (c *Caller) ClaimedDiff(by string) string {
	if c.Claimed == "" || c.Claimed == by {
		return ""
	}
	return c.Claimed
}

// peer is one connection's process; its window is looked up once, on the
// first request that needs it.
type peer struct {
	pid  int
	find func(pid int) engine.Pane
	once sync.Once
	s    string // the window's slug ("": none)
	w    string // the window's kind (engine.WinAgent …)
}

func (p *peer) look() {
	p.once.Do(func() {
		if p.pid > 1 && p.find != nil {
			w := p.find(p.pid)
			p.s, p.w = w.Slug, w.Window
		}
	})
}

func (p *peer) slug() string {
	p.look()
	return p.s
}

func (p *peer) window() string {
	if p == nil {
		return ""
	}
	p.look()
	return p.w
}

// fixedPeer is a peer already identified as slug's agent window (tests).
func fixedPeer(slug string) *peer { return fixedWindow(slug, engine.WinAgent) }

// fixedWindow is a peer already identified as one window of slug (tests).
func fixedWindow(slug, window string) *peer {
	p := &peer{s: slug}
	if slug != "" {
		p.w = window
	}
	p.once.Do(func() {})
	return p
}

// peerPID is the connecting process of a socket connection (0: not known;
// an in-memory pipe in the tests). Tests swap Daemon.peerPID.
func peerPID(c net.Conn) int {
	pid, err := sockPeerPID(c)
	if err != nil {
		return 0
	}
	return pid
}

// maxAncestors bounds the walk up the process tree.
const maxAncestors = 64

// paneOf walks from pid up through its parents to the root process of a
// pekoe window and returns that window's slug ("" when none is reached).
func (d *Daemon) paneOf(pid int) string { return d.windowOf(pid).Slug }

// windowOf is paneOf with the window's kind (the zero Pane when none is
// reached).
func (d *Daemon) windowOf(pid int) engine.Pane {
	panes, err := d.Eng.Panes()
	if err != nil || len(panes) == 0 {
		return engine.Pane{}
	}
	// a dead pane's pid is only the one its process had, and the system
	// may since have given it to any process: never a root
	roots := make(map[int]engine.Pane, len(panes))
	for _, p := range panes {
		if !p.Dead {
			roots[p.PID] = p
		}
	}
	for i := 0; i < maxAncestors && pid > 1; i++ {
		pp, err := engine.ParentPID(pid)
		if err != nil || pp == pid {
			return engine.Pane{}
		}
		// a pane's root process is its server's child; a process that
		// got the pid after the pane's process ended (before tmux shows
		// the pane dead) is not
		if w, ok := roots[pid]; ok && w.Server > 1 && pp == w.Server {
			return w
		}
		pid = pp
	}
	return engine.Pane{}
}
