package daemon

import (
	"strings"
	"testing"

	"pekoe/internal/state"
)

// --after is recorded as slugs (in order, without repeats, <repo>/<slug>
// resolved) and comes back in session.list.
func TestNewAfterRecorded(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a"})
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b"})
	name := waitCreated(t, d, "feat-a").Repo
	waitCreated(t, d, "feat-b")
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/c",
		"after": []string{"feat-b", name + "/feat-a", "feat-b", " "}})
	if strings.Join(s.After, ",") != "feat-b,feat-a" {
		t.Fatalf("after %v", s.After)
	}
	for _, x := range call[[]state.Session](t, d, "session.list", map[string]any{}) {
		if x.Slug == "feat-c" && strings.Join(x.After, ",") != "feat-b,feat-a" {
			t.Fatalf("session.list after %v", x.After)
		}
	}
}

// An unknown slug, the session itself and a cycle are refused, and nothing
// is made.
func TestNewAfterRefused(t *testing.T) {
	d, _, repo := setup(t)
	if err := callErr(d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "after": []string{"nope"}}); err == nil || !strings.Contains(err.Error(), "no such session") {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := d.snapshot("feat-x"); err == nil {
		t.Fatal("refused session was made")
	}
	// A landed, B after A, then a new A after B would be a cycle
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a"})
	waitCreated(t, d, "feat-a")
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/b", "after": []string{"feat-a"}})
	waitCreated(t, d, "feat-b")
	d.update("feat-a", func(x *state.Session) { x.State = state.Landed })
	if err := callErr(d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "after": []string{"feat-a"}}); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("self: %v", err)
	}
	err := callErr(d, "session.new", map[string]any{"repo": repo, "branch": "feat/a", "after": []string{"feat-b"}})
	if err == nil || !strings.Contains(err.Error(), "feat-a → feat-b → feat-a") {
		t.Fatalf("cycle: %v", err)
	}
	if s, _ := d.snapshot("feat-a"); s == nil || s.State != state.Landed {
		t.Fatalf("the landed session was replaced: %+v", s)
	}
	many := make([]string, 0, afterMax+1)
	for i := 0; i <= afterMax; i++ {
		many = append(many, "feat-a", "feat-b")
	}
	if err := callErr(d, "session.new", map[string]any{"repo": repo, "branch": "feat/y", "after": many}); err != nil {
		t.Fatalf("repeats count once: %v", err)
	}
}
