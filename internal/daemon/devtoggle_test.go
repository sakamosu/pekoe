package daemon

import (
	"net"
	"os"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// Without the human token, dev.toggle starts and stops the dev server of
// the caller's own session only: a process in main's window toggles main,
// another slug and a process outside every window are refused, and nothing
// reaches the engine for them. With the token any session still works.
func TestAgentDevToggleOnlyOwnSession(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	call[state.Session](t, d, "session.open", map[string]any{"path": gitRepo(t, "x", "other"), "resume": "new"})
	writeRepoConfig(t, d, repo, "[hooks]\ndev = \"npm run dev\"\n")
	fe.panes["main~agent"] = os.Getpid()
	pid := childOfTest(t)
	d.peerPID = func(net.Conn) int { return pid }
	c := dialPipe(t, d)

	// its own slug: started, then stopped
	if e := c.call("dev.toggle", map[string]any{"slug": "main"}, ""); e != "" {
		t.Fatalf("own slug: %q", e)
	}
	sp, ok := fe.spawn["main~dev"]
	if !ok || !strings.Contains(sp.Command, "npm run dev") || sp.Dir != call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}).Path {
		t.Fatalf("dev window: %v %+v", ok, sp)
	}
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); !s.Dev {
		t.Fatal("Dev not set")
	}
	if e := c.call("dev.toggle", map[string]any{"slug": "main"}, ""); e != "" {
		t.Fatalf("stop: %q", e)
	}
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.Dev || fe.Alive("main", "dev") {
		t.Fatal("dev server still running")
	}

	before := sentCount(fe)
	// another session's slug, whatever $PEKOE_SESSION says
	if _, e := c.rawCall(map[string]any{"method": "dev.toggle", "session": "other", "params": map[string]any{"slug": "other"}}); !strings.Contains(e, "only its own dev server") {
		t.Fatalf("other slug: %q", e)
	}
	// a process in no pekoe window
	d.peerPID = func(net.Conn) int { return os.Getppid() }
	c2 := dialPipe(t, d)
	for _, slug := range []string{"main", "other"} {
		if e := c2.call("dev.toggle", map[string]any{"slug": slug}, ""); !strings.Contains(e, "cannot tell which session") {
			t.Fatalf("unknown caller, %s: %q", slug, e)
		}
	}
	// tapi chat is not a session
	if err := callAsErr(d, agentAs(TapiSlug), "dev.toggle", map[string]any{"slug": TapiSlug}); err == nil {
		t.Fatal("tapi chat toggled a dev server")
	}
	if _, ok := fe.spawn["other~dev"]; ok {
		t.Fatal("other's dev window spawned")
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("refused calls reached the engine: %v", got)
	}

	// with the token: any session, as before
	if e := c2.call("dev.toggle", map[string]any{"slug": "main"}, d.humanToken); e != "" {
		t.Fatalf("human: %q", e)
	}
	if !fe.Alive("main", "dev") {
		t.Fatal("human toggle did not start main's dev server")
	}
}
