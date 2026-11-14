package main

import (
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

func TestDevStatus(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	cases := []struct {
		running bool
		ex      *state.DevExit
		want    string
	}{
		{true, nil, "running"},
		{false, nil, "not started"},
		{false, &state.DevExit{Status: 1, At: at}, "ended: exit status 1 at 2026-01-02 03:04:05"},
		{false, &state.DevExit{Status: -1, Signal: "SIGKILL", At: at}, "ended: killed by SIGKILL"},
		{false, &state.DevExit{Status: -1, Stopped: true, At: at, DevStop: &state.HookRun{Command: "make down"}}, "stopped by pekoe at 2026-01-02 03:04:05 · dev_stop ok"},
		{false, &state.DevExit{Status: -1, Stopped: true, At: at, DevStop: &state.HookRun{Status: 2, Error: "exit status 2"}}, "· dev_stop failed: exit status 2"},
	}
	for _, c := range cases {
		if got := devStatus(c.running, c.ex); !strings.Contains(got, c.want) {
			t.Errorf("%v %+v: %q, want %q", c.running, c.ex, got, c.want)
		}
	}
}
