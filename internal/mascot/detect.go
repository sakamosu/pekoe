package mascot

import (
	"os"
	"strconv"
	"strings"
)

// DetectShape resolves mascot.shape: "auto" → round where the terminal
// draws sextant blocks itself (SextantTerminal), else block.
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

// Terminal versions from which sextants (U+1FB00–U+1FB3B) are drawn by the
// terminal itself (sources in docs/ambiguous-width.md, "5. 端末").
const (
	vteSextantVersion     = 6000   // VTE 0.60.0: VTE_VERSION (GNOME Terminal, Ptyxis, …)
	konsoleSextantVersion = 220400 // Konsole 22.04.0: KONSOLE_VERSION
)

// SextantTerminal: terminals with built-in sextant rendering — Ghostty,
// kitty, WezTerm, foot, VTE 0.60+ (GNOME Terminal, Ptyxis, …), Konsole
// 22.04+ and Alacritty. Ghostty's env survives into tmux, so
// GHOSTTY_RESOURCES_DIR is checked too; so do VTE_VERSION, KONSOLE_VERSION
// and ALACRITTY_WINDOW_ID. Those three are read only while TERM_PROGRAM is
// empty or tmux: a terminal that names itself there (VS Code started from
// GNOME Terminal, …) may only have inherited them.
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
	case strings.HasPrefix(term, "alacritty"):
		return true
	}
	if tp != "" && tp != "tmux" {
		return false
	}
	switch {
	case versionAtLeast(os.Getenv("VTE_VERSION"), vteSextantVersion):
		return true
	case versionAtLeast(os.Getenv("KONSOLE_VERSION"), konsoleSextantVersion):
		return true
	case os.Getenv("ALACRITTY_WINDOW_ID") != "":
		return true
	}
	return false
}

// versionAtLeast: v is a numeric version (VTE_VERSION=7802) of at least min.
func versionAtLeast(v string, min int) bool {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	return err == nil && n >= min
}

// DetectGlyphs resolves mascot.glyphs: "auto" → ascii when the icon set is
// ascii or the TUI draws with the wide table ([theme] ambiguous, decided by
// internal/ambiguous: the terminal counts the outline's █ ▌ as two cells),
// else unicode. "narrow" is a former setting (● as 🞄 for fonts that draw ●
// two cells wide) that does nothing now: it is read as auto, since the
// default glyphs have no ambiguous-width eyes any more.
func DetectGlyphs(cfg, icons string, wide bool) string {
	switch cfg {
	case "unicode", "ascii":
		return cfg
	}
	if icons == "ascii" || wide {
		return "ascii"
	}
	return "unicode"
}
