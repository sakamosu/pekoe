package tui

import (
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Bubble Tea runs every command on its own goroutine, and pekoed handles
// every request on its own goroutine too. A key typed into a pty is one
// call (session.keys), so a burst of keys — text arriving in
// one read is a run of letters, then a message per space — used to reach
// tmux in any order ("reply with" → "replywith "). ptyOrder chains those
// writes: each starts only once the one queued before it has been answered.
// The place in the chain is taken in Update, i.e. in the order keys came.

// orderWait bounds the wait for the previous write, so a command that is
// never run (or a call that hangs) cannot hold up typing for good.
const orderWait = 3 * time.Second

type ptyOrder struct {
	mu   sync.Mutex
	last chan struct{} // closed when the latest queued write is done
}

func (o *ptyOrder) take() (prev, done chan struct{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	prev, done = o.last, make(chan struct{})
	o.last = done
	return prev, done
}

// inOrder returns a command running call after every pty write queued
// before it. Call it from Update (not from inside another command).
func (m *model) inOrder(call func() error) tea.Cmd {
	if m.order == nil {
		m.order = &ptyOrder{}
	}
	prev, done := m.order.take()
	return func() tea.Msg {
		if prev != nil {
			select {
			case <-prev:
			case <-time.After(orderWait):
			}
		}
		err := call()
		close(done)
		if err != nil {
			return errMsg{err}
		}
		return nil
	}
}
