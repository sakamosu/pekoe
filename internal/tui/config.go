package tui

// <prefix> , (and :config): `pekoe config edit` in this terminal. The TUI
// hands the terminal over (tea.Exec) until the editor and the check after
// it are done, then takes it back. The edit runs in this process's own
// `pekoe`, not through pekoed: config.toml is never written over the socket.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/theme"
)

// configKey follows the prefix: <prefix> , edits config.toml.
const configKey = ","

type configEditedMsg struct{ err error }

// configCmd is `pekoe config edit` as a tea.ExecCommand: the Kitty keyboard
// mode is popped for the editor and pushed again after, and a failure
// waits for Enter so its message can be read before the TUI repaints.
type configCmd struct {
	cmd *exec.Cmd
	out io.Writer
}

func (c *configCmd) SetStdin(r io.Reader)  { c.cmd.Stdin = r }
func (c *configCmd) SetStdout(w io.Writer) { c.cmd.Stdout, c.out = w, w }
func (c *configCmd) SetStderr(w io.Writer) { c.cmd.Stderr = w }

func (c *configCmd) Run() error {
	kitty := kittyEnabled()
	if kitty {
		io.WriteString(c.out, kittyPop)
	}
	err := c.cmd.Run()
	if err != nil {
		fmt.Fprintf(c.out, "\npekoe config edit: %v — press Enter to go back to pekoe ", err)
		if c.cmd.Stdin != nil {
			bufio.NewReader(c.cmd.Stdin).ReadString('\n')
		}
	}
	if kitty {
		io.WriteString(c.out, kittyPush)
	}
	return err
}

// editConfig runs `pekoe config edit` with this binary.
func (m *model) editConfig() tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return errMsg{fmt.Errorf("pekoe config edit: %w", err)} }
	}
	c := &configCmd{cmd: exec.Command(exe, "config", "edit"), out: os.Stdout}
	return tea.Exec(c, func(err error) tea.Msg { return configEditedMsg{err} })
}

func (m *model) onConfigEdited(msg configEditedMsg) tea.Cmd {
	if msg.err != nil {
		m.setMsg("pekoe config edit: "+msg.err.Error(), true)
	}
	// bubbletea turns the mouse off for the command and leaves it off
	return tea.EnableMouseCellMotion
}

// configLoadedMsg: pekoed saw config.toml change (config.reloaded) and the
// TUI read it again.
type configLoadedMsg struct {
	g       *config.Global
	err     error
	restart []string // settings pekoed takes up only after a restart
}

// reloadConfig reads config.toml again after pekoed said it changed. The
// TUI reads the file itself (as at start): the event carries no settings.
func (m *model) reloadConfig(ev daemon.ConfigReload) tea.Cmd {
	return func() tea.Msg {
		g, err := config.LoadGlobal()
		return configLoadedMsg{g: g, err: err, restart: ev.Restart}
	}
}

// applyConfig puts the settings read into use: most are read where they
// are used (keys, review, notify …); the theme, the icons, the mascot, the
// tips and the animation are set up here again.
func (m *model) applyConfig(msg configLoadedMsg) tea.Cmd {
	if msg.err != nil || msg.g == nil {
		m.setMsg(fmt.Sprintf("config.toml not applied, the settings in use stay: %v", msg.err), true)
		return nil
	}
	old, g := m.g, msg.g
	m.g = g
	themed := !reflect.DeepEqual(old.Theme, g.Theme)
	if themed {
		m.th = theme.Load(&g.Theme)
		m.ic = theme.Icons(theme.IconsName(g.Theme.Icons))
	}
	if themed || !reflect.DeepEqual(old.Mascot, g.Mascot) {
		m.buildTapi(nil)
	}
	if old.TipsOn() != g.TipsOn() {
		m.initTips(m.tipsPath)
	}
	var cmds []tea.Cmd
	if g.AnimateOn() && m.tapi.focused {
		if !m.spinning {
			m.spinning = true
			cmds = append(cmds, spin(true))
		}
		if !m.tapi.ticking {
			m.tapi.ticking = true
			cmds = append(cmds, tapiTick())
		}
	}
	text := "config.toml applied"
	if len(msg.restart) > 0 {
		text += " · " + strings.Join(msg.restart, ", ") + " apply after pekoe daemon restart"
	}
	m.setMsg(text, false)
	return tea.Batch(cmds...)
}
