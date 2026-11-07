package theme

import (
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
