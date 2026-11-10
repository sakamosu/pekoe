package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/config"
	"pekoe/internal/daemon"
)

func cleanupModel(t *testing.T) (*model, *cleanupModal) {
	t.Helper()
	t.Setenv("PEKOE_ICONS", "unicode")
	m := tapiModel(t, "none", config.Mascot{Glyphs: "ascii"})
	m.width, m.height = 140, 30
	old := time.Now().Add(-100 * time.Hour)
	l := daemon.CleanupList{StaleAfter: "72h0m0s", Items: []daemon.CleanupItem{
		{Kind: daemon.CleanupLanded, Slug: "feat-a", Branch: "feat/a", State: "landed", Since: old},
		{Kind: daemon.CleanupLanded, Slug: "feat-b", Branch: "feat/b", State: "landed", Since: old},
		{Kind: daemon.CleanupStale, Slug: "feat-d", Branch: "feat/d", State: "idle", Since: old, Path: "/w/app/feat-d",
			Worktree: true, DeletesWorktree: true, HumanOnly: true, Dirty: 2, Warnings: []string{"2 uncommitted files"}},
	}}
	m.onCleanup(cleanupMsg{list: l})
	c, ok := m.modal.(*cleanupModal)
	if !ok {
		t.Fatalf("modal %T, want the cleanup list", m.modal)
	}
	return m, c
}

func runeKey(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// The list shows each candidate with its warnings; nothing is removed
// without the confirm, which repeats the warnings.
func TestCleanupModal(t *testing.T) {
	m, c := cleanupModel(t)
	out := ansi.Strip(strings.Join(c.view(), "\n"))
	for _, want := range []string{"Cleanup", "stopped for 72h+", "feat/a", "feat/d", "2 uncommitted files"} {
		if !strings.Contains(out, want) {
			t.Errorf("list misses %q:\n%s", want, out)
		}
	}

	// a marks the landed ones; D asks about those only
	c.key(runeKey("a"))
	c.key(runeKey("D"))
	cf, ok := m.modal.(*confirmModal)
	if !ok {
		t.Fatalf("D: modal %T, want a confirm", m.modal)
	}
	if !strings.Contains(cf.text, "only pekoe's record") || cf.subject != "2 sessions" || len(cf.notes) != 2 {
		t.Errorf("landed confirm: %q, notes %+v", cf.text, cf.notes)
	}

	// nothing marked: the selected one; its worktree and warnings are named
	m, c = cleanupModel(t)
	c.key(runeKey("j"))
	c.key(runeKey("j"))
	c.key(runeKey("D"))
	cf = m.modal.(*confirmModal)
	if !strings.Contains(cf.text, "delete 1 worktree") || cf.subject != "feat-d" || len(cf.notes) != 1 ||
		!strings.Contains(cf.notes[0].detail, "deletes /w/app/feat-d") || !strings.Contains(cf.notes[0].detail, "2 uncommitted files") {
		t.Errorf("stale confirm: %q, notes %+v", cf.text, cf.notes)
	}
	if cmd := cf.key(runeKey("n")); cmd == nil {
		t.Error("n did not close the confirm")
	}

	// space toggles one mark
	m, c = cleanupModel(t)
	c.key(runeKey(" "))
	if !c.marked["feat-a"] || c.sel != 1 {
		t.Errorf("space: marked %v sel %d", c.marked, c.sel)
	}
	c.key(runeKey("k"))
	c.key(runeKey(" "))
	if len(c.marked) != 0 {
		t.Errorf("space again: marked %v", c.marked)
	}
	// a refresh keeps the marks of what is still listed
	c.key(runeKey("k"))
	c.key(runeKey(" "))
	m.onCleanup(cleanupMsg{list: daemon.CleanupList{Items: c.list.Items[:1]}, info: "removed 1"})
	if m.modal != c || !c.marked["feat-a"] || len(c.list.Items) != 1 {
		t.Errorf("refresh: modal %T, marked %v", m.modal, c.marked)
	}
}

// An empty list opens nothing and says so.
func TestCleanupEmpty(t *testing.T) {
	m := tapiModel(t, "none", config.Mascot{})
	m.onCleanup(cleanupMsg{list: daemon.CleanupList{StaleAfter: "72h0m0s"}})
	if m.modal != nil {
		t.Errorf("modal %T on an empty list", m.modal)
	}
}
