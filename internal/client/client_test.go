package client

import (
	"os/exec"
	"testing"
	"time"
)

// `boba daemon stop` returns only once bobad's process is gone, and says so
// when it is not gone in time.
func TestWaitExit(t *testing.T) {
	quick := exec.Command("sleep", "0.2")
	if err := quick.Start(); err != nil {
		t.Fatal(err)
	}
	go quick.Wait() // reap, as a detached bobad is reaped by init
	if !WaitExit(quick.Process.Pid, 5*time.Second) {
		t.Fatal("WaitExit: exited process reported alive")
	}

	slow := exec.Command("sleep", "10")
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { slow.Process.Kill(); slow.Wait() }()
	start := time.Now()
	if WaitExit(slow.Process.Pid, 150*time.Millisecond) {
		t.Fatal("WaitExit: running process reported gone")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("WaitExit overran its timeout: %s", d)
	}
}
