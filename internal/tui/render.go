package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"boba/internal/theme"
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
	b := func(s string) string { return th.FG(bc, s) }

	// title line embedded in the top border: ╭─[1] Sessions 2/4 ──── right ─╮
	var top string
	switch {
	case o.title == "":
		top = b(tl + strings.Repeat(hz, inner) + tr)
	default:
		t := clip(o.title, max(inner-3, 1))
		wt := w(t)
		if o.right != "" && inner-wt-w(o.right)-5 >= 1 {
			k := inner - wt - w(o.right) - 5
			top = b(tl+hz) + t + " " + b(strings.Repeat(hz, k)) + " " + o.right + " " + b(hz+tr)
		} else {
			k := max(inner-wt-2, 0)
			top = b(tl+hz) + t + " " + b(strings.Repeat(hz, k)+tr)
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
		if o.bottom {
			rows = rows[len(rows)-avail:]
		} else {
			rows = rows[:avail]
		}
	}
	lines := make([]string, 0, bodyH)
	padS := strings.Repeat(" ", pad)
	render := func(r row) string {
		content := padS + fit(r.Text, cw) + padS
		if r.Sel {
			if th.Mode == theme.Truecolor {
				return lipgloss.NewStyle().Background(th.Color(theme.Select)).Render(content)
			}
			return lipgloss.NewStyle().Reverse(true).Render(ansi.Strip(content))
		}
		if o.bgSunk && th.Mode == theme.Truecolor {
			return lipgloss.NewStyle().Background(th.Color(theme.Bg)).Render(content)
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
	for _, l := range lines {
		out = append(out, b(vt)+l+b(vt))
	}
	out = append(out, b(bl+strings.Repeat(hz, inner)+br))
	return out
}

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
	` _           _`,
	`| |__   ___ | |__   __ _`,
	`| '_ \ / _ \| '_ \ / _` + "`" + ` |`,
	`| |_) | (_) | |_) | (_| |`,
	`|_.__/ \___/|_.__/ \__,_|`,
}

// logoRows centers the dim ASCII logo (one primary accent) with a caption.
func (m *model) logoRows(width, height int, caption string) []row {
	var rows []row
	top := (height - len(logo) - 2) / 2
	for i := 0; i < top; i++ {
		rows = append(rows, row{})
	}
	lw := 26
	left := strings.Repeat(" ", max((width-lw)/2, 0))
	for i, l := range logo {
		t := m.th.FG(theme.Dim, l)
		if i == len(logo)-1 {
			t += "  " + m.th.FG(theme.Primary, "●")
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
