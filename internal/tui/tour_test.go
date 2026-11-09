package tui

import (
	"encoding/json"
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
	if got := tourStepTitle(m); got != "Welcome to pekoe" {
		t.Errorf("first step %q", got)
	}
	out := ansi.Strip(m.View())
	for _, want := range []string{"Welcome to pekoe", "sample · 1/", "TUTORIAL", "fix/login-redirect"} {
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
