package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pekoe/internal/state"
)

// lateTranscript is a transcript that has no assistant line yet: a Stop
// that names it (with no last_assistant_message) waits for its reply
// (stopMessage) until reply writes one, so the hooks sent in between come
// while that Stop is still settling, whatever the timing.
func lateTranscript(t *testing.T) (path string, reply func(text string)) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, func(text string) {
		t.Helper()
		line, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
			"role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}}})
		if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func stopsSettled(t *testing.T, d *Daemon) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); d.stopping.Load() != 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the Stop hook is still being handled")
		}
	}
}

// The Stop of a turn may settle late (onStop waits up to 2 s for the
// transcript). A turn that begins meanwhile (the person, pekoe send, an
// inbox answer: UserPromptSubmit) or goes on (another Stop hook sent the
// agent back to work: PreToolUse) stays running: done would free its
// max_running slot, let the inbox type into it, and tell wait it is over.
// The late Stop's reply still becomes the last message; the turn's own
// Stop ends it.
func TestLateStopKeepsNextTurn(t *testing.T) {
	for _, next := range []struct {
		hook    string
		payload map[string]any
	}{
		{"UserPromptSubmit", map[string]any{"session_id": "abc", "prompt": "second"}},
		{"PreToolUse", map[string]any{"session_id": "abc", "tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}}},
	} {
		t.Run(next.hook, func(t *testing.T) {
			d, _, repo := setup(t)
			call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
			event(t, d, "SessionStart", "main", map[string]any{"session_id": "abc", "cwd": repo})
			event(t, d, "UserPromptSubmit", "main", map[string]any{"session_id": "abc", "prompt": "first"})
			tr, reply := lateTranscript(t)
			event(t, d, "Stop", "main", map[string]any{"session_id": "abc", "transcript_path": tr})
			event(t, d, next.hook, "main", next.payload)
			reply("first done")
			stopsSettled(t, d)
			s, _ := d.snapshot("main")
			if s.State != state.Running {
				t.Fatalf("after %s the turn is going on, but state = %s (the earlier Stop overwrote it)", next.hook, s.State)
			}
			if s.LastMessage != "first done" {
				t.Errorf("last message %q, want the late Stop's reply", s.LastMessage)
			}
			stop(t, d, "main", map[string]any{"session_id": "abc", "last_assistant_message": "second done"})
			if s, _ := d.snapshot("main"); s.State != state.Done || s.LastMessage != "second done" {
				t.Fatalf("the turn's own Stop: %s %q, want done", s.State, s.LastMessage)
			}
		})
	}
}
