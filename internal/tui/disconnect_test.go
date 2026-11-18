package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Losing pekoed ends the TUI with ErrDisconnected, so the caller can say why
// on the terminal (the message line goes with the alt screen) and wait for
// pekoed to come back. A plain quit ends it without one.
func TestDisconnectEndsWithReason(t *testing.T) {
	m := newModel(nil, "")
	if err := m.exitErr(); err != nil {
		t.Fatalf("before losing pekoed: %v", err)
	}
	_, cmd := m.Update(disconnectedMsg{})
	if cmd == nil {
		t.Fatal("no command: the TUI does not end")
	}
	if !quits(cmd) {
		t.Fatal("the TUI does not quit")
	}
	if err := m.exitErr(); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("exitErr = %v, want ErrDisconnected", err)
	}
}

// quits reports whether cmd, or a command batched in it, quits the program.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if quits(c) {
				return true
			}
		}
	}
	return false
}
