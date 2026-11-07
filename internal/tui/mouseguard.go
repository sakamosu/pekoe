package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Bubble Tea v1 parses SGR mouse reports (ESC [ < b ; x ; y M) only when the
// whole report arrives in one read. When a read ends mid-report — common while
// scrolling fast — the head comes out as Esc or alt+[ and the tail as plain
// runes ("<65;102;32M"), which we would then type into the agent's prompt.
//
// mouseGuard sits in front of key handling: a key that starts with ESC and
// could be the head of such a report is held until the following keys either
// complete the report (dropped) or break the pattern (released in order). A
// lone Esc is released after guardWait.

const guardWait = 40 * time.Millisecond

type guardFlushMsg int

type mouseGuard struct {
	held []tea.KeyMsg
	raw  string
	gen  int
}

// keyRaw is the byte form of k when it can be part of a split mouse report.
func keyRaw(k tea.KeyMsg) (string, bool) {
	if k.Paste {
		return "", false
	}
	switch k.Type {
	case tea.KeyEsc:
		if k.Alt {
			return "", false
		}
		return "\x1b", true
	case tea.KeyRunes:
		if k.Alt {
			return "\x1b" + string(k.Runes), true
		}
		return string(k.Runes), true
	}
	return "", false
}

// sgrMatch reports whether s starts with a complete SGR mouse report (n is
// its length) or is a proper prefix of one (partial).
func sgrMatch(s string) (n int, partial bool) {
	i := 0
	for _, c := range []byte("\x1b[<") {
		if i == len(s) {
			return 0, true
		}
		if s[i] != c {
			return 0, false
		}
		i++
	}
	for field := 0; field < 3; field++ {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == len(s) {
			return 0, true
		}
		if i == start {
			return 0, false
		}
		want := byte(';')
		if field == 2 {
			if s[i] == 'M' || s[i] == 'm' {
				return i + 1, false
			}
			return 0, false
		}
		if s[i] != want {
			return 0, false
		}
		i++
	}
	return 0, false
}

func (g *mouseGuard) reset() {
	g.held, g.raw = nil, ""
}

// feed takes the next key and returns the keys to handle now. wait is true
// when keys are being held and a flush should be scheduled for g.gen.
func (g *mouseGuard) feed(k tea.KeyMsg) (out []tea.KeyMsg, wait bool) {
	raw, ok := keyRaw(k)
	if len(g.held) == 0 && (!ok || raw == "" || raw[0] != '\x1b') {
		return []tea.KeyMsg{k}, false
	}
	if !ok {
		out = g.held
		g.reset()
		return append(out, k), false
	}
	cand := g.raw + raw
	n, partial := sgrMatch(cand)
	switch {
	case partial:
		g.held = append(g.held, k)
		g.raw = cand
		g.gen++
		return nil, true
	case n > 0:
		// a reassembled mouse report: drop it, keep whatever followed
		g.reset()
		if rest := cand[n:]; rest != "" {
			return g.feed(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(rest)})
		}
		return nil, false
	}
	if len(g.held) == 0 {
		return []tea.KeyMsg{k}, false // ESC-led, but not a mouse report
	}
	out = g.held
	g.reset()
	more, wait := g.feed(k)
	return append(out, more...), wait
}

// flush releases held keys if no key arrived since the flush for gen was
// scheduled.
func (g *mouseGuard) flush(gen int) []tea.KeyMsg {
	if gen != g.gen || len(g.held) == 0 {
		return nil
	}
	out := g.held
	g.reset()
	return out
}

func guardTick(gen int) tea.Cmd {
	return tea.Tick(guardWait, func(time.Time) tea.Msg { return guardFlushMsg(gen) })
}
