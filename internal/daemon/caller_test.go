package daemon

import (
	"net"
	"os"
	"testing"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// The token says "human" only outside a session's agent window: an agent
// whose sandbox is off reads the token, and what it starts is still its
// own. A person's windows of a session (run, review) stay human.
func TestCallerID(t *testing.T) {
	cases := []struct {
		name string
		who  *Caller
		want string
	}{
		{"token, agent window", &Caller{human: true, peer: fixedPeer("feat-x")}, "feat-x"},
		{"token, tapi chat's window", &Caller{human: true, peer: fixedPeer(TapiSlug)}, TapiSlug},
		{"token, run window (TUI !)", &Caller{human: true, peer: fixedWindow("feat-x", engine.WinRun)}, callerHuman},
		{"token, review window (nvim)", &Caller{human: true, peer: fixedWindow("feat-x", engine.WinReview)}, callerHuman},
		{"token, dev window", &Caller{human: true, peer: fixedWindow("feat-x", engine.WinDev)}, callerHuman},
		{"token, no window", &Caller{human: true, peer: fixedPeer("")}, callerHuman},
		{"token, in process (Web UI)", humanCaller(viaWeb), callerHuman},
		{"no token, agent window", agentAs("feat-x"), "feat-x"},
		{"no token, run window", &Caller{peer: fixedWindow("feat-x", engine.WinRun)}, "feat-x"},
		{"no token, no window", &Caller{peer: fixedPeer("")}, callerUnknown},
	}
	for _, c := range cases {
		if got := c.who.ID(); got != c.want {
			t.Errorf("%s: ID() = %q, want %q", c.name, got, c.want)
		}
		// the token still opens the human-only methods, wherever it comes from
		if c.who.human && !c.who.Human() {
			t.Errorf("%s: lost the token", c.name)
		}
	}
}

// The window's kind comes from the process tree with its slug.
func TestWindowOfGivesKind(t *testing.T) {
	d, fe, _ := setup(t)
	fe.panes["feat-x~run"] = os.Getpid()
	if w := d.windowOf(childOfTest(t)); w.Slug != "feat-x" || w.Window != engine.WinRun {
		t.Fatalf("run window: %+v", w)
	}
	fe.panes = map[string]int{"+tapi~agent": os.Getpid()}
	if w := d.windowOf(childOfTest(t)); w.Slug != TapiSlug || w.Window != engine.WinAgent {
		t.Fatalf("tapi's window: %+v", w)
	}
}

// tapi chat with sandbox off reads the human token: the sessions it starts
// are still tapi's (parent, send log, wait --mine), not a person's.
func TestTokenFromAgentWindowIsTheWindow(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	tapi := &Caller{human: true, Via: viaSocket, peer: fixedPeer(TapiSlug)}
	s := callAs[state.Session](t, d, tapi, "session.new", map[string]any{"repo": repo, "branch": "feat/t", "prompt": "go"})
	if s.Parent != TapiSlug {
		t.Fatalf("parent: %q", s.Parent)
	}
	run := &Caller{human: true, Via: viaSocket, peer: fixedWindow("main", engine.WinRun)}
	h := callAs[state.Session](t, d, run, "session.new", map[string]any{"repo": repo, "branch": "feat/h"})
	if h.Parent != callerHuman {
		t.Fatalf("a person's `!` in a run window: %q", h.Parent)
	}
	callAs[any](t, d, tapi, "session.send", map[string]any{"slug": "main", "text": "x", "submit": true})
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	got := ""
	for _, r := range rs {
		got += r.From + ">" + r.To + ":" + r.Kind + " "
	}
	if got != "+tapi>main:send human>feat-h:new +tapi>feat-t:new " {
		t.Fatalf("send log: %s", got)
	}
	// the human-only methods still take the token from there (authorization
	// is unchanged): send.log is not cut down to tapi's own
	if all := callAs[[]state.SendRecord](t, d, tapi, "send.log", nil); len(all) != len(rs) {
		t.Fatalf("send.log with the token: %d of %d", len(all), len(rs))
	}

	// wait --mine: a person's session is not tapi's
	w0 := callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "timeout_s": 1})
	d.setState(h.Slug, state.Done, "")
	if w := callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "since": w0.Cursor, "timeout_s": 1}); !w.TimedOut {
		t.Fatalf("a person's session came to tapi: %+v", w)
	}
	d.setState(s.Slug, state.Done, "")
	if w := callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "since": w0.Cursor, "timeout_s": 1}); w.kinds() != s.Slug+":done" {
		t.Fatalf("tapi's session: %+v", w)
	}
}

// Over the socket: a request with the token from a process in tapi's agent
// window records +tapi; from outside every window, human; without the
// token from outside, unknown (send.log refuses it).
func TestTokenOverSocketByWindow(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	pid := childOfTest(t)
	d.peerPID = func(net.Conn) int { return pid }
	send := func(text string) {
		c := dialPipe(t, d)
		if e := c.call("session.send", map[string]any{"slug": "main", "text": text, "submit": true}, d.humanToken); e != "" {
			t.Fatal(e)
		}
	}
	fe.panes["+tapi~agent"] = os.Getpid()
	send("from tapi")
	fe.panes = map[string]int{}
	send("from a terminal")
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	if len(rs) != 2 || rs[0].From != callerHuman || rs[1].From != TapiSlug {
		t.Fatalf("send log: %+v", rs)
	}
	if e := dialPipe(t, d).call("send.log", nil, ""); e == "" {
		t.Fatal("no token, no window: send.log for unknown")
	}
}

// Hooks read the token and come from the agent's window: they still go to
// that window's session, as before.
func TestHookWithTokenFromAgentWindow(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	who := &Caller{human: true, Via: viaSocket, peer: fixedPeer("main")}
	callAs[any](t, d, who, "session.event", map[string]any{"hook": "UserPromptSubmit", "session": "bogus", "payload": map[string]any{"prompt": "x", "cwd": "/"}})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.State != state.Running {
		t.Fatalf("hook with the token from main's window: %+v", s.State)
	}
	r := callAs[state.Report](t, d, who, "report.post", map[string]any{"summary": "done", "status": "done"})
	if r.Slug != "main" || r.By != "main" {
		t.Fatalf("report with the token from main's window: %+v", r)
	}
}

// A dead pane keeps the pid its process had, and the system may give that
// pid to any process: a process under it is not the window's.
func TestWindowOfSkipsDeadPane(t *testing.T) {
	d, fe, _ := setup(t)
	fe.panes[TapiSlug+"~agent"] = os.Getpid()
	fe.dead[TapiSlug+"~agent"] = true
	if w := d.windowOf(childOfTest(t)); w.Slug != "" {
		t.Fatalf("dead pane's old pid identified the caller: %+v", w)
	}
	delete(fe.dead, TapiSlug+"~agent")
	if w := d.windowOf(childOfTest(t)); w.Slug != TapiSlug {
		t.Fatalf("live pane: %+v", w)
	}
}

// A process that got a pane's pid but is not the server's child (the
// pane's process ended and tmux does not show it dead yet, or the server
// was started anew) is not the pane's root.
func TestWindowOfNeedsServersChild(t *testing.T) {
	d, fe, _ := setup(t)
	pid := childOfTest(t) // a child of the test process, not of the "server" (its parent)
	fe.panes["feat-x~agent"] = pid
	if w := d.windowOf(pid); w.Slug != "" {
		t.Fatalf("reused pid identified the caller: %+v", w)
	}
}
