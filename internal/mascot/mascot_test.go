package mascot

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"boba/internal/state"
)

func plain(o Options) *Renderer { o.NoColor = true; return New(o) }

func TestSextantMatchesSprite(t *testing.T) {
	top := []int{32, 60, 60, 60, 16}
	bottom := []int{2, 15, 15, 15, 1}
	sp := spec.Sprites["pearl3"]
	for i, v := range top {
		if got := Sextant(v); got != sp[0][i].G {
			t.Errorf("top %d: Sextant(%d)=%q sprite=%q", i, v, got, sp[0][i].G)
		}
	}
	for i, v := range bottom {
		if got := Sextant(v); got != sp[2][i].G {
			t.Errorf("bottom %d: Sextant(%d)=%q sprite=%q", i, v, got, sp[2][i].G)
		}
	}
	for v, want := range map[int]string{0: " ", 21: "▌", 42: "▐", 63: "█", 1: "\U0001FB00", 62: "\U0001FB3B"} {
		if got := Sextant(v); got != want {
			t.Errorf("Sextant(%d)=%q want %q", v, got, want)
		}
	}
}

func TestShapes(t *testing.T) {
	cases := []struct {
		o    Options
		eyes string
		want []string
	}{
		{Options{Shape: "round"}, "● ●", []string{"🬞🬹🬹🬹🬏", "█● ●█", "🬁🬎🬎🬎🬀"}},
		{Options{Shape: "block"}, "● ●", []string{"▗▄▄▄▖", "▐● ●▌", "▝▀▀▀▘"}},
		{Options{Glyphs: "ascii"}, "o o", []string{".---.", "|o o|", "'---'"}},
	}
	for _, c := range cases {
		got := plain(c.o).Big(c.eyes)
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%+v: got\n%s\nwant\n%s", c.o, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
	}
	if got := plain(Options{}).Line("◕ ◕"); got != "▐◕ ◕▌" {
		t.Errorf("line=%q", got)
	}
	if got := plain(Options{Glyphs: "ascii"}).Line("0 0"); got != "(0 0)" {
		t.Errorf("ascii line=%q", got)
	}
	if got := plain(Options{}).Small("● ●"); got[1] != "█●●█" {
		t.Errorf("small=%q", got)
	}
}

func TestEyesAndCells(t *testing.T) {
	r := plain(Options{})
	want := map[Mood][2]string{Error: {"⨯ ⨯", "⨯"}, Wait: {"◕ ◕", "◉"}, Work: {"● ●", "●"}, Done: {"^ ^", "●"}, Idle: {"• •", "•"}, Sleep: {"- -", "◦"}}
	for m, w := range want {
		if r.Eyes(m) != w[0] || r.CellGlyph(m) != w[1] {
			t.Errorf("%s: eyes=%q cell=%q", m, r.Eyes(m), r.CellGlyph(m))
		}
	}
	n := plain(Options{Glyphs: "narrow"})
	if n.Eyes(Work) != "⬤ ⬤" || n.CellGlyph(Done) != "⬤" || n.Eyes(Error) != "⨯ ⨯" || n.ASCII() {
		t.Errorf("narrow: work=%q done=%q error=%q", n.Eyes(Work), n.CellGlyph(Done), n.Eyes(Error))
	}
	a := plain(Options{Glyphs: "ascii"})
	for _, m := range Moods {
		for _, s := range []string{a.Eyes(m), a.CellGlyph(m)} {
			for _, c := range s {
				if c > 127 {
					t.Errorf("%s: non-ascii %q", m, s)
				}
			}
		}
	}
}

func TestColorOverride(t *testing.T) {
	r := New(Options{Colors: map[string]string{"body": "#010203"}})
	if r.pal.Bd != "#010203" || r.pal.Hi != PaletteDark.Hi {
		t.Fatalf("pal=%+v", r.pal)
	}
	if New(Options{Light: true}).pal != PaletteLight {
		t.Fatal("light palette")
	}
}

func TestCupWidths(t *testing.T) {
	r := plain(Options{})
	for n := 0; n <= 6; n++ {
		var ps []string
		for i := 0; i < n; i++ {
			ps = append(ps, "● ●")
		}
		rows := r.Cup(ps, nil, nil)
		wd := ansi.StringWidth(rows[1])
		for i, l := range rows {
			if ansi.StringWidth(l) != wd {
				t.Errorf("n=%d row %d width %d != %d: %q", n, i, ansi.StringWidth(l), wd, l)
			}
		}
		if got := strings.Count(rows[4], "●●"); got != min(n, 5) {
			t.Errorf("n=%d pearls=%d", n, got)
		}
	}
}

func sess(slug string, st state.State, since time.Time) *state.Session {
	return &state.Session{Slug: slug, Branch: slug, State: st, StateSince: since}
}

func TestAggregate(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	active := now
	cases := []struct {
		name   string
		ss     []*state.Session
		seen   map[string]time.Time
		active time.Time
		mood   Mood
		speech string
	}{
		{"none", nil, nil, active, Idle, "no sessions"},
		{"working", []*state.Session{sess("a", state.Running, ago(time.Second)), sess("b", state.Running, ago(time.Second)), sess("c", state.Running, ago(time.Second))}, nil, active, Work, "3 working"},
		{"wait beats work", []*state.Session{sess("a", state.Running, ago(time.Second)), sess("fix-ci", state.Waiting, ago(time.Second))}, nil, active, Wait, "fix-ci needs you"},
		{"two waits", []*state.Session{sess("a", state.Waiting, ago(time.Second)), sess("b", state.Waiting, ago(time.Second))}, nil, active, Wait, "2 need you"},
		{"error wins", []*state.Session{sess("auth-refactor", state.Failed, ago(time.Hour)), sess("b", state.Waiting, ago(time.Second))}, nil, active, Error, "auth-refactor failed"},
		{"seen error is idle", []*state.Session{sess("a", state.Failed, ago(time.Minute))}, map[string]time.Time{"a": now}, active, Idle, "idle"},
		{"fresh done", []*state.Session{sess("a", state.Done, ago(2*time.Second))}, nil, active, Done, "a done"},
		{"done fades", []*state.Session{sess("a", state.Done, ago(11*time.Second))}, nil, active, Idle, "idle"},
		{"seen done", []*state.Session{sess("a", state.Done, ago(2*time.Second))}, map[string]time.Time{"a": now}, active, Idle, "idle"},
		{"sleep", []*state.Session{sess("a", state.Idle, ago(4*time.Minute))}, nil, ago(4 * time.Minute), Sleep, "all quiet"},
		{"key wakes", []*state.Session{sess("a", state.Idle, ago(4*time.Minute))}, nil, ago(time.Second), Idle, "idle"},
		{"recent idle", []*state.Session{sess("a", state.Idle, ago(time.Minute))}, nil, ago(4 * time.Minute), Idle, "idle"},
		{"landed ignored", []*state.Session{sess("a", state.Landed, ago(time.Second))}, nil, active, Idle, "no sessions"},
	}
	for _, c := range cases {
		s := Aggregate(c.ss, c.seen, now, c.active)
		if s.Mood != c.mood || s.Speech() != c.speech {
			t.Errorf("%s: mood=%s speech=%q, want %s %q", c.name, s.Mood, s.Speech(), c.mood, c.speech)
		}
	}
}

func TestRowMoodKeepsDoneUntilSeen(t *testing.T) {
	s := sess("a", state.Done, time.Now().Add(-time.Hour))
	if RowMood(s, time.Time{}) != Done {
		t.Fatal("unseen done dot should stay green")
	}
	if RowMood(s, time.Now()) != Idle {
		t.Fatal("seen done dot returns to idle")
	}
}

func TestParseCPR(t *testing.T) {
	if c, ok := parseCPR([]byte("junk\x1b[12;3R")); !ok || c != 3 {
		t.Fatalf("c=%d ok=%v", c, ok)
	}
	if _, ok := parseCPR([]byte("\x1b[12")); ok {
		t.Fatal("partial")
	}
}

func TestDetectShape(t *testing.T) {
	for _, k := range []string{"TERM_PROGRAM", "TERM", "GHOSTTY_RESOURCES_DIR", "KITTY_WINDOW_ID", "WEZTERM_PANE"} {
		t.Setenv(k, "")
	}
	if DetectShape("auto") != "block" {
		t.Fatal("unknown terminal → block")
	}
	t.Setenv("TERM_PROGRAM", "ghostty")
	if DetectShape("auto") != "round" || DetectShape("block") != "block" {
		t.Fatal("ghostty → round, config wins")
	}
}

// Ghostty widens a symbol followed by a space, so no eye may touch one.
func TestNoSpaceAfterEyes(t *testing.T) {
	r := New(Options{Shape: "round"})
	r.noColor = false
	faces := append([]string{r.Line("● ●"), r.Line("◕ ◕")}, r.Big("⨯ ⨯")...)
	for _, f := range faces {
		if p := ansi.Strip(f); strings.Contains(p, " ") {
			t.Errorf("space in face %q", p)
		}
	}
}

func TestGapIsOneCellNotSpace(t *testing.T) {
	g := plain(Options{}).Gap()
	if ansi.StringWidth(g) != 1 || strings.TrimSpace(g) == "" {
		t.Errorf("gap=%q", g)
	}
	if plain(Options{Glyphs: "ascii"}).Gap() != " " {
		t.Error("ascii gap should be a space")
	}
}
