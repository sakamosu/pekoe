package daemon

import (
	"testing"

	"pekoe/internal/state"
)

// A new session that takes the slug of a landed one starts clean: the
// landed session's prompts, comments and risks are not inherited (undo
// would pick its last prompt, whose snapshot ref is gone).
func TestNewOverLandedSlugStartsClean(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	for i := 0; i < 100; i++ {
		if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "feat-x"}); s.State != state.Creating {
			break
		}
		sleep()
	}
	for i := 1; i <= 3; i++ {
		if err := d.DB.AddPrompt("feat-x", i, "p", "refs/pekoe/feat-x/p"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.DB.AddComment("feat-x", state.Comment{Path: "a.txt", Line: 1, Text: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.SetRisks("feat-x", []state.Risk{{Path: "a.txt", Text: "old"}}); err != nil {
		t.Fatal(err)
	}
	d.update("feat-x", func(x *state.Session) { x.State = state.Landed; x.Prompts = 3 })

	n := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x"})
	if n.Slug != "feat-x" {
		t.Fatalf("slug %s", n.Slug)
	}
	ps, _ := d.DB.Prompts("feat-x")
	for _, p := range ps {
		if p.N > 0 { // #0 is the new session's own start snapshot
			t.Fatalf("prompts of the landed session carried over: %+v", ps)
		}
	}
	if cs, _ := d.DB.Comments("feat-x", false); len(cs) != 0 {
		t.Fatalf("comments carried over: %+v", cs)
	}
	if rs, _ := d.DB.Risks("feat-x"); len(rs) != 0 {
		t.Fatalf("risks carried over: %+v", rs)
	}
}
