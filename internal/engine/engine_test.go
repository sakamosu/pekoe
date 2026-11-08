package engine

import "testing"

func TestSignalName(t *testing.T) {
	for in, want := range map[string]string{"": "", "0": "", "int": "SIGINT", "TERM": "SIGTERM", "2": "SIGINT", "9": "SIGKILL", "31": "signal 31"} {
		if got := SignalName(in); got != want {
			t.Errorf("SignalName(%q) = %q, want %q", in, got, want)
		}
	}
}
