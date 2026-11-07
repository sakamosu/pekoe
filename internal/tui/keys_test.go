package tui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestToTmux(t *testing.T) {
	cases := []struct {
		k    tea.KeyMsg
		want fwd
	}{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, fwd{literal: "a"}},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("日")}, fwd{literal: "日"}},
		{tea.KeyMsg{Type: tea.KeyEnter}, fwd{keys: []string{"Enter"}}},
		{tea.KeyMsg{Type: tea.KeyEsc}, fwd{keys: []string{"Escape"}}},
		{tea.KeyMsg{Type: tea.KeyCtrlC}, fwd{keys: []string{"C-c"}}},
		{tea.KeyMsg{Type: tea.KeyUp, Alt: true}, fwd{keys: []string{"M-Up"}}},
		{tea.KeyMsg{Type: tea.KeyShiftTab}, fwd{keys: []string{"BTab"}}},
		{tea.KeyMsg{Type: tea.KeyF5}, fwd{keys: []string{"F5"}}},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x\ny"), Paste: true}, fwd{paste: "x\ny"}},
	}
	for _, c := range cases {
		if got := toTmux(c.k); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %+v want %+v", c.k, got, c.want)
		}
	}
}

func TestWithCursorKeepsWidth(t *testing.T) {
	line := "\x1b[32mhello\x1b[0m world"
	out := withCursor(line, 3)
	if w(out) != w(line) {
		t.Fatalf("width changed: %d vs %d", w(out), w(line))
	}
	if w(withCursor("ab", 5)) != 6 {
		t.Fatal("cursor past end should pad")
	}
}

func TestDecodeCSIu(t *testing.T) {
	cases := map[string]tea.Msg{
		"\x1b[13;2u":  kittyKeyMsg{name: "S-Enter"},
		"\x1b[13;5u":  kittyKeyMsg{name: "C-Enter"},
		"\x1b[27u":    tea.KeyMsg{Type: tea.KeyEsc},
		"\x1b[99;5u":  tea.KeyMsg{Type: tea.KeyCtrlC},
		"\x1b[92;5u":  tea.KeyMsg{Type: tea.KeyCtrlBackslash},
		"\x1b[106;3u": tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j"), Alt: true},
		"\x1b[9;2u":   tea.KeyMsg{Type: tea.KeyShiftTab},
		"\x1b[99;69u": tea.KeyMsg{Type: tea.KeyCtrlC}, // caps lock bit ignored
	}
	for in, want := range cases {
		got, ok := decodeCSIu([]byte(in))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %#v want %#v", in, got, want)
		}
	}
	if _, ok := decodeCSIu([]byte("\x1b[1;2A")); ok {
		t.Error("non-u sequence must be left alone")
	}
}
