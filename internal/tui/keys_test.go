package tui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/config"
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
		// report alternate keys: a shifted symbol is what it types; a
		// letter keeps shift, and without a legacy form the name stays
		"\x1b[97:65;6u":    kittyKeyMsg{name: "C-S-a"},
		"\x1b[49:33;6u":    kittyKeyMsg{name: "C-S-1"},
		"\x1b[97:65;4u":    tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A"), Alt: true},
		"\x1b[54:94;8u":    tea.KeyMsg{Type: tea.KeyCtrlCaret, Alt: true},
		"\x1b[57377u":      tea.KeyMsg{Type: tea.KeyF14},
		"\x1b[57383;3u":    tea.KeyMsg{Type: tea.KeyF20, Alt: true},
		"\x1b[57376;5u":    kittyKeyMsg{name: "C-F13"},
		"\x1b[57384u":      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{57384}}, // F21: no tea key
		"\x1b[54:94:54;6u": tea.KeyMsg{Type: tea.KeyCtrlCaret},                   // with the base layout key
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

// Every [keys] prefix PrefixKey takes matches when pressed under the Kitty
// keyboard protocol. ctrl+^ _ @ (shifted on a US keyboard) and alt+ a
// shifted symbol come with the key unshifted plus what it types (report
// alternate keys, which kittyPush asks for), F13 and up as kitty's own
// codes. The sequences follow the protocol's spec; not checked on a real
// terminal.
func TestKittyPrefixMatches(t *testing.T) {
	if kittyPush != "\x1b[>5u" {
		t.Fatalf("kittyPush %q: the shifted keys below need report alternate keys (flag 4)", kittyPush)
	}
	for _, c := range []struct{ prefix, seq string }{
		{"ctrl+q", "\x1b[113;5u"},
		{"ctrl+]", "\x1b[93;5u"},
		{"ctrl+^", "\x1b[54:94;6u"}, // US: ctrl+shift+6
		{"ctrl+^", "\x1b[94;5u"},    // JIS: ^ without shift
		{"ctrl+_", "\x1b[45:95;6u"}, // US: ctrl+shift+-
		{"ctrl+@", "\x1b[50:64;6u"}, // US: ctrl+shift+2
		{"ctrl+@", "\x1b[64;5u"},    // JIS
		{"alt+!", "\x1b[49:33;4u"},
		{"alt+A", "\x1b[97:65;4u"},
		{"f13", "\x1b[57376u"},
		{"f20", "\x1b[57383u"},
		{"alt+f13", "\x1b[57376;3u"},
	} {
		m := &model{g: &config.Global{}}
		m.g.Keys.Prefix = c.prefix
		msg, ok := decodeCSIu([]byte(c.seq))
		k, isKey := msg.(tea.KeyMsg)
		if !ok || !isKey || !m.prefixKey(k) {
			t.Errorf("prefix %q pressed as %q: got %#v (%v), not the prefix", c.prefix, c.seq, msg, msg)
		}
	}
}
