package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Kitty keyboard protocol passthrough (design: 入れ子の税).
//
// The TUI asks the outer terminal (Ghostty) for the "disambiguate" mode
// (CSI > 1 u), so keys like Shift+Enter arrive as CSI u sequences instead of
// a plain CR. Bubble Tea v1 reports those as unknown CSI sequences; they are
// decoded here. Keys that have a legacy meaning (Esc, Ctrl+C, Alt+x …) turn
// back into ordinary tea.KeyMsg; the rest become kittyKeyMsg carrying a tmux
// key name (S-Enter …), which tmux (extended-keys on, csi-u) delivers to the
// agent in CSI u form when the agent asked for extended keys.

const (
	kittyPush = "\x1b[>1u"
	kittyPop  = "\x1b[<u"
)

type kittyKeyMsg struct{ name string }

func kittyEnabled() bool { return os.Getenv("BOBA_KITTY") != "0" }

// kittyFilter is a tea.WithFilter hook.
func kittyFilter(_ tea.Model, msg tea.Msg) tea.Msg {
	if msg == nil {
		return nil
	}
	t := reflect.TypeOf(msg)
	if t.Name() != "unknownCSISequenceMsg" || t.Kind() != reflect.Slice {
		return msg
	}
	if out, ok := decodeCSIu(reflect.ValueOf(msg).Bytes()); ok {
		return out
	}
	return msg
}

// decodeCSIu parses ESC [ code[:alt] [; mods[:event]] [; text] u.
func decodeCSIu(b []byte) (tea.Msg, bool) {
	s := string(b)
	if !strings.HasPrefix(s, "\x1b[") || !strings.HasSuffix(s, "u") {
		return nil, false
	}
	parts := strings.Split(s[2:len(s)-1], ";")
	code, err := strconv.Atoi(strings.SplitN(parts[0], ":", 2)[0])
	if err != nil {
		return nil, false
	}
	mods := 0
	if len(parts) > 1 && parts[1] != "" {
		m, err := strconv.Atoi(strings.SplitN(parts[1], ":", 2)[0])
		if err != nil {
			return nil, false
		}
		if ev := strings.SplitN(parts[1], ":", 2); len(ev) == 2 && ev[1] == "3" {
			return nil, true // key release: ignore
		}
		mods = m - 1
	}
	mods &^= 64 | 128 // caps lock, num lock
	shift, alt, ctrl := mods&1 != 0, mods&2 != 0, mods&4 != 0

	var name string
	switch code {
	case 13:
		name = "Enter"
	case 9:
		name = "Tab"
	case 27:
		name = "Escape"
	case 127:
		name = "BSpace"
	case 32:
		name = "Space"
	default:
		if code < 32 || code > 0x10ffff {
			return nil, false
		}
		name = string(rune(code))
	}

	// legacy-representable keys go back to ordinary key messages
	switch {
	case mods == 0 && code == 27:
		return tea.KeyMsg{Type: tea.KeyEsc}, true
	case mods == 0 && code == 13:
		return tea.KeyMsg{Type: tea.KeyEnter}, true
	case mods == 0 && code == 9:
		return tea.KeyMsg{Type: tea.KeyTab}, true
	case mods == 0 && code == 127:
		return tea.KeyMsg{Type: tea.KeyBackspace}, true
	case mods == 1 && code == 9:
		return tea.KeyMsg{Type: tea.KeyShiftTab}, true
	case ctrl && !shift && code >= 'a' && code <= 'z':
		return tea.KeyMsg{Type: tea.KeyType(code - 'a' + 1), Alt: alt}, true
	case ctrl && !shift && code >= '@' && code <= '_':
		return tea.KeyMsg{Type: tea.KeyType(code & 0x1f), Alt: alt}, true
	case !ctrl && alt && code >= 32 && code != 127 && len(name) <= 4 && name != "Space":
		r := []rune(name)
		if shift {
			r = []rune(strings.ToUpper(name))
		}
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: r, Alt: true}, true
	case mods == 0 || (mods == 1 && name != "Enter" && name != "BSpace" && name != "Space" && name != "Escape"):
		r := []rune(name)
		if shift {
			r = []rune(strings.ToUpper(name))
		}
		if name == "Space" {
			return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}, true
		}
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: r}, true
	}
	// no legacy form (Shift+Enter, Ctrl+Enter, Ctrl+Shift+x …): pass by name
	prefix := ""
	if ctrl {
		prefix += "C-"
	}
	if alt {
		prefix += "M-"
	}
	if shift {
		prefix += "S-"
	}
	return kittyKeyMsg{name: prefix + name}, true
}

func (k kittyKeyMsg) String() string { return fmt.Sprintf("kitty(%s)", k.name) }

// writeTerm writes a control sequence straight to the outer terminal.
func writeTerm(seq string) tea.Cmd {
	return func() tea.Msg {
		os.Stdout.WriteString(seq)
		return nil
	}
}

// ---- Kitty graphics: inline images in our own UI (pick screenshots) ----

// graphicsCapable: Kitty graphics can't be detected reliably in-band through
// every multiplexer, so go by terminal identity (BOBA_GRAPHICS=0/1 overrides).
func graphicsCapable() bool {
	switch os.Getenv("BOBA_GRAPHICS") {
	case "1":
		return true
	case "0":
		return false
	}
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	term := os.Getenv("TERM")
	return tp == "ghostty" || tp == "wezterm" || strings.Contains(term, "kitty") || strings.Contains(term, "ghostty")
}

// row/column diacritics of the Unicode placeholder scheme (first entries).
var placeholderDiacritics = []rune{0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F}

const placeholder = '\U0010EEEE'

func imageID(pickID int64) int { return 0x0B0000 | int(pickID&0xFFFF) }

// transmitImage sends a PNG file once and creates a virtual placement of
// cols×rows cells; placeholder cells decide where it shows.
func transmitImage(id int, path string, cols, rows int) string {
	return fmt.Sprintf("\x1b_Ga=T,t=f,f=100,U=1,q=2,i=%d,c=%d,r=%d;%s\x1b\\",
		id, cols, rows, base64.StdEncoding.EncodeToString([]byte(path)))
}

// placeholderRow renders one row of placeholder cells; the foreground
// color carries the image id.
func placeholderRow(id, row, cols int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
	b.WriteRune(placeholder)
	b.WriteRune(placeholderDiacritics[row%len(placeholderDiacritics)])
	b.WriteRune(placeholderDiacritics[0])
	for i := 1; i < cols; i++ {
		b.WriteRune(placeholder) // row/column inferred from the left neighbour
	}
	b.WriteString("\x1b[39m")
	return b.String()
}
