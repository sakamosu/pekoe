package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	"pekoe/internal/theme"
)

// tourLocale sets the locale variables the tour's language goes by.
func tourLocale(t *testing.T, all, messages, lang string) {
	t.Helper()
	t.Setenv("LC_ALL", all)
	t.Setenv("LC_MESSAGES", messages)
	t.Setenv("LANG", lang)
}

// tourModel is a model whose tutorial record lives in a temp dir (none
// yet), in a Japanese locale.
func tourModel(t *testing.T, color string, width, height int) *model {
	t.Helper()
	tourLocale(t, "", "", "ja_JP.UTF-8")
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
	return m.tourCopy().title
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
			for _, lang := range []string{tourJa, tourEn} {
				m := tourModel(t, color, size[0], size[1])
				m.tourLang = lang
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				seen := 0
				for m.tour != nil && seen < 50 {
					seen++
					out := m.View()
					keepsSize(t, m, out)
					if !strings.Contains(ansi.Strip(out), tourStepTitle(m)) && size[1] >= 18 {
						t.Errorf("%dx%d %s %s: step %q has no card:\n%s", size[0], size[1], color, lang, tourStepTitle(m), ansi.Strip(out))
					}
					m.Update(key("enter"))
				}
				if m.tour != nil {
					t.Fatalf("%dx%d: the tour never ended", size[0], size[1])
				}
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
	for _, step := range tourSteps {
		for _, st := range []tourCopy{step.ja, step.en} {
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
					face := ansi.Strip(r.Line(r.Eyes(mascot.Done)))
					if n == 1 && size[1] >= 30 {
						if line := lineWith(t, plain, face); !strings.Contains(line, m.tourCopy().title) {
							t.Errorf("%s: the face is not on the title line: %q", where, line)
						}
					}
				}
			}
		}
	}
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
		for _, lang := range []string{tourJa, tourEn} {
			m := tourModel(t, "none", size[0], size[1])
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.tour.lang = lang
			bodyH := size[1] - 1
			for step, st := range tourSteps {
				m.tourGo(step)
				tgt, ok := rect{}, false
				if st.target != nil {
					tgt, ok = st.target(m)
				}
				x, y, card := m.tourPlace(tgt, ok, bodyH)
				cw := ansi.StringWidth(card[0])
				text := m.tourText(m.tourBody(), cw-4)
				where := fmt.Sprintf("%dx%d %q", size[0], size[1], m.tourCopy().title)
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
}

// the card is framed with the focus ramp in truecolor, solid primary in
// ansi, no color in none; the title is solid primary
func TestTourCardFrame(t *testing.T) {
	for _, mode := range []string{"truecolor", "ansi", "none"} {
		m := borderModel(t, mode)
		m.tapi.splash = false
		m.initTour(filepath.Join(t.TempDir(), "tutorial.json"))
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		for step := range tourSteps {
			m.tourGo(step)
			card := m.tourCard(50, 39)
			h := len(card)
			switch mode {
			case "truecolor":
				ramp := m.th.FocusRamp(focusRampLen(50, h))
				if got := firstSGRBefore(card[0], "┏"); got != ramp[0] {
					t.Errorf("step %d: top-left %q, want primary %q", step, got, ramp[0])
				}
				if got := firstSGRBefore(card[h-1], "┛"); got != ramp[len(ramp)-1] {
					t.Errorf("step %d: bottom-right %q, want primary_end %q", step, got, ramp[len(ramp)-1])
				}
			case "ansi":
				// solid primary: one color sequence, the primary's, for the whole run
				seq := strings.TrimSuffix(m.th.FG(theme.Primary, "x"), "x\x1b[0m")
				for _, l := range []string{card[0], card[h-1]} {
					if !strings.HasPrefix(l, seq) || strings.Count(l, "\x1b[3") != 1 {
						t.Errorf("step %d: ansi frame is not solid primary %q: %q", step, seq, l)
					}
				}
			case "none":
				for _, l := range []string{card[0], card[h-1]} {
					if strings.Contains(l, "\x1b[3") {
						t.Errorf("step %d: colors in none: %q", step, l)
					}
				}
			}
			if mode != "none" && !strings.Contains(card[1], m.th.Bold(theme.Primary, m.tourCopy().title)) {
				t.Errorf("%s step %d: the title is not bold primary: %q", mode, step, card[1])
			}
		}
	}
}

// outside the target (and the card) everything recedes to one dim color;
// the target keeps its colors
func TestTourDimsOutside(t *testing.T) {
	sgr := func(m *model, r theme.Role) string { // "" when the role draws no color here
		c := m.th.FG(r, "x")
		if !strings.HasPrefix(c, "\x1b[") {
			return ""
		}
		return strings.TrimSuffix(c, "x\x1b[0m")
	}
	// the colors in effect where text is drawn: the last sequence of each run
	// (Cut keeps sequences that color nothing)
	shown := func(s string) string {
		return regexp.MustCompile(`(\x1b\[[0-9;]*m)+`).ReplaceAllStringFunc(s, func(run string) string {
			i := strings.LastIndex(run, "\x1b[")
			return run[i:]
		})
	}
	for _, mode := range []string{"truecolor", "ansi", "none"} {
		m := borderModel(t, mode)
		m.tapi.splash = false
		m.initTour(filepath.Join(t.TempDir(), "tutorial.json"))
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		m.tourGo(1) // [1] Sessions: the agent panel is outside
		lines := strings.Split(m.View(), "\n")
		tgt := m.panelRect(pSessions)
		agent := m.panelRect(pAgent)
		for y := agent.y; y < agent.y+agent.h; y++ {
			outside := shown(ansi.Cut(lines[y], agent.x, agent.x+agent.w))
			plain := ansi.Strip(outside)
			if strings.Contains(plain, "┃") || strings.Contains(plain, "┏") || strings.Contains(plain, "┗") || strings.TrimSpace(plain) == "" {
				continue // the card, or nothing to dim
			}
			switch mode {
			case "none":
				if !strings.Contains(outside, "\x1b[2m") {
					t.Errorf("none: row %d is not faint: %q", y, outside)
				}
			default:
				for _, r := range []theme.Role{theme.Ok, theme.Secondary, theme.Primary, theme.Text, theme.Warn} {
					if c := sgr(m, r); c != "" && strings.Contains(outside, c) {
						t.Errorf("%s: row %d keeps %s outside the target: %q", mode, y, r, plain)
					}
				}
				if !strings.Contains(outside, sgr(m, theme.Border)) {
					t.Errorf("%s: row %d is not dimmed: %q", mode, y, outside)
				}
			}
		}
		if mode == "truecolor" {
			inside := ansi.Cut(lines[tgt.y], tgt.x, tgt.x+tgt.w)
			if ramp := m.th.FocusRamp(focusRampLen(tgt.w, tgt.h)); firstSGRBefore(inside, "╭") != ramp[0] {
				t.Errorf("the target lost its focus frame: %q", inside)
			}
		}
		// Welcome has no target: all but the card recedes
		m.tourGo(0)
		if out := m.View(); mode != "none" && strings.Contains(strings.Split(out, "\n")[1], sgr(m, theme.Ok)) {
			t.Errorf("%s: welcome keeps colors behind the card", mode)
		}
	}
}

// the card's face is ^ ^ in every glyph setting: no East Asian Ambiguous
// character (● •) that a font may draw two cells wide
func TestTourFaceIsNarrowEverywhere(t *testing.T) {
	for _, g := range []string{"unicode", "narrow", "ascii", "auto"} {
		m := tapiModel(t, "truecolor", config.Mascot{Glyphs: g})
		m.initTour(filepath.Join(t.TempDir(), "tutorial.json"))
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		for step := range tourSteps {
			m.tourGo(step)
			card := ansi.Strip(strings.Join(m.tourCard(56, 39), "\n"))
			if face := ansi.Strip(m.tapi.r.Line(m.tapi.r.Eyes(mascot.Done))); !strings.Contains(card, face) || !strings.Contains(face, "^") {
				t.Errorf("%s step %d: no ^ ^ face in the card:\n%s", g, step, card)
			}
			for _, amb := range []string{"●", "•", "🞄"} {
				if strings.Contains(strings.SplitN(card, "\n", 3)[1], amb) {
					t.Errorf("%s step %d: %s in the face line", g, step, amb)
				}
			}
		}
	}
}

// the environment picks the first language: the first of LC_ALL,
// LC_MESSAGES and LANG that is set; ja… is Japanese, anything else English
func TestTourLangFromEnv(t *testing.T) {
	for _, c := range []struct{ all, messages, lang, want string }{
		{"", "", "", tourEn},
		{"", "", "ja_JP.UTF-8", tourJa},
		{"", "", "ja", tourJa},
		{"", "", "en_US.UTF-8", tourEn},
		{"", "", "C", tourEn},
		{"ja_JP.UTF-8", "", "en_US.UTF-8", tourJa},
		{"C", "", "ja_JP.UTF-8", tourEn},
		{"", "en_US.UTF-8", "ja_JP.UTF-8", tourEn},
		{"", "ja_JP.UTF-8", "en_US.UTF-8", tourJa},
	} {
		env := map[string]string{"LC_ALL": c.all, "LC_MESSAGES": c.messages, "LANG": c.lang}
		if got := tourEnvLang(func(k string) string { return env[k] }); got != c.want {
			t.Errorf("LC_ALL=%q LC_MESSAGES=%q LANG=%q: %s, want %s", c.all, c.messages, c.lang, got, c.want)
		}
	}
	// the first run goes by it
	for _, c := range []struct{ lang, title string }{{"en_US.UTF-8", "Welcome to pekoe"}, {"ja_JP.UTF-8", "pekoe へようこそ"}, {"", "Welcome to pekoe"}} {
		tourLocale(t, "", "", c.lang)
		m := tapiModel(t, "none", config.Mascot{})
		m.width, m.height = 120, 40
		m.initTour(filepath.Join(t.TempDir(), "tutorial.json"))
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		if got := tourStepTitle(m); got != c.title {
			t.Errorf("LANG=%q: first step %q, want %q", c.lang, got, c.title)
		}
	}
}

// a record's language wins over the environment; a record without one
// (older pekoe) goes by the environment
func TestTourLangFromRecord(t *testing.T) {
	for _, c := range []struct{ record, env, title string }{
		{`{"seen_at":"2026-01-01T00:00:00Z","lang":"ja"}`, "en_US.UTF-8", "pekoe へようこそ"},
		{`{"seen_at":"2026-01-01T00:00:00Z","lang":"en"}`, "ja_JP.UTF-8", "Welcome to pekoe"},
		{`{"seen_at":"2026-01-01T00:00:00Z"}`, "ja_JP.UTF-8", "pekoe へようこそ"},
		{`{"seen_at":"2026-01-01T00:00:00Z"}`, "en_US.UTF-8", "Welcome to pekoe"},
		{`{"seen_at":"2026-01-01T00:00:00Z","lang":"fr"}`, "ja_JP.UTF-8", "pekoe へようこそ"},
	} {
		tourLocale(t, "", "", c.env)
		path := filepath.Join(t.TempDir(), "tutorial.json")
		if err := os.WriteFile(path, []byte(c.record), 0o600); err != nil {
			t.Fatal(err)
		}
		m := tapiModel(t, "none", config.Mascot{})
		m.width, m.height = 120, 40
		m.initTour(path)
		if m.tourAuto {
			t.Fatalf("%s: seen, but due again", c.record)
		}
		m.Update(key(tourKey))
		if got := tourStepTitle(m); got != c.title {
			t.Errorf("%s with LANG=%s: %q, want %q", c.record, c.env, got, c.title)
		}
	}
}

// tourCardKeys is the key line of the tour's card (not the bottom bar).
func tourCardKeys(m *model) string {
	card := m.tourCard(tourCardW, 39)
	return ansi.Strip(card[len(card)-2])
}

// The language key switches the language and stays on the step; the
// card's keys name the language it switches to; the language is recorded
// and the next tour starts in it
func TestTourLangKey(t *testing.T) {
	m := tourModel(t, "none", 120, 40) // Japanese locale
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(key("enter"))
	if got := tourStepTitle(m); got != "[1] tapi の行" {
		t.Fatalf("step %q", got)
	}
	if keys := tourCardKeys(m); !strings.Contains(keys, tourLangKey+" en") {
		t.Errorf("no %q in the card's keys %q", tourLangKey+" en", keys)
	}
	press(m, tourLangKey)
	if m.tour == nil || m.tour.step != 1 {
		t.Fatalf("%s left the step", tourLangKey)
	}
	if keys := tourCardKeys(m); !strings.Contains(keys, tourLangKey+" ja") || !strings.Contains(keys, "next") {
		t.Errorf("the card's keys after %s: %q", tourLangKey, keys)
	}
	out := ansi.Strip(m.View())
	for _, want := range []string{"[1] The tapi row", "sample · 2/"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q after %s:\n%s", want, tourLangKey, out)
		}
	}
	if strings.Contains(out, "tapi の行") {
		t.Error("the Japanese title is still shown")
	}
	keepsSize(t, m, m.View())
	// the narrow layout swaps the text too
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !strings.Contains(ansi.Strip(m.View()), "The tapi row") {
		t.Error("the narrow card is not in English")
	}
	press(m, tourLangKey)
	press(m, tourLangKey)
	press(m, "esc")
	if m.tour != nil {
		t.Fatal("esc did not end the tour")
	}
	b, err := os.ReadFile(m.tourPath)
	if err != nil {
		t.Fatal(err)
	}
	var r tourRecord
	if json.Unmarshal(b, &r) != nil || r.Lang != tourEn || r.SeenAt.IsZero() {
		t.Errorf("record %s", b)
	}
	// H in the same run, and the next run (whatever the locale)
	m.Update(key(tourKey))
	if got := tourStepTitle(m); got != "Welcome to pekoe" {
		t.Errorf("H starts in %q", got)
	}
	m2 := tapiModel(t, "none", config.Mascot{})
	m2.width, m2.height = 120, 40
	m2.initTour(m.tourPath)
	m2.Update(key(tourKey))
	if got := tourStepTitle(m2); got != "Welcome to pekoe" {
		t.Errorf("the next run starts in %q", got)
	}
}

// the language key is in the card's keys on every step, in both languages,
// at the card's full width; narrower, the key line still fits the card
func TestTourLangHint(t *testing.T) {
	m := tourModel(t, "none", 120, 40)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for _, lang := range []string{tourJa, tourEn} {
		m.tour.lang = lang
		want := tourLangKey + " en"
		if lang == tourEn {
			want = tourLangKey + " ja"
		}
		for step := range tourSteps {
			m.tourGo(step)
			if keys := tourCardKeys(m); !strings.Contains(keys, want) {
				t.Errorf("%s step %d: keys %q lack %q", lang, step, keys, want)
			}
			for cw := tourSideMin; cw <= tourCardW; cw++ {
				if l := m.keyHints(m.tourHints(), cw-4); ansi.StringWidth(l) > cw-4 {
					t.Errorf("%s step %d width %d: keys %q do not fit", lang, step, cw, ansi.Strip(l))
				}
			}
		}
	}
}

// Leaving the tour without the language key records no language: the locale decides the
// next one (a language picked before stays)
func TestTourLangNotRecordedUnlessPicked(t *testing.T) {
	for _, c := range []struct{ record, lang, next, title string }{
		// English locale, never picked; the locale turns Japanese: Japanese
		{"", "", "ja_JP.UTF-8", "pekoe へようこそ"},
		// Japanese picked before: it stays, whatever the locale
		{`{"seen_at":"2026-01-01T00:00:00Z","lang":"ja"}`, tourJa, "en_US.UTF-8", "pekoe へようこそ"},
	} {
		tourLocale(t, "", "", "en_US.UTF-8")
		path := filepath.Join(t.TempDir(), "tutorial.json")
		if c.record != "" {
			if err := os.WriteFile(path, []byte(c.record), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		m := tapiModel(t, "none", config.Mascot{})
		m.width, m.height = 120, 40
		m.initTour(path)
		m.Update(key(tourKey))
		press(m, "esc")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var r tourRecord
		if json.Unmarshal(b, &r) != nil || r.Lang != c.lang || r.SeenAt.IsZero() {
			t.Errorf("record %s, want lang %q", b, c.lang)
		}
		tourLocale(t, "", "", c.next)
		m.Update(key(tourKey))
		if got := tourStepTitle(m); got != c.title {
			t.Errorf("record %q, then %s: next tour in %q", c.record, c.next, got)
		}
	}
}

// Caps Lock on: L / H / Q go on, back and skip like l / h / q, and never
// switch the language
func TestTourCapsLock(t *testing.T) {
	m := tourModel(t, "none", 120, 40) // Japanese locale
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	press(m, "L")
	press(m, "L")
	press(m, "H")
	if m.tour == nil || m.tour.step != 1 || m.tour.lang != tourJa {
		t.Fatalf("L L H: step %d lang %s", m.tour.step, m.tour.lang)
	}
	press(m, "Q")
	if m.tour != nil {
		t.Fatal("Q did not end the tour")
	}
}

// the first part of the tour is tapi's: the tapi row with the dashboard,
// tapi chat, the inbox and the pipeline, all on the sample (none of the
// real inbox or dashboard shows, and both come back afterwards)
func TestTourOrchestratorSteps(t *testing.T) {
	m := tourModel(t, "none", 150, 45)
	m.inbox = []state.InboxItem{{ID: 99, Kind: "decision", From: "real-one", Question: "a real question", Status: state.InboxOpen}}
	m.dash.ledger = []state.LedgerEntry{{ID: 98, Text: "a real request", Status: "open"}}
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	if m.tour == nil {
		t.Fatal("no tour")
	}
	agent := func() string {
		r := m.panelRect(pAgent)
		lines := strings.Split(m.View(), "\n")
		var out []string
		for y := r.y; y < r.y+r.h && y < len(lines); y++ {
			out = append(out, ansi.Strip(ansi.Cut(lines[y], r.x, r.x+r.w)))
		}
		return strings.Join(out, "\n")
	}
	seen := map[string]bool{}
	for i := 0; m.tour != nil && i < 50; i++ {
		title := tourStepTitle(m)
		out := ansi.Strip(m.View())
		if strings.Contains(out, "a real question") || strings.Contains(out, "a real request") {
			t.Errorf("%q: the real inbox or ledger shows in the tour", title)
		}
		switch title {
		case "[1] tapi の行":
			seen[title] = true
			if !m.onTapiRow() || !m.dashShown() {
				t.Errorf("%q: the tapi row is not selected with the dashboard", title)
			}
		case "tapi chat":
			seen[title] = true
			if a := agent(); !m.chat.open || !strings.Contains(a, "Leave it to me") {
				t.Errorf("%q: no sample chat:\n%s", title, a)
			}
		case "ダッシュボード":
			seen[title] = true
			a := agent()
			for _, want := range []string{"Dashboard", "Overview", "Inbox", "#12", "decide", "merge ready", "Pipeline", "from tapi · #7",
				tourCSV, tourReady, "#131", "changes requested", "review: no independent review reported", "Requests", "3 open", "Sends", "Decided", "→ cursor"} {
				if !strings.Contains(a, want) {
					t.Errorf("%q: no %q in [4]:\n%s", title, want, a)
				}
			}
			if strings.Contains(a, "loading") {
				t.Errorf("%q: the dashboard is loading:\n%s", title, a)
			}
		case "受信箱":
			seen[title] = true
			im, ok := m.modal.(*inboxModal)
			if !ok || im.it.ID != 12 || im.sel != -1 {
				t.Fatalf("%q: modal %T", title, m.modal)
			}
			for _, want := range []string{"Inbox #12", "pekoe read", "feat-csv-export", "Dates in the CSV", "recommended (agent)"} {
				if !strings.Contains(out, want) {
					t.Errorf("%q: no %q:\n%s", title, want, out)
				}
			}
		case "工程表とマージ":
			seen[title] = true
			rows, keys := m.dashRows(m.panelRect(pAgent).w - 4)
			sel := -1
			for j, k := range keys {
				if k == "s:"+tourReady && rows[j].Sel {
					sel = j
				}
			}
			if sel < 0 {
				t.Fatalf("%q: the ready session's row is not chosen", title)
			}
			if a := agent(); !strings.Contains(a, tourReady) || !strings.Contains(a, "ready to merge") {
				t.Errorf("%q: no ready mark for %s:\n%s", title, tourReady, a)
			}
		}
		m.Update(key("enter"))
	}
	for _, s := range []string{"[1] tapi の行", "tapi chat", "ダッシュボード", "受信箱", "工程表とマージ"} {
		if !seen[s] {
			t.Errorf("step %q not reached", s)
		}
	}
	if len(m.inbox) != 1 || m.inbox[0].ID != 99 || len(m.dash.ledger) != 1 || m.dash.ledger[0].ID != 98 {
		t.Errorf("the real inbox or ledger did not come back: %+v %+v", m.inbox, m.dash.ledger)
	}
}
