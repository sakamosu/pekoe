package theme

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"boba/internal/state"
)

var allStates = []state.State{state.Creating, state.Running, state.Waiting, state.Idle, state.Done,
	state.Reviewing, state.Approved, state.Landed, state.Failed}

// In none mode and with ascii icons every state must still be readable from
// its symbol and word alone (guidelines: 色だけの状態表示をしない).
func TestStatesReadableWithoutColor(t *testing.T) {
	t.Setenv("BOBA_COLOR", "none")
	th := Load(nil)
	if th.Mode != None {
		t.Fatalf("mode = %s", th.Mode)
	}
	for _, set := range []string{"nerd", "unicode", "ascii"} {
		ic := Icons(set)
		words := map[string]bool{}
		for _, st := range allStates {
			lk := Look(ic, st, 0, false)
			if lk.Icon == "" || lk.Word == "" {
				t.Errorf("%s/%s: empty look %+v", set, st, lk)
			}
			if words[lk.Word] {
				t.Errorf("%s: word %q used twice", set, lk.Word)
			}
			words[lk.Word] = true
			line := SessionLine(th, ic, &state.Session{Branch: "feat/x", State: st, StateSince: time.Now()}, time.Now(), 0)
			if line != ansi.Strip(line) {
				t.Errorf("none mode emitted escape codes: %q", line)
			}
			if !strings.Contains(line, lk.Word) {
				t.Errorf("%s/%s: state word missing in %q", set, st, line)
			}
		}
	}
}

func TestDetectMode(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if DetectMode("auto") != None {
		t.Fatal("NO_COLOR must win")
	}
	t.Setenv("NO_COLOR", "")
	if DetectMode("ansi") != ANSI {
		t.Fatal("explicit mode ignored")
	}
}

func TestAccentPaletteOnlyFromRoles(t *testing.T) {
	// five accent hues + neutrals: no more roles than the token table
	if len(defaultHex) != 11 {
		t.Fatalf("token table has %d roles, guidelines define 11", len(defaultHex))
	}
}

// Every PR state has its own icon in every icon set (the #42 tag is read by
// its icon and color only; distinct icons keep it readable without color).
func TestPRLookDistinct(t *testing.T) {
	cases := []state.PRStatus{
		{State: "MERGED"}, {State: "CLOSED"}, {State: "OPEN", Fail: 1}, {State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"},
		{State: "OPEN", Pending: 1}, {State: "OPEN", Pass: 1},
	}
	for _, set := range []string{"nerd", "unicode", "ascii"} {
		ic := Icons(set)
		seen := map[string]string{}
		for _, st := range cases {
			st := st
			lk, ok := PRLook(ic, &st)
			if !ok || lk.Icon == "" || lk.Word == "" {
				t.Errorf("%s %+v: empty look", set, st)
			}
			if w, dup := seen[lk.Icon]; dup {
				t.Errorf("%s: %q used for %s and %s", set, lk.Icon, w, lk.Word)
			}
			seen[lk.Icon] = lk.Word
		}
		if _, ok := PRLook(ic, &state.PRStatus{State: "OPEN"}); ok {
			t.Errorf("%s: nothing to show for a PR without checks or reviews", set)
		}
	}
}

// auto colors only when stdout is a terminal: `boba issues | cat` must not
// leak escape codes, while an explicit mode still wins.
func TestAutoModeFollowsStdoutTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	t.Setenv("BOBA_COLOR", "")
	t.Setenv("COLORTERM", "truecolor")
	prev := StdoutIsTerminal
	t.Cleanup(func() { StdoutIsTerminal = prev })

	StdoutIsTerminal = func() bool { return false }
	if m := DetectMode("auto"); m != None {
		t.Fatalf("pipe: mode = %s, want none", m)
	}
	if s := Load(nil).FG(Dim, "3c"); strings.Contains(s, "\x1b") {
		t.Fatalf("pipe: escape code in %q", s)
	}
	if m := DetectMode("truecolor"); m != Truecolor {
		t.Fatalf("pipe: explicit mode ignored, got %s", m)
	}

	StdoutIsTerminal = func() bool { return true }
	if m := DetectMode("auto"); m != Truecolor {
		t.Fatalf("terminal: mode = %s, want truecolor", m)
	}
	if s := Load(nil).FG(Dim, "3c"); !strings.Contains(s, "\x1b[") {
		t.Fatalf("terminal: no color in %q", s)
	}
}
