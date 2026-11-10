package daemon

import (
	"strings"
	"sync"
	"testing"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// An OSC 9 split across two agent chunks still reaches the terminal when a
// dev server chunk arrives in between (the carry was shared by all windows,
// so the first half was glued to the dev output and lost).
func TestOSCCarryIsPerWindow(t *testing.T) {
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	var mu sync.Mutex
	n := 0
	d.testHook = func(ev string, data any) {
		if ev == "term.passthrough" {
			mu.Lock()
			n++
			mu.Unlock()
		}
	}
	d.onOutput(engine.Output{Slug: s.Slug, Window: engine.WinAgent, Data: []byte("hi \x1b]9;done")})
	// a long dev log chunk (past the carry limit) or one with a BEL ends a
	// shared carry inside the dev window
	d.onOutput(engine.Output{Slug: s.Slug, Window: engine.WinDev, Data: []byte(strings.Repeat("GET / 200\n", 500))})
	d.onOutput(engine.Output{Slug: s.Slug, Window: engine.WinAgent, Data: []byte(" now\x07 more")})
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Fatalf("notification passed through %d times, want 1", n)
	}
}
