package config

import (
	"strings"
	"unicode/utf8"
)

// DefaultPrefix is [keys] prefix when it is unset or not a key that can be
// pressed (PrefixKey).
const DefaultPrefix = "ctrl+q" // reachable on JIS keyboards; unused by Claude Code

// PrefixKey reads [keys] prefix as the TUI compares it with a key pressed
// (bubbletea's KeyMsg.String()): modifiers in any case and in tmux or
// caret style (C-a, Ctrl+A, ^a, M-x) become ctrl+a / alt+x. ok is false
// for what never arrives as that key — ctrl+i, ctrl+m and ctrl+[ come in as
// tab, enter and esc — and for a key without ctrl or alt (a letter, tab …)
// that the agent could then never be sent: the default is used instead.
// Taken: ctrl+ a letter or @ \ ] ^ _, F1–F20, and alt+ any of those or a
// single character.
func PrefixKey(s string) (key string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultPrefix, true
	}
	ctrl, alt := false, false
	rest := s
strip:
	for {
		l := strings.ToLower(rest)
		for _, p := range []struct {
			mod  string
			ctrl bool
		}{{"ctrl+", true}, {"ctrl-", true}, {"control+", true}, {"control-", true}, {"c-", true}, {"^", true},
			{"alt+", false}, {"alt-", false}, {"meta+", false}, {"meta-", false}, {"m-", false}} {
			if strings.HasPrefix(l, p.mod) && len(rest) > len(p.mod) {
				if p.ctrl {
					ctrl = true
				} else {
					alt = true
				}
				rest = rest[len(p.mod):]
				continue strip
			}
		}
		break
	}
	mod := ""
	if alt {
		mod = "alt+"
	}
	lower := strings.ToLower(rest)
	if ctrl {
		if len(lower) == 1 && (lower[0] >= 'a' && lower[0] <= 'z' && lower != "i" && lower != "m" || strings.Contains(`@\]^_`, lower)) {
			return mod + "ctrl+" + lower, true
		}
		return DefaultPrefix, false
	}
	if fKey(lower) {
		return mod + lower, true
	}
	if alt && utf8.RuneCountInString(rest) == 1 && rest != " " {
		return mod + rest, true
	}
	return DefaultPrefix, false
}

// fKey: f1 … f20.
func fKey(s string) bool {
	n, ok := strings.CutPrefix(s, "f")
	if !ok || n == "" || len(n) > 2 || n[0] == '0' {
		return false
	}
	v := 0
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
		v = v*10 + int(c-'0')
	}
	return v >= 1 && v <= 20
}
