package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/theme"
)

// width-aware helpers on ANSI strings
func w(s string) int { return ansi.StringWidth(s) }

func fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if w(s) > n {
		s = ansi.Truncate(s, n, "…")
	}
	if d := n - w(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if w(s) > n {
		return ansi.Truncate(s, n, "")
	}
	return s
}

// row is one content line of a panel; Sel paints it with the select role
// across the full inner width (no cursor glyphs).
type row struct {
	Text string
	Sel  bool
}

type boxOpt struct {
	title    string // already styled
	right    string // right side of the title line, already styled
	rows     []row
	footer   string // dim summary line pinned to the bottom
	focused  bool
	pad      int // horizontal padding inside the border (default 1)
	modal    bool
	destruct bool // err border for destructive confirmation
	bgSunk   bool
	bottom   bool // align rows to the bottom (agent output)
}

// box draws a rounded panel of exactly width×height cells.
// Focus is expressed only by border + title color (primary).
func (m *model) box(width, height int, o boxOpt) []string {
	th := m.th
	if width < 4 || height < 2 {
		return nil
	}
	bc := theme.Border
	if o.focused || o.modal {
		bc = theme.Primary
	}
	if o.destruct {
		bc = theme.Err
	}
	if m.modal != nil && !o.modal {
		bc = theme.Border // the modal is the only focus
	}
	pad := 1
	if o.pad > 0 || o.pad == -1 {
		pad = max(o.pad, 0)
	}
	inner := width - 2
	cw := inner - 2*pad
	tl, tr, bl, br, hz, vt := "╭", "╮", "╰", "╯", "─", "│"
	if m.square {
		tl, tr, bl, br = "┌", "┐", "└", "┘"
	}
	b := m.borderPainter(bc, width, height)

	// title line embedded in the top border: ╭─[1] Sessions 2/4 ──── right ─╮
	var top string
	switch {
	case o.title == "" || inner < 3:
		// ╭─T ╮ needs 5 cells; a narrower box keeps a plain top border
		top = b(0, 0, tl+strings.Repeat(hz, inner)+tr)
	default:
		t := clip(o.title, max(inner-3, 1))
		wt := w(t)
		if o.right != "" && inner-wt-w(o.right)-5 >= 1 {
			k := inner - wt - w(o.right) - 5
			top = b(0, 0, tl+hz) + t + " " + b(3+wt, 0, strings.Repeat(hz, k)) + " " + o.right + " " + b(inner, 0, hz+tr)
		} else {
			k := max(inner-wt-2, 0)
			top = b(0, 0, tl+hz) + t + " " + b(3+wt, 0, strings.Repeat(hz, k)+tr)
		}
	}
	out := []string{top}

	bodyH := height - 2
	rows := o.rows
	hasFooter := o.footer != "" && bodyH >= 2
	avail := bodyH
	if hasFooter {
		avail--
	}
	if len(rows) > avail {
		switch {
		case o.bottom:
			rows = rows[len(rows)-avail:]
		case selRow(rows) >= avail: // keep the selected row in view
			start := clampi(selRow(rows)-avail+1, 0, len(rows)-avail)
			rows = rows[start : start+avail]
		default:
			rows = rows[:avail]
		}
	}
	lines := make([]string, 0, bodyH)
	padS := strings.Repeat(" ", pad)
	render := func(r row) string {
		content := padS + fit(r.Text, cw) + padS
		if r.Sel {
			if th.Mode == theme.Truecolor {
				// 中の色付き部分(状態の点など)が \x1b[0m で背景まで消すので、リセットのたびに塗り直す
				return lipgloss.NewStyle().Background(th.Color(theme.Select)).Render(strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Select)))
			}
			return lipgloss.NewStyle().Reverse(true).Render(ansi.Strip(content))
		}
		if o.bgSunk && th.Mode == theme.Truecolor {
			return lipgloss.NewStyle().Background(th.Color(theme.Bg)).Render(strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Bg)))
		}
		return content
	}
	blank := strings.Repeat(" ", inner)
	if o.bottom {
		for i := 0; i < avail-len(rows); i++ {
			lines = append(lines, blank)
		}
	}
	for _, r := range rows {
		lines = append(lines, render(r))
	}
	for len(lines) < avail {
		lines = append(lines, blank)
	}
	if hasFooter {
		lines = append(lines, padS+fit(o.footer, cw)+padS)
	}
	for i, l := range lines {
		out = append(out, b(0, i+1, vt)+l+b(width-1, i+1, vt))
	}
	out = append(out, b(0, height-1, bl+strings.Repeat(hz, inner)+br))
	return out
}

// borderPainter returns b(x, y, s), which paints the border run s whose first
// cell is at (x, y) of a width×height frame. A primary border follows the
// focus ramp (primary → blue end) along the TL→BR diagonal; rows count double
// because a cell is about twice as tall as it is wide. Every other border, and
// ansi/none or a theme that overrides primary, stays one solid role.
func (m *model) borderPainter(bc theme.Role, width, height int) func(x, y int, s string) string {
	th := m.th
	var ramp []string
	if bc == theme.Primary {
		ramp = th.FocusRamp(focusRampLen(width, height))
	}
	return func(x, y int, s string) string {
		if ramp == nil {
			return th.FG(bc, s)
		}
		var sb strings.Builder
		for i, r := range []rune(s) {
			if j := x + i + 2*y; j >= 0 && j < len(ramp) {
				sb.WriteString(ramp[j])
			}
			sb.WriteRune(r)
		}
		sb.WriteString("\x1b[0m")
		return sb.String()
	}
}

// focusRampLen is the number of ramp steps a width×height border needs:
// one per diagonal position x+2y, from the top-left to the bottom-right corner.
func focusRampLen(width, height int) int { return width + 2*(height-1) }

// panelTitle renders "[n] icon Name position state".
func (m *model) panelTitle(n int, icon, name, extra string, focused bool) string {
	th := m.th
	head := ""
	if n > 0 {
		head = fmt.Sprintf("[%d] ", n)
	}
	head += theme.Prefix(icon) + name
	if focused && m.modal == nil {
		head = th.Bold(theme.Primary, head)
	} else {
		head = th.FG(theme.Dim, head)
	}
	if extra != "" {
		head += " " + th.FG(theme.Dim, extra)
	}
	return head
}

// pill renders a powerline segment with rounded caps.
func (m *model) pill(text string, fill theme.Role, fg theme.Role, bold bool) string {
	th := m.th
	if th.Mode != theme.Truecolor {
		t := ansi.Strip(text)
		if bold {
			return th.Bold(theme.Primary, t)
		}
		return t
	}
	st := lipgloss.NewStyle().Background(th.Color(fill)).Foreground(th.Color(fg))
	if bold {
		st = st.Bold(true)
	}
	// re-apply the fill after inner resets so nested colored spans keep the background
	body := strings.ReplaceAll(text, "\x1b[0m", "\x1b[0m"+bgSeq(th, fill))
	l, r := m.ic.CapL, m.ic.CapR
	if l == "" {
		return st.Render(" " + body + " ")
	}
	cap := lipgloss.NewStyle().Foreground(th.Color(fill))
	return cap.Render(l) + st.Render(body) + cap.Render(r)
}

func bgSeq(th *theme.Theme, r theme.Role) string {
	s := lipgloss.NewStyle().Background(th.Color(r)).Render("x")
	if i := strings.Index(s, "x"); i > 0 {
		return s[:i]
	}
	return ""
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// spark renders activity (0..7); latest point secondary, the rest dim.
func (m *model) spark(vals []int) string {
	if len(vals) == 0 {
		return ""
	}
	var b strings.Builder
	for _, v := range vals[:len(vals)-1] {
		b.WriteRune(sparks[clampi(v, 0, 7)])
	}
	last := string(sparks[clampi(vals[len(vals)-1], 0, 7)])
	return m.th.FG(theme.Dim, b.String()) + m.th.FG(theme.Secondary, last)
}

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ktok(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprint(n)
}

var logo = []string{
	`             _`,
	` _ __   ___ | | _____   ___`,
	`| '_ \ / _ \| |/ / _ \ / _ \`,
	`| |_) |  __/|   < (_) |  __/`,
	`| .__/ \___||_|\_\___/ \___|`,
	`|_|`,
}

// logoBase is the logo's baseline row (the p's tail hangs below it).
const logoBase = 4

// logoDot is the one primary accent beside the logo.
const logoDot = "●"

// logoRows centers the dim ASCII logo (one primary accent) with a caption.
func (m *model) logoRows(width, height int, caption string) []row {
	var rows []row
	top := (height - len(logo) - 2) / 2
	for i := 0; i < top; i++ {
		rows = append(rows, row{})
	}
	lw := 0
	for _, l := range logo {
		lw = max(lw, len(l))
	}
	left := strings.Repeat(" ", max((width-lw)/2, 0))
	for i, l := range logo {
		t := m.th.FG(theme.Dim, l)
		if i == logoBase {
			t += "  " + m.th.FG(theme.Primary, logoDot)
		}
		rows = append(rows, row{Text: left + t})
	}
	rows = append(rows, row{}, row{Text: left + m.th.FG(theme.Dim, caption)})
	return rows
}

// keyHints renders `key action` pairs: key secondary (bold), action dim.
func (m *model) keyHints(pairs [][2]string, width int) string {
	// drop from the back when narrow; "?" stays last
	for len(pairs) > 0 {
		var parts []string
		for _, p := range pairs {
			parts = append(parts, m.th.Bold(theme.Secondary, p[0])+" "+m.th.FG(theme.Dim, p[1]))
		}
		s := strings.Join(parts, "  ")
		if w(s) <= width || len(pairs) == 1 {
			return s
		}
		if len(pairs) >= 2 && pairs[len(pairs)-1][0] == "?" {
			pairs = append(pairs[:len(pairs)-2], pairs[len(pairs)-1])
		} else {
			pairs = pairs[:len(pairs)-1]
		}
	}
	return ""
}

func joinH(a, b []string, gap int) []string {
	n := max(len(a), len(b))
	out := make([]string, n)
	aw := 0
	for _, l := range a {
		aw = max(aw, w(l))
	}
	sp := strings.Repeat(" ", gap)
	for i := 0; i < n; i++ {
		l := ""
		if i < len(a) {
			l = a[i]
		}
		r := ""
		if i < len(b) {
			r = b[i]
		}
		out[i] = fit(l, aw) + sp + r
	}
	return out
}

// placeAt draws block over base with its top-left corner at (x, y).
func placeAt(base []string, y, x int, block []string) []string {
	out := append([]string(nil), base...)
	bw := 0
	for _, l := range block {
		bw = max(bw, w(l))
	}
	for i, l := range block {
		yy := y + i
		if yy < 0 || yy >= len(out) {
			continue
		}
		bl := out[yy]
		pre := ansi.Truncate(bl, x, "")
		if pw := w(pre); pw < x {
			pre += strings.Repeat(" ", x-pw)
		}
		post := ansi.TruncateLeft(bl, x+bw, "")
		out[yy] = pre + "\x1b[0m" + l + "\x1b[0m" + post
	}
	return out
}

// overlay draws a modal block centered over base lines.
func overlay(base []string, block []string, width int) []string {
	if len(block) == 0 {
		return base
	}
	bw := 0
	for _, l := range block {
		bw = max(bw, w(l))
	}
	top := max((len(base)-len(block))/2-1, 0)
	left := max((width-bw)/2, 0)
	out := append([]string(nil), base...)
	for i, l := range block {
		y := top + i
		if y >= len(out) {
			break
		}
		bl := out[y]
		pre := ansi.Truncate(bl, left, "")
		if pw := w(pre); pw < left {
			pre += strings.Repeat(" ", left-pw)
		}
		post := ansi.TruncateLeft(bl, left+bw, "")
		out[y] = pre + "\x1b[0m" + l + "\x1b[0m" + post
	}
	return out
}
