package mascot

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DetectShape resolves mascot.shape: "auto" → round where the terminal
// draws sextant blocks itself (Ghostty, kitty, WezTerm, foot), else block.
func DetectShape(cfg string) string {
	switch cfg {
	case "round", "block":
		return cfg
	}
	if SextantTerminal() {
		return "round"
	}
	return "block"
}

// SextantTerminal: terminals with built-in sextant rendering. Ghostty's
// env survives into tmux, so GHOSTTY_RESOURCES_DIR is checked too.
func SextantTerminal() bool {
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	term := os.Getenv("TERM")
	switch {
	case tp == "ghostty" || tp == "wezterm" || tp == "kitty":
		return true
	case os.Getenv("GHOSTTY_RESOURCES_DIR") != "", os.Getenv("KITTY_WINDOW_ID") != "", os.Getenv("WEZTERM_PANE") != "":
		return true
	case strings.Contains(term, "ghostty"), strings.Contains(term, "kitty"), strings.HasPrefix(term, "foot"):
		return true
	}
	return false
}

// DetectGlyphs resolves mascot.glyphs: "auto" → ascii when the icon set is
// ascii or the terminal draws ambiguous-width glyphs (● ◉ ◕ ×) two cells
// wide, measured with a cursor position report.
func DetectGlyphs(cfg, icons string, tty *os.File) string {
	switch cfg {
	case "unicode", "ascii":
		return cfg
	}
	if icons == "ascii" {
		return "ascii"
	}
	if tty != nil {
		if wide, ok := AmbiguousWide(tty, 300*time.Millisecond); ok && wide {
			return "ascii"
		}
	}
	return "unicode"
}

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

// AmbiguousWide prints ● at column 1, asks for the cursor position
// (ESC[6n) and reports whether it advanced two cells. ok is false when the
// terminal does not answer in time. The line is cleared afterwards.
func AmbiguousWide(tty *os.File, timeout time.Duration) (wide, ok bool) {
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
