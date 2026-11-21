package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pekoe/internal/state"
)

// fakeVersion replaces `claude --version` for a test and empties the
// cache; n counts the runs.
func fakeVersion(t *testing.T, out string, err error) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := runVersion
	runVersion = func(bin string) ([]byte, error) {
		n.Add(1)
		return []byte(out), err
	}
	agentVersions.mu.Lock()
	agentVersions.by = nil
	agentVersions.mu.Unlock()
	t.Cleanup(func() {
		runVersion = old
		agentVersions.mu.Lock()
		agentVersions.by = nil
		agentVersions.mu.Unlock()
	})
	return &n
}

func TestOlderVersion(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"2.1.325", "2.1.330", true},
		{"2.1.330", "2.1.330", false},
		{"2.1.331", "2.1.330", false},
		{"2.1.99", "2.1.100", true}, // numbers, not strings
		{"1.9.999", "2.0.0", true},
		{"2.10.0", "2.9.9", false},
		{"2.1.325", "", false},
		{"", "2.1.330", false},
		{"2.1.325-beta", "2.1.330", false}, // not a plain x.y.z: not compared
		{"2.1", "2.1.330", false},
		{"v2.1.325", "2.1.330", false},
	} {
		if got := olderVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("olderVersion(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestParseInstalled(t *testing.T) {
	for in, want := range map[string]string{
		"2.1.330 (Claude Code)\n":           "2.1.330",
		"  2.1.330 (Claude Code)  \n":       "2.1.330",
		"2.1.330 (Claude Code)\nmore\n":     "2.1.330",
		"2.1.330\n":                         "",
		"2.1.330 (Something else)\n":        "",
		"warning: x\n2.1.330 (Claude Code)": "",
		"":                                  "",
		"2.1.330-rc1 (Claude Code)":         "",
	} {
		if got := parseInstalled([]byte(in)); got != want {
			t.Errorf("parseInstalled(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelName(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5":           "Opus 5.5",
		"claude-fable-5-2":          "Fable 5.2",
		"claude-haiku-5-5-20261001": "Haiku 5.5",
		"claude-3-opus":             "",
		"gpt-5":                     "",
		"":                          "",
	} {
		if got := modelName(in); got != want {
			t.Errorf("modelName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The hooks' permission_mode and effort.level are kept (documented values
// only); a hook without them leaves the last. readTokens takes the
// version from the transcript's lines.
func TestSessionPanelFacts(t *testing.T) {
	n := fakeVersion(t, "2.1.330 (Claude Code)\n", nil)
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	panel := func() PanelInfo {
		t.Helper()
		return call[PanelInfo](t, d, "session.panel", map[string]any{"slug": s.Slug})
	}
	p := panel()
	if !p.Supported || p.Mode != "" || p.Running != "" || p.UpdateInstalled || n.Load() != 0 {
		t.Fatalf("before any hook: %+v (runs %d)", p, n.Load())
	}
	event(t, d, "PreToolUse", s.Slug, map[string]any{"tool_name": "Bash", "permission_mode": "auto", "effort": map[string]any{"level": "high"}})
	event(t, d, "UserPromptSubmit", s.Slug, map[string]any{"prompt": "x"}) // no mode: kept
	event(t, d, "PostToolUse", s.Slug, map[string]any{"tool_name": "Bash", "permission_mode": "manual", "effort": map[string]any{"level": "huge"}})
	f := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(f, []byte(`{"type":"user","version":"2.1.325"}
{"type":"assistant","version":"2.1.325","message":{"model":"claude-opus-5-5","usage":{"input_tokens":115000,"output_tokens":409}}}
{"type":"system","version":"<b>x</b>"}
`), 0o600)
	d.readTokens(s.Slug, f)
	p = panel()
	if p.Mode != "auto" || p.Effort != "high" || p.EffortFrom != "hook" {
		t.Fatalf("mode and effort: %+v", p)
	}
	if p.Model != "claude-opus-5-5" || p.ModelName != "Opus 5.5" || p.Tokens != 115409 || p.TokensShort != "115.4k" {
		t.Fatalf("model and tokens: %+v", p)
	}
	if p.Running != "2.1.325" || p.Installed != "2.1.330" || !p.UpdateInstalled {
		t.Fatalf("update: %+v", p)
	}
	// --version is run once, then kept
	panel()
	panel()
	if n.Load() != 1 {
		t.Fatalf("claude --version ran %d times", n.Load())
	}
	event(t, d, "PreToolUse", s.Slug, map[string]any{"tool_name": "Bash", "permission_mode": "plan"})
	if p := panel(); p.Mode != "plan" || p.Effort != "high" {
		t.Fatalf("mode changed: %+v", p)
	}
}

// Up to date, or a --version that says something else: no notice. The
// effort pekoe started the agent with stands in until a hook tells.
func TestSessionPanelNoUpdate(t *testing.T) {
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.update(s.Slug, func(s *state.Session) { s.AgentVersion, s.Effort = "2.1.330", "max" })
	fakeVersion(t, "2.1.330 (Claude Code)\n", nil)
	if p := call[PanelInfo](t, d, "session.panel", map[string]any{"slug": s.Slug}); p.UpdateInstalled || p.Installed != "2.1.330" || p.Effort != "max" || p.EffortFrom != "start" {
		t.Fatalf("same version: %+v", p)
	}
	fakeVersion(t, "", errors.New("no claude"))
	if p := call[PanelInfo](t, d, "session.panel", map[string]any{"slug": s.Slug}); p.UpdateInstalled || p.Installed != "" {
		t.Fatalf("--version failed: %+v", p)
	}
}

// codex: nothing is read and --version is not run; the page says why.
func TestSessionPanelNotClaude(t *testing.T) {
	n := fakeVersion(t, "2.1.330 (Claude Code)\n", nil)
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new", "agent": "codex"})
	d.update(s.Slug, func(s *state.Session) {
		s.AgentVersion, s.LastModel, s.PermissionMode, s.Tokens = "2.1.1", "claude-opus-5-5", "auto", 100
	})
	p := call[PanelInfo](t, d, "session.panel", map[string]any{"slug": s.Slug})
	if p.Supported || p.Why == "" || p.Agent != "codex" || p.Model != "" || p.Mode != "" || p.Tokens != 0 || p.UpdateInstalled || n.Load() != 0 {
		t.Fatalf("codex: %+v (runs %d)", p, n.Load())
	}
	if _, err := d.Call("session.panel", mustJSON(map[string]any{"slug": TapiSlug})); err == nil {
		t.Fatal("tapi has no session panel")
	}
}

// A person's: refused over the socket without the human token.
func TestSessionPanelNeedsToken(t *testing.T) {
	fakeVersion(t, "2.1.330 (Claude Code)\n", nil)
	d, _, _ := chatSetup(t)
	c := dialPipe(t, d)
	for _, tok := range []string{"", "wrong-" + d.humanToken} {
		if e := c.call("session.panel", map[string]any{"slug": "main"}, tok); !strings.Contains(e, "only a person can do this") {
			t.Errorf("token %q: %q, want the human-only refusal", tok, e)
		}
	}
	if e := c.call("session.panel", map[string]any{"slug": "main"}, d.humanToken); e != "" {
		t.Errorf("with the token: %s", e)
	}
}

// On the GUI's mux only, with a session token; the phone's Web UI has no
// such route.
func TestGUISessionPanel(t *testing.T) {
	fakeVersion(t, "2.1.330 (Claude Code)\n", nil)
	d, _, repo, h, _ := guiSetup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	const path = "/api/sessions/main/panel"
	if w := doGUI(t, h, guiReq{path: path}); w.Code != http.StatusUnauthorized {
		t.Errorf("without a bearer: %d", w.Code)
	}
	if w := doWeb(t, testWebHandler(d), webReq{path: path, from: ipPhone}); w.Code == 200 && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("the phone has %s: %s", path, w.Body)
	}
	s := guiSignIn(t, d, h)
	w := doGUI(t, h, guiReq{path: path, bearer: s})
	var p PanelInfo
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || !p.Supported {
		t.Fatalf("panel: %d %s", w.Code, w.Body)
	}
	if w := doGUI(t, h, guiReq{path: "/api/sessions/nope/panel", bearer: s}); w.Code != http.StatusNotFound {
		t.Errorf("no such session: %d", w.Code)
	}
}

// The page's rows (gui/panel.js): auto mode is warn, the effort pekoe
// asked for says so, codex gets the reason, the notice only when older.
func TestGUIPanelRows(t *testing.T) {
	out := runWebModule(t, `import { agentRows, updateNotice, modeLook } from "gui/panel.js";
const r = {};
const p = {supported: true, model: "claude-opus-5-5", model_name: "Opus 5.5", effort: "high", effort_from: "hook", mode: "auto", tokens: 115409, tokens_short: "115.4k",
  running_version: "2.1.325", installed_version: "2.1.330", update_installed: true};
r.rows = agentRows(p).map((x) => [x.k, x.parts.map((y) => (y.role ? "[" + y.role + "]" : "") + y.text).join("")]);
r.started = agentRows({supported: true, effort: "max", effort_from: "start"})[0].parts[0].text;
r.manual = agentRows({supported: true, mode: "default"})[0].parts[0];
r.bogusMode = agentRows({supported: true, mode: "manual"}).length;
r.codex = agentRows({supported: false, agent: "codex", why: "not Claude Code: x"}).map((x) => x.parts.map((y) => y.text).join(""));
r.error = agentRows({error: ""}).length;
r.notice = updateNotice(p).parts.map((y) => (y.role ? "[" + y.role + "]" : "") + y.text).join("");
r.noNotice = [updateNotice({...p, update_installed: false}), updateNotice({...p, supported: false}), updateNotice(null)];
r.bypass = modeLook("bypassPermissions").role;
console.log(JSON.stringify(r));`)
	var r struct {
		Rows      [][2]string       `json:"rows"`
		Started   string            `json:"started"`
		Manual    map[string]string `json:"manual"`
		BogusMode int               `json:"bogusMode"`
		Codex     []string          `json:"codex"`
		Error     int               `json:"error"`
		Notice    string            `json:"notice"`
		NoNotice  []any             `json:"noNotice"`
		Bypass    string            `json:"bypass"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := [][2]string{{"model", "Opus 5.5"}, {"effort", "high"}, {"mode", "[warn]auto mode on"}, {"context", "115.4k tokens"}}
	if !reflect.DeepEqual(r.Rows, want) {
		t.Errorf("rows %v, want %v", r.Rows, want)
	}
	if r.Started != "max · as started" || r.Manual["text"] != "manual" || r.Manual["role"] != "" || r.BogusMode != 0 {
		t.Errorf("effort %q, manual %v, bogus mode rows %d", r.Started, r.Manual, r.BogusMode)
	}
	if len(r.Codex) != 1 || r.Codex[0] != "◦ not Claude Code: x" || r.Error != 0 {
		t.Errorf("codex %q, error rows %d", r.Codex, r.Error)
	}
	if r.Notice != "[secondary]✓ Update installed · 2.1.325 → 2.1.330 · restart the agent to apply" {
		t.Errorf("notice %q", r.Notice)
	}
	for i, n := range r.NoNotice {
		if n != nil {
			t.Errorf("notice %d: %v", i, n)
		}
	}
	if r.Bypass != "warn" {
		t.Errorf("bypassPermissions: %q", r.Bypass)
	}
}

// The real runVersion: the output is cut at 4KB, and a child left holding
// stdout does not keep it (and the cache's lock) waiting.
func TestRunVersionCapped(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "claude")
	os.WriteFile(big, []byte("#!/bin/sh\necho '2.1.330 (Claude Code)'\nhead -c 10000 /dev/zero | tr '\\\\0' x\n"), 0o755)
	out, err := runVersion(big)
	if err != nil || len(out) != 4096 || parseInstalled(out) != "2.1.330" {
		t.Fatalf("big: %d bytes, %v, %q", len(out), err, parseInstalled(out))
	}
	held := filepath.Join(dir, "held", "claude")
	os.MkdirAll(filepath.Dir(held), 0o755)
	os.WriteFile(held, []byte("#!/bin/sh\necho '2.1.330 (Claude Code)'\nsleep 30 &\n"), 0o755)
	start := time.Now()
	out, _ = runVersion(held)
	if took := time.Since(start); took > 4*time.Second || parseInstalled(out) != "2.1.330" {
		t.Fatalf("held stdout: %s, %q", took, out)
	}
}
