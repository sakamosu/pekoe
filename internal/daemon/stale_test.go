package daemon

import (
	"sync"
	"testing"

	"boba/internal/state"
)

// rm racing with background updates (hooks, PR polling, issue matching)
// must not write the removed session back to state.db.
func TestRmRacingUpdateDoesNotResurrect(t *testing.T) {
	d, _, repo := setup(t)
	for i := 0; i < 30; i++ {
		s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					d.update(s.Slug, func(x *state.Session) { x.Tokens++ })
				}
			}()
		}
		call[map[string]any](t, d, "worktree.rm", map[string]any{"slug": s.Slug, "force": true})
		close(stop)
		wg.Wait()
		list, err := d.DB.Sessions()
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range list {
			if x.Slug == s.Slug {
				t.Fatalf("round %d: removed session %s came back in state.db", i, s.Slug)
			}
		}
	}
}
