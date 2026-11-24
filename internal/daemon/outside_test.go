package daemon

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

// eventAs sends one hook as who (nil: a process in no pekoe window).
func eventAs(t *testing.T, d *Daemon, who *Caller, hook, claimed string, payload map[string]any) {
	t.Helper()
	if who == nil {
		who = &Caller{Via: viaSocket}
	}
	payload["hook_event_name"] = hook
	callAs[map[string]any](t, d, who, "session.event", map[string]any{"hook": hook, "session": claimed, "payload": payload})
}

func bashTool(cmd string) map[string]any {
	return map[string]any{"command": cmd}
}

// Claude Code moved tapi's conversation into a background session of its
// own service (← on an empty prompt), which runs outside every pekoe window
// with the environment of the window that first started the service:
// $PEKOE_SESSION names another session. The hooks belong to tapi (the
// conversation's file is in tapi's project directory), not to that
// session, and tapi says it runs outside its window.
func TestHookOutsideWindowFollowsConversation(t *testing.T) {
	d, _, repo := setup(t)
	main := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	call[Tapi](t, d, "tapi.open", nil)
	const bg = "c6270ea4-8ca2-41a2-8b1b-2addbec46356"
	tapiConv := filepath.Join(tapiProjectDirs()[0], bg+".jsonl")

	eventAs(t, d, nil, "PreToolUse", "main", map[string]any{
		"session_id": bg, "transcript_path": tapiConv, "cwd": repo,
		"tool_name": "Bash", "tool_input": bashTool("pekoe --json ledger"),
	})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.LastTool != "" || s.ClaudeSessionID == bg {
		t.Fatalf("tapi's hook landed on main: %+v", s)
	}
	v := call[Tapi](t, d, "tapi.get", nil)
	if v.PendingTool == nil || !strings.Contains(v.PendingTool.Input, "pekoe --json ledger") {
		t.Fatalf("tapi did not get its hook: %+v", v)
	}
	if v.Outside == nil || v.Outside.ClaudeSessionID != bg || v.Outside.Claimed != "main" || v.Outside.Window != "" {
		t.Fatalf("outside not noted: %+v", v.Outside)
	}

	// a conversation of somewhere else entirely (any background session
	// inherits the same environment): not main's, whatever it claims
	other := filepath.Join(claudeProjectDir("/somewhere/else"), "0a0b0c0d-1111-2222-3333-444455556666.jsonl")
	eventAs(t, d, nil, "PreToolUse", "main", map[string]any{
		"transcript_path": other, "cwd": "/somewhere/else", "tool_name": "Bash", "tool_input": bashTool("make"),
	})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.LastTool != "" {
		t.Fatalf("a stranger's hook landed on main: %+v", s)
	}

	// main's own conversation outside its window (an adopted agent) still
	// counts as main
	own := filepath.Join(claudeProjectDir(main.Path), "0a0b0c0d-aaaa-bbbb-cccc-444455556666.jsonl")
	eventAs(t, d, nil, "PreToolUse", "main", map[string]any{
		"transcript_path": own, "cwd": repo, "tool_name": "Bash", "tool_input": bashTool("go test"),
	})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.LastTool != "Bash go test" {
		t.Fatalf("main's own hook: %+v", s)
	}
	// and so does one that names nobody: the conversation says main
	eventAs(t, d, nil, "PreToolUse", "", map[string]any{
		"transcript_path": own, "cwd": "/tmp", "tool_name": "Bash", "tool_input": bashTool("go vet"),
	})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.LastTool != "Bash go vet" {
		t.Fatalf("main's unnamed hook: %+v", s)
	}

	// restarting tapi forgets it; the conversation is noted again if it is
	// still out there
	call[Tapi](t, d, "tapi.restart", nil)
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside != nil {
		t.Fatalf("outside kept after restart: %+v", v.Outside)
	}
	// from another session's window: the window decides (a process cannot
	// pass itself off as another), and tapi still says where it runs
	eventAs(t, d, agentAs("main"), "PreToolUse", "main", map[string]any{
		"session_id": bg, "transcript_path": tapiConv, "cwd": repo, "tool_name": "Bash", "tool_input": bashTool("ls"),
	})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.LastTool != "Bash ls" {
		t.Fatalf("main's window: %+v", s)
	}
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside == nil || v.Outside.Window != "main" {
		t.Fatalf("outside in main's window: %+v", v.Outside)
	}
	// from tapi's own window it is just tapi
	call[Tapi](t, d, "tapi.restart", nil)
	eventAs(t, d, agentAs(TapiSlug), "PreToolUse", TapiSlug, map[string]any{
		"session_id": bg, "transcript_path": tapiConv, "tool_name": "Bash", "tool_input": bashTool("pwd"),
	})
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside != nil {
		t.Fatalf("tapi's own window taken for outside: %+v", v.Outside)
	}
}

// claimOutside keeps the claim when the conversation's file is not one of
// Claude Code's (none, or a path elsewhere): as before.
func TestClaimOutsideNoTranscript(t *testing.T) {
	d, _, repo := setup(t)
	main := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	for _, tr := range []string{"", filepath.Join(t.TempDir(), "x.jsonl"), "relative.jsonl",
		filepath.Join(claudeProjectsDir(), "a", "b", "0a0b0c0d-1111-2222-3333-444455556666.jsonl")} {
		if got := d.claimOutside("main", tr); got != "main" {
			t.Errorf("%q: %q", tr, got)
		}
	}
	if got := d.claimOutside(TapiSlug, filepath.Join(claudeProjectDir(main.Path), "0a0b0c0d-1111-2222-3333-444455556666.jsonl")); got != "main" {
		t.Errorf("+tapi naming main's conversation: %q", got)
	}
}

// The ledger's refusal of an unknown caller says why (tapi run outside its
// window) and what to do; a known one is just refused.
func TestLedgerRefusalExplainsOutside(t *testing.T) {
	d, _, _ := setup(t)
	err := callAsErr(d, &Caller{Via: viaSocket, Claimed: "pekoe-theme"}, "ledger.list", nil)
	if err == nil || !strings.Contains(err.Error(), "not unknown") || !strings.Contains(err.Error(), "background session") || !strings.Contains(err.Error(), "pekoe tapi reset") {
		t.Fatalf("unknown: %v", err)
	}
	err = callAsErr(d, agentAs("main"), "ledger.list", nil)
	if err == nil || strings.Contains(err.Error(), "background session") {
		t.Fatalf("a session's window: %v", err)
	}
}

// pekoe starts Claude Code with agent view off, so ← cannot carry the
// conversation out of its window; other agents are left alone, and a value
// the repository sets is kept.
func TestClaudeStaysInWindow(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	call[Tapi](t, d, "tapi.open", nil)
	for _, slug := range []string{"main", TapiSlug} {
		if env := strings.Join(fe.spawned(slug, engine.WinAgent).Env, " "); !strings.Contains(env, "CLAUDE_CODE_DISABLE_AGENT_VIEW=1") {
			t.Errorf("%s: %s", slug, env)
		}
	}
	if env := claudeStayEnv("codex", []string{"A=1"}); len(env) != 1 {
		t.Errorf("codex: %v", env)
	}
	if env := claudeStayEnv("claude --permission-mode plan", []string{"CLAUDE_CODE_DISABLE_AGENT_VIEW="}); len(env) != 1 {
		t.Errorf("repository's value: %v", env)
	}
}

// Resetting or restarting a live tapi kills its window. Claude Code
// catches the SIGHUP and runs its SessionEnd hook after the window is gone
// (2.1.326 with tmux kill-window): from no pekoe window, naming +tapi and
// tapi's transcript. That is the agent pekoe killed itself, not tapi's
// conversation running outside its window.
func TestTapiKilledSessionEndIsNotOutside(t *testing.T) {
	for _, how := range []string{"tapi.reset", "tapi.restart"} {
		t.Run(how, func(t *testing.T) {
			d, _, _ := setup(t)
			call[Tapi](t, d, "tapi.open", nil)
			const sid = "c6270ea4-8ca2-41a2-8b1b-2addbec46356"
			conv := filepath.Join(tapiProjectDirs()[0], sid+".jsonl")
			eventAs(t, d, agentAs(TapiSlug), "SessionStart", TapiSlug, map[string]any{"session_id": sid, "transcript_path": conv})

			call[Tapi](t, d, how, nil)
			// the killed Claude Code's SessionEnd, from a process no window holds
			eventAs(t, d, nil, "SessionEnd", TapiSlug, map[string]any{"session_id": sid, "transcript_path": conv, "reason": "other"})
			if v := call[Tapi](t, d, "tapi.get", nil); v.Outside != nil {
				t.Fatalf("warns that the agent pekoe killed runs outside tapi's window: %+v", v.Outside)
			}

			// another conversation of tapi's out there is still told
			const other = "c6270ea4-8ca2-41a2-8b1b-2addbec46357"
			eventAs(t, d, nil, "PreToolUse", "main", map[string]any{"session_id": other, "transcript_path": filepath.Join(tapiProjectDirs()[0], other+".jsonl"), "tool_name": "Bash"})
			if v := call[Tapi](t, d, "tapi.get", nil); v.Outside == nil || v.Outside.ClaudeSessionID != other {
				t.Fatalf("outside not told: %+v", v.Outside)
			}
		})
	}
}

// Only lately: a conversation pekoe killed long ago that shows up outside
// tapi's window again is told.
func TestTapiKilledLongAgo(t *testing.T) {
	d, _, _ := setup(t)
	call[Tapi](t, d, "tapi.open", nil)
	const sid = "c6270ea4-8ca2-41a2-8b1b-2addbec46356"
	conv := filepath.Join(tapiProjectDirs()[0], sid+".jsonl")
	eventAs(t, d, agentAs(TapiSlug), "SessionStart", TapiSlug, map[string]any{"session_id": sid, "transcript_path": conv})
	call[Tapi](t, d, "tapi.restart", nil)
	d.mu.Lock()
	d.tapiKilled.at = time.Now().Add(-tapiKillGrace)
	d.mu.Unlock()
	eventAs(t, d, nil, "PreToolUse", TapiSlug, map[string]any{"session_id": sid, "transcript_path": conv, "tool_name": "Bash"})
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside == nil {
		t.Fatal("outside not told")
	}
}

// `pekoe event MessageDisplay` writes its line and closes without waiting
// (read must not hold the display up), so pekoed often finds no caller
// left to look up: the hook comes from no window. That is no sign of tapi's
// conversation running outside its window (p1353: outside came with the
// first MessageDisplay of every conversation and stayed until a restart).
func TestTapiMessageDisplayIsNotOutside(t *testing.T) {
	d, _, _ := setup(t)
	call[Tapi](t, d, "tapi.open", nil)
	const sid = "c6270ea4-8ca2-41a2-8b1b-2addbec46356"
	conv := filepath.Join(tapiProjectDirs()[0], sid+".jsonl")
	eventAs(t, d, agentAs(TapiSlug), "SessionStart", TapiSlug, map[string]any{"session_id": sid, "transcript_path": conv})

	eventAs(t, d, nil, "MessageDisplay", TapiSlug, map[string]any{
		"session_id": sid, "transcript_path": conv, "turn_id": "t1", "message_id": "m1", "index": 0, "final": true, "delta": "hi",
	})
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside != nil {
		t.Fatalf("MessageDisplay with no caller taken for outside: %+v", v.Outside)
	}
	// from another session's window as well: read needs no window
	eventAs(t, d, agentAs("main"), "MessageDisplay", "main", map[string]any{
		"session_id": sid, "transcript_path": conv, "turn_id": "t1", "message_id": "m1", "index": 1, "final": true, "delta": "hi",
	})
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside != nil {
		t.Fatalf("MessageDisplay from another window taken for outside: %+v", v.Outside)
	}

	// any other hook of tapi's conversation from no window is still told
	for _, hook := range []string{"UserPromptSubmit", "Stop"} {
		call[Tapi](t, d, "tapi.restart", nil)
		d.mu.Lock()
		d.tapiKilled.at = time.Now().Add(-tapiKillGrace) // not the agent pekoe killed
		d.mu.Unlock()
		eventAs(t, d, nil, hook, TapiSlug, map[string]any{"session_id": sid, "transcript_path": conv, "prompt": "x"})
		if v := call[Tapi](t, d, "tapi.get", nil); v.Outside == nil || v.Outside.ClaudeSessionID != sid {
			t.Fatalf("%s outside tapi's window not told: %+v", hook, v.Outside)
		}
	}
}

// A hook of the conversation noted outside that comes from tapi's own
// window clears it (it is back, or the note was stale) and says so; one of
// another conversation does not (the one outside may still run there).
func TestTapiOutsideClearedFromWindow(t *testing.T) {
	d, _, _ := setup(t)
	call[Tapi](t, d, "tapi.open", nil)
	const sid = "c6270ea4-8ca2-41a2-8b1b-2addbec46356"
	const other = "c6270ea4-8ca2-41a2-8b1b-2addbec46357"
	conv := filepath.Join(tapiProjectDirs()[0], sid+".jsonl")
	eventAs(t, d, nil, "PreToolUse", "main", map[string]any{"session_id": sid, "transcript_path": conv, "tool_name": "Bash"})
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside == nil {
		t.Fatal("outside not told")
	}

	var mu sync.Mutex
	var told []*Tapi
	d.testHook = func(ev string, data any) {
		if v, ok := data.(*Tapi); ok && ev == "tapi.updated" {
			mu.Lock()
			told = append(told, v)
			mu.Unlock()
		}
	}
	eventAs(t, d, agentAs(TapiSlug), "PreToolUse", TapiSlug, map[string]any{
		"session_id": other, "transcript_path": filepath.Join(tapiProjectDirs()[0], other+".jsonl"), "tool_name": "Bash",
	})
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside == nil {
		t.Fatal("another conversation in tapi's window cleared outside")
	}
	// a hook tapi's state does not follow (no tapi.updated of its own)
	eventAs(t, d, agentAs(TapiSlug), "PreCompact", TapiSlug, map[string]any{"session_id": sid, "transcript_path": conv})
	if v := call[Tapi](t, d, "tapi.get", nil); v.Outside != nil {
		t.Fatalf("outside kept after a hook from tapi's window: %+v", v.Outside)
	}
	mu.Lock()
	defer mu.Unlock()
	cleared := false
	for _, v := range told {
		if v.Outside == nil {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("tapi.updated without outside not sent")
	}
}
