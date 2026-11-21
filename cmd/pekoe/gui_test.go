package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// stubOpen replaces openGUIPage and records what it was asked to open.
func stubOpen(t *testing.T) *[][2]string {
	t.Helper()
	var opened [][2]string
	old := openGUIPage
	openGUIPage = func(file, browser string) error {
		opened = append(opened, [2]string{file, browser})
		return nil
	}
	t.Cleanup(func() { openGUIPage = old })
	return &opened
}

// pekoe gui opens the page gui.open wrote, with [gui] browser: the path
// only, never a URL with the token.
func TestGUIOpensThePage(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"gui.open": map[string]any{"port": 50123, "file": "/data/gui/open-abc.html", "browser": "Safari"}}
	opened := stubOpen(t)
	if err := cmdGUI(nil); err != nil {
		t.Fatal(err)
	}
	if want := [][2]string{{"/data/gui/open-abc.html", "Safari"}}; !reflect.DeepEqual(*opened, want) {
		t.Fatalf("opened %v", *opened)
	}
	if c := fd.calls(); !reflect.DeepEqual(c, []string{"gui.open"}) {
		t.Fatalf("calls %v", c)
	}
}

// When pekoed cannot bind the port (someone else holds it, maybe posing
// as the GUI), no browser is opened.
func TestGUIBindFailureOpensNoBrowser(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.errs = map[string]string{"gui.open": "gui: cannot listen on 127.0.0.1:47778: address already in use"}
	opened := stubOpen(t)
	err := cmdGUI(nil)
	if err == nil || !strings.Contains(err.Error(), "cannot listen") {
		t.Fatalf("err = %v", err)
	}
	if len(*opened) != 0 {
		t.Fatalf("opened %v", *opened)
	}
}

// --no-open prints the file instead; --status / --revoke / --stop call
// their methods and open nothing.
func TestGUIFlags(t *testing.T) {
	fd := startFakeDaemon(t)
	fd.results = map[string]any{"gui.open": map[string]any{"port": 1, "file": "/data/gui/open-x.html"}}
	opened := stubOpen(t)
	for _, c := range []struct {
		args []string
		call string
	}{
		{[]string{"--no-open"}, "gui.open"},
		{[]string{"--status"}, "gui.status"},
		{[]string{"--revoke"}, "gui.revoke"},
		{[]string{"--stop"}, "gui.stop"},
	} {
		fd.reset()
		if err := cmdGUI(c.args); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if got := fd.calls(); !reflect.DeepEqual(got, []string{c.call}) {
			t.Fatalf("%v: calls %v", c.args, got)
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("opened %v", *opened)
	}
	for _, bad := range [][]string{{"--stop", "--revoke"}, {"extra"}, {"--bogus"}} {
		fd.reset()
		if err := cmdGUI(bad); err == nil {
			t.Fatalf("%v: no error", bad)
		}
		if c := fd.calls(); len(c) > 0 {
			t.Fatalf("%v: called %v", bad, c)
		}
	}
}

// [gui] enable = false while the GUI listens: the open tabs still work
// (only the next gui.open is refused), so --status says it listens, how
// many are signed in, and how to stop it; off and not listening is "off".
func TestGUIStatusDisabledButListening(t *testing.T) {
	now := time.Now()
	var b strings.Builder
	printGUIStatus(&b, guiStatus{Enabled: false, Listening: true, Port: 51234, Sessions: 1, LastUsed: &now})
	for _, want := range []string{"enable = false", "127.0.0.1:51234", "sessions   1", "last used", "pekoe gui --stop"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("no %q in:\n%s", want, b.String())
		}
	}
	b.Reset()
	printGUIStatus(&b, guiStatus{Enabled: false})
	if b.String() != "the local GUI is off ([gui] enable = false)\n" {
		t.Fatalf("off: %q", b.String())
	}
}
