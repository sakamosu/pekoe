package daemon

import (
	"strings"
	"testing"

	"pekoe/internal/state"
)

// condOf is the merge condition name of c.
func condOf(t *testing.T, c state.MergeCheck, name string) state.MergeCond {
	t.Helper()
	for _, x := range c.Conds {
		if x.Name == name {
			return x
		}
	}
	t.Fatalf("no %s in %+v", name, c)
	return state.MergeCond{}
}

// A session it comes after that landed and was then removed (pekoe rm) is
// met, also across a restart of pekoed; one removed before it landed is
// not (is gone).
func TestMergeAfterLandedThenRemoved(t *testing.T) {
	d, fe := mergeSetup(t)
	call[any](t, d, "worktree.rm", map[string]any{"slug": "feat-a", "force": true})
	if _, err := d.snapshot("feat-a"); err == nil {
		t.Fatal("feat-a still there")
	}
	c := mergeCheckOf(t, d)
	if a := condOf(t, c, "after"); !a.OK || !strings.Contains(a.Detail, "feat-a landed and was removed") || !c.Ready {
		t.Fatalf("after: %+v", c)
	}
	d2 := restartWith(t, d, fe)
	c = mergeCheckOf(t, d2)
	if a := condOf(t, c, "after"); !a.OK || !strings.Contains(a.Detail, "feat-a landed and was removed") {
		t.Fatalf("after restart: %+v", c)
	}
}

func TestMergeAfterRemovedBeforeLanding(t *testing.T) {
	d, _ := mergeSetup(t)
	d.setState("feat-a", state.Done, "")
	call[any](t, d, "worktree.rm", map[string]any{"slug": "feat-a", "force": true})
	c := mergeCheckOf(t, d)
	if a := condOf(t, c, "after"); a.OK || a.Detail != "feat-a is gone" {
		t.Fatalf("after: %+v", c)
	}
}

// A new session made with the slug of one that landed and was removed is
// judged by its own state; when it goes too before landing, the old
// record does not count.
func TestMergeAfterRemadeSameSlug(t *testing.T) {
	d, _ := mergeSetup(t)
	call[any](t, d, "worktree.rm", map[string]any{"slug": "feat-a", "force": true})
	repo := d.snapshotMust(t, "main").RepoDir
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a"})
	waitCreated(t, d, "feat-a")
	d.setState("feat-a", state.Done, "")
	c := mergeCheckOf(t, d)
	if a := condOf(t, c, "after"); a.OK || a.Detail != "feat-a is done" {
		t.Fatalf("remade: %+v", c)
	}
	call[any](t, d, "worktree.rm", map[string]any{"slug": "feat-a", "force": true})
	c = mergeCheckOf(t, d)
	if a := condOf(t, c, "after"); a.OK || a.Detail != "feat-a is gone" {
		t.Fatalf("remade, removed: %+v", c)
	}
}

func (d *Daemon) snapshotMust(t *testing.T, slug string) *state.Session {
	t.Helper()
	s, err := d.snapshot(slug)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
