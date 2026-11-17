package config

import (
	"strings"
	"testing"
)

// [keys] prefix is read as bubbletea names the key pressed; what never
// arrives as that key falls back to the default and config check says so.
func TestPrefixKey(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"", "ctrl+q", true},
		{"ctrl+q", "ctrl+q", true},
		{" Ctrl+A ", "ctrl+a", true},
		{"C-a", "ctrl+a", true},
		{"c-B", "ctrl+b", true},
		{"^q", "ctrl+q", true},
		{"control-x", "ctrl+x", true},
		{"ctrl+]", "ctrl+]", true},
		{"ctrl+\\", "ctrl+\\", true},
		{"F12", "f12", true},
		{"alt+q", "alt+q", true},
		{"M-q", "alt+q", true},
		{"Alt+Ctrl+A", "alt+ctrl+a", true},
		{"alt+Q", "alt+Q", true}, // shift+q with alt: the case is the key
		// arrive as tab / enter / esc
		{"ctrl+i", "ctrl+q", false},
		{"C-m", "ctrl+q", false},
		{"ctrl+[", "ctrl+q", false},
		// no modifier: the agent could never be sent it
		{"a", "ctrl+q", false},
		{"tab", "ctrl+q", false},
		{"esc", "ctrl+q", false},
		{"ctrl+", "ctrl+q", false},
		{"ctrl+qq", "ctrl+q", false},
		{"f21", "ctrl+q", false},
		{"alt+ ", "ctrl+q", false},
	} {
		got, ok := PrefixKey(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("PrefixKey(%q) = %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCheckPrefix(t *testing.T) {
	for _, p := range []string{"ctrl+q", "C-a", "Ctrl+A", "alt+x", "f5"} {
		if ps := Check([]byte("[keys]\nprefix = \"" + p + "\"\n")); len(ps) != 0 {
			t.Errorf("%q: %s", p, problems(ps))
		}
	}
	for _, p := range []string{"ctrl+i", "ctrl+m", "ctrl+[", "q", "C-"} {
		ps := problems(Check([]byte("[keys]\nprefix = \"" + p + "\"\n")))
		if !strings.Contains(ps, "keys.prefix:") || !strings.Contains(ps, "ctrl+q is used") {
			t.Errorf("%q: %q", p, ps)
		}
	}
}
