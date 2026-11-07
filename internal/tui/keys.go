package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// tmuxKey translates a Bubble Tea key into what `tmux send-keys` takes.
// literal is set for printable text (sent with -l), otherwise keys holds
// tmux key names. Pastes are returned as paste text (bracketed paste).
type fwd struct {
	literal string
	keys    []string
	paste   string
}

func toTmux(k tea.KeyMsg) fwd {
	if k.Paste {
		return fwd{paste: string(k.Runes)}
	}
	alt := func(name string) string {
		if k.Alt {
			return "M-" + name
		}
		return name
	}
	switch k.Type {
	case tea.KeyRunes, tea.KeySpace:
		r := string(k.Runes)
		if k.Type == tea.KeySpace {
			r = " "
		}
		if k.Alt {
			return fwd{keys: []string{"M-" + r}}
		}
		return fwd{literal: r}
	case tea.KeyEnter:
		return fwd{keys: []string{alt("Enter")}}
	case tea.KeyTab:
		return fwd{keys: []string{alt("Tab")}}
	case tea.KeyShiftTab:
		return fwd{keys: []string{"BTab"}}
	case tea.KeyBackspace:
		return fwd{keys: []string{alt("BSpace")}}
	case tea.KeyDelete:
		return fwd{keys: []string{alt("DC")}}
	case tea.KeyEsc:
		return fwd{keys: []string{"Escape"}}
	case tea.KeyUp:
		return fwd{keys: []string{alt("Up")}}
	case tea.KeyDown:
		return fwd{keys: []string{alt("Down")}}
	case tea.KeyLeft:
		return fwd{keys: []string{alt("Left")}}
	case tea.KeyRight:
		return fwd{keys: []string{alt("Right")}}
	case tea.KeyShiftUp:
		return fwd{keys: []string{"S-Up"}}
	case tea.KeyShiftDown:
		return fwd{keys: []string{"S-Down"}}
	case tea.KeyShiftLeft:
		return fwd{keys: []string{"S-Left"}}
	case tea.KeyShiftRight:
		return fwd{keys: []string{"S-Right"}}
	case tea.KeyCtrlUp:
		return fwd{keys: []string{"C-Up"}}
	case tea.KeyCtrlDown:
		return fwd{keys: []string{"C-Down"}}
	case tea.KeyCtrlLeft:
		return fwd{keys: []string{"C-Left"}}
	case tea.KeyCtrlRight:
		return fwd{keys: []string{"C-Right"}}
	case tea.KeyHome:
		return fwd{keys: []string{alt("Home")}}
	case tea.KeyEnd:
		return fwd{keys: []string{alt("End")}}
	case tea.KeyPgUp:
		return fwd{keys: []string{alt("PPage")}}
	case tea.KeyPgDown:
		return fwd{keys: []string{alt("NPage")}}
	case tea.KeyInsert:
		return fwd{keys: []string{"IC"}}
	}
	if k.Type <= tea.KeyF1 && k.Type >= tea.KeyF20 {
		return fwd{keys: []string{fmt.Sprintf("F%d", int(tea.KeyF1-k.Type)+1)}}
	}
	// control keys: tea's String() gives "ctrl+a" etc.
	s := k.String()
	if strings.HasPrefix(s, "ctrl+") {
		name := strings.TrimPrefix(s, "ctrl+")
		switch name {
		case "@":
			name = "Space"
		}
		key := "C-" + name
		if k.Alt {
			key = "M-" + key
		}
		return fwd{keys: []string{key}}
	}
	if strings.HasPrefix(s, "alt+") {
		return fwd{keys: []string{"M-" + strings.TrimPrefix(s, "alt+")}}
	}
	if len(k.Runes) > 0 {
		return fwd{literal: string(k.Runes)}
	}
	return fwd{}
}

// sgrMouse encodes a mouse event in SGR 1006 form for apps that enabled
// mouse reporting (coordinates already translated to the pane).
func sgrMouse(m tea.MouseMsg, x, y int) []string {
	var b int
	release := m.Action == tea.MouseActionRelease
	switch m.Button {
	case tea.MouseButtonLeft:
		b = 0
	case tea.MouseButtonMiddle:
		b = 1
	case tea.MouseButtonRight:
		b = 2
	case tea.MouseButtonWheelUp:
		b = 64
	case tea.MouseButtonWheelDown:
		b = 65
	case tea.MouseButtonNone:
		b = 35 // motion without buttons
	default:
		return nil
	}
	if m.Action == tea.MouseActionMotion && m.Button != tea.MouseButtonNone {
		b += 32
	}
	if m.Shift {
		b += 4
	}
	if m.Alt {
		b += 8
	}
	if m.Ctrl {
		b += 16
	}
	end := "M"
	if release {
		end = "m"
	}
	seq := fmt.Sprintf("\x1b[<%d;%d;%d%s", b, x+1, y+1, end)
	hex := make([]string, 0, len(seq))
	for i := 0; i < len(seq); i++ {
		hex = append(hex, fmt.Sprintf("%02x", seq[i]))
	}
	return hex
}
