package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"pekoe/internal/config"
	"pekoe/internal/engine"
	"pekoe/internal/theme"
)

// paintBgRender is paintBg as it was: the whole frame through lipgloss's
// Render with the background.
func paintBgRender(th *theme.Theme, out string) string {
	if th.Mode == theme.Truecolor {
		out = lipgloss.NewStyle().Background(th.Color(theme.Bg)).Render(strings.ReplaceAll(out, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Bg)))
	}
	return out
}

// agentScreen: what an agent's pane may hold (SGR with resets, 256 and
// true colors, wide and ambiguous-width text, blank lines).
func agentScreen(w, h int) *engine.Screen {
	sc := &engine.Screen{Width: w, Height: h}
	for i := 0; i < h; i++ {
		var l string
		switch i % 5 {
		case 0:
			l = "\x1b[38;2;215;119;87m⏺\x1b[0m \x1b[1mUpdate\x1b[22m(internal/tui/app.go) 日本語のテキスト ①②"
		case 1:
			l = "  \x1b[2m⎿\x1b[22m  \x1b[48;2;2;40;0m+ added := foo(bar)\x1b[0m done"
		case 2:
			l = "\x1b[38;5;246m" + strings.Repeat("plain ascii text ", 12) + "\x1b[m"
		case 3:
			l = ""
		default:
			l = "> 絵文字 🙂 と罫線 ─│┌ \x1b[7m reverse \x1b[0m"
		}
		sc.Lines = append(sc.Lines, l)
	}
	sc.Cursor.X, sc.Cursor.Y, sc.Cursor.Visible = 2, h-1, true
	return sc
}

// The new paintBg paints every screen exactly as Render did: the splash,
// a session's screen (wide, narrow, zoomed), the help over it, the
// dashboard, in true color (painted) and 256 colors / none (as is).
func TestPaintBgSameAsRender(t *testing.T) {
	for _, color := range []string{"truecolor", "256", "none"} {
		screens := map[string]func() *model{
			"splash": func() *model {
				m := repoModel(t, color, 140)
				m.tapi.splash = true
				return m
			},
			"dashboard": func() *model {
				m, _ := dashModel(t)
				dashLook(t, m, color, "nerd")
				return m
			},
		}
		for _, width := range []int{140, 220, 60} {
			screens[fmt.Sprintf("session-%d", width)] = func() *model {
				m := repoModel(t, color, width)
				m.height = 45
				m.sel = 0
				m.lastSel = m.current().Slug
				m.screen, m.screenOf = agentScreen(m.layout.agentWidth(m.width), m.height-3), m.current().Slug
				return m
			}
		}
		screens["help"] = func() *model {
			m := screens["session-140"]()
			m.onKey(key("?"))
			return m
		}
		screens["zoomed"] = func() *model {
			m := screens["session-140"]()
			m.layout.Zoom = true
			return m
		}
		for name, mk := range screens {
			m := mk()
			m.th = theme.Load(&config.Theme{}) // after PEKOE_COLOR (and the lipgloss profile) are set
			raw, paint := m.screenFrame()
			if !paint {
				t.Fatalf("%s %s: not painted", color, name)
			}
			if color == "truecolor" && name == "help" && !strings.Contains(raw, "\x1b[") {
				t.Fatalf("%s: no color in the frame", name)
			}
			want, got := paintBgRender(m.th, raw), m.paintBg(raw)
			if got != want {
				t.Errorf("%s %s: paintBg differs from Render\n%s", color, name, firstDiff(want, got))
			}
			if got != m.View() {
				t.Errorf("%s %s: View is not paintBg(screenFrame)", color, name)
			}
			if color == "truecolor" && got == raw {
				t.Errorf("%s: true color frame not painted", name)
			}
		}
	}
	// what screenFrame never gives (tabs, CRLF): still Render's
	t.Setenv("PEKOE_COLOR", "truecolor")
	m := repoModel(t, "truecolor", 140)
	m.th = theme.Load(&config.Theme{})
	for _, raw := range []string{"a\tb\nc   ", "a\r\nb", "", "one line", "\x1b[0m\n\x1b[0m"} {
		if want, got := paintBgRender(m.th, raw), m.paintBg(raw); got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < max(len(al), len(bl)); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d:\n want %q\n got  %q", i, x, y)
		}
	}
	return ""
}
