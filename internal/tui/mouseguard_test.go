package tui

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestSgrMatch(t *testing.T) {
	cases := []struct {
		in      string
		n       int
		partial bool
	}{
		{"\x1b", 0, true},
		{"\x1b[", 0, true},
		{"\x1b[<65;102", 0, true},
		{"\x1b[<65;102;32", 0, true},
		{"\x1b[<65;102;32M", 13, false},
		{"\x1b[<0;1;2mabc", 9, false},
		{"\x1b[A", 0, false},
		{"\x1b[<;1;2M", 0, false},
		{"\x1bx", 0, false},
	}
	for _, c := range cases {
		n, p := sgrMatch(c.in)
		if n != c.n || p != c.partial {
			t.Errorf("%q: got (%d,%v) want (%d,%v)", c.in, n, p, c.n, c.partial)
		}
	}
}

func TestMouseGuardPassThrough(t *testing.T) {
	var g mouseGuard
	for _, k := range []tea.KeyMsg{runes("a"), runes("<65;1;1M"), {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune("x"), Alt: true}} {
		out, wait := g.feed(k)
		if wait || !reflect.DeepEqual(out, []tea.KeyMsg{k}) {
			t.Errorf("%v: got %v wait=%v", k, out, wait)
		}
	}
}

func TestMouseGuardLoneEsc(t *testing.T) {
	var g mouseGuard
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	out, wait := g.feed(esc)
	if out != nil || !wait {
		t.Fatalf("esc should be held: %v %v", out, wait)
	}
	if got := g.flush(g.gen - 1); got != nil {
		t.Fatalf("stale flush released %v", got)
	}
	if got := g.flush(g.gen); !reflect.DeepEqual(got, []tea.KeyMsg{esc}) {
		t.Fatalf("flush: %v", got)
	}
	// Esc followed by a normal key is released in order right away
	g.feed(esc)
	out, wait = g.feed(runes("j"))
	if wait || !reflect.DeepEqual(out, []tea.KeyMsg{esc, runes("j")}) {
		t.Fatalf("esc+j: %v %v", out, wait)
	}
}

// End to end through Bubble Tea's real input reader: a mouse report split
// across two reads at every position must never surface as keys.
func TestMouseGuardSplitReads(t *testing.T) {
	const s = "\x1b[<65;102;32M"
	for cut := 1; cut < len(s); cut++ {
		var g mouseGuard
		var keys []string
		r := &guardRec{g: &g, keys: &keys}
		pr, pw := io.Pipe()
		p := tea.NewProgram(r, tea.WithInput(pr), tea.WithOutput(io.Discard))
		go func() {
			pw.Write([]byte(s + s[:cut]))
			time.Sleep(20 * time.Millisecond)
			pw.Write([]byte(s[cut:] + s + "hi"))
			time.Sleep(20 * time.Millisecond)
			p.Quit()
		}()
		if _, err := p.Run(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(keys, "|"); got != "hi" {
			t.Errorf("cut=%d: keys %q", cut, got)
		}
	}
}

type guardRec struct {
	g    *mouseGuard
	keys *[]string
}

func (r *guardRec) Init() tea.Cmd { return nil }
func (r *guardRec) Update(m tea.Msg) (tea.Model, tea.Cmd) {
	var out []tea.KeyMsg
	switch m := m.(type) {
	case tea.KeyMsg:
		out, _ = r.g.feed(m)
	case guardFlushMsg:
		out = r.g.flush(int(m))
	}
	for _, k := range out {
		*r.keys = append(*r.keys, fmt.Sprint(k))
	}
	return r, nil
}
func (r *guardRec) View() string { return "" }
