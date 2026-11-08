package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/config"
	"pekoe/internal/state"
	"pekoe/internal/tips"
)

func tipModel(t *testing.T, w, h int) *model {
	t.Helper()
	m := tapiModel(t, "none", config.Mascot{Glyphs: "ascii"})
	m.width, m.height = w, h
	m.tipSync()
	return m
}

// pinTip shows a known tip: random ones could share words with the caption
// ("pekoe …") and fool the checks below.
func pinTip(t *testing.T, m *model) tips.Tip {
	t.Helper()
	tp, ok := tips.ByID("zoom")
	if !ok {
		t.Fatal("no zoom tip")
	}
	m.tip.cur = &tp
	return tp
}

// keepsSize: every line of the view is exactly the terminal's width.
func keepsSize(t *testing.T, m *model, out string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != m.height {
		t.Errorf("%dx%d: %d lines", m.width, m.height, len(lines))
	}
	for i, l := range lines {
		if ansi.StringWidth(l) != m.width {
			t.Errorf("%dx%d line %d: width %d", m.width, m.height, i, ansi.StringWidth(l))
		}
	}
}

func TestTipInEmptyPane(t *testing.T) {
	m := tipModel(t, 120, 40)
	if m.tip.cur == nil {
		t.Fatal("no tip picked for the empty pane")
	}
	out := ansi.Strip(m.View())
	keepsSize(t, m, m.View())
	for _, want := range []string{"no session selected", "n new session · pekoe new <branch>", "tip  . next", m.tip.cur.Short} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// the detail is there too (its first few characters, before any wrap)
	tp := pinTip(t, m)
	out = ansi.Strip(m.View())
	if !strings.Contains(out, "左の列を隠し") || !strings.Contains(out, tp.Short) {
		t.Errorf("detail missing")
	}
}

func TestTipWhileCreating(t *testing.T) {
	m := tipModel(t, 120, 40)
	m.sessions = []*state.Session{{Slug: "feat-x", Branch: "feat/x", State: state.Creating, StateSince: time.Now()}}
	m.tipSync()
	if out := ansi.Strip(m.View()); !strings.Contains(out, "creating worktree feat/x") || !strings.Contains(out, m.tip.cur.Short) {
		t.Errorf("no tip while creating:\n%s", out)
	}
	// other empty panes carry no tip
	for _, s := range []*state.Session{
		{Slug: "feat-x", Branch: "feat/x", State: state.Failed, Error: "setup failed: exit 1"},
		{Slug: "feat-x", Branch: "feat/x", State: state.Idle, Stopped: true},
	} {
		m.sessions = []*state.Session{s}
		m.tipSync()
		if out := ansi.Strip(m.View()); strings.Contains(out, "tip  . next") {
			t.Errorf("%s: tip shown:\n%s", s.State, out)
		}
	}
}

// with too little room the detail goes first, then the whole tip; the face
// and the caption stay as they are
func TestTipFitsOrGoes(t *testing.T) {
	full := tipModel(t, 120, 40)
	cur := pinTip(t, full)
	var sawShortOnly, sawNone bool
	for _, size := range [][2]int{{120, 40}, {120, 18}, {120, 16}, {120, 14}, {120, 12}, {100, 20}, {60, 30}, {40, 30}, {30, 20}} {
		m := tipModel(t, size[0], size[1])
		m.tip.cur = &cur
		raw := m.View()
		keepsSize(t, m, raw)
		out := ansi.Strip(raw)
		if !strings.Contains(out, "no session selected") {
			t.Errorf("%v: caption lost\n%s", size, out)
		}
		hasTip := strings.Contains(out, "tip  . next")
		hasDetail := strings.Contains(out, "左の列を隠し")
		if hasDetail && !hasTip {
			t.Errorf("%v: detail without the tip", size)
		}
		sawShortOnly = sawShortOnly || hasTip && !hasDetail
		sawNone = sawNone || !hasTip
	}
	if !sawShortOnly || !sawNone {
		t.Errorf("short only seen %v, no tip seen %v", sawShortOnly, sawNone)
	}
	// a tip never pushes the caption: rows are only what the pane has
	m := tipModel(t, 120, 40)
	for h := 1; h < 30; h++ {
		rows := m.bigRowsTip(80, h, m.emptyMood(), "no session selected", "sub")
		plain := m.bigRows(80, h, m.emptyMood(), "no session selected", "sub")
		if len(rows) > max(h, len(plain)) {
			t.Errorf("height %d: %d rows (without a tip %d)", h, len(rows), len(plain))
		}
	}
}

func TestTipWrapsFullWidth(t *testing.T) {
	if got := wrapTip("^q z で右のペイン", 12); strings.Join(got, "|") != "^q z で右の|ペイン" {
		t.Errorf("wrap: %q", got)
	}
	for _, width := range []int{20, 21, 22, 23, 33, 40, 58, 60} {
		for _, tp := range tips.All() {
			for _, l := range wrapTip(tp.Detail, width) {
				if ansi.StringWidth(l) > width {
					t.Errorf("%s at %d: %q is %d cells", tp.ID, width, l, ansi.StringWidth(l))
				}
				if strings.ContainsRune(noLineStart, []rune(l)[0]) {
					t.Errorf("%s at %d: %q starts with punctuation", tp.ID, width, l)
				}
			}
		}
	}
}

func TestTipNextKey(t *testing.T) {
	m := tipModel(t, 120, 40)
	seen := map[string]bool{m.tip.cur.ID: true}
	for i := 1; i < len(tips.All()); i++ {
		m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
		if seen[m.tip.cur.ID] {
			t.Fatalf("%s again before every tip was shown (%d)", m.tip.cur.ID, i)
		}
		seen[m.tip.cur.ID] = true
	}
	if !strings.Contains(ansi.Strip(m.View()), m.tip.cur.Short) {
		t.Error("the new tip is not drawn")
	}
	// the help lists the key only while a tip is shown
	if !helpHas(newHelpModal(m), "next tip") {
		t.Error("help misses next tip")
	}
	m.sessions = []*state.Session{{Slug: "a", Branch: "a", State: state.Running}}
	before := m.tip.cur
	m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	if m.tip.cur != before {
		t.Error(". changed the tip while none was shown")
	}
	if helpHas(newHelpModal(m), "next tip") {
		t.Error("help lists next tip without a tip on screen")
	}
}

func helpHas(h *helpModal, action string) bool {
	for _, it := range h.items {
		if it.action == action {
			return true
		}
	}
	return false
}

func TestTipPrefixLabel(t *testing.T) {
	m := tipModel(t, 120, 40)
	tp, _ := tips.ByID("zoom")
	m.tip.cur = &tp
	m.g.Keys.Prefix = "ctrl+g"
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "^g z") || strings.Contains(out, "^q z") {
		t.Errorf("prefix not applied:\n%s", out)
	}
}

func TestTipsOff(t *testing.T) {
	m := tapiModel(t, "none", config.Mascot{})
	off := false
	m.g.Tips.Enable = &off
	m.initTips("")
	m.tipSync()
	if m.tip.cur != nil || strings.Contains(ansi.Strip(m.View()), "tip  . next") {
		t.Error("a tip with [tips] enable = false")
	}
	m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")}) // must not panic
}
