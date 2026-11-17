package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/paths"
)

// updateMainCmd runs one message through the model.
func (m *model) updateMainCmd(msg tea.Msg) tea.Cmd {
	_, cmd := m.updateMain(msg)
	return cmd
}

// <prefix> , and :config hand the terminal to `pekoe config edit`
func TestConfigKey(t *testing.T) {
	m := tapiModel(t, "none", config.Mascot{})
	m.prefix = true
	if cmd := m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(configKey)}); cmd == nil {
		t.Fatal("prefix , did nothing")
	}
	if m.prefix {
		t.Error("prefix still pending")
	}
	c := newCommandModal(m)
	m.modal = c
	c.in.text = []rune("config")
	if cmd := c.key(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil || c.err != "" {
		t.Fatalf(":config: %q", c.err)
	}
}

// the Kitty keyboard mode is off while the command runs, and a failure
// waits for Enter before the TUI takes the terminal back
func TestConfigCmdRun(t *testing.T) {
	t.Setenv("PEKOE_KITTY", "1")
	var out bytes.Buffer
	c := &configCmd{cmd: exec.Command("/bin/sh", "-c", "printf ran; exit 3")}
	c.SetStdout(&out)
	c.SetStderr(&out)
	c.SetStdin(strings.NewReader("\n"))
	if err := c.Run(); err == nil {
		t.Fatal("exit 3 reported as success")
	}
	got := out.String()
	if !strings.HasPrefix(got, kittyPop+"ran") || !strings.HasSuffix(got, kittyPush) || !strings.Contains(got, "press Enter") {
		t.Errorf("out = %q", got)
	}
	m := tapiModel(t, "none", config.Mascot{})
	if m.onConfigEdited(configEditedMsg{err: exec.ErrNotFound}) == nil || !m.msgErr {
		t.Error("the failure is not shown")
	}
}

// pekoed's config.reloaded: the TUI reads config.toml again and puts it
// to use; a file that does not read keeps the settings in use
func TestConfigReloaded(t *testing.T) {
	t.Setenv("PEKOE_HOME", t.TempDir())
	t.Setenv("PEKOE_ICONS", "")
	m := tapiModel(t, "", config.Mascot{})
	m.g.Animate = new(bool) // off at start
	if err := os.MkdirAll(filepath.Dir(paths.GlobalConfig()), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if err := os.WriteFile(paths.GlobalConfig(), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reload := func(restart ...string) tea.Cmd {
		msg := m.reloadConfig(daemon.ConfigReload{Restart: restart})()
		return m.updateMainCmd(msg)
	}

	write("[keys]\nprefix = \"ctrl+b\"\n[theme]\nicons = \"ascii\"\n[mascot]\npalette = \"light\"\n[orchestrator]\nnotify = \"quiet\"\n")
	cmd := reload()
	if m.g.Keys.Prefix != "ctrl+b" || !m.quietNotify() || m.ic.Name != "ascii" || !m.g.Mascot.LightPalette() {
		t.Fatalf("not applied: prefix %q quiet %v icons %q", m.g.Keys.Prefix, m.quietNotify(), m.ic.Name)
	}
	if !m.tapi.r.ASCII() {
		t.Error("the mascot was not rebuilt for ascii icons")
	}
	if cmd == nil || !m.spinning {
		t.Error("animate (default on) did not start the animation")
	}
	if m.msg != "config.toml applied" || m.msgErr {
		t.Errorf("msg %q", m.msg)
	}

	reload("http.port", "web")
	if !strings.Contains(m.msg, "http.port, web apply after pekoe daemon restart") {
		t.Errorf("msg %q", m.msg)
	}

	write("[keys\n")
	reload()
	if m.g.Keys.Prefix != "ctrl+b" || !m.msgErr || !strings.Contains(m.msg, "not applied") {
		t.Errorf("a broken file: prefix %q msg %q", m.g.Keys.Prefix, m.msg)
	}
}

// [keys] prefix written as tmux or with capitals still leaves typing; one
// that never arrives as itself (ctrl+i is tab) falls back to ^q, so the pty
// can always be left from the keyboard.
func TestPrefixKeyForms(t *testing.T) {
	for _, c := range []struct {
		prefix, label string
		press         tea.KeyMsg
	}{
		{"C-a", "^a", tea.KeyMsg{Type: tea.KeyCtrlA}},
		{"Ctrl+A", "^a", tea.KeyMsg{Type: tea.KeyCtrlA}},
		{"ctrl+i", "^q", tea.KeyMsg{Type: tea.KeyCtrlQ}},
		{"tab", "^q", tea.KeyMsg{Type: tea.KeyCtrlQ}},
	} {
		m, f, _ := chatModel(t)
		m.g.Keys.Prefix = c.prefix
		if got := m.prefixLabel(); got != c.label {
			t.Errorf("%q: label %q, want %q", c.prefix, got, c.label)
		}
		m.focus, m.typing = pAgent, true
		if c.prefix == "ctrl+i" { // tab, as the terminal sends ctrl+i, is typed
			runCmd(m, m.onKey(tea.KeyMsg{Type: tea.KeyTab}))
			if !m.typing || len(f.find("session.keys")) != 1 {
				t.Errorf("%q: tab did not go to the pty", c.prefix)
			}
		}
		runCmd(m, m.onKey(c.press))
		if m.typing {
			t.Errorf("prefix %q: %s did not leave typing", c.prefix, c.press)
		}
	}
}
