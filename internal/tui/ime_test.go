package tui

import (
	"strings"
	"testing"
)

func TestExtractCursor(t *testing.T) {
	lines := []string{
		"│ header",
		"│ ❯ あい" + withCursor("", 0) + "う",
		"bar",
	}
	x, y, ok := extractCursor(lines)
	if !ok || x != 8 || y != 1 {
		t.Fatalf("got (%d,%d,%v), want (8,1,true)", x, y, ok)
	}
	for _, l := range lines {
		if strings.Contains(l, cursorMark) {
			t.Fatalf("marker left in %q", l)
		}
	}
}

func TestExtractCursorNone(t *testing.T) {
	if _, _, ok := extractCursor([]string{"a", "b"}); ok {
		t.Fatal("expected no cursor")
	}
}

func TestWithCursorMarksPosition(t *testing.T) {
	lines := []string{withCursor("あいう", 2)}
	if x, _, ok := extractCursor(lines); !ok || x != 2 {
		t.Fatalf("got x=%d ok=%v, want 2", x, ok)
	}
}
