package daemon

import (
	"path/filepath"
	"strings"
	"testing"

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
