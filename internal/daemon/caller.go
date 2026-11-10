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
// token is "human". $PEKOE_SESSION is kept as Claimed, only to show when it
// differs.

import (
	"net"
	"sync"
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

// ID is the caller as recorded: "human" with the token, else the window's
// slug ("+tapi" included), else "unknown".
func (c *Caller) ID() string {
	if c.human {
		return callerHuman
	}
	if p := c.Pane(); p != "" {
		return p
	}
	return callerUnknown
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
	find func(pid int) string
	once sync.Once
	s    string
}

func (p *peer) slug() string {
	p.once.Do(func() {
		if p.pid > 1 && p.find != nil {
			p.s = p.find(p.pid)
		}
	})
	return p.s
}

// fixedPeer is a peer already identified as slug (tests).
func fixedPeer(slug string) *peer {
	p := &peer{s: slug}
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
func (d *Daemon) paneOf(pid int) string {
	panes, err := d.Eng.Panes()
	if err != nil || len(panes) == 0 {
		return ""
	}
	roots := make(map[int]string, len(panes))
	for _, p := range panes {
		roots[p.PID] = p.Slug
	}
	for i := 0; i < maxAncestors && pid > 1; i++ {
		if s, ok := roots[pid]; ok {
			return s
		}
		pp, err := parentPID(pid)
		if err != nil || pp == pid {
			return ""
		}
		pid = pp
	}
	return ""
}
