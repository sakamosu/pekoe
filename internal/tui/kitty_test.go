package tui

import (
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// With Num Lock (128) or Caps Lock (64) on, a terminal speaking the Kitty
// keyboard protocol sends ↑ as ESC [ 1;129 A. Bubble Tea only knows mods
// 2–8, so these must be decoded here or the key is lost.
func TestDecodeCSILegacy(t *testing.T) {
	cases := map[string]tea.Msg{
		"\x1b[1;129A":    tea.KeyMsg{Type: tea.KeyUp},   // num lock only
		"\x1b[1;65B":     tea.KeyMsg{Type: tea.KeyDown}, // caps lock only
		"\x1b[1;193C":    tea.KeyMsg{Type: tea.KeyRight},
		"\x1b[1;129D":    tea.KeyMsg{Type: tea.KeyLeft},
		"\x1b[1;129H":    tea.KeyMsg{Type: tea.KeyHome},
		"\x1b[1;129F":    tea.KeyMsg{Type: tea.KeyEnd},
		"\x1b[2;129~":    tea.KeyMsg{Type: tea.KeyInsert},
		"\x1b[3;129~":    tea.KeyMsg{Type: tea.KeyDelete},
		"\x1b[5;129~":    tea.KeyMsg{Type: tea.KeyPgUp},
		"\x1b[6;129~":    tea.KeyMsg{Type: tea.KeyPgDown},
		"\x1b[1;130A":    kittyKeyMsg{name: "S-Up"},
		"\x1b[1;133D":    kittyKeyMsg{name: "C-Left"},
		"\x1b[1;131B":    kittyKeyMsg{name: "M-Down"},
		"\x1b[1;136C":    kittyKeyMsg{name: "C-M-S-Right"},
		"\x1b[3;134~":    kittyKeyMsg{name: "C-S-DC"},
		"\x1b[1;129:1A":  tea.KeyMsg{Type: tea.KeyUp}, // press
		"\x1b[1;129:2A":  tea.KeyMsg{Type: tea.KeyUp}, // repeat
		"\x1b[1;129:3A":  nil,                         // release: dropped
		"\x1b[1;129P":    tea.KeyMsg{Type: tea.KeyF1},
		"\x1b[1;129Q":    tea.KeyMsg{Type: tea.KeyF2},
		"\x1b[13;129~":   tea.KeyMsg{Type: tea.KeyF3},
		"\x1b[1;129S":    tea.KeyMsg{Type: tea.KeyF4},
		"\x1b[15;129~":   tea.KeyMsg{Type: tea.KeyF5},
		"\x1b[17;129~":   tea.KeyMsg{Type: tea.KeyF6},
		"\x1b[18;129~":   tea.KeyMsg{Type: tea.KeyF7},
		"\x1b[19;129~":   tea.KeyMsg{Type: tea.KeyF8},
		"\x1b[20;129~":   tea.KeyMsg{Type: tea.KeyF9},
		"\x1b[21;129~":   tea.KeyMsg{Type: tea.KeyF10},
		"\x1b[23;129~":   tea.KeyMsg{Type: tea.KeyF11},
		"\x1b[24;129~":   tea.KeyMsg{Type: tea.KeyF12},
		"\x1b[1;65P":     tea.KeyMsg{Type: tea.KeyF1}, // caps lock
		"\x1b[1;130Q":    kittyKeyMsg{name: "S-F2"},
		"\x1b[1;133P":    kittyKeyMsg{name: "C-F1"},
		"\x1b[15;130~":   kittyKeyMsg{name: "S-F5"},
		"\x1b[13;131~":   kittyKeyMsg{name: "M-F3"},
		"\x1b[1;129:3P":  nil, // release: dropped
		"\x1b[24;129:3~": nil,
	}
	for in, want := range cases {
		got, ok := decodeCSILegacy([]byte(in))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %#v (%v) want %#v", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"\x1b[9;129~", // no such key
		"\x1b[16;129~",
		"\x1b[22;129~",
		"\x1b[1;129R", // cursor position report, not F3
		"\x1b[2;129P", // F1, F2, F4 are 1;mods
		"\x1b[2;129A", // arrows are 1;mods
		"\x1b[A",      // no mods: Bubble Tea's own
		"\x1b[13;129u",
		"\x1b[1;0A",
	} {
		if got, ok := decodeCSILegacy([]byte(in)); ok {
			t.Errorf("%q: decoded as %#v, want left alone", in, got)
		}
	}
}

// Num/Caps Lock alone on a CSI u key is no modifier either.
func TestDecodeCSIuLockBits(t *testing.T) {
	for in, want := range map[string]tea.Msg{
		"\x1b[13;129u": tea.KeyMsg{Type: tea.KeyEnter},
		"\x1b[13;65u":  tea.KeyMsg{Type: tea.KeyEnter},
		"\x1b[27;129u": tea.KeyMsg{Type: tea.KeyEsc},
		"\x1b[13;130u": kittyKeyMsg{name: "S-Enter"},
	} {
		got, ok := decodeCSIu([]byte(in))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %#v want %#v", in, got, want)
		}
	}
}

// The whole path: bytes from the terminal through Bubble Tea's input reader
// and kittyFilter to the model.
func TestKittyFilterNumLockInput(t *testing.T) {
	in := "\x1b[1;129A" + "\x1b[A" + "\x1b[1;2A" + "\x1b[1;129:3A" + "\x1b[1;130A" + "\x1b[3;129~" + "\x1b[13;129u" +
		"\x1b[1;129P" + "\x1b[15~" + "\x1b[1;2P" + "\x1b[24;129~" + "\x1b[1;129:3P" + "\x1b[1;130Q" + "q"
	rec := &keyRecorder{}
	p := tea.NewProgram(rec, tea.WithInput(strings.NewReader(in)), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithFilter(kittyFilter))
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("program did not end on q")
	}
	want := []tea.Msg{
		tea.KeyMsg{Type: tea.KeyUp},      // num lock
		tea.KeyMsg{Type: tea.KeyUp},      // legacy, unchanged
		tea.KeyMsg{Type: tea.KeyShiftUp}, // Bubble Tea's own, unchanged
		kittyKeyMsg{name: "S-Up"},
		tea.KeyMsg{Type: tea.KeyDelete},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyF1},  // num lock
		tea.KeyMsg{Type: tea.KeyF5},  // legacy, unchanged
		tea.KeyMsg{Type: tea.KeyF13}, // Bubble Tea's own, unchanged
		tea.KeyMsg{Type: tea.KeyF12},
		kittyKeyMsg{name: "S-F2"},
	}
	if got := rec.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
	if f := toTmux(tea.KeyMsg{Type: tea.KeyUp}); !reflect.DeepEqual(f, fwd{keys: []string{"Up"}}) {
		t.Errorf("KeyUp forwards as %+v", f)
	}
	if f := toTmux(tea.KeyMsg{Type: tea.KeyF12}); !reflect.DeepEqual(f, fwd{keys: []string{"F12"}}) {
		t.Errorf("KeyF12 forwards as %+v", f)
	}
}

type keyRecorder struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (r *keyRecorder) Init() tea.Cmd { return nil }

func (r *keyRecorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" {
			return r, tea.Quit
		}
	case kittyKeyMsg:
	default:
		if reflect.TypeOf(msg).Name() != "unknownCSISequenceMsg" {
			return r, nil
		}
	}
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	return r, nil
}

func (r *keyRecorder) View() string { return "" }

func (r *keyRecorder) get() []tea.Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.msgs
}

// A terminal keeping a Kitty keyboard stack per screen (kitty, Ghostty,
// Alacritty) ends a TUI run, and a config edit inside it, with both stacks
// as they were: the pop lands on the alternate screen, where Init pushed.
func TestKittyPopOnAltScreen(t *testing.T) {
	const enter, leave = "\x1b[?1049h", "\x1b[?1049l" // what Bubble Tea writes
	out := enter + kittyPush +                        // Run, Init
		leave + onAltScreen(kittyPop) + "editor" + onAltScreen(kittyPush) + enter + // configCmd under tea.Exec
		leave + onAltScreen(kittyPop) // Run returns
	var main, alt []string
	cur := &main
	for len(out) > 0 {
		switch {
		case strings.HasPrefix(out, enter):
			cur, out = &alt, out[len(enter):]
		case strings.HasPrefix(out, leave):
			cur, out = &main, out[len(leave):]
		case strings.HasPrefix(out, kittyPush):
			*cur, out = append(*cur, "5"), out[len(kittyPush):]
		case strings.HasPrefix(out, kittyPop):
			if len(*cur) > 0 {
				*cur = (*cur)[:len(*cur)-1]
			}
			out = out[len(kittyPop):]
		default:
			if strings.HasPrefix(out, "editor") && len(main) != 0 {
				t.Errorf("editor runs with main stack %v", main)
			}
			out = out[1:]
		}
	}
	if len(main) != 0 || len(alt) != 0 {
		t.Errorf("left main %v alt %v", main, alt)
	}
}
