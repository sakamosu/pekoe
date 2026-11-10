package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pekoe/internal/config"
	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

func decodeSettings(t *testing.T, s string) claudeSettings {
	t.Helper()
	var c claudeSettings
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		t.Fatalf("settings are not JSON: %v\n%s", err, s)
	}
	return c
}

// The settings pin the sandbox on, keep pekoe's secrets from Bash (denyRead)
// and from the Read / Edit tools (permissions.deny, "//" = absolute), let
// the pekoe CLI reach the socket, and keep the repository's git config and
// hooks from being written.
func TestClaudeSandboxSettings(t *testing.T) {
	d, _, repo := setup(t)
	moved := filepath.Join(t.TempDir(), "ts")
	d.Global.Web.StateDir = moved
	c := decodeSettings(t, d.claudeSandboxSettings([]string{repo}))

	sb := c.Sandbox
	if !strings.Contains(d.claudeSandboxSettings(nil), `"disabled":false`) {
		t.Error("filesystem.disabled is not pinned to false")
	}
	if !sb.Enabled || sb.AllowUnsandboxedCommands || !sb.FailIfUnavailable {
		t.Fatalf("sandbox switches: %+v", sb)
	}
	gitDir := filepath.Join(repo, ".git")
	db := paths.StateDB()
	for _, p := range []string{paths.HumanTokenFile(), paths.TokenFile(), db, db + "-wal", db + "-shm", paths.WebStateDir(), paths.ConfigDir(), moved} {
		if !slices.Contains(sb.Filesystem.DenyRead, p) {
			t.Errorf("denyRead lacks %s: %v", p, sb.Filesystem.DenyRead)
		}
		// the resolved path too (macOS: /var/folders is /private/var/folders)
		if r := realpath(p); !slices.Contains(sb.Filesystem.DenyRead, r) {
			t.Errorf("denyRead lacks the resolved %s", r)
		}
	}
	for _, p := range []string{filepath.Join(gitDir, "config"), filepath.Join(gitDir, "config.worktree"), filepath.Join(gitDir, "hooks")} {
		if !slices.Contains(sb.Filesystem.DenyWrite, realpath(p)) {
			t.Errorf("denyWrite lacks %s: %v", p, sb.Filesystem.DenyWrite)
		}
	}
	if !slices.Contains(sb.Network.AllowUnixSockets, paths.Socket()) {
		t.Errorf("allowUnixSockets: %v", sb.Network.AllowUnixSockets)
	}
	deny := c.Permissions.Deny
	for _, rule := range []string{
		"Read(/" + paths.HumanTokenFile() + ")",
		"Edit(/" + paths.HumanTokenFile() + ")",
		"Read(/" + paths.TokenFile() + ")",
		"Read(/" + db + ")",
		"Read(/" + db + "-wal)",
		"Edit(/" + db + ")",
		"Read(/" + paths.ConfigDir() + "/**)",
		"Edit(/" + paths.ConfigDir() + "/**)",
		"Read(/" + paths.WebStateDir() + "/**)",
		"Read(/" + moved + "/**)",
		"Edit(/" + realpath(filepath.Join(gitDir, "config")) + ")",
		"Edit(/" + realpath(filepath.Join(gitDir, "hooks")) + "/**)",
		"Edit(/" + paths.SkillsDir() + "/**)",
	} {
		if !slices.Contains(deny, rule) {
			t.Errorf("permissions.deny lacks %s:\n%s", rule, strings.Join(deny, "\n"))
		}
	}
	for _, rule := range deny {
		if !strings.HasPrefix(rule, "Read(//") && !strings.HasPrefix(rule, "Edit(//") {
			t.Errorf("rule without an absolute path: %s", rule)
		}
	}
}

// settingsFlag is what a Claude Code agent's command line gets.
func settingsFlag(slug string) string { return " --settings " + shellQuote(agentSettingsFile(slug)) }

func agentCommand(t *testing.T, fe *fakeEngine, slug string) string {
	t.Helper()
	fe.mu.Lock()
	defer fe.mu.Unlock()
	s, ok := fe.spawn[slug+"~"+engine.WinAgent]
	if !ok {
		t.Fatalf("%s: no agent spawned", slug)
	}
	return s.Command
}

// Claude Code agents get --settings on every start, before the prompt's
// "--"; [agents.claude] sandbox = false and other agents get none.
func TestAgentStartGetsSandboxSettings(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "-- do it"})
	waitCreated(t, d, s.Slug)
	cmd := agentCommand(t, fe, s.Slug)
	at, dash := strings.Index(cmd, " --settings '"), strings.Index(cmd, " -- '")
	if at < 0 || dash < 0 || at > dash {
		t.Fatalf("--settings missing or after the prompt: %s", cmd)
	}
	if !strings.HasPrefix(cmd, "claude"+settingsFlag(s.Slug)+" -- ") {
		t.Fatalf("command: %s", cmd)
	}
	b, err := os.ReadFile(agentSettingsFile(s.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if c := decodeSettings(t, string(b)); !c.Sandbox.Enabled || !slices.Contains(c.Sandbox.Filesystem.DenyRead, paths.HumanTokenFile()) {
		t.Fatalf("settings file: %s", b)
	}

	call[any](t, d, "session.restart", map[string]any{"slug": s.Slug})
	if cmd := agentCommand(t, fe, s.Slug); !strings.Contains(cmd, " --settings '") {
		t.Fatalf("restart without settings: %s", cmd)
	}

	off := false
	ag := d.Global.Agents["claude"]
	ag.Sandbox = &off
	d.Global.Agents["claude"] = ag
	call[any](t, d, "session.restart", map[string]any{"slug": s.Slug})
	if cmd := agentCommand(t, fe, s.Slug); strings.Contains(cmd, "--settings") {
		t.Fatalf("sandbox = false still passes settings: %s", cmd)
	}

	d.Global.Agents["codex"] = config.Agent{Command: "codex", Prompt: "arg"}
	c := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/codex", "agent": "codex"})
	waitCreated(t, d, c.Slug)
	if cmd := agentCommand(t, fe, c.Slug); strings.Contains(cmd, "--settings") {
		t.Fatalf("codex got Claude Code settings: %s", cmd)
	}
}

func TestTapiGetsSandboxSettings(t *testing.T) {
	d, fe, _ := setup(t)
	if err := d.tapiSpawn(80, 24); err != nil {
		t.Fatal(err)
	}
	cmd := agentCommand(t, fe, TapiSlug)
	if !strings.Contains(cmd, settingsFlag(TapiSlug)+";") {
		t.Fatalf("tapi command: %s", cmd)
	}
	if _, err := os.Stat(agentSettingsFile(TapiSlug)); err != nil {
		t.Fatal(err)
	}
}
