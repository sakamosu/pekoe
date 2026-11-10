package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// `pekoe daemon stop` returns only once pekoed's process is gone, and says so
// when it is not gone in time.
func TestWaitExit(t *testing.T) {
	quick := exec.Command("sleep", "0.2")
	if err := quick.Start(); err != nil {
		t.Fatal(err)
	}
	go quick.Wait() // reap, as a detached pekoed is reaped by init
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

// Dial sends the human token from the data directory with each request;
// DialAgent (the MCP server) never does.
func TestHumanToken(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "pekoecl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("PEKOE_HOME", home)
	sock := filepath.Join(home, "s")
	t.Setenv("PEKOE_SOCK", sock)
	os.MkdirAll(filepath.Join(home, "share"), 0o700)
	if err := os.WriteFile(filepath.Join(home, "share", "human-token"), []byte("tok123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var r struct {
						ID    int64  `json:"id"`
						Token string `json:"token"`
					}
					json.Unmarshal(sc.Bytes(), &r)
					got <- r.Token
					fmt.Fprintf(c, "{\"id\":%d,\"result\":null}\n", r.ID)
				}
			}(c)
		}
	}()
	for _, tc := range []struct {
		name string
		dial func() (*Client, error)
		want string
	}{{"Dial", Dial, "tok123"}, {"DialAgent", DialAgent, ""}} {
		cl, err := tc.dial()
		if err != nil {
			t.Fatal(err)
		}
		if err := cl.CallTimeout("session.list", nil, nil, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		if tok := <-got; tok != tc.want {
			t.Errorf("%s sent token %q, want %q", tc.name, tok, tc.want)
		}
		cl.Close()
	}
}

// land waits past the 30 minutes pekoed gives [hooks] test: timing out
// first reports a failure while pekoed goes on to push and open the PR.
func TestLandOutwaitsTests(t *testing.T) {
	if got := timeoutFor("land"); got <= 30*time.Minute {
		t.Fatalf("land timeout %v, want longer than the 30m test cap", got)
	}
	if got := timeoutFor("session.list"); got != 10*time.Minute {
		t.Fatalf("session.list timeout %v", got)
	}
}
