package ambiguous

import (
	"os"
	"slices"
	"testing"

	"pekoe/internal/glyphguard"
)

// The wide table replaces only ambiguous-width glyphs, and only with ASCII;
// box drawing and blocks one to one (frames are built by counting cells).
func TestTable(t *testing.T) {
	for r, s := range Table() {
		if why := glyphguard.BadWide(r); why == "" {
			t.Errorf("%q (U+%04X) is not ambiguous: it needs no replacing", r, r)
		}
		if s == "" {
			t.Errorf("%q has no replacement", r)
		}
		for _, c := range s {
			if c >= 0x80 {
				t.Errorf("%q → %q is not ASCII", r, s)
			}
		}
		if r >= 0x2500 && r <= 0x259F && len(s) != 1 {
			t.Errorf("box / block %q → %q must stay one cell", r, s)
		}
	}
}

func TestText(t *testing.T) {
	defer SetWide(IsWide())
	s := "a · b … c → d ╭─╮ ▁█"
	SetWide(false)
	if got := Text(s); got != s {
		t.Errorf("narrow: Text changed %q to %q", s, got)
	}
	SetWide(true)
	if got, want := Text(s), "a - b ... c -> d +-+ _#"; got != want {
		t.Errorf("wide: Text(%q) = %q, want %q", s, got, want)
	}
	if got := Text("\x1b[31m·\x1b[0m"); got != "\x1b[31m-\x1b[0m" {
		t.Errorf("escape sequences must pass: %q", got)
	}
}

func TestDecide(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	measured := func(wide, ok bool) func() (bool, bool) {
		return func() (bool, bool) { return wide, ok }
	}
	none := env()
	for _, c := range []struct {
		name    string
		cfg     string
		getenv  func(string) string
		counted bool
		measure func() (bool, bool)
		want    Plan
	}{
		{"default, terminal counts one", "", none, false, measured(false, true), Plan{}},
		{"auto, terminal counts two", Auto, none, false, measured(true, true), Plan{Wide: true, Exec: true}},
		{"auto, no answer", Auto, none, false, measured(false, false), Plan{}},
		{"auto, no terminal", Auto, none, false, nil, Plan{}},
		{"narrow is trusted", Narrow, none, false, measured(true, true), Plan{}},
		{"wide is trusted", Wide, none, false, measured(false, true), Plan{Wide: true, Exec: true}},
		{"wide, no terminal", Wide, none, false, nil, Plan{Wide: true, Exec: true}},
		{"the process run again", Wide, env(ReexecEnv, "1", EastAsianEnv, "1"), true, nil, Plan{Wide: true, Unset: true}},
		{"run again, whatever the setting", Narrow, env(ReexecEnv, "1"), true, nil, Plan{Wide: true, Unset: true}},
		{"RUNEWIDTH_EASTASIAN from the person: wide table, kept", Narrow, env(EastAsianEnv, "1"), true, nil, Plan{Wide: true}},
	} {
		if got := Decide(c.cfg, c.getenv, c.counted, c.measure); got != c.want {
			t.Errorf("%s: Decide = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestExecEnv(t *testing.T) {
	got := ExecEnv([]string{"HOME=/h", EastAsianEnv + "=0", "PATH=/bin", ReexecEnv + "=x"})
	want := []string{"HOME=/h", "PATH=/bin", EastAsianEnv + "=1", ReexecEnv + "=1"}
	if !slices.Equal(got, want) {
		t.Errorf("ExecEnv = %q, want %q", got, want)
	}
}

// The process run again takes both variables out of its environment at
// once, so nothing it starts (pekoed, tmux, the agents, an editor) counts
// ambiguous glyphs as two cells.
func TestSetupRunAgainUnsets(t *testing.T) {
	defer SetWide(IsWide())
	t.Setenv(ReexecEnv, "1")
	t.Setenv(EastAsianEnv, "1")
	p := Setup(Narrow, nil)
	if p != (Plan{Wide: true, Unset: true}) {
		t.Fatalf("plan = %+v", p)
	}
	for _, k := range []string{ReexecEnv, EastAsianEnv} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%q is still set", k, v)
		}
	}
	if !IsWide() {
		t.Error("the wide table is not in use")
	}
}

func TestSetupNarrow(t *testing.T) {
	defer SetWide(IsWide())
	SetWide(true)
	if p := Setup(Narrow, nil); p != (Plan{}) || IsWide() {
		t.Errorf("plan = %+v, wide = %v", p, IsWide())
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
