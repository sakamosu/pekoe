package mascot

import (
	"os"
	"strings"
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
