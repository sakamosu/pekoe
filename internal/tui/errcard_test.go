package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/engine"
	"pekoe/internal/errcatch"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

func errModel(t *testing.T) *model {
	t.Helper()
	m := screenModel(t)
	m.screen = &engine.Screen{Width: 161, Height: 57, Lines: []string{"> claude is working"}}
	m.window = engine.WinAgent
	now := time.Now()
	m.errs = []errcatch.Report{
		{ID: 2, Slug: "a", Source: "browser", Port: 3001, Kind: "browser", Message: "TypeError: a is null", File: "src/List.tsx", Line: 8, Count: 1, First: now.Add(-time.Minute), Last: now, Status: "new"},
		{ID: 1, Slug: "a", Source: "dev", Kind: "next", Message: "TypeError: Cannot read properties of undefined (reading 'id')", File: "src/api.ts", Line: 12, Count: 3, First: now.Add(-2 * time.Minute), Last: now.Add(-2 * time.Minute), Status: "new"},
		{ID: 3, Slug: "b", Source: "dev", Kind: "go", Message: "panic: boom", Count: 1, First: now, Last: now, Status: "new"},
	}
	return m
}

func plainRows(rs []row) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(ansi.Strip(r.Text) + "\n")
	}
	return b.String()
}

// The card sits at the bottom of the agent pane: title "error", the message,
// the origin, file:line, the count, and its own keys on the last row.
func TestErrorCardRenders(t *testing.T) {
	m := errModel(t)
	m.focus = pAgent
	cw, ch := m.agentSize()
	rows := m.agentRows(cw, ch)
	text := plainRows(rows)
	card := plainRows(rows[len(rows)-6:])
	for _, want := range []string{"error from dev · next", "TypeError: Cannot read properties of undefined (reading 'id')", "at src/api.ts:12", "×3", "s send  i ignore  ↵ detail"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	if !strings.Contains(text, "claude is working") {
		t.Error("the agent screen stays above the card")
	}
	lines := strings.Split(strings.TrimRight(card, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "╭") || !strings.HasPrefix(lines[5], "╰") || !strings.Contains(lines[4], "s send") {
		t.Fatalf("frame / key row:\n%s", card)
	}
	// the oldest error first; the browser one waits
	if strings.Contains(card, "a is null") {
		t.Error("only one card at a time")
	}
	// border role, never primary (focus); icon secondary
	raw := rows[len(rows)-6].Text
	if strings.Contains(raw, m.th.FG("primary", "╭")) {
		t.Error("cards never use the primary border")
	}
	// not focused: the card only says how to reach it
	m.focus = pSessions
	card = plainRows(m.errorCard(cw))
	if !strings.Contains(card, "4 focus card") || strings.Contains(card, "s send") {
		t.Fatalf("unfocused card keys:\n%s", card)
	}
	// browser origin reads "browser :3001"
	m.errs = m.errs[:1]
	if card := plainRows(m.errorCard(cw)); !strings.Contains(card, "from browser :3001") || !strings.Contains(card, "×1") {
		t.Fatalf("browser card:\n%s", card)
	}
	// ascii icons: still readable (state word, no glyphs needed)
	m.ic = theme.Icons("ascii")
	if card := plainRows(m.errorCard(cw)); !strings.Contains(card, "x error from browser :3001") {
		t.Fatalf("ascii card:\n%s", card)
	}
}

// A pick card wins (the human asked for it); the error waits in Picks.
func TestErrorCardYieldsToPick(t *testing.T) {
	m := errModel(t)
	m.focus = pAgent
	m.picks = []*state.Pick{{ID: 7, Slug: "a", URL: "http://localhost:3001/", OuterHTML: "<b>x</b>", At: time.Now()}}
	w, h := m.agentSize()
	text := plainRows(m.agentRows(w, h))
	if !strings.Contains(text, "Pick #0007") || strings.Contains(text, "error from") {
		t.Fatalf("pick card first:\n%s", text)
	}
	if m.pendingError() != nil {
		t.Fatal("no error card while a pick card is up")
	}
	if _, used := m.errorKey("s"); used {
		t.Fatal("s belongs to the pick card's input then")
	}
	// the queue: Picks panel rows after the picks
	m.sub3 = 1
	m.focus = pThird
	panel := strings.Join(m.thirdPanel(80, 12, true), "\n")
	panel = ansi.Strip(panel)
	for _, want := range []string{"Picks 1 · 3 errors", "#0007", "err   TypeError: a is null", "browser :3001 ×1", "panic: boom", "→ b"} {
		if !strings.Contains(panel, want) {
			t.Errorf("Picks panel lacks %q:\n%s", want, panel)
		}
	}
	if m.thirdCount() != 4 {
		t.Fatalf("thirdCount = %d", m.thirdCount())
	}
	m.psel = 2
	if r := m.selectedError(); r == nil || r.ID != 1 {
		t.Fatalf("selected error: %+v", r)
	}
	if hints := m.hints(); hints[2][0] != "s" || hints[3][1] != "ignore" {
		t.Fatalf("hints for an error row: %v", hints)
	}
	m.keyPicks("i")
	if len(m.errs) != 2 {
		t.Fatal("i on an error row ignores it")
	}
}

// Keys: s / i / ↵ on the card (agent pane focused, not typing).
func TestErrorCardKeys(t *testing.T) {
	m := errModel(t)
	m.focus = pAgent
	if m.hints()[0][0] != "a" {
		t.Fatalf("bar: typing moves to a while the card owns i/↵: %v", m.hints())
	}
	m.typing = true
	if _, used := m.errorKey("s"); used {
		t.Fatal("while typing, every key goes to the agent")
	}
	m.typing = false
	if _, used := m.errorKey("enter"); !used {
		t.Fatal("enter opens the detail")
	}
	em, ok := m.modal.(*errModal)
	if !ok || em.r.ID != 1 {
		t.Fatalf("detail modal: %#v", m.modal)
	}
	em.excerpt = "> 12 | boom()"
	view := ansi.Strip(strings.Join(em.view(), "\n"))
	for _, want := range []string{"Error", "TypeError: Cannot read properties", "dev · next · ×3", "src/api.ts:12", "> 12 | boom()", "s send", "i ignore"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail lacks %q:\n%s", want, view)
		}
	}
	m.modal = nil
	if _, used := m.errorKey("i"); !used || len(m.errs) != 2 || m.pendingError().ID != 2 {
		t.Fatal("i ignores the card's error; the next one takes its place")
	}
	if _, used := m.errorKey("s"); !used || m.pendingError() != nil {
		t.Fatal("s sends; no error left for a")
	}
	m.window = engine.WinDev
	m.errs = append(m.errs, errcatch.Report{ID: 9, Slug: "a", Message: "x", Status: "new"})
	if m.pendingError() != nil {
		t.Fatal("the card lives in the agent window only")
	}
}

// The Sessions list marks sessions with open errors (err color, word).
func TestSessionLineErrorMarker(t *testing.T) {
	m := errModel(t)
	m.sessions = append(m.sessions, &state.Session{Slug: "b", Branch: "b", State: state.Running}, &state.Session{Slug: "c", Branch: "c", State: state.Running})
	now := time.Now()
	if l := ansi.Strip(m.sessionLine(m.sessions[0], 60, now)); !strings.Contains(l, "a 2 err") {
		t.Errorf("a: %q", l)
	}
	if l := ansi.Strip(m.sessionLine(m.sessions[1], 60, now)); !strings.Contains(l, "b err") || strings.Contains(l, "1 err") {
		t.Errorf("b: %q", l)
	}
	if l := ansi.Strip(m.sessionLine(m.sessions[2], 60, now)); strings.Contains(l, "err") {
		t.Errorf("c: %q", l)
	}
	raw := m.sessionLine(m.sessions[1], 60, now)
	if !strings.Contains(raw, m.th.FG("err", "err")) {
		t.Error("the marker uses the err role")
	}
}

// A message over several lines (throw new Error("a\nb")) keeps the card,
// the Picks row and the detail dialog to their rows: the card and the row
// show its first line, the dialog all of it.
func TestErrorMultilineMessage(t *testing.T) {
	m := errModel(t)
	m.errs[1].Message = "Error: first line\nsecond line"
	m.focus = pAgent
	cw, ch := m.agentSize()
	rows := m.agentRows(cw, ch)
	for _, r := range rows {
		if strings.Contains(r.Text, "\n") {
			t.Fatalf("card row with a newline: %q", r.Text)
		}
	}
	if card := plainRows(rows); !strings.Contains(card, "Error: first line") {
		t.Fatalf("card:\n%s", card)
	}
	m.sub3 = 1
	for _, l := range m.thirdPanel(80, 12, true) {
		if strings.Contains(l, "\n") {
			t.Fatalf("Picks row with a newline: %q", l)
		}
	}
	e := &errModal{m: m, r: m.errs[1]}
	body := e.body()
	for _, l := range body {
		if strings.Contains(l, "\n") {
			t.Fatalf("detail line with a newline: %q", l)
		}
	}
	if !strings.Contains(ansi.Strip(strings.Join(body, "|")), "Error: first line|second line") {
		t.Fatalf("detail: %q", body)
	}
}
