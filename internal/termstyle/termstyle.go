// Package termstyle parses capture-pane -e lines into runs of text and
// style. bobad uses it for the phone's agent screen (web.go): nothing here
// is HTML, the page puts each run's text in with textContent and its colors
// through classes (a theme role) or a CSSOM color (#rrggbb, built here from
// numbers), so the agent's output cannot become markup. The TUI uses it to
// find code drawn in a colour of its own (run.go).
package termstyle

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Run is a stretch of one line in one style. Fg and Bg are a theme role
// ("err", "ok", …: the page's CSS variables, so the user's theme applies)
// or "#rrggbb" (256-color and truecolor); empty is the default.
type Run struct {
	Text      string `json:"t"`
	Fg        string `json:"fg,omitempty"`
	Bg        string `json:"bg,omitempty"`
	Bold      bool   `json:"b,omitempty"`
	Faint     bool   `json:"d,omitempty"`
	Italic    bool   `json:"i,omitempty"`
	Underline bool   `json:"u,omitempty"`
	Strike    bool   `json:"s,omitempty"`
}

// ansiRoles maps the 16 terminal colors to the TUI's roles, as the theme's
// ANSI fallback does (1 err, 2 ok, 3 warn, 5 primary, 6 secondary, 8 dim).
// Blue has no role; it borrows primary-end, the theme's periwinkle.
var ansiRoles = [16]string{
	"border", "err", "ok", "warn", "primary-end", "primary", "secondary", "text",
	"dim", "err", "ok", "warn", "primary-end", "primary", "secondary", "text",
}

// ColorRoles are the role names a run may carry (the web page allows only
// these as classes: app.js TERM_ROLES).
var ColorRoles = map[string]bool{}

func init() {
	for _, r := range ansiRoles {
		ColorRoles[r] = true
	}
	ColorRoles["bg"] = true
}

type sgrState struct {
	fg, bg                                    string
	bold, faint, italic, under, strike, rever bool
}

func (s sgrState) run(text string) Run {
	fg, bg := s.fg, s.bg
	if s.rever {
		fg, bg = bg, fg
		if fg == "" {
			fg = "bg"
		}
		if bg == "" {
			bg = "text"
		}
	}
	return Run{Text: text, Fg: fg, Bg: bg, Bold: s.bold, Faint: s.faint, Italic: s.italic, Underline: s.under, Strike: s.strike}
}

func color256(n int) string {
	switch {
	case n < 0 || n > 255:
		return ""
	case n < 16:
		return ansiRoles[n]
	case n < 232:
		n -= 16
		lv := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + 40*v
		}
		return fmt.Sprintf("#%02x%02x%02x", lv(n/36), lv(n/6%6), lv(n%6))
	}
	g := 8 + 10*(n-232)
	return fmt.Sprintf("#%02x%02x%02x", g, g, g)
}

// extColor reads 38/48's tail: "5;n" or "2;r;g;b" (also with colons, and
// the "2::r:g:b" form with an empty color-space id). It returns the color
// and how many of ps it used.
func extColor(ps []string) (string, int) {
	if len(ps) == 0 {
		return "", 0
	}
	num := func(i int) (int, bool) {
		if i >= len(ps) {
			return 0, false
		}
		v, err := strconv.Atoi(ps[i])
		return v, err == nil
	}
	switch ps[0] {
	case "5":
		if n, ok := num(1); ok {
			return color256(n), 2
		}
		return "", len(ps)
	case "2":
		i := 1
		if len(ps) >= 5 && ps[1] == "" { // 2::r:g:b
			i = 2
		}
		r, ok1 := num(i)
		g, ok2 := num(i + 1)
		b, ok3 := num(i + 2)
		if ok1 && ok2 && ok3 && r <= 255 && g <= 255 && b <= 255 && r >= 0 && g >= 0 && b >= 0 {
			return fmt.Sprintf("#%02x%02x%02x", r, g, b), i + 3
		}
		return "", len(ps)
	}
	return "", len(ps)
}

func (s *sgrState) apply(params string) {
	if params == "" {
		*s = sgrState{}
		return
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		// colon form: one field carries its own sub-parameters (38:2::r:g:b, 4:3)
		var sub []string
		if strings.Contains(f, ":") {
			sub = strings.Split(f, ":")
			f = sub[0]
		}
		n, err := strconv.Atoi(f)
		if err != nil && f != "" {
			continue
		}
		switch {
		case f == "" || n == 0:
			*s = sgrState{}
		case n == 1:
			s.bold = true
		case n == 2:
			s.faint = true
		case n == 3:
			s.italic = true
		case n == 4:
			s.under = len(sub) < 2 || sub[1] != "0"
		case n == 7:
			s.rever = true
		case n == 9:
			s.strike = true
		case n == 22:
			s.bold, s.faint = false, false
		case n == 23:
			s.italic = false
		case n == 24:
			s.under = false
		case n == 27:
			s.rever = false
		case n == 29:
			s.strike = false
		case n >= 30 && n <= 37:
			s.fg = ansiRoles[n-30]
		case n >= 90 && n <= 97:
			s.fg = ansiRoles[n-90+8]
		case n >= 40 && n <= 47:
			s.bg = ansiRoles[n-40]
		case n >= 100 && n <= 107:
			s.bg = ansiRoles[n-100+8]
		case n == 39:
			s.fg = ""
		case n == 49:
			s.bg = ""
		case n == 58: // underline colour: not drawn, but its arguments are not SGR codes
			if sub == nil {
				_, used := extColor(fields[i+1:])
				i += used
			}
		case n == 38 || n == 48:
			var c string
			if sub != nil {
				c, _ = extColor(sub[1:])
			} else {
				var used int
				c, used = extColor(fields[i+1:])
				i += used
			}
			if n == 38 {
				s.fg = c
			} else {
				s.bg = c
			}
		}
	}
}

// Line parses one capture-pane -e line. Each line starts from the
// default style, as the TUI draws them (it closes the SGR at every line end).
// Other escapes (OSC 8 links, cursor moves) and control characters are
// dropped; trailing blanks too, background or not, so a full-width bar does
// not wrap into empty colored rows on a narrow screen.
func Line(line string) []Run {
	var st sgrState
	var runs []Run
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		r := st.run(buf.String())
		buf.Reset()
		if n := len(runs); n > 0 {
			last := runs[n-1]
			last.Text = ""
			cmp := r
			cmp.Text = ""
			if last == cmp {
				runs[n-1].Text += r.Text
				return
			}
		}
		runs = append(runs, r)
	}
	for i := 0; i < len(line); {
		c := line[i]
		if c == 0x1b && i+1 < len(line) {
			switch line[i+1] {
			case '[': // CSI: params, intermediates, final
				j := i + 2
				for j < len(line) && line[j] >= 0x30 && line[j] <= 0x3f {
					j++
				}
				params := line[i+2 : j]
				for j < len(line) && line[j] >= 0x20 && line[j] <= 0x2f {
					j++
				}
				if j >= len(line) {
					i = len(line)
					continue
				}
				if line[j] == 'm' && !strings.ContainsAny(params, "<=>?") {
					flush()
					st.apply(params)
				}
				i = j + 1
				continue
			case ']', 'P', '_', '^', 'X': // OSC / DCS / APC / PM / SOS: to ST or BEL
				j := i + 2
				for j < len(line) && line[j] != 0x07 && !(line[j] == 0x1b && j+1 < len(line) && line[j+1] == '\\') {
					j++
				}
				switch {
				case j >= len(line):
					i = len(line)
				case line[j] == 0x07:
					i = j + 1
				default:
					i = j + 2
				}
				continue
			default:
				i += 2
				continue
			}
		}
		r, size := rune(c), 1
		if c >= 0x80 {
			r, size = utf8.DecodeRuneInString(line[i:])
		}
		if r == '\t' || !unicode.IsControl(r) {
			buf.WriteRune(r)
		}
		i += size
	}
	flush()
	// trailing blanks off
	for len(runs) > 0 {
		last := &runs[len(runs)-1]
		last.Text = strings.TrimRight(last.Text, " \t")
		if last.Text != "" {
			break
		}
		runs = runs[:len(runs)-1]
	}
	if runs == nil {
		runs = []Run{}
	}
	return runs
}

// Tail is the last n lines as runs, trailing blank lines dropped.
func Tail(lines []string, n int) [][]Run {
	out := make([][]Run, 0, len(lines))
	for _, l := range lines {
		out = append(out, Line(l))
	}
	for len(out) > 0 && len(out[len(out)-1]) == 0 {
		out = out[:len(out)-1]
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// Text is a styled line as plain text.
func Text(runs []Run) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.Text)
	}
	return b.String()
}
