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

// row/column diacritics of the Unicode placeholder scheme: the n-th entry
// encodes row (or column) n. The full table from kitty's
// gen/rowcolumn-diacritics.txt, so images can span up to 297 rows.
var placeholderDiacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F, 0x0346, 0x034A,
	0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357, 0x035B, 0x0363, 0x0364, 0x0365,
	0x0366, 0x0367, 0x0368, 0x0369, 0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F,
	0x0483, 0x0484, 0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1, 0x05A8, 0x05A9,
	0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611, 0x0612, 0x0613, 0x0614, 0x0615,
	0x0616, 0x0617, 0x0657, 0x0658, 0x0659, 0x065A, 0x065B, 0x065D, 0x065E, 0x06D6,
	0x06D7, 0x06D8, 0x06D9, 0x06DA, 0x06DB, 0x06DC, 0x06DF, 0x06E0, 0x06E1, 0x06E2,
	0x06E4, 0x06E7, 0x06E8, 0x06EB, 0x06EC, 0x0730, 0x0732, 0x0733, 0x0735, 0x0736,
	0x073A, 0x073D, 0x073F, 0x0740, 0x0741, 0x0743, 0x0745, 0x0747, 0x0749, 0x074A,
	0x07EB, 0x07EC, 0x07ED, 0x07EE, 0x07EF, 0x07F0, 0x07F1, 0x07F3, 0x0816, 0x0817,
	0x0818, 0x0819, 0x081B, 0x081C, 0x081D, 0x081E, 0x081F, 0x0820, 0x0821, 0x0822,
	0x0823, 0x0825, 0x0826, 0x0827, 0x0829, 0x082A, 0x082B, 0x082C, 0x082D, 0x0951,
	0x0953, 0x0954, 0x0F82, 0x0F83, 0x0F86, 0x0F87, 0x135D, 0x135E, 0x135F, 0x17DD,
	0x193A, 0x1A17, 0x1A75, 0x1A76, 0x1A77, 0x1A78, 0x1A79, 0x1A7A, 0x1A7B, 0x1A7C,
	0x1B6B, 0x1B6D, 0x1B6E, 0x1B6F, 0x1B70, 0x1B71, 0x1B72, 0x1B73, 0x1CD0, 0x1CD1,
	0x1CD2, 0x1CDA, 0x1CDB, 0x1CE0, 0x1DC0, 0x1DC1, 0x1DC3, 0x1DC4, 0x1DC5, 0x1DC6,
	0x1DC7, 0x1DC8, 0x1DC9, 0x1DCB, 0x1DCC, 0x1DD1, 0x1DD2, 0x1DD3, 0x1DD4, 0x1DD5,
	0x1DD6, 0x1DD7, 0x1DD8, 0x1DD9, 0x1DDA, 0x1DDB, 0x1DDC, 0x1DDD, 0x1DDE, 0x1DDF,
	0x1DE0, 0x1DE1, 0x1DE2, 0x1DE3, 0x1DE4, 0x1DE5, 0x1DE6, 0x1DFE, 0x20D0, 0x20D1,
	0x20D4, 0x20D5, 0x20D6, 0x20D7, 0x20DB, 0x20DC, 0x20E1, 0x20E7, 0x20E9, 0x20F0,
	0x2CEF, 0x2CF0, 0x2CF1, 0x2DE0, 0x2DE1, 0x2DE2, 0x2DE3, 0x2DE4, 0x2DE5, 0x2DE6,
	0x2DE7, 0x2DE8, 0x2DE9, 0x2DEA, 0x2DEB, 0x2DEC, 0x2DED, 0x2DEE, 0x2DEF, 0x2DF0,
	0x2DF1, 0x2DF2, 0x2DF3, 0x2DF4, 0x2DF5, 0x2DF6, 0x2DF7, 0x2DF8, 0x2DF9, 0x2DFA,
	0x2DFB, 0x2DFC, 0x2DFD, 0x2DFE, 0x2DFF, 0xA66F, 0xA67C, 0xA67D, 0xA6F0, 0xA6F1,
	0xA8E0, 0xA8E1, 0xA8E2, 0xA8E3, 0xA8E4, 0xA8E5, 0xA8E6, 0xA8E7, 0xA8E8, 0xA8E9,
	0xA8EA, 0xA8EB, 0xA8EC, 0xA8ED, 0xA8EE, 0xA8EF, 0xA8F0, 0xA8F1, 0xAAB0, 0xAAB2,
	0xAAB3, 0xAAB7, 0xAAB8, 0xAABE, 0xAABF, 0xAAC1, 0xFE20, 0xFE21, 0xFE22, 0xFE23,
	0xFE24, 0xFE25, 0xFE26, 0x10A0F, 0x10A38, 0x1D185, 0x1D186, 0x1D187, 0x1D188, 0x1D189,
	0x1D1AA, 0x1D1AB, 0x1D1AC, 0x1D1AD, 0x1D242, 0x1D243, 0x1D244,
}

const placeholder = '\U0010EEEE'

func imageID(pickID int64) int { return 0x0B0000 | int(pickID&0xFFFF) }

// deleteImage frees an image and all its placements (d=I: by id, data too).
func deleteImage(id int) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
}

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
