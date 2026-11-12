package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

func (f *fakeEngine) spawns(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sent {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeEngine) spawned(slug, win string) engine.Spawn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawn[slug+"~"+win]
}

// No session slug can be the reserved name: every slug goes through
// paths.Slug, which never yields "+".
func TestTapiSlugReserved(t *testing.T) {
	for _, b := range []string{"+tapi", "tapi", "@tapi", "feat/+tapi", "+", "a+b"} {
		if s := paths.Slug(b); s == TapiSlug || strings.Contains(s, "+") {
			t.Fatalf("paths.Slug(%q) = %q", b, s)
		}
	}
}

// tapi starts on the first open (never with the daemon), in its own
// directory, with every known repository readable, and once only. A new
// pekoed finds the window the tmux server kept.
func TestTapiOpenStartsOnceAndReconnects(t *testing.T) {
	d, fe, repo := setup(t)
	os.MkdirAll(paths.WorktreesRoot(), 0o755)
	call[any](t, d, "repo.list", map[string]any{"cwd": repo}) // pekoe knows the repository
	if v := call[Tapi](t, d, "tapi.get", nil); v.Started || v.Alive || v.Agent != "claude" {
		t.Fatalf("before open: %+v", v)
	}
	if n := fe.spawns("spawn:"); n != 0 {
		t.Fatalf("spawned before open: %s", fe.last())
	}
	v := call[Tapi](t, d, "tapi.open", map[string]any{"width": 90, "height": 30})
	if !v.Started || !v.Alive || v.State != state.Idle || v.Dir != paths.TapiDir() {
		t.Fatalf("open: %+v", v)
	}
	sp := fe.spawned(TapiSlug, engine.WinAgent)
	if sp.Dir != paths.TapiDir() || sp.Width != 90 || sp.Height != 30 {
		t.Fatalf("spawn: %+v", sp)
	}
	if !strings.Contains(strings.Join(sp.Env, " "), "PEKOE_SESSION="+TapiSlug) {
		t.Fatalf("env: %v", sp.Env)
	}
	for _, want := range []string{"claude --add-dir", shellQuote(realpath(repo)), shellQuote(paths.WorktreesRoot()), "--append-system-prompt"} {
		if !strings.Contains(sp.Command, want) {
			t.Fatalf("command lacks %q: %s", want, sp.Command)
		}
	}
	if strings.Contains(sp.Command, "--continue") {
		t.Fatalf("nothing to continue: %s", sp.Command)
	}
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		b, err := os.ReadFile(filepath.Join(paths.TapiDir(), f))
		if err != nil || !strings.Contains(string(b), realpath(repo)) || !strings.Contains(string(b), "pekoe --json new") || strings.Contains(string(b), "%") {
			t.Fatalf("%s: %v\n%s", f, err, b)
		}
	}
	call[Tapi](t, d, "tapi.open", nil)
	if n := fe.spawns("spawn:agent"); n != 1 {
		t.Fatalf("second open spawned again (%d)", n)
	}
	if ss := call[[]state.Session](t, d, "session.list", nil); len(ss) != 0 {
		t.Fatalf("tapi listed as a session: %+v", ss)
	}

	// pekoed restarts; the tmux server (the fake) keeps the window
	d.DB.Close()
	d.releaseLock()
	d2, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d2.Eng = fe
	t.Cleanup(func() { d2.DB.Close(); d2.releaseLock() })
	d2.restore()
	if v := call[Tapi](t, d2, "tapi.get", nil); !v.Started || !v.Alive {
		t.Fatalf("after restart: %+v", v)
	}
	call[Tapi](t, d2, "tapi.open", nil)
	if n := fe.spawns("spawn:agent"); n != 1 {
		t.Fatalf("reopen after restart spawned again (%d)", n)
	}
	if ss, _ := d2.DB.Sessions(); len(ss) != 0 {
		t.Fatalf("tapi stored in state.db: %+v", ss)
	}
}

// Hooks with $PEKOE_SESSION=+tapi, or from inside tapi's directory, move
// tapi's state and nothing of the sessions'.
func TestTapiHooks(t *testing.T) {
	d, _, repo := setup(t)
	call[any](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	call[Tapi](t, d, "tapi.open", nil)
	var mu sync.Mutex
	var evs []string
	d.testHook = func(ev string, data any) {
		mu.Lock()
		evs = append(evs, ev)
		mu.Unlock()
	}
	tapi := func() Tapi { return call[Tapi](t, d, "tapi.get", nil) }

	event(t, d, "SessionStart", TapiSlug, map[string]any{"session_id": "t1", "cwd": paths.TapiDir()})
	event(t, d, "UserPromptSubmit", TapiSlug, map[string]any{"prompt": "dependabot?"})
	if v := tapi(); v.State != state.Running || v.ClaudeSessionID != "t1" {
		t.Fatalf("prompt: %+v", v)
	}
	// no $PEKOE_SESSION: matched by the cwd
	event(t, d, "PreToolUse", "", map[string]any{"cwd": filepath.Join(paths.TapiDir(), "sub"), "tool_name": "Bash", "tool_input": map[string]any{"command": "gh api x"}})
	event(t, d, "Notification", "", map[string]any{"cwd": paths.TapiDir(), "notification_type": "permission_prompt"})
	if v := tapi(); v.State != state.Waiting || v.WaitKind != "permission" || v.PendingTool == nil || v.PendingTool.Input != "gh api x" {
		t.Fatalf("permission: %+v", v)
	}
	event(t, d, "PostToolUse", TapiSlug, map[string]any{"tool_name": "Bash"})
	event(t, d, "PreToolUse", TapiSlug, map[string]any{"tool_name": "AskUserQuestion"})
	if v := tapi(); v.State != state.Waiting || v.WaitKind != "question" {
		t.Fatalf("question: %+v", v)
	}
	// Claude Code shows the question through its permission dialog (#66)
	event(t, d, "Notification", TapiSlug, map[string]any{"notification_type": "permission_prompt"})
	if v := tapi(); v.State != state.Waiting || v.WaitKind != "question" {
		t.Fatalf("question after permission_prompt: %+v", v)
	}
	// another tool's permission prompt stays a permission
	event(t, d, "PostToolUse", TapiSlug, map[string]any{"tool_name": "AskUserQuestion"})
	event(t, d, "PreToolUse", TapiSlug, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "rm x"}})
	event(t, d, "Notification", TapiSlug, map[string]any{"notification_type": "permission_prompt"})
	if v := tapi(); v.WaitKind != "permission" {
		t.Fatalf("Bash permission: %+v", v)
	}
	event(t, d, "Stop", TapiSlug, map[string]any{"last_assistant_message": "3 alerts"})
	waitFor(t, func() bool { v := tapi(); return v.State == state.Idle && v.LastMessage == "3 alerts" })

	mu.Lock()
	for _, ev := range evs {
		if strings.HasPrefix(ev, "session.") {
			t.Fatalf("tapi hook emitted %s", ev)
		}
	}
	mu.Unlock()
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if s.State != state.Idle || s.Prompts != 0 || s.ClaudeSessionID != "" {
		t.Fatalf("session touched by tapi hooks: %+v", s)
	}
	// the session still gets its own hooks
	event(t, d, "UserPromptSubmit", "", map[string]any{"cwd": repo, "prompt": "hi"})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.State != state.Running {
		t.Fatalf("session hook: %+v", s)
	}
	// a cwd elsewhere under the data directory is not tapi
	res := call[map[string]any](t, d, "session.event", map[string]any{"hook": "UserPromptSubmit", "payload": map[string]any{"cwd": paths.TapiDir() + "-x"}})
	if res["ignored"] != true {
		t.Fatalf("tapi-x matched: %v", res)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	// a deadline in time, not in rounds: under load what it waits for (git
	// several times, a hook) can take seconds
	for end := time.Now().Add(30 * time.Second); !ok(); sleep10ms() {
		if time.Now().After(end) {
			t.Fatal("timed out")
		}
	}
}

// The session.* methods (which the web UI calls) do not reach tapi.
func TestTapiNotASession(t *testing.T) {
	d, _, _ := setup(t)
	call[Tapi](t, d, "tapi.open", nil)
	for _, m := range []string{"session.get", "session.screen", "session.send", "session.keys", "session.kill", "session.answer"} {
		if _, err := d.Call(m, mustJSON(map[string]any{"slug": TapiSlug, "text": "x", "keys": []string{"Enter"}, "answer": "allow"})); err == nil {
			t.Fatalf("%s reached tapi", m)
		}
	}
	if _, err := d.Call("tapi.screen", nil); err != nil {
		t.Fatal(err)
	}
}

// Output of tapi's window is announced as tapi.output, not session.output.
func TestTapiOutput(t *testing.T) {
	d, _, _ := setup(t)
	var evs []string
	d.testHook = func(ev string, data any) { evs = append(evs, ev) }
	d.onOutput(engine.Output{Slug: TapiSlug, Window: engine.WinAgent, Data: []byte("hi")})
	d.flushOutputEvents()
	if strings.Join(evs, ",") != "tapi.output" {
		t.Fatalf("events: %v", evs)
	}
}

// Input goes to tapi's window; reset kills it and forgets the conversation
// (Claude Code's record of tapi's directory, nothing else), so the next
// open starts afresh instead of --continue.
func TestTapiInputRestartReset(t *testing.T) {
	d, fe, _ := setup(t)
	if _, err := d.Call("tapi.send", mustJSON(map[string]any{"text": "x"})); err == nil {
		t.Fatal("send before open")
	}
	// a conversation of tapi's directory is left over: continue it
	proj := claudeProjectDir(paths.TapiDir())
	other := claudeProjectDir(filepath.Join(paths.DataDir(), "other"))
	for _, p := range []string{proj, other} {
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, "c1.jsonl"), []byte("{}\n"), 0o644)
	}
	call[Tapi](t, d, "tapi.open", nil)
	if c := fe.spawned(TapiSlug, engine.WinAgent).Command; !strings.Contains(c, "claude --continue") {
		t.Fatalf("not continued: %s", c)
	}
	call[any](t, d, "tapi.send", map[string]any{"text": "hello", "submit": true})
	call[any](t, d, "tapi.keys", map[string]any{"keys": []string{"Escape"}})
	if l := fe.last(); !strings.HasSuffix(l, "text:hello\nkeys:Enter\nkeys:Escape") {
		t.Fatalf("input: %s", l)
	}

	// the agent exited: the window stays (dead) until restarted
	fe.mu.Lock()
	fe.dead[TapiSlug+"~"+engine.WinAgent] = true
	fe.mu.Unlock()
	if v := call[Tapi](t, d, "tapi.get", nil); !v.Started || v.Alive {
		t.Fatalf("dead: %+v", v)
	}
	call[Tapi](t, d, "tapi.open", nil)
	if n := fe.spawns("spawn:agent"); n != 1 {
		t.Fatal("open restarted a dead window (↵ restarts it)")
	}
	if v := call[Tapi](t, d, "tapi.restart", nil); !v.Alive {
		t.Fatalf("restart: %+v", v)
	}

	v := call[Tapi](t, d, "tapi.reset", nil)
	if v.Started || v.Alive || v.State != state.Idle || v.ClaudeSessionID != "" {
		t.Fatalf("reset: %+v", v)
	}
	if _, err := os.Stat(proj); !os.IsNotExist(err) {
		t.Fatalf("conversation kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "c1.jsonl")); err != nil {
		t.Fatalf("another project removed: %v", err)
	}
	call[Tapi](t, d, "tapi.open", nil)
	if c := fe.spawned(TapiSlug, engine.WinAgent).Command; strings.Contains(c, "--continue") {
		t.Fatalf("continued after reset: %s", c)
	}
}

// Other agents get the command as configured: no Claude Code flags.
func TestTapiOtherAgent(t *testing.T) {
	d, fe, _ := setup(t)
	d.Global.Tapi.Agent = "codex"
	call[Tapi](t, d, "tapi.open", nil)
	if c := fe.spawned(TapiSlug, engine.WinAgent).Command; !strings.HasPrefix(c, "codex;") {
		t.Fatalf("codex command: %s", c)
	}
	d.Global.Tapi.Agent = "nope"
	if _, err := d.Call("tapi.restart", nil); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown agent: %v", err)
	}
	if !isClaude("claude --permission-mode plan") || !isClaude("/opt/bin/claude") || isClaude("claudette") {
		t.Fatal("isClaude")
	}
}

func sleep10ms() { time.Sleep(10 * time.Millisecond) }
