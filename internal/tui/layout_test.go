package tui

import (
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/config"
	"pekoe/internal/engine"
	"pekoe/internal/paths"
)

func TestLeftWidthDefaultUnchanged(t *testing.T) {
	// nothing saved: 40% between 34 and 56, as before the layout keys
	for _, c := range []struct{ total, want int }{{100, 40}, {80, 34}, {120, 48}, {140, 56}, {240, 56}} {
		if got := (Layout{}).leftWidth(c.total); got != c.want {
			t.Errorf("total %d: got %d want %d", c.total, got, c.want)
		}
	}
}

func TestLeftWidthLimits(t *testing.T) {
	// the left column never drops below leftMin, the main panel's pty never
	// below agentMin; no fixed upper bound otherwise
	if got := (Layout{Left: 0.1}).leftWidth(200); got != leftMin {
		t.Errorf("min: got %d", got)
	}
	l := Layout{Left: 0.9}
	if got := l.leftWidth(200); got != 200-1-agentChrome-agentMin {
		t.Errorf("max: got %d", got)
	}
	if got := l.agentWidth(200); got != agentMin {
		t.Errorf("agent at max: got %d", got)
	}
	if got := (Layout{Left: 0.5}).leftWidth(200); got != 100 {
		t.Errorf("past the old 56: got %d", got)
	}
}

func TestResizedSteps(t *testing.T) {
	l := Layout{}
	for i, want := range []int{60, 64, 68} {
		l = l.resized(150, leftStep)
		if got := l.leftWidth(150); got != want {
			t.Fatalf("step %d: got %d want %d", i, got, want)
		}
	}
	for range 30 {
		l = l.resized(150, -leftStep)
	}
	if got := l.leftWidth(150); got != leftMin {
		t.Fatalf("narrowest: got %d", got)
	}
	// a share: a wider terminal keeps the proportion
	l = Layout{}.resized(100, leftStep) // 44 of 100
	if got := l.leftWidth(200); got != 88 {
		t.Fatalf("share: got %d", got)
	}
	if (Layout{Zoom: true}).resized(150, leftStep).Zoom {
		t.Fatal("resizing should unzoom")
	}
}

func TestAgentWidthZoom(t *testing.T) {
	if got := (Layout{Zoom: true}).agentWidth(150); got != 150-agentChrome {
		t.Errorf("zoom: got %d", got)
	}
	if got, want := (Layout{}).agentWidth(150), 150-56-1-agentChrome; got != want {
		t.Errorf("default: got %d want %d", got, want)
	}
}

func TestLayoutSaveLoad(t *testing.T) {
	t.Setenv("PEKOE_HOME", t.TempDir())
	if got := LoadLayout(); got != (Layout{}) {
		t.Fatalf("nothing saved: %+v", got)
	}
	want := Layout{Left: 0.45, Zoom: true}
	if err := saveLayout(paths.LayoutFile(), want); err != nil {
		t.Fatal(err)
	}
	if got := LoadLayout(); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	// the CLI's agentPaneSize sees the same width
	if got := AgentWidth(150); got != 150-agentChrome {
		t.Fatalf("AgentWidth: got %d", got)
	}
	for _, bad := range []string{"{", `{"left":1.5}`, `{"left":-1}`} {
		if err := os.WriteFile(paths.LayoutFile(), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := LoadLayout(); got != (Layout{}) {
			t.Errorf("%s: got %+v", bad, got)
		}
	}
}

func layoutModel(t *testing.T, width int) *model {
	t.Helper()
	m := tapiModel(t, "none", config.Mascot{Glyphs: "ascii"})
	m.width, m.height = width, 40
	t.Setenv("PEKOE_HOME", t.TempDir())
	m.layoutPath = paths.LayoutFile()
	return m
}

var ctrlQ = tea.KeyMsg{Type: tea.KeyCtrlQ}

func TestZoomToggle(t *testing.T) {
	m := layoutModel(t, 150)
	m.onKey(ctrlQ)
	if m.onKey(runes("z")) == nil || !m.layout.Zoom || m.focus != pAgent {
		t.Fatalf("zoom: %+v focus %d", m.layout, m.focus)
	}
	if aw, _ := m.agentSize(); aw != 150-agentChrome {
		t.Fatalf("zoomed pty width %d", aw)
	}
	if x, _ := m.agentOrigin(); x != 2 {
		t.Fatalf("zoomed origin %d", x)
	}
	m.onKey(ctrlQ)
	m.onKey(runes("z"))
	if m.layout.Zoom {
		t.Fatal("second z should unzoom")
	}
	if aw, _ := m.agentSize(); aw != (Layout{}).agentWidth(150) {
		t.Fatalf("unzoomed pty width %d", aw)
	}
}

func TestZoomFromTypingKeepsTyping(t *testing.T) {
	m := layoutModel(t, 150)
	m.focus, m.typing = pAgent, true
	m.onKey(ctrlQ)
	if m.typing {
		t.Fatal("prefix should leave typing")
	}
	m.onKey(runes("z"))
	if !m.layout.Zoom || !m.typing || m.focus != pAgent {
		t.Fatalf("zoom from typing: %+v typing %v focus %d", m.layout, m.typing, m.focus)
	}
	// zoomed, the prefix leaves typing but stays on the (only) main panel
	m.onKey(ctrlQ)
	if m.typing || m.focus != pAgent {
		t.Fatalf("leave while zoomed: typing %v focus %d", m.typing, m.focus)
	}
	// any other key after ^q is a plain key, not <prefix> l
	m.focus = pSessions
	m.layout.Zoom = false
	m.typing = true
	m.onKey(ctrlQ)
	m.onKey(runes("h"))
	if m.prefix || m.prefixType {
		t.Fatal("prefix should be spent")
	}
}

func TestResizeKeysUnzoomAndReset(t *testing.T) {
	m := layoutModel(t, 150)
	m.layout.Zoom = true
	m.onKey(ctrlQ)
	m.onKey(runes(">"))
	if m.layout.Zoom || m.leftWidth() != 60 {
		t.Fatalf("> from zoom: %+v left %d", m.layout, m.leftWidth())
	}
	m.onKey(ctrlQ)
	m.onKey(runes("<"))
	m.onKey(ctrlQ)
	m.onKey(runes("<"))
	if m.leftWidth() != 52 {
		t.Fatalf("< <: left %d", m.leftWidth())
	}
	m.onKey(ctrlQ)
	m.onKey(runes("="))
	if m.layout != (Layout{}) {
		t.Fatalf("= : %+v", m.layout)
	}
}

func TestNarrowIgnoresLayoutKeys(t *testing.T) {
	m := layoutModel(t, 90)
	m.layout = Layout{Left: 0.3}
	for _, k := range []string{"z", "<", ">", "="} {
		m.onKey(ctrlQ)
		m.onKey(runes(k))
	}
	if m.layout != (Layout{Left: 0.3}) || m.layoutPending() {
		t.Fatalf("narrow changed the layout: %+v", m.layout)
	}
}

func TestFocusLeftUnzooms(t *testing.T) {
	m := layoutModel(t, 150)
	m.onKey(ctrlQ)
	m.onKey(runes("z"))
	m.onKey(runes("1"))
	if m.unzoomIfLeft() == nil || m.layout.Zoom {
		t.Fatal("focusing Sessions should bring the left column back")
	}
}

func TestLayoutDebounce(t *testing.T) {
	m := layoutModel(t, 150)
	for range 3 {
		m.onKey(ctrlQ)
		m.onKey(runes(">"))
	}
	if !m.layoutPending() {
		t.Fatal("should wait to settle")
	}
	// the ptys are not chased while the keys repeat
	if m.fitScreen("a", engine.WinAgent, &engine.Screen{Width: 80, Height: 30}) != nil {
		t.Fatal("fitScreen resized before the layout settled")
	}
	if m.layoutSettled(1) != nil || m.layoutSettled(2) != nil {
		t.Fatal("a stale tick should do nothing")
	}
	if _, err := os.Stat(m.layoutPath); err == nil {
		t.Fatal("saved before settling")
	}
	cmd := m.layoutSettled(3)
	if cmd == nil || m.layoutPending() {
		t.Fatal("the last tick should settle")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("save: %v", msg)
	}
	if got := LoadLayout(); got.leftWidth(150) != 68 {
		t.Fatalf("saved %+v", got)
	}
}
