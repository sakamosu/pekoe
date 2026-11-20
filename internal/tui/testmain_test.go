package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/testenv"
)

// TestMain keeps every test away from the real ~/.local/share/pekoe,
// ~/.config/pekoe, ~/worktrees and tmux -L pekoe (internal/testenv), and
// makes every timer (after) answer at once with its duration: runCmd decides
// by the duration whether it fires.
func TestMain(m *testing.M) {
	after = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return timerMsg{d, fn} }
	}
	testenv.Main(m)
}

// timerMsg is what a timer's command returns in the tests.
type timerMsg struct {
	d  time.Duration
	fn func(time.Time) tea.Msg
}
