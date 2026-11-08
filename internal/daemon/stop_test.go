package daemon

import (
	"strings"
	"testing"
)

// daemon.stop keeps the agents (the tmux server) by default; kill_agents
// also stops the server, for throwaway sandboxes.
func TestStopKillAgents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params any
		kill   bool
	}{
		{"default keeps agents", nil, false},
		{"explicit false keeps agents", map[string]any{"kill_agents": false}, false},
		{"kill_agents", map[string]any{"kill_agents": true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, fe, _ := setup(t)
			call[any](t, d, "daemon.stop", tc.params)
			d.shutdown()
			if got := strings.Contains(fe.last(), "killserver"); got != tc.kill {
				t.Fatalf("KillServer called = %v, want %v (sent: %q)", got, tc.kill, fe.last())
			}
		})
	}
}
