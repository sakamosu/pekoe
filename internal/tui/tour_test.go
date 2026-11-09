package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/client"
	"pekoe/internal/config"
	"pekoe/internal/daemon"
	"pekoe/internal/mascot"
	"pekoe/internal/state"
)

// tourModel is a model whose tutorial record lives in a temp dir (none yet).
func tourModel(t *testing.T, color string, width, height int) *model {
	t.Helper()
	m := tapiModel(t, color, config.Mascot{Glyphs: "ascii"})
	m.width, m.height = width, height
	m.initTour(filepath.Join(t.TempDir(), "tutorial.json"))
	return m
}

// realSessions is what the daemon would have sent.
func realSessions() []*state.Session {
	now := time.Now()
	return []*state.Session{{Slug: "real-one", Branch: "real/one", State: state.Idle, StateSince: now, CreatedAt: now}}
}

// press sends a key the way the runtime does: an esc held by the mouse
// guard is let go by the flush the guard schedules.
func press(m *model, k string) {
	m.Update(key(k))
	if len(m.guard.held) > 0 {
		m.Update(guardFlushMsg(m.guard.gen))
	}
}

func tourStepTitle(m *model) string {
	if m.tour == nil {
		return ""
	}
	return tourSteps[m.tour.step].title
}

func TestTourStartsOnFirstRun(t *testing.T) {
	m := tourModel(t, "none", 120, 40)
	if !m.tourAuto {
		t.Fatal("no record: the tour should be due")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.tour == nil {
		t.Fatal("the tour did not start on the first update")
	}
	if got := tourStepTitle(m); got != "pekoe へようこそ" {
		t.Errorf("first step %q", got)
	}
	out := ansi.Strip(m.View())
	for _, want := range []string{"pekoe へようこそ", "sample · 1/", "TUTORIAL", "fix/login-redirect"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	keepsSize(t, m, m.View())
}

func TestTourWaitsForTheSplash(t *testing.T) {
	m := tourModel(t, "none", 120, 40)
	m.tapi.splash = true
	m.Update(tickMsg(time.Now()))
	if m.tour != nil {
		t.Fatal("the tour started under the splash")
	}
	m.Update(splashDoneMsg{})
	if m.tour == nil {
		t.Fatal("the tour did not start after the splash")
	}
}

func TestTourNotWhenSeen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tutorial.json")
	if err := os.WriteFile(path, []byte(`{"seen_at":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := tapiModel(t, "none", config.Mascot{})
	m.initTour(path)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.tour != nil {
		t.Fatal("the tour started although it was seen")
	}
	// tests (no record path) never start it by themselves either
	m2 := tapiModel(t, "none", config.Mascot{})
	m2.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m2.tour != nil {
		t.Fatal("the tour started without a record path")
	}
}

func TestTourRecordsWhenSkipped(t *testing.T) {
	m := tourModel(t, "none", 120, 40)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	press(m, "esc")
	if m.tour != nil {
		t.Fatal("esc did not end the tour")
	}
	b, err := os.ReadFile(m.tourPath)
	if err != nil {
		t.Fatalf("not recorded: %v", err)
	}
	var r tourRecord
	if json.Unmarshal(b, &r) != nil || r.SeenAt.IsZero() {
		t.Errorf("record %s", b)
	}
	m2 := tapiModel(t, "none", config.Mascot{})
	m2.initTour(m.tourPath)
	if m2.tourAuto {
		t.Error("the next start would show it again")
	}
}

func TestTourReplayKeyAndCommand(t *testing.T) {
	m := tapiModel(t, "none", config.Mascot{})
	m.width, m.height = 120, 40
	m.Update(key(tourKey))
	if m.tour == nil {
		t.Fatalf("%s did not start the tour", tourKey)
	}
	m.Update(key("q"))
	if m.tour != nil {
		t.Fatal("q did not end the tour")
	}
	m.Update(key(":"))
	for _, r := range "tutorial" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if m.tour == nil {
		t.Fatal(":tutorial did not start the tour")
	}
	if m.modal != nil {
		t.Error("the command line is still open")
	}
	press(m, "esc")
	// from the help list, too
	m.Update(key("?"))
	h := m.modal.(*helpModal)
	for i, it := range h.items {
		if it.key == tourKey {
			h.sel = i
		}
	}
	if h.items[h.sel].key != tourKey {
		t.Fatal("the help does not list the tutorial")
	}
	m.Update(key("enter"))
	if m.tour == nil {
		t.Fatal("the help's tutorial line did not start it")
	}
}

func TestTourWalksEveryStep(t *testing.T) {
	for _, size := range [][2]int{{150, 45}, {120, 40}, {100, 30}, {80, 24}, {60, 18}, {40, 12}} {
		for _, color := range []string{"truecolor", "ansi", "none"} {
			m := tourModel(t, color, size[0], size[1])
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			seen := 0
			for m.tour != nil && seen < 50 {
				seen++
				out := m.View()
				keepsSize(t, m, out)
				if !strings.Contains(ansi.Strip(out), tourSteps[m.tour.step].title) && size[1] >= 18 {
					t.Errorf("%dx%d %s: step %q has no card:\n%s", size[0], size[1], color, tourSteps[m.tour.step].title, ansi.Strip(out))
				}
				m.Update(key("enter"))
			}
			if m.tour != nil {
				t.Fatalf("%dx%d: the tour never ended", size[0], size[1])
			}
		}
	}
}

func TestTourBackAndPosition(t *testing.T) {
	m := tourModel(t, "none", 120, 40)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if n := len(tourSteps); n < 8 || n > 14 {
		t.Errorf("%d steps", n)
	}
	m.Update(key("left")) // nothing before the first
	if m.tour.step != 0 {
		t.Errorf("back from the first step went to %d", m.tour.step)
	}
	m.Update(key("l"))
	m.Update(key("right"))
	if m.tour.step != 2 {
		t.Errorf("at step %d after two steps", m.tour.step)
	}
	m.Update(key("h"))
	if m.tour.step != 1 {
		t.Errorf("at step %d after one back", m.tour.step)
	}
	if !strings.Contains(ansi.Strip(m.View()), "sample · 2/") {
		t.Error("the card shows no position")
	}
}

// the sample replaces the panels; the real sessions come back afterwards,
// with what arrived meanwhile
func TestTourSampleAndRestore(t *testing.T) {
	m := tourModel(t, "none", 120, 40)
	m.sessions = realSessions()
	m.focus = pThird
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.tour == nil {
		t.Fatal("no tour")
	}
	if _, s := m.find("real-one"); s != nil {
		t.Fatal("the real session is shown during the tour")
	}
	if _, s := m.find(tourRun); s == nil {
		t.Fatal("no sample session")
	}
	// updates from pekoed during the tour do not touch the sample …
	now := time.Now()
	m.Update(sessionsMsg([]*state.Session{
		{Slug: "real-one", Branch: "real/one", State: state.Idle, StateSince: now, CreatedAt: now},
		{Slug: "real-two", Branch: "real/two", State: state.Running, StateSince: now, CreatedAt: now.Add(time.Second)},
	}))
	two, _ := json.Marshal(state.Session{Slug: "real-two", Branch: "real/two", State: state.Waiting, StateSince: now, CreatedAt: now.Add(time.Second)})
	m.Update(evMsg(client.Event{Event: "session.updated", Data: two}))
	m.Update(worktreesMsg{dir: "/src/real", list: []daemon.WorktreeView{{Path: "/src/real", Branch: "main", Main: true}}})
	if _, s := m.find("real-two"); s != nil {
		t.Fatal("an update reached the sample")
	}
	if !strings.Contains(ansi.Strip(m.View()), "fix/login-redirect") {
		t.Fatal("the sample is gone")
	}
	press(m, "esc")
	// … and are there once it ends
	if m.tour != nil {
		t.Fatal("still in the tour")
	}
	if _, s := m.find(tourRun); s != nil {
		t.Error("a sample session is left")
	}
	_, two2 := m.find("real-two")
	if two2 == nil || two2.State != state.Waiting {
		t.Errorf("the update during the tour was lost: %+v", two2)
	}
	if m.focus != pThird {
		t.Errorf("focus %d, was %d before the tour", m.focus, pThird)
	}
	if m.pr.view != nil || m.layout.Zoom {
		t.Error("sample state leaked into the real model")
	}
	out := ansi.Strip(m.View())
	if strings.Contains(out, "fix/login-redirect") || !strings.Contains(out, "real/two") {
		t.Errorf("the screen does not show the real sessions:\n%s", out)
	}
}

// no key reaches pekoed while the tour runs, destructive or not
func TestTourSwallowsKeys(t *testing.T) {
	f, cl := startFakePekoed(t)
	m := tapiModel(t, "none", config.Mascot{})
	m.cl = cl
	m.width, m.height = 120, 40
	m.initTour(filepath.Join(t.TempDir(), "tutorial.json"))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.tour == nil {
		t.Fatal("no tour")
	}
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	var cmds []tea.Cmd
	for _, k := range []string{"D", "y", "x", "u", "R", "d", "i", "a", "s", "p", "n", "t", "T", "w", "b", "!", "v", "1", "4", "tab", ":", "?", "/", "]", "a", "ctrl+q", "E", "A", "g", "o", "f", "S", "c", "m", "z", "x"} {
		_, c := m.Update(key(k))
		cmds = append(cmds, c)
		if m.tour == nil {
			t.Fatalf("%q ended the tour", k)
		}
	}
	// the next steps (a PR view, the land step …) do not call out either
	for i := 0; i < 8; i++ {
		_, c := m.Update(key("enter"))
		cmds = append(cmds, c)
		for _, k := range []string{"l", "D", "y", "s", " "} {
			if k == "l" || k == " " {
				continue // l and space are "next" in the tour
			}
			_, c := m.Update(key(k))
			cmds = append(cmds, c)
		}
	}
	for _, c := range cmds {
		runCmd(m, c)
	}
	f.mu.Lock()
	calls := append([]fakeCall(nil), f.calls...)
	f.mu.Unlock()
	if len(calls) > 0 {
		var names []string
		for _, c := range calls {
			names = append(names, c.method)
		}
		t.Errorf("calls during the tour: %v", names)
	}
	if m.modal != nil && m.tour == nil {
		t.Error("a modal opened")
	}
}

// the tutorial key is listed among the global keys of the help
func TestTourInHelp(t *testing.T) {
	m := tapiModel(t, "none", config.Mascot{})
	found := false
	for _, k := range m.globalHelpKeys() {
		if k[0] == tourKey {
			found = true
		}
	}
	if !found {
		t.Errorf("%s is not in the help", tourKey)
	}
}

// the card's text wraps to its width with full-width characters counted as
// two, loses nothing, and no line starts with closing punctuation
func TestTourTextWrapsJapanese(t *testing.T) {
	m := tourModel(t, "none", 120, 40)
	strip := func(s string) string { return strings.ReplaceAll(ansi.Strip(s), " ", "") }
	for _, st := range tourSteps {
		for _, body := range []string{st.body, st.narrow} {
			if body == "" {
				continue
			}
			src := strings.ReplaceAll(body, "^q", m.prefixLabel())
			want := strip(m.tourText(src, 1000)[0])
			for width := 16; width <= 60; width++ {
				lines := m.tourText(src, width)
				got := ""
				for _, l := range lines {
					got += strip(l)
					if lw := ansi.StringWidth(l); lw > width && !strings.Contains(l, "{") {
						// only a single piece longer than the line may stick out
						if len(strings.Fields(ansi.Strip(l))) > 1 && lw > width {
							t.Errorf("%q at %d: line %q is %d wide", st.title, width, ansi.Strip(l), lw)
						}
					}
					if r := []rune(ansi.Strip(l)); len(r) > 0 && strings.ContainsRune(noLineStart, r[0]) {
						t.Errorf("%q at %d: line starts with %q: %q", st.title, width, r[0], ansi.Strip(l))
					}
				}
				if got != want {
					t.Errorf("%q at %d: text changed:\n%s\n%s", st.title, width, got, want)
				}
			}
		}
	}
	// a key and the punctuation after it stay together
	lines := m.tourText("あいうえお {↵}、かきくけこ", 13)
	if r := []rune(ansi.Strip(lines[1])); r[0] != '↵' {
		t.Errorf("「、」 left its key: %q", ansi.Strip(strings.Join(lines, "|")))
	}
}

// tapi tells the tour: one face on screen, in the card, whatever the glyphs,
// shape, palette or colors
func TestTourOneFace(t *testing.T) {
	for _, color := range []string{"truecolor", "ansi", "none"} {
		for _, mc := range []config.Mascot{
			{Shape: "round", Glyphs: "unicode"}, {Shape: "block", Glyphs: "narrow"}, {Glyphs: "ascii"},
			{Shape: "round", Glyphs: "unicode", Palette: "light"},
		} {
			m := tapiModel(t, color, mc)
			m.initTour(filepath.Join(t.TempDir(), "tutorial.json"))
			for _, size := range [][2]int{{150, 45}, {90, 30}, {60, 16}} {
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				r := m.tapi.r
				var faces []string
				for _, mood := range mascot.Moods {
					faces = append(faces, ansi.Strip(r.Line(r.Eyes(mood))))
				}
				faces = append(faces, ansi.Strip(r.Line(r.Blink())), ansi.Strip(r.Line(r.Peek())), ansi.Strip(r.Line(r.CloudyEyes())))
				for step := range tourSteps {
					m.tourGo(step)
					out := m.View()
					keepsSize(t, m, out)
					plain := ansi.Strip(out)
					n := 0
					for _, f := range uniq(faces) {
						n += strings.Count(plain, f)
					}
					where := fmt.Sprintf("%s %+v %dx%d step %d", color, mc, size[0], size[1], step)
					if n != 1 {
						t.Errorf("%s: %d faces:\n%s", where, n, plain)
					}
					face := ansi.Strip(r.Line(r.Eyes(tourMood(step))))
					if n == 1 && size[1] >= 30 {
						if line := lineWith(t, plain, face); !strings.Contains(line, tourSteps[step].title) {
							t.Errorf("%s: the face is not on the title line: %q", where, line)
						}
					}
				}
			}
		}
	}
}

func tourMood(step int) mascot.Mood {
	if md := tourSteps[step].mood; md != "" {
		return md
	}
	return mascot.Idle
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// from 80×24 up, every card fits whole: no text row dropped, inside the screen
func TestTourCardsFit(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {90, 30}, {100, 26}, {120, 40}, {150, 45}} {
		m := tourModel(t, "none", size[0], size[1])
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		bodyH := size[1] - 1
		for step, st := range tourSteps {
			m.tourGo(step)
			tgt, ok := rect{}, false
			if st.target != nil {
				tgt, ok = st.target(m)
			}
			x, y, card := m.tourPlace(tgt, ok, bodyH)
			cw := ansi.StringWidth(card[0])
			body := st.body
			if narrow(m.width) && st.narrow != "" {
				body = st.narrow
			}
			text := m.tourText(strings.ReplaceAll(body, "^q", m.prefixLabel()), cw-4)
			where := fmt.Sprintf("%dx%d %q", size[0], size[1], st.title)
			if want := len(text) + 6; len(card) != want {
				t.Errorf("%s: card has %d rows, the text needs %d", where, len(card), want)
			}
			if x < 0 || y < 0 || x+cw > size[0] || y+len(card) > bodyH {
				t.Errorf("%s: card at %d,%d %dx%d leaves the screen", where, x, y, cw, len(card))
			}
			for _, l := range text {
				if ansi.StringWidth(l) > cw-4 {
					t.Errorf("%s: %q is wider than the card", where, ansi.Strip(l))
				}
			}
		}
	}
}
