package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"pekoe/internal/tui"
)

// When pekoed goes away the TUI says so on the terminal, waits for it to come
// back (a restart) and opens again; when it does not come back (a stop), it
// ends with an error that says why, not silently with 0.
func TestTUILoopReconnects(t *testing.T) {
	var out bytes.Buffer
	runs, waits := 0, 0
	err := tuiLoop(&out, func() error {
		runs++
		if runs == 1 {
			return tui.ErrDisconnected
		}
		return nil // the person quits
	}, func() error { waits++; return nil })
	if err != nil || runs != 2 || waits != 1 {
		t.Fatalf("err %v, runs %d, waits %d", err, runs, waits)
	}
	if s := out.String(); !strings.Contains(s, "lost connection to pekoed") || !strings.Contains(s, "pekoed is back") {
		t.Errorf("what the terminal is told:\n%s", s)
	}
}

func TestTUILoopGivesUp(t *testing.T) {
	var out bytes.Buffer
	runs := 0
	err := tuiLoop(&out, func() error { runs++; return tui.ErrDisconnected },
		func() error { return errors.New("pekoed did not start") })
	if err == nil || runs != 1 {
		t.Fatalf("err %v, runs %d", err, runs)
	}
	if !strings.Contains(err.Error(), "lost connection to pekoed") || !strings.Contains(err.Error(), "did not come back") {
		t.Errorf("the error: %v", err)
	}
	if !strings.Contains(out.String(), "waiting") {
		t.Errorf("the terminal is not told it waits:\n%s", out.String())
	}
}

func TestTUILoopOtherError(t *testing.T) {
	boom := errors.New("boom")
	waits := 0
	err := tuiLoop(&bytes.Buffer{}, func() error { return boom }, func() error { waits++; return nil })
	if !errors.Is(err, boom) || waits != 0 {
		t.Fatalf("err %v, waits %d", err, waits)
	}
}
