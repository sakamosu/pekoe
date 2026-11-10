package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// A review question that was not sent (a permission prompt came up) must
// not make the next ordinary Stop count as its answer.
func TestReviewAskNotSentIsNotAnswered(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "edit"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\nX\n3\n"), 0o644)
	stop(t, d, "main", map[string]any{})
	rv := call[ReviewView](t, d, "review.files", map[string]any{"slug": "main"})
	hid := rv.Files[0].Hunks[0].ID
	call[any](t, d, "review.start", map[string]any{"slug": "main"})
	answers := make(chan any, 4)
	d.testHook = func(ev string, data any) {
		if ev == "review.answer" {
			answers <- data
		}
	}
	fe.mu.Lock()
	fe.lines = []string{"Do you want to proceed?", "❯ 1. Yes", "  2. No"}
	fe.mu.Unlock()
	if _, err := d.Call("review.ask", mustJSON(map[string]any{"slug": "main", "hunk_id": hid})); err == nil {
		t.Fatal("ask over a permission prompt should fail")
	}
	fe.mu.Lock()
	fe.lines = nil
	fe.mu.Unlock()
	stop(t, d, "main", map[string]any{})
	select {
	case a := <-answers:
		t.Fatalf("an ordinary Stop was taken as the answer: %v", a)
	case <-time.After(200 * time.Millisecond):
	}
}

// While the agent is in a turn, ask is refused: the turn's own Stop would
// be taken as the answer.
func TestReviewAskRefusedWhileRunning(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "edit"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\nX\n3\n"), 0o644)
	stop(t, d, "main", map[string]any{})
	rv := call[ReviewView](t, d, "review.files", map[string]any{"slug": "main"})
	hid := rv.Files[0].Hunks[0].ID
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "more"})
	_, err := d.Call("review.ask", mustJSON(map[string]any{"slug": "main", "hunk_id": hid}))
	if err == nil || !strings.Contains(err.Error(), "middle of a turn") {
		t.Fatalf("want refusal, got %v", err)
	}
}
