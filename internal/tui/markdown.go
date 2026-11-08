package tui

// Light markdown for text that comes from GitHub (issue / PR bodies, reviews,
// comments): headings, lists, task lists, quotes, code blocks, rules and
// simple tables, plus inline emphasis, code spans and links. Only theme roles
// are used; the input goes through sanitize first, so the only escape
// sequences in the result are the ones drawn here.

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"boba/internal/theme"
)

var (
	mdHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdList     = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	mdTask     = regexp.MustCompile(`^\[([ xX])\](?:\s+(.*))?$`)
	mdRule     = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	mdTableSep = regexp.MustCompile(`^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$`)
	mdComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// renderMarkdown lays out a markdown body for a cw-wide panel.
func renderMarkdown(th *theme.Theme, ic theme.IconSet, body string, cw int) []string {
	body = mdComment.ReplaceAllString(sanitize(strings.ReplaceAll(body, "\r\n", "\n")), "")
	ascii := ic.Name == "ascii"
	bullet := "•"
	if ascii {
		bullet = "-"
	}
	var out []string
	blank := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	wrap := func(text string, role theme.Role, width int) []string {
		return wrapANSI(renderInline(th, text, role), width)
	}
	src := strings.Split(body, "\n")
	for i := range src {
		src[i] = strings.TrimRight(strings.ReplaceAll(src[i], "\t", "    "), " \r")
	}
	inCode := false
	for i := 0; i < len(src); i++ {
		ln := src[i]
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			if !inCode {
				blank()
			}
			inCode = !inCode
			if lang := strings.Trim(trim, "`~ "); inCode && lang != "" {
				out = append(out, th.FG(theme.Border, "│ ")+th.FG(theme.Dim, lang))
			}
			continue
		}
		if inCode {
			out = append(out, th.FG(theme.Border, "│ ")+th.FG(theme.Text, clip(ln, max(cw-2, 1))))
			continue
		}
		switch {
		case trim == "":
			blank()
		case isTableStart(src, i):
			rows := [][]string{splitCells(trim)}
			aligns := tableAligns(strings.TrimSpace(src[i+1]))
			i += 2
			for ; i < len(src); i++ {
				t := strings.TrimSpace(src[i])
				if t == "" || !strings.Contains(t, "|") {
					break
				}
				rows = append(rows, splitCells(t))
			}
			i--
			blank()
			out = append(out, renderTable(th, rows, aligns, cw)...)
			blank()
		case mdHeading.MatchString(trim):
			g := mdHeading.FindStringSubmatch(trim)
			blank()
			for j, l := range wrap(g[2], theme.Text, cw-len(g[1])-1) {
				mark := strings.Repeat(" ", len(g[1])+1)
				if j == 0 {
					mark = g[1] + " "
				}
				out = append(out, th.FG(theme.Dim, mark)+l)
			}
		case mdRule.MatchString(trim):
			out = append(out, th.FG(theme.Border, strings.Repeat("─", max(cw, 1))))
		case strings.HasPrefix(trim, ">"):
			text := strings.TrimSpace(strings.TrimPrefix(trim, ">"))
			for _, l := range wrap(text, theme.Dim, cw-2) {
				out = append(out, th.FG(theme.Border, "▎ ")+l)
			}
		case mdList.MatchString(ln):
			g := mdList.FindStringSubmatch(ln)
			indent := min(len(g[1]), 8)
			mark, text := g[2], g[3]
			if strings.ContainsAny(mark, "-*+") {
				mark = bullet
				if t := mdTask.FindStringSubmatch(text); t != nil {
					mark, text = taskBox(t[1] != " ", ascii), t[2]
				}
			}
			pre := strings.Repeat(" ", indent) + mark + " "
			for j, l := range wrap(text, theme.Text, cw-w(pre)) {
				if j == 0 {
					out = append(out, strings.Repeat(" ", indent)+th.FG(theme.Dim, mark)+" "+l)
				} else {
					out = append(out, strings.Repeat(" ", w(pre))+l)
				}
			}
		default:
			out = append(out, wrap(trim, theme.Text, cw)...)
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func taskBox(done, ascii bool) string {
	switch {
	case ascii && done:
		return "[x]"
	case ascii:
		return "[ ]"
	case done:
		return "☑"
	}
	return "☐"
}

// ---- tables ----

func isTableStart(src []string, i int) bool {
	if i+1 >= len(src) || !strings.Contains(src[i], "|") {
		return false
	}
	sep := strings.TrimSpace(src[i+1])
	return strings.Contains(sep, "|") && mdTableSep.MatchString(sep)
}

// splitCells splits a table row on the pipes outside code spans; a leading
// and a trailing pipe are optional and \| is a literal pipe.
func splitCells(line string) []string {
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case c == '`':
			n := runLen(line, i, '`')
			end := i + n
			if j := findCodeClose(line, i+n, n); j >= 0 {
				end = j + n
			}
			cur.WriteString(strings.ReplaceAll(line[i:end], `\|`, "|"))
			i = end - 1
		case c == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// tableAligns reads the delimiter row: 'l', 'c' or 'r' per column.
func tableAligns(sep string) []byte {
	var out []byte
	for _, c := range splitCells(sep) {
		switch {
		case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
			out = append(out, 'c')
		case strings.HasSuffix(c, ":"):
			out = append(out, 'r')
		default:
			out = append(out, 'l')
		}
	}
	return out
}

// renderTable draws the cells in aligned columns separated by │, with a rule
// under the header. Columns that don't fit are narrowed, widest first, and
// their cells cut with "…".
func renderTable(th *theme.Theme, rows [][]string, aligns []byte, cw int) []string {
	n := 0
	for _, r := range rows {
		n = max(n, len(r))
	}
	cells := make([][]string, len(rows))
	widths := make([]int, n)
	for i, r := range rows {
		cells[i] = make([]string, n)
		for j := range n {
			if j < len(r) {
				cells[i][j] = renderInline(th, r[j], theme.Text)
			}
			widths[j] = max(widths[j], w(cells[i][j]), 1)
		}
	}
	avail := max(cw-3*(n-1), n)
	for sum(widths) > avail {
		wi := 0
		for j := range widths {
			if widths[j] > widths[wi] {
				wi = j
			}
		}
		if widths[wi] <= 1 {
			break
		}
		widths[wi]--
	}
	sep := th.FG(theme.Border, " │ ")
	var out []string
	for i, r := range cells {
		parts := make([]string, n)
		for j, c := range r {
			a := byte('l')
			if j < len(aligns) {
				a = aligns[j]
			}
			parts[j] = alignCell(c, widths[j], a)
		}
		out = append(out, clip(strings.Join(parts, sep), cw))
		if i == 0 {
			rules := make([]string, n)
			for j, wd := range widths {
				rules[j] = strings.Repeat("─", wd)
			}
			out = append(out, th.FG(theme.Border, clip(strings.Join(rules, "─┼─"), cw)))
		}
	}
	return out
}

func alignCell(s string, width int, align byte) string {
	if w(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	d := max(width-w(s), 0)
	switch align {
	case 'r':
		return strings.Repeat(" ", d) + s
	case 'c':
		return strings.Repeat(" ", d/2) + s + strings.Repeat(" ", d-d/2)
	}
	return s + strings.Repeat(" ", d)
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

// ---- inline ----

type mdStyle struct {
	bold, italic, strike, code bool
	url                        string // link target ("" when not a link)
}

type mdSpan struct {
	text string
	st   mdStyle
}

// renderInline draws the inline markup of one line of text in role base.
// Every span closes its own styling (and link), so the result can be cut or
// wrapped anywhere by wrapANSI / fit / clip.
func renderInline(th *theme.Theme, s string, base theme.Role) string {
	var b strings.Builder
	for _, sp := range parseInline(s, mdStyle{}) {
		role := base
		if sp.st.url != "" {
			role = theme.Secondary // links (ui-guidelines: secondary)
		}
		st := th.Style(role)
		text := sp.text
		if sp.st.code && th.Mode != theme.None {
			st = th.Style(theme.Text)
			if th.Has(theme.Surface) {
				st = st.Background(th.Color(theme.Surface))
			}
		}
		st = st.Bold(sp.st.bold).Italic(sp.st.italic).Strikethrough(sp.st.strike)
		seg := text
		if th.Mode != theme.None {
			seg = st.Render(text)
		}
		if sp.st.code && !th.Has(theme.Surface) {
			tick := th.FG(theme.Dim, "`")
			seg = tick + seg + tick
		}
		if sp.st.url != "" {
			seg = ansi.SetHyperlink(sp.st.url) + seg + ansi.ResetHyperlink()
		}
		b.WriteString(seg)
	}
	return b.String()
}

// parseInline splits s into styled spans: **bold** / __bold__, *italic* /
// _italic_, ~~strike~~, `code`, [text](url), ![alt](url), <url> and bare
// URLs. Nothing is parsed inside a code span, and links don't nest.
func parseInline(s string, st mdStyle) []mdSpan {
	var out []mdSpan
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, mdSpan{lit.String(), st})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isPunct(s[i+1]):
			lit.WriteByte(s[i+1])
			i += 2
			continue
		case c == '`':
			n := runLen(s, i, '`')
			if j := findCodeClose(s, i+n, n); j >= 0 {
				flush()
				code := s[i+n : j]
				if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.Trim(code, " ") != "" {
					code = code[1 : len(code)-1]
				}
				cs := st
				cs.code = true
				out = append(out, mdSpan{code, cs})
				i = j + n
				continue
			}
			lit.WriteString(s[i : i+n])
			i += n
			continue
		case (c == '[' || (c == '!' && i+1 < len(s) && s[i+1] == '[')) && st.url == "":
			at := i
			if c == '!' {
				at++
			}
			if text, url, end, ok := parseLink(s, at); ok {
				flush()
				if c == '!' && strings.TrimSpace(text) == "" {
					text = "image"
				}
				ls := st
				if safeURL(url) {
					ls.url = url
				}
				out = append(out, parseInline(text, ls)...)
				i = end
				continue
			}
		case c == '<' && st.url == "":
			if j := strings.IndexByte(s[i:], '>'); j > 0 {
				if u := s[i+1 : i+j]; safeURL(u) && !strings.ContainsAny(u, " <") {
					flush()
					ls := st
					ls.url = u
					out = append(out, mdSpan{u, ls})
					i += j + 1
					continue
				}
			}
		case c == 'h' && st.url == "" && (i == 0 || !isWord(s[i-1])):
			if loc := bareURL.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
				if u := trimURL(s[i : i+loc[1]]); safeURL(u) {
					flush()
					ls := st
					ls.url = u
					out = append(out, mdSpan{u, ls})
					i += len(u)
					continue
				}
			}
		case c == '*' || c == '_' || c == '~':
			n := runLen(s, i, c)
			if j := emphasisClose(s, i, n); j >= 0 {
				flush()
				es := st
				switch {
				case c == '~':
					es.strike = true
				case n == 1:
					es.italic = true
				case n == 2:
					es.bold = true
				default:
					es.bold, es.italic = true, true
				}
				out = append(out, parseInline(s[i+n:j], es)...)
				i = j + n
				continue
			}
			lit.WriteString(s[i : i+n])
			i += n
			continue
		}
		lit.WriteByte(c)
		i++
	}
	flush()
	return out
}

// emphasisClose finds the closer of the delimiter run s[i:i+n]: a run of the
// same length that follows a non-space. "_" doesn't open or close inside a
// word (snake_case stays as written); "~" only pairs as "~~".
func emphasisClose(s string, i, n int) int {
	c := s[i]
	if n > 3 || (c == '~' && n != 2) || i+n >= len(s) || s[i+n] == ' ' {
		return -1
	}
	if c == '_' && i > 0 && isWord(s[i-1]) {
		return -1
	}
	for k := i + n; k < len(s); {
		switch b := s[k]; {
		case b == '\\':
			k += 2
			continue
		case b == '`':
			m := runLen(s, k, '`')
			if e := findCodeClose(s, k+m, m); e >= 0 {
				k = e + m
			} else {
				k += m
			}
			continue
		case b == c:
			m := runLen(s, k, c)
			if m == n && k > i+n && s[k-1] != ' ' && (c != '_' || k+m >= len(s) || !isWord(s[k+m])) {
				return k
			}
			k += m
			continue
		}
		k++
	}
	return -1
}

// parseLink reads "[text](url "title")" at s[i] == '['.
func parseLink(s string, i int) (text, url string, end int, ok bool) {
	depth := 0
	j := i
	for ; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
			continue
		case '`':
			n := runLen(s, j, '`')
			if e := findCodeClose(s, j+n, n); e >= 0 {
				j = e + n - 1
			} else {
				j += n - 1
			}
			continue
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if j >= len(s) || j+1 >= len(s) || s[j+1] != '(' {
		return "", "", 0, false
	}
	depth = 0
	k := j + 1
	for ; k < len(s); k++ {
		if s[k] == '(' {
			depth++
		} else if s[k] == ')' {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	if k >= len(s) {
		return "", "", 0, false
	}
	dest := strings.TrimSpace(s[j+2 : k])
	if strings.HasPrefix(dest, "<") {
		if e := strings.IndexByte(dest, '>'); e > 0 {
			dest = dest[1:e]
		}
	} else if f := strings.Fields(dest); len(f) > 0 {
		dest = f[0]
	}
	return s[i+1 : j], dest, k + 1, true
}

// safeURL: only web and mail links become clickable; a file:// or app link
// from GitHub text must not be handed to the OS.
func safeURL(u string) bool {
	l := strings.ToLower(u)
	return (strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:")) &&
		!strings.ContainsAny(u, " \x1b\x07")
}

func runLen(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

// findCodeClose returns the start of the next run of exactly n backticks.
func findCodeClose(s string, from, n int) int {
	for k := from; k < len(s); {
		if s[k] != '`' {
			k++
			continue
		}
		m := runLen(s, k, '`')
		if m == n {
			return k
		}
		k += m
	}
	return -1
}

func isWord(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func isPunct(b byte) bool {
	return b < 0x80 && b > ' ' && !isWord(b)
}

// ---- wrapping ----

// wrapANSI wraps a styled line to width and makes every resulting line
// self-contained: styles and links still open at a break are closed at the
// end of the line and reopened at the start of the next one.
func wrapANSI(s string, width int) []string {
	lines := strings.Split(ansi.Wrap(s, max(width, 1), ""), "\n")
	var (
		sgr  []string // SGR sequences in effect
		link string   // OSC 8 sequence in effect
		p    = ansi.NewParser()
	)
	for i, l := range lines {
		pre := link + strings.Join(sgr, "")
		var state byte
		for rest := l; len(rest) > 0; {
			seq, width, n, ns := ansi.DecodeSequence(rest, state, p)
			state = ns
			rest = rest[n:]
			if width > 0 {
				continue
			}
			switch {
			case strings.HasPrefix(seq, "\x1b]8;"):
				link = seq
				if osc8Target(seq) == "" {
					link = ""
				}
			case strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m"):
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					sgr = nil
				} else {
					sgr = append(sgr, seq)
				}
			}
		}
		post := ""
		if len(sgr) > 0 {
			post += "\x1b[0m"
		}
		if link != "" {
			post += ansi.ResetHyperlink()
		}
		lines[i] = pre + l + post
	}
	return lines
}

// mdFirstLine is the first line of text of a markdown body, without the
// block markers and inline markup (a one-line summary for a list row).
func mdFirstLine(body string) string {
	body = mdComment.ReplaceAllString(sanitize(body), "")
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") || mdRule.MatchString(t) {
			continue
		}
		if g := mdHeading.FindStringSubmatch(t); g != nil {
			t = g[2]
		} else if g := mdList.FindStringSubmatch(t); g != nil {
			t = g[3]
		}
		t = strings.TrimSpace(strings.TrimPrefix(t, ">"))
		var b strings.Builder
		for _, sp := range parseInline(t, mdStyle{}) {
			b.WriteString(sp.text)
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s
		}
	}
	return ""
}
