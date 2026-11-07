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

// 全角文字の上のカーソルは、その文字を丸ごと反転し、行の幅を変えない。
func TestWithCursorWideChar(t *testing.T) {
	line := "❯ １２３４５６"
	got := withCursor(line, 10)
	want := "❯ １２３４" + cursorMark + "\x1b[7m５\x1b[27m６"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if w(got) != w(line) {
		t.Fatalf("width changed: %d -> %d", w(line), w(got))
	}
}
