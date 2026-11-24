package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// convLine is a transcript line with a message: a conversation to continue.
const convLine = `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"

// queueOnly is what Claude Code leaves when a background task's result
// arrives after its conversation was killed (tapi reset): no message.
const queueOnly = `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-24T00:00:00Z","sessionId":"x","content":"pekoe wait done"}` + "\n" +
	`{"type":"queue-operation","operation":"dequeue","timestamp":"2026-11-24T00:00:01Z","sessionId":"x"}` + "\n"

// A transcript with only queue-operation lines is no conversation: tapi
// starts afresh (--continue would end with "No conversation found to
// continue", on every start), and a session open offers nothing to resume.
func TestTapiQueueOnlyTranscriptStartsAfresh(t *testing.T) {
	d, fe, repo := setup(t)
	proj := claudeProjectDir(paths.TapiDir())
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, "c6270ea4-8ca2-41a2-8b1b-2addbec46356.jsonl"), []byte(queueOnly), 0o644)
	call[Tapi](t, d, "tapi.open", nil)
	if c := fe.spawned(TapiSlug, engine.WinAgent).Command; strings.Contains(c, "--continue") {
		t.Fatalf("continued a transcript with no message: %s", c)
	}

	// a message further down makes it a conversation
	os.WriteFile(filepath.Join(proj, "c6270ea4-8ca2-41a2-8b1b-2addbec46356.jsonl"), []byte(queueOnly+`{"type":"file-history-snapshot"}`+"\n"+convLine), 0o644)
	call[Tapi](t, d, "tapi.restart", nil)
	if c := fe.spawned(TapiSlug, engine.WinAgent).Command; !strings.Contains(c, "--continue") {
		t.Fatalf("conversation left, but not continued: %s", c)
	}

	// session.open: only transcripts with a message are offered
	own := claudeProjectDir(realpath(repo))
	os.MkdirAll(own, 0o755)
	os.WriteFile(filepath.Join(own, "empty-1.jsonl"), []byte(queueOnly), 0o644)
	if ps := pastClaudeSessions(realpath(repo)); len(ps) != 0 {
		t.Fatalf("offered a transcript with no message: %+v", ps)
	}
	os.WriteFile(filepath.Join(own, "conv-1.jsonl"), []byte(`{"type":"assistant","message":{"role":"assistant","content":"yo"}}`+"\n"), 0o644)
	if ps := pastClaudeSessions(realpath(repo)); len(ps) != 1 || ps[0].ID != "conv-1" {
		t.Fatalf("past = %+v, want conv-1 only", ps)
	}
}

// addDirScreen is Claude Code 2.1.331 asking about a directory given with
// --add-dir that no session has worked in.
func addDirScreen(dir string) []string {
	return []string{
		" Adding a new directory:",
		" " + dir,
		"",
		" This session hasn't worked here before. Is this a directory you created or one you trust?",
		"",
		" ❯ No, exit",
		"   Yes, add it",
		"",
		" Enter to confirm · Esc to cancel",
	}
}

// tapiOutputQuiet: output came from tapi's window and it has gone quiet.
func tapiOutputQuiet(d *Daemon) {
	d.tapiOutput(engine.Output{Slug: TapiSlug, Window: engine.WinAgent, Data: []byte("x")})
	d.mu.Lock()
	d.tapi.lastOut = time.Now().Add(-time.Second)
	d.tapi.checkedAt = time.Now().Add(-2 * time.Second) // the last read came before it
	d.mu.Unlock()
}

// Before tapi's first hook, Claude Code's question about a directory pekoe
// added is answered "Yes, add it"; a directory pekoe did not add, or the
// same words after a hook, get no answer.
func TestTapiAnswersAddDir(t *testing.T) {
	d, fe, _ := setup(t)
	os.MkdirAll(paths.WorktreesRoot(), 0o755)
	call[Tapi](t, d, "tapi.open", nil)

	// a directory pekoe did not add
	fe.mu.Lock()
	fe.lines = addDirScreen("/somewhere/else")
	fe.mu.Unlock()
	tapiOutputQuiet(d)
	n := len(fe.sent)
	d.fallbackDetect()
	if got := sentSince(fe, n); len(got) != 0 {
		t.Fatalf("answered for a directory pekoe did not add: %v", got)
	}

	// the worktrees root pekoe added (a long path wraps on the screen)
	wt := paths.WorktreesRoot()
	fe.mu.Lock()
	fe.lines = addDirScreen(wt[:len(wt)/2] + "\n" + wt[len(wt)/2:])
	fe.lines = strings.Split(strings.Join(fe.lines, "\n"), "\n")
	fe.mu.Unlock()
	tapiOutputQuiet(d)
	n = len(fe.sent)
	d.fallbackDetect()
	if got := sentSince(fe, n); len(got) != 1 || got[0] != "keys:Down,Enter" {
		t.Fatalf("sent %v, want Down Enter", got)
	}
	// no new output: the screen is not read (nor answered) again
	n = len(fe.sent)
	d.fallbackDetect()
	if got := sentSince(fe, n); len(got) != 0 {
		t.Fatalf("answered twice: %v", got)
	}

	// after a hook the same words are only words on a screen
	event(t, d, "SessionStart", TapiSlug, map[string]any{"session_id": "c6270ea4-8ca2-41a2-8b1b-2addbec46356"})
	fe.mu.Lock()
	fe.lines = addDirScreen(wt)
	fe.mu.Unlock()
	tapiOutputQuiet(d)
	n = len(fe.sent)
	d.fallbackDetect()
	if got := sentSince(fe, n); len(got) != 0 {
		t.Fatalf("answered after a hook: %v", got)
	}

	// a restart reads the screen again until its first hook
	call[Tapi](t, d, "tapi.restart", nil)
	tapiOutputQuiet(d)
	n = len(fe.sent)
	d.fallbackDetect()
	if got := sentSince(fe, n); len(got) != 1 || got[0] != "keys:Down,Enter" {
		t.Fatalf("after restart: sent %v, want Down Enter", got)
	}
}

// A session shown Claude Code's new add-dir question waits for a person,
// as with the folder trust dialog: pekoe does not answer it by itself.
func TestSessionAddDirQuestionWaits(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.mu.Lock()
	fe.lines = addDirScreen(repo)
	fe.mu.Unlock()
	d.mu.Lock()
	d.out["main"].lastOut = time.Now().Add(-2 * time.Second)
	d.mu.Unlock()
	n := len(fe.sent)
	d.fallbackDetect()
	if s, _ := d.snapshot("main"); s.State != state.Waiting || s.PendingTool == nil || s.PendingTool.Name != "Trust" {
		t.Fatalf("add-dir question: %s pending %+v, want waiting on Trust", s.State, s.PendingTool)
	}
	if got := sentSince(fe, n); len(got) != 0 {
		t.Fatalf("answered a session's question by itself: %v", got)
	}
	// a person's yes picks "Yes, add it" (the cursor starts on "No, exit")
	call[any](t, d, "session.answer", map[string]any{"slug": "main", "answer": "allow"})
	if got := sentSince(fe, n); len(got) != 1 || got[0] != "keys:Down,Enter" {
		t.Fatalf("allow sent %v, want Down Enter", got)
	}
}

// reset waits for tapi's agent to exit before it forgets the record: what
// Claude Code writes on its way out (a background task's result, even its
// last message) is gone too, and the next open starts afresh.
func TestTapiResetWaitsForTheAgent(t *testing.T) {
	d, fe, _ := setup(t)
	call[Tapi](t, d, "tapi.open", nil)
	proj := claudeProjectDir(paths.TapiDir())
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, "old.jsonl"), []byte(convLine), 0o644)
	fe.onKillWait = func(slug, w string) { // still exiting: it writes once more
		os.MkdirAll(proj, 0o755)
		os.WriteFile(filepath.Join(proj, "old.jsonl"), []byte(convLine+`{"type":"assistant","message":{"role":"assistant","content":"bye"}}`+"\n"), 0o644)
		os.WriteFile(filepath.Join(proj, "late.jsonl"), []byte(queueOnly), 0o644)
	}
	call[Tapi](t, d, "tapi.reset", nil)
	if strings.Join(fe.killWaits, ",") != TapiSlug+"~"+engine.WinAgent {
		t.Fatalf("killWaits = %v", fe.killWaits)
	}
	if _, err := os.Stat(proj); !os.IsNotExist(err) {
		t.Fatalf("record left after reset: %v", err)
	}
	call[Tapi](t, d, "tapi.open", nil)
	if c := fe.spawned(TapiSlug, engine.WinAgent).Command; strings.Contains(c, "--continue") {
		t.Fatalf("continued after reset: %s", c)
	}
}

// A transcript with no message that came after reset anyway (the agent
// outlived the wait) is removed when tapi starts; conversations stay.
func TestTapiSpawnDropsEmptyTranscripts(t *testing.T) {
	d, fe, _ := setup(t)
	proj := claudeProjectDir(paths.TapiDir())
	os.MkdirAll(proj, 0o755)
	empty := filepath.Join(proj, "late.jsonl")
	os.WriteFile(empty, []byte(queueOnly), 0o644)
	call[Tapi](t, d, "tapi.open", nil)
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("empty transcript left: %v", err)
	}
	if c := fe.spawned(TapiSlug, engine.WinAgent).Command; strings.Contains(c, "--continue") {
		t.Fatalf("continued with no conversation: %s", c)
	}
	conv := filepath.Join(proj, "conv.jsonl")
	os.WriteFile(conv, []byte(queueOnly+convLine), 0o644)
	os.WriteFile(empty, []byte(queueOnly), 0o644)
	call[Tapi](t, d, "tapi.restart", nil)
	if _, err := os.Stat(conv); err != nil {
		t.Fatalf("conversation removed: %v", err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("empty transcript left: %v", err)
	}
	if c := fe.spawned(TapiSlug, engine.WinAgent).Command; !strings.Contains(c, "--continue") {
		t.Fatalf("conversation left, but not continued: %s", c)
	}
}
