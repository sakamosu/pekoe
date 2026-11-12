package tui

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/config"
)

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
