package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"pekoe/internal/config"
	"pekoe/internal/state"
)

// fakeSelf makes the pekoe the hooks name exe for the test.
func fakeSelf(t *testing.T, exe string) {
	t.Helper()
	old := selfExe
	selfExe = func() string { return exe }
	t.Cleanup(func() { selfExe = old })
}

// readHookJSON is the "hooks" pekoe writes for read, with pekoe at
// /opt/pekoe/bin/pekoe.
const readHookJSON = `{"MessageDisplay":[{"hooks":[{"type":"command","command":"/opt/pekoe/bin/pekoe event MessageDisplay","timeout":2}]}]}`

// The --settings file a Claude Code agent gets, by [voice] (enable, the
// modes), [agents.*] sandbox and the agent (docs/voice.md 3.1): the
// MessageDisplay hook only when voice is on and either mode is read, a
// file with the hook alone when the sandbox is off, nothing for an agent
// other than Claude Code.
func TestAgentSettingsReadHook(t *testing.T) {
	d, _, repo := setup(t)
	fakeSelf(t, "/opt/pekoe/bin/pekoe")
	const slug = "feat-x"
	for _, c := range []struct {
		name          string
		voice         config.Voice
		sandbox       bool
		cmd           string
		wantFile      bool   // --settings is passed
		wantSandbox   bool   // the file has the sandbox's settings
		wantHooks     string // the file's "hooks" ("" none)
		wantOnlyHooks bool   // the file has nothing but "hooks"
	}{
		{name: "voice off", voice: config.Voice{}, sandbox: true, cmd: "claude", wantFile: true, wantSandbox: true},
		{name: "voice off, read modes", voice: config.Voice{FocusMode: "read", BackgroundMode: "read"}, sandbox: true, cmd: "claude", wantFile: true, wantSandbox: true},
		{name: "voice on, default modes (focus read)", voice: config.Voice{Enable: true}, sandbox: true, cmd: "claude", wantFile: true, wantSandbox: true, wantHooks: readHookJSON},
		{name: "voice on, background read", voice: config.Voice{Enable: true, FocusMode: "mute", BackgroundMode: "read"}, sandbox: true, cmd: "claude", wantFile: true, wantSandbox: true, wantHooks: readHookJSON},
		{name: "voice on, no read", voice: config.Voice{Enable: true, FocusMode: "narrate", BackgroundMode: "mute"}, sandbox: true, cmd: "claude", wantFile: true, wantSandbox: true},
		{name: "voice on, unknown mode is the default (focus read)", voice: config.Voice{Enable: true, FocusMode: "loud", BackgroundMode: "mute"}, sandbox: true, cmd: "claude", wantFile: true, wantSandbox: true, wantHooks: readHookJSON},
		{name: "sandbox off, voice on", voice: config.Voice{Enable: true}, sandbox: false, cmd: "claude", wantFile: true, wantHooks: readHookJSON, wantOnlyHooks: true},
		{name: "sandbox off, voice on, no read", voice: config.Voice{Enable: true, FocusMode: "narrate", BackgroundMode: "narrate"}, sandbox: false, cmd: "claude"},
		{name: "sandbox off, voice off", voice: config.Voice{}, sandbox: false, cmd: "claude"},
		{name: "not Claude Code, voice on", voice: config.Voice{Enable: true}, sandbox: true, cmd: "codex"},
		{name: "not Claude Code, sandbox off", voice: config.Voice{Enable: true}, sandbox: false, cmd: "codex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			os.Remove(agentSettingsFile(slug))
			d.Global.Voice = c.voice
			cmd, err := d.withAgentSandbox(slug, c.cmd, c.sandbox, []string{repo}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !c.wantFile {
				if cmd != c.cmd {
					t.Errorf("command = %s, want %s", cmd, c.cmd)
				}
				if _, err := os.Stat(agentSettingsFile(slug)); err == nil {
					t.Error("a settings file was written")
				}
				return
			}
			if cmd != c.cmd+settingsFlag(slug) {
				t.Fatalf("command = %s", cmd)
			}
			b, err := os.ReadFile(agentSettingsFile(slug))
			if err != nil {
				t.Fatal(err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(b, &top); err != nil {
				t.Fatalf("not JSON: %s", b)
			}
			if got := string(top["hooks"]); got != c.wantHooks {
				t.Errorf("hooks = %s, want %s", got, c.wantHooks)
			}
			if _, ok := top["sandbox"]; ok != c.wantSandbox {
				t.Errorf("sandbox present = %v, want %v: %s", ok, c.wantSandbox, b)
			}
			if c.wantOnlyHooks && len(top) != 1 {
				t.Errorf("want hooks alone: %s", b)
			}
			if c.wantSandbox {
				if s := decodeSettings(t, string(b)); !s.Sandbox.Enabled || !s.Sandbox.FailIfUnavailable {
					t.Errorf("sandbox settings: %s", b)
				}
			}
		})
	}
}

// pekoe's path is quoted for the shell when it needs it, as `pekoe hooks
// install` writes it.
func TestReadHookQuotesPath(t *testing.T) {
	d, _, _ := setup(t)
	fakeSelf(t, "/Users/a b/bin/pekoe")
	d.Global.Voice = config.Voice{Enable: true}
	h := d.readHooks()["MessageDisplay"]
	if len(h) != 1 || len(h[0].Hooks) != 1 {
		t.Fatalf("hooks: %+v", h)
	}
	if got, want := h[0].Hooks[0].Command, `'/Users/a b/bin/pekoe' event MessageDisplay`; got != want {
		t.Errorf("command = %s, want %s", got, want)
	}
	fakeSelf(t, "")
	if h := d.readHooks(); h != nil {
		t.Errorf("no pekoe found, still hooks: %+v", h)
	}
}

// Every start goes through withAgentSandbox: a session's and tapi chat's
// settings carry the hook while read is on, and lose it at the next start
// once it is off.
func TestAgentStartGetsReadHook(t *testing.T) {
	d, fe, repo := setup(t)
	fakeSelf(t, "/opt/pekoe/bin/pekoe")
	d.Global.Voice = config.Voice{Enable: true}
	hooksOf := func(slug string) string {
		t.Helper()
		b, err := os.ReadFile(agentSettingsFile(slug))
		if err != nil {
			t.Fatal(err)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(b, &top); err != nil {
			t.Fatal(err)
		}
		return string(top["hooks"])
	}

	s := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/x", "prompt": "-- do it"})
	waitCreated(t, d, s.Slug)
	if got := hooksOf(s.Slug); got != readHookJSON {
		t.Errorf("session hooks = %s", got)
	}
	if err := d.tapiSpawn(80, 24); err != nil {
		t.Fatal(err)
	}
	if got := hooksOf(TapiSlug); got != readHookJSON {
		t.Errorf("tapi hooks = %s", got)
	}

	// sandbox = false: the hook alone, still passed
	off := false
	ag := d.Global.Agents["claude"]
	ag.Sandbox = &off
	d.Global.Agents["claude"] = ag
	call[any](t, d, "session.restart", map[string]any{"slug": s.Slug})
	if cmd := agentCommand(t, fe, s.Slug); !strings.Contains(cmd, settingsFlag(s.Slug)) {
		t.Fatalf("sandbox = false with read: no settings: %s", cmd)
	}
	if got := hooksOf(s.Slug); got != readHookJSON {
		t.Errorf("hooks-only file = %s", got)
	}

	// read off: no hook from the next start
	d.Global.Voice.Enable = false
	call[any](t, d, "session.restart", map[string]any{"slug": s.Slug})
	if cmd := agentCommand(t, fe, s.Slug); strings.Contains(cmd, "--settings") {
		t.Fatalf("no sandbox, no read, still settings: %s", cmd)
	}
}
