package glyphguard

import "testing"

func TestBad(t *testing.T) {
	for r, bad := range map[rune]bool{
		'a': false, '─': false, '│': false, '╭': false, '█': false, '▌': false, '▀': false, // ascii, box, block
		'': false, '\U000F06A9': false, // private use
		'∙': false, '◦': false, '▸': false, '◊': false, '◉': false, '✦': false, '\U0001FB00': false, // Neutral
		'✓': false, '✗': false, // AllowedNarrow
		'●': true, '•': true, '○': true, '…': true, '·': true, '╥': false, // ╥ is box drawing (A, but allowed)
		'⚠': true, '▱': true, '▰': true, // WideInFonts
		'˙': true, '°': true,
	} {
		if got := Bad(r) != ""; got != bad {
			t.Errorf("Bad(%q U+%04X) = %q, want bad=%v", r, r, Bad(r), bad)
		}
	}
}

func TestLists(t *testing.T) {
	for r := range AllowedNarrow {
		if _, ok := WideInFonts[r]; ok {
			t.Errorf("%q is both allowed and wide", r)
		}
		if Bad(r) != "" {
			t.Errorf("allowed %q is bad: %s", r, Bad(r))
		}
	}
	for r := range WideInFonts {
		if r < 0x2500 || r > 0x259F {
			continue
		}
		// a block element here would be dead: the range check allows it first
		t.Errorf("%q is in the allowed block range", r)
	}
}

type rec struct {
	testing.TB
	errs int
}

func (r *rec) Errorf(string, ...any) { r.errs++ }
func (r *rec) Helper()               {}

func TestCheck(t *testing.T) {
	uses := []Use{{"a.Run", "●"}, {"a.Idle", "∙"}, {"a.Sep", "│"}}
	for _, c := range []struct {
		name  string
		known []Known
		errs  int
	}{
		{"listed", []Known{{"a.Run", '●', "段階 2"}}, 0},
		{"new violation", nil, 1},
		{"stale entry", []Known{{"a.Run", '●', "段階 2"}, {"a.Idle", '○', "段階 2"}}, 1},
		{"not bad", []Known{{"a.Run", '●', "段階 2"}, {"a.Sep", '│', "段階 2"}}, 2}, // not bad + stale
		{"no stage", []Known{{"a.Run", '●', ""}}, 1},
	} {
		r := &rec{TB: t}
		Check(r, uses, c.known)
		if r.errs != c.errs {
			t.Errorf("%s: %d errors, want %d", c.name, r.errs, c.errs)
		}
	}
}
