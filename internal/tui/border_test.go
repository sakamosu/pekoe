package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/theme"
)

// borderModel ignores the user's global theme so the defaults are tested.
func borderModel(t *testing.T, mode string) *model {
	t.Helper()
	t.Setenv("PEKOE_COLOR", mode)
	m := newModel(nil, "")
	m.th = theme.Load(nil)
	m.width, m.height = 120, 40
	return m
}

func firstSGRBefore(line, glyph string) string {
	i := strings.Index(line, glyph)
	if i < 0 {
		return ""
	}
	j := strings.LastIndex(line[:i], "\x1b[38;2;")
	if j < 0 {
		return ""
	}
	return line[j:i]
}

func TestFocusedBorderIsPrimaryToBlueRamp(t *testing.T) {
	m := borderModel(t, "truecolor")
	for _, sz := range [][2]int{{4, 2}, {5, 3}, {30, 8}, {90, 30}} {
		wd, ht := sz[0], sz[1]
		ramp := m.th.FocusRamp(focusRampLen(wd, ht))
		title := m.panelTitle(1, "", "Sessions", "2/4", true)
		out := m.box(wd, ht, boxOpt{title: title, right: m.th.FG(theme.Dim, "r"), focused: true, rows: []row{{Text: "x"}}})
		if len(out) != ht {
			t.Fatalf("%dx%d: %d lines", wd, ht, len(out))
		}
		for i, l := range out {
			if w(l) != wd {
				t.Errorf("%dx%d line %d width %d", wd, ht, i, w(l))
			}
		}
		if got := firstSGRBefore(out[0], "╭"); got != ramp[0] {
			t.Errorf("%dx%d: top-left %q, want primary %q", wd, ht, got, ramp[0])
		}
		last := out[ht-1]
		if got := firstSGRBefore(last, "╯"); got != ramp[len(ramp)-1] {
			t.Errorf("%dx%d: bottom-right %q, want end %q", wd, ht, got, ramp[len(ramp)-1])
		}
		// the title text stays solid primary (bold), not ramped
		if wd >= 30 && !strings.Contains(out[0], m.th.Bold(theme.Primary, "[1] Sessions")) {
			t.Errorf("%dx%d: focused title is not solid primary: %q", wd, ht, out[0])
		}
	}
}

func TestUnfocusedAndDestructiveBordersStaySolid(t *testing.T) {
	m := borderModel(t, "truecolor")
	for name, o := range map[string]boxOpt{
		"unfocused":   {title: "t"},
		"destructive": {title: "t", modal: true, destruct: true},
	} {
		out := m.box(20, 4, o)
		if strings.Count(strings.Join(out, ""), "\x1b[38;2;") > 2*len(out) {
			t.Errorf("%s: border is painted per cell", name)
		}
	}
	m.modal = &confirmModal{m: m}
	out := m.box(20, 4, boxOpt{focused: true})
	if strings.Contains(strings.Join(out, ""), m.th.FocusRamp(focusRampLen(20, 4))[0]) {
		t.Error("with a modal open, the panel behind loses the focus ramp")
	}
}

func TestModalFrameUsesRamp(t *testing.T) {
	m := borderModel(t, "truecolor")
	lines := []string{"", "text", sunk("cmd"), "\x00rule", "more"}
	keys := [][2]string{{"y", "confirm"}}
	out := m.modalFrame(40, "Title", lines, keys, false)
	ramp := m.th.FocusRamp(focusRampLen(40, len(out)))
	if got := firstSGRBefore(out[0], "╭"); got != ramp[0] {
		t.Errorf("modal top-left %q, want %q", got, ramp[0])
	}
	if got := firstSGRBefore(out[len(out)-1], "╯"); got != ramp[len(ramp)-1] {
		t.Errorf("modal bottom-right %q, want %q", got, ramp[len(ramp)-1])
	}
	for i, l := range out {
		if w(l) != 40 {
			t.Errorf("modal line %d width %d", i, w(l))
		}
	}
	if d := m.modalFrame(40, "Delete", lines, keys, true); strings.Contains(strings.Join(d, ""), ramp[0]) {
		t.Error("destructive modal border must stay solid err")
	}
}

func TestANSIBorderIsSolidPrimary(t *testing.T) {
	m := borderModel(t, "ansi")
	out := m.box(20, 4, boxOpt{title: "t", focused: true})
	all := strings.Join(out, "")
	if strings.Contains(all, "38;2;") {
		t.Errorf("ansi mode emitted truecolor: %q", all)
	}
	if !strings.HasPrefix(out[3], "\x1b[35m") { // ANSI 5 magenta
		t.Errorf("ansi focused border is not primary: %q", out[3])
	}
	if ansi.Strip(out[3]) != "╰"+strings.Repeat("─", 18)+"╯" {
		t.Errorf("bottom border %q", ansi.Strip(out[3]))
	}
}

// Every line of a box is exactly its width, even when the title cannot fit
// (a 4-wide box used to grow to 5 with a title).
func TestBoxNeverOverflowsWidth(t *testing.T) {
	for _, mode := range []string{"truecolor", "ansi", "none"} {
		m := borderModel(t, mode)
		title := m.panelTitle(1, "", "Sessions", "2/4", true)
		for wd := 4; wd <= 12; wd++ {
			for ht := 2; ht <= 4; ht++ {
				for _, o := range []boxOpt{
					{title: title, focused: true},
					{title: title, right: m.th.FG(theme.Dim, "right"), focused: true},
					{title: "x"},
				} {
					o.rows = []row{{Text: "content"}}
					out := m.box(wd, ht, o)
					if len(out) != ht {
						t.Fatalf("%s %dx%d: %d lines", mode, wd, ht, len(out))
					}
					for i, l := range out {
						if w(l) != wd {
							t.Errorf("%s %dx%d title %q line %d: width %d %q", mode, wd, ht, ansi.Strip(o.title), i, w(l), ansi.Strip(l))
						}
					}
				}
			}
		}
	}
}
