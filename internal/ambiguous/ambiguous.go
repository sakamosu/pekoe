// Package ambiguous decides how the TUI treats East Asian Ambiguous glyphs
// ([theme] ambiguous, docs/ambiguous-width.md): whether the terminal counts
// them as two cells (path A), measured with a cursor position report or
// set by hand, and the "wide" glyph table pekoe then draws its own text with.
//
// In the wide table pekoe's own glyphs carry no ambiguous-width character
// (Text), and the TUI runs again with RUNEWIDTH_EASTASIAN=1 so that the
// width functions (x/ansi, lipgloss) count what is left — an agent's screen
// pekoe shows as it is — as two cells, like the terminal does (Setup).
package ambiguous

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// The values of [theme] ambiguous.
const (
	Auto   = "auto"   // measure with a cursor position report; narrow when no answer
	Narrow = "narrow" // the terminal counts them one cell: the default table
	Wide   = "wide"   // two cells: the wide table, and count them as two
)

var wide atomic.Bool

// IsWide reports whether the wide table is in use (Setup decided it).
func IsWide() bool { return wide.Load() }

// SetWide switches the wide table on or off. Setup calls it; tests use it
// to draw with the wide table (restore it afterwards).
func SetWide(on bool) { wide.Store(on) }

// table maps every ambiguous-width glyph pekoe draws as its own text to
// ASCII. Box drawing and block elements map one to one (pekoe builds frames
// by counting cells); the rest may take more cells (… → ...), which the
// width functions see, since Text runs before anything is measured.
var table = map[rune]string{
	// body symbols
	'·': "-", '…': "...", '→': "->", '←': "<-", '↔': "<->", '×': "x", '—': "-", '–': "-",
	'’': "'", '‘': "'", '“': `"`, '”': `"`, '°': "o", '˙': "'", '•': "*", '●': "*", '○': "o",
	'◆': "*", '▶': ">", '±': "+-", '≡': "=", '⇧': "Shift+",
	// box drawing
	'─': "-", '━': "-", '┄': "-", '┅': "-", '┈': "-",
	'│': "|", '┃': "|", '┆': "|", '┇': "|", '┊': "|",
	'╭': "+", '╮': "+", '╰': "+", '╯': "+", '┌': "+", '┐': "+", '└': "+", '┘': "+",
	'┏': "+", '┓': "+", '┗': "+", '┛': "+", '├': "+", '┤': "+", '┬': "+", '┴': "+", '┼': "+",
	'╥': "T",
	// block elements: the sparkline's eight heights keep their order
	'▁': "_", '▂': ".", '▃': ":", '▄': "-", '▅': "=", '▆': "+", '▇': "*", '█': "#",
	'▏': "|", '▎': "|", '▍': "|", '▌': "|", '▀': `"`,
}

var replacer = func() *strings.Replacer {
	var pairs []string
	for r, s := range table {
		pairs = append(pairs, string(r), s)
	}
	return strings.NewReplacer(pairs...)
}()

// Text is s as pekoe draws its own text: unchanged in the default table,
// with the ambiguous-width glyphs of Table replaced in the wide one. Never
// use it on what pekoe shows from elsewhere (an agent's screen, a diff, a
// report): that is counted as two cells instead.
func Text(s string) string {
	if !IsWide() {
		return s
	}
	return ASCII(s)
}

// ASCII is s with the glyphs of Table replaced, whichever table is in use.
func ASCII(s string) string { return replacer.Replace(s) }

// Table is the wide table: each glyph and what replaces it (a copy).
func Table() map[rune]string {
	out := make(map[rune]string, len(table))
	for r, s := range table {
		out[r] = s
	}
	return out
}

// ---- deciding, and running again with RUNEWIDTH_EASTASIAN=1 ----

const (
	// EastAsianEnv is read by x/ansi (and so lipgloss) in its init: "1"
	// counts ambiguous-width glyphs as two cells.
	EastAsianEnv = "RUNEWIDTH_EASTASIAN"
	// ReexecEnv marks the process Setup started again; it and EastAsianEnv
	// are taken out of the environment right away, so what the TUI starts
	// (an editor, pekoed, tmux and the agents) does not inherit them.
	ReexecEnv = "PEKOE_AMBIGUOUS_REEXEC"
)

// Plan is what Setup does.
type Plan struct {
	Wide  bool // use the wide table
	Exec  bool // run again with EastAsianEnv=1
	Unset bool // this is the process run again: take the two variables out
}

// Decide is Setup's decision. cfg is [theme] ambiguous; getenv reads the
// environment; counted reports whether the width functions already count
// ambiguous glyphs as two cells; measure (nil without a terminal) asks the
// terminal.
func Decide(cfg string, getenv func(string) string, counted bool, measure func() (wide, ok bool)) Plan {
	if getenv(ReexecEnv) == "1" {
		return Plan{Wide: true, Unset: true}
	}
	if counted { // RUNEWIDTH_EASTASIAN set by the person: already counted as two
		return Plan{Wide: true}
	}
	switch cfg {
	case Narrow:
		return Plan{}
	case Wide:
		return Plan{Wide: true, Exec: true}
	}
	if measure != nil {
		if w, ok := measure(); ok && w {
			return Plan{Wide: true, Exec: true}
		}
	}
	return Plan{}
}

// Counted reports whether the width functions count ambiguous-width
// glyphs as two cells (RUNEWIDTH_EASTASIAN was set when pekoe started).
func Counted() bool { return ansi.StringWidth("·") == 2 }

// Setup decides the table for the TUI before it takes the terminal: with
// the wide table it runs pekoe again with RUNEWIDTH_EASTASIAN=1 (exec does
// not return); in the process run again it only takes the variables out.
// When exec fails the wide table stays, without counting as two.
func Setup(cfg string, tty *os.File) Plan {
	var measure func() (bool, bool)
	if tty != nil {
		measure = func() (bool, bool) { return Measure(tty, 300*time.Millisecond) }
	}
	p := Decide(cfg, os.Getenv, Counted(), measure)
	if p.Unset {
		os.Unsetenv(ReexecEnv)
		os.Unsetenv(EastAsianEnv)
	}
	SetWide(p.Wide)
	if p.Exec {
		if exe, err := os.Executable(); err == nil {
			_ = execve(exe, os.Args, ExecEnv(os.Environ()))
		}
	}
	return p
}

// ExecEnv is environ for the process run again: EastAsianEnv=1 and
// ReexecEnv=1, replacing any value they had (the first of two would win).
func ExecEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		if strings.HasPrefix(kv, EastAsianEnv+"=") || strings.HasPrefix(kv, ReexecEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, EastAsianEnv+"=1", ReexecEnv+"=1")
}

// TTY returns stdin when both stdin and stdout are a terminal (Measure
// needs to write and read the answer), else nil.
func TTY() *os.File {
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		return os.Stdin
	}
	return nil
}

// ---- measuring: cursor position report ----

var cprRe = regexp.MustCompile(`\x1b\[(\d+);(\d+)R`)

// parseCPR returns the column of the last cursor position report in b.
func parseCPR(b []byte) (int, bool) {
	m := cprRe.FindAllSubmatch(b, -1)
	if len(m) == 0 {
		return 0, false
	}
	col, err := strconv.Atoi(string(m[len(m)-1][2]))
	return col, err == nil
}

// Measure prints ● at column 1, asks for the cursor position (ESC[6n) and
// reports whether it advanced two cells. ok is false when the terminal
// does not answer in time. The line is cleared afterwards.
func Measure(tty *os.File, timeout time.Duration) (wide, ok bool) {
	restore, err := rawWithTimeout(tty, timeout)
	if err != nil {
		return false, false
	}
	defer restore()
	if _, err := tty.WriteString("\r●\x1b[6n"); err != nil {
		return false, false
	}
	defer tty.WriteString("\r\x1b[2K")
	var buf []byte
	tmp := make([]byte, 64)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := tty.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if col, ok := parseCPR(buf); ok {
				return col >= 3, true
			}
		}
		if err != nil || n == 0 {
			break // VTIME expired
		}
	}
	return false, false
}
