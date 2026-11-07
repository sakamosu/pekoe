package engine

import (
	"reflect"
	"testing"
)

func TestTerminalFeatureIndexes(t *testing.T) {
	out := `terminal-features[0] xterm*:clipboard:ccolour:cstyle:focus:title
terminal-features[1] screen*:title
terminal-features[3] xterm-256color:RGB:hyperlinks
terminal-features[4] "xterm-256color:RGB:hyperlinks"
terminal-features[5] xterm-256color:RGB`
	got := terminalFeatureIndexes(out, "xterm-256color:RGB:hyperlinks")
	if want := []int{3, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := terminalFeatureIndexes("terminal-features[0] screen*:title", "x"); got != nil {
		t.Fatalf("got %v, want none", got)
	}
}

// Against a real private tmux server: repeated starts keep one entry, and
// copies left by older versions collapse to one.
func TestEnsureTerminalFeature(t *testing.T) {
	tm := &Tmux{Socket: "boba-test-features", Session: "t"}
	if _, err := tm.run("new-session", "-d", "-s", "t", "sleep 30"); err != nil {
		t.Skip("tmux unavailable:", err)
	}
	defer tm.KillServer()
	const f = "xterm-256color:RGB:hyperlinks"
	count := func() int {
		out, _ := tm.run("show-options", "-s", "terminal-features")
		return len(terminalFeatureIndexes(out, f))
	}
	tm.ensureTerminalFeature(f)
	tm.ensureTerminalFeature(f)
	if n := count(); n != 1 {
		t.Fatalf("after two starts: %d entries", n)
	}
	for i := 0; i < 3; i++ {
		tm.run("set-option", "-sa", "terminal-features", ","+f)
	}
	tm.ensureTerminalFeature(f)
	if n := count(); n != 1 {
		t.Fatalf("after dedupe: %d entries", n)
	}
}
