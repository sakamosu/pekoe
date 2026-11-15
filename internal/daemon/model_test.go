package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/config"
	"pekoe/internal/state"
)

// [agents.claude] model / effort go on every start as --model / --effort,
// before the prompt's "--", so a /model in some other Claude Code session
// (which rewrites ~/.claude/settings.json) does not change pekoe's agents.
func TestAgentStartPinsModel(t *testing.T) {
	d, fe, repo := setup(t)
	ag := d.Global.Agents["claude"]
	ag.Model, ag.Effort = "claude-opus-5-5", "medium"
	d.Global.Agents["claude"] = ag

	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "do it"})
	waitCreated(t, d, s.Slug)
	cmd := agentCommand(t, fe, s.Slug)
	pin := " '--model' 'claude-opus-5-5' '--effort' 'medium'"
	at, dash := strings.Index(cmd, pin), strings.Index(cmd, " -- '")
	if at < 0 || dash < 0 || at > dash {
		t.Fatalf("--model / --effort missing or after the prompt: %s", cmd)
	}
	call[any](t, d, "session.restart", map[string]any{"slug": s.Slug})
	if cmd := agentCommand(t, fe, s.Slug); !strings.Contains(cmd, pin) {
		t.Fatalf("restart without the model: %s", cmd)
	}

	// pekoe new --model / --effort: this session only, kept on restart
	o := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/y", "model": "sonnet", "effort": "low"})
	waitCreated(t, d, o.Slug)
	if o.Model != "sonnet" || o.Effort != "low" {
		t.Fatalf("override not kept: %+v", o)
	}
	call[any](t, d, "session.restart", map[string]any{"slug": o.Slug})
	if cmd := agentCommand(t, fe, o.Slug); !strings.Contains(cmd, " '--model' 'sonnet' '--effort' 'low'") || strings.Contains(cmd, "opus") {
		t.Fatalf("override: %s", cmd)
	}

	// unset: nothing passed (Claude Code's own default)
	d.Global.Agents["claude"] = config.Agent{Command: "claude", Resume: "claude --resume {{session_id}}", Prompt: "arg", Hooks: true}
	call[any](t, d, "session.restart", map[string]any{"slug": s.Slug})
	if cmd := agentCommand(t, fe, s.Slug); strings.Contains(cmd, "--model") || strings.Contains(cmd, "--effort") {
		t.Fatalf("unset model passed: %s", cmd)
	}
}

func TestNewRefusesBadModel(t *testing.T) {
	d, _, repo := setup(t)
	d.Global.Agents["codex"] = config.Agent{Command: "codex", Prompt: "arg"}
	for _, p := range []map[string]any{
		{"repo": repo, "branch": "feat/a", "model": "opus; rm -rf ~"},
		{"repo": repo, "branch": "feat/b", "effort": "huge"},
		{"repo": repo, "branch": "feat/c", "agent": "codex", "model": "o3"},
	} {
		if err := callErr(d, "session.new", p); err == nil {
			t.Errorf("%v: accepted", p)
		}
	}
	if ss, _ := d.DB.Sessions(); len(ss) != 0 {
		t.Fatalf("sessions made: %d", len(ss))
	}
}

// resume templates and tapi get the model too; other agents never do.
func TestWithModel(t *testing.T) {
	ag := config.Agent{Command: "claude", Model: "claude-opus-5-5", Effort: "medium"}
	resume := strings.ReplaceAll("claude --resume {{session_id}}", "{{session_id}}", shellQuote("abc"))
	if got := withModel(resume, ag, "", ""); got != "claude --resume 'abc' '--model' 'claude-opus-5-5' '--effort' 'medium'" {
		t.Fatalf("resume: %s", got)
	}
	if got := withModel("claude --continue", ag, "claude-opus-5-5[1m]", ""); got != "claude --continue '--model' 'claude-opus-5-5[1m]' '--effort' 'medium'" {
		t.Fatalf("override: %s", got)
	}
	if got := withModel("codex", config.Agent{Command: "codex", Model: "o3"}, "", ""); got != "codex" {
		t.Fatalf("codex: %s", got)
	}
	bad := config.Agent{Command: "claude", Model: "x y", Effort: "huge"}
	if got := withModel("claude", bad, "", ""); got != "claude" {
		t.Fatalf("invalid values passed: %s", got)
	}
}

func TestTapiPinsModel(t *testing.T) {
	d, fe, _ := setup(t)
	ag := d.Global.Agents["claude"]
	ag.Model, ag.Effort = "claude-opus-5-5", "medium"
	d.Global.Agents["claude"] = ag
	if err := d.tapiSpawn(80, 24); err != nil {
		t.Fatal(err)
	}
	if cmd := agentCommand(t, fe, TapiSlug); !strings.Contains(cmd, " '--model' 'claude-opus-5-5' '--effort' 'medium'") {
		t.Fatalf("tapi command: %s", cmd)
	}
}

// last_model is the model of the conversation's last real assistant
// message (not Claude Code's own "<synthetic>" ones).
func TestReadTokensLastModel(t *testing.T) {
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	f := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(f, []byte(`{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10}}}
{"type":"assistant","message":{"model":"claude-fable-5-2","usage":{"input_tokens":20,"output_tokens":5}}}
{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":0}}}
`), 0o600)
	d.readTokens(s.Slug, f)
	got, err := d.snapshot(s.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastModel != "claude-fable-5-2" || got.Tokens != 25 {
		t.Fatalf("last_model %q tokens %d", got.LastModel, got.Tokens)
	}
}
