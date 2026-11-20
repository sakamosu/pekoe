package tui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// runCmd waits for a command however late it answers (a call to the fake
// daemon on a loaded machine), and fires or drops a timer by its duration,
// not by when its message comes.
func TestRunCmdWaitsForLateCommands(t *testing.T) {
	m := repoModel(t, "never", 120)
	release := make(chan struct{})
	go func() {
		time.Sleep(time.Second) // later than runCmd used to wait
		close(release)
	}()
	runCmd(m, func() tea.Msg {
		<-release
		return errMsg{errors.New("late answer")}
	})
	if m.msg != "late answer" {
		t.Fatalf("a late command's message was dropped: msg %q", m.msg)
	}

	runCmd(m, tea.Batch(
		after(time.Hour, func(time.Time) tea.Msg { return errMsg{errors.New("an hour later")} }),
		after(timerFires-time.Millisecond, func(time.Time) tea.Msg { return errMsg{errors.New("short timer")} }),
		after(timerFires, func(time.Time) tea.Msg { return errMsg{errors.New("long timer")} }),
	))
	if m.msg != "short timer" {
		t.Fatalf("timers: msg %q, want only the short one fired", m.msg)
	}
}
