package daemon

import (
	"os"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// rm and a landing stop what the session's windows left running
// (Engine.KillLeftovers), after its windows closed: in pekoe's own
// worktree with the place to go by, in a checkout pekoe did not make
// (session.open on the repository itself) by the environment only.
func TestRmAndLandStopLeftovers(t *testing.T) {
	d, f, repo := setup(t)
	s := newSession(t, d, repo, "feat/x")
	if !s.Managed || s.Path == "" {
		t.Fatalf("expected a managed session: %+v", s)
	}
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": s.Slug, "force": true})
	if want := "leftovers:" + s.Slug + ":" + s.Path; !strings.Contains(f.last(), want) {
		t.Errorf("rm: no %q in\n%s", want, f.last())
	}

	s = newSession(t, d, repo, "feat/y")
	d.update(s.Slug, func(x *state.Session) { x.Landing, x.PR = true, 1 })
	if err := d.finishLanding(s.Slug); err != nil {
		t.Fatal(err)
	}
	if want := "leftovers:" + s.Slug + ":" + s.Path; !strings.Contains(f.last(), want) {
		t.Errorf("land: no %q in\n%s", want, f.last())
	}

	o := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	if o.Managed {
		t.Fatalf("expected an unmanaged session: %+v", o)
	}
	call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": o.Slug})
	if want := "leftovers:" + o.Slug + ":\n"; !strings.Contains(f.last()+"\n", want) {
		t.Errorf("rm of an opened checkout: no %q in\n%s", want, f.last())
	}
}

// pekoed started from a session's window (an agent's pekoe while pekoed
// was down) drops what says so: what it starts and leaves behind must not
// pass for that session's leftovers.
func TestForgetWindowEnv(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/pekoe,1,0")
	t.Setenv("TMUX_PANE", "%3")
	t.Setenv("PEKOE_SESSION", "feat-x")
	t.Setenv("PEKOE_SOCK", "/s")
	ForgetWindowEnv()
	for _, k := range []string{"TMUX", "TMUX_PANE", "PEKOE_SESSION"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%s still set", k, v)
		}
	}
	if os.Getenv("PEKOE_SOCK") != "/s" {
		t.Error("PEKOE_SOCK went too")
	}
}
