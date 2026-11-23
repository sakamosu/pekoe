package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/client"
	"pekoe/internal/testenv"
)

const messageDisplayPayload = `{"hook_event_name":"MessageDisplay","turn_id":"t1","message_id":"m1","index":0,"final":true,"delta":"こんにちは"}`

// runEvent runs cmdEvent(args) with stdin as its input and returns what it
// wrote to stdout and how long it took.
func runEvent(t *testing.T, stdin string, args ...string) (string, time.Duration) {
	t.Helper()
	in := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(in, []byte(stdin), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = f, w
	start := time.Now()
	cmdEvent(args)
	took := time.Since(start)
	os.Stdin, os.Stdout = oldIn, oldOut
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), took
}

// fakeSocket stands in for pekoed's socket: each line it reads is sent on
// the returned channel, and it never answers. accept=false never accepts
// a connection (a pekoed that is stuck).
func fakeSocket(t *testing.T, accept bool) <-chan string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s")
	t.Setenv("PEKOE_SOCK", sock)
	ln := testenv.Listen(sock)
	t.Cleanup(func() { ln.Close() })
	oldDial := client.DialSocket
	client.DialSocket = ln.Dial
	t.Cleanup(func() { client.DialSocket = oldDial })
	lines := make(chan string, 8)
	if accept {
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					sc := bufio.NewScanner(c)
					for sc.Scan() {
						lines <- sc.Text()
					}
				}()
			}
		}()
	}
	return lines
}

// MessageDisplay holds the message's text back from the screen until the
// hook returns, and what the hook prints replaces the text (docs/voice.md
// 3.1): `pekoe event MessageDisplay` sends one line, does not wait for an
// answer and prints nothing.
func TestEventMessageDisplayDoesNotWait(t *testing.T) {
	t.Setenv("PEKOE_SESSION", "feat-x")
	lines := fakeSocket(t, true)
	out, took := runEvent(t, messageDisplayPayload, "MessageDisplay")
	if out != "" {
		t.Errorf("stdout = %q, want nothing (it would replace the message's text)", out)
	}
	if took > 500*time.Millisecond {
		t.Errorf("took %v: it waited for pekoed, which never answers", took)
	}
	select {
	case l := <-lines:
		var req struct {
			Method  string `json:"method"`
			Session string `json:"session"`
			Params  struct {
				Hook    string          `json:"hook"`
				Session string          `json:"session"`
				Payload json.RawMessage `json:"payload"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(l), &req); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		if req.Method != "session.event" || req.Params.Hook != "MessageDisplay" || req.Params.Session != "feat-x" || req.Session != "feat-x" {
			t.Errorf("request = %s", l)
		}
		if string(req.Params.Payload) != messageDisplayPayload {
			t.Errorf("payload = %s, want %s", req.Params.Payload, messageDisplayPayload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pekoed got no session.event")
	}
}

// A pekoed that does not accept the connection holds the hook (and the
// screen) for no more than noWaitTimeout.
func TestEventMessageDisplayStuckDaemon(t *testing.T) {
	fakeSocket(t, false)
	out, took := runEvent(t, messageDisplayPayload, "MessageDisplay")
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if took > noWaitTimeout+500*time.Millisecond {
		t.Errorf("took %v, more than %v", took, noWaitTimeout)
	}
}

// Without pekoed's socket, `pekoe event MessageDisplay` ends at once with
// exit 0 and nothing on stdout. Run as its own process (TestEventHelper)
// to see the exit code main gives.
func TestEventMessageDisplayNoSocket(t *testing.T) {
	stdout := filepath.Join(t.TempDir(), "stdout")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEventHelper$")
	cmd.Env = append(os.Environ(), "PEKOE_EVENT_HELPER="+stdout)
	cmd.Stdin = strings.NewReader(messageDisplayPayload)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	took := time.Since(start)
	if err != nil {
		t.Fatalf("exit: %v\n%s", err, out)
	}
	b, err := os.ReadFile(stdout)
	if err != nil {
		t.Fatalf("the helper did not run: %v\n%s", err, out)
	}
	if len(b) != 0 {
		t.Errorf("stdout = %q, want nothing", b)
	}
	// the process itself starts in this time too
	if took > 3*time.Second {
		t.Errorf("took %v", took)
	}
}

// TestEventHelper is `pekoe event MessageDisplay` for
// TestEventMessageDisplayNoSocket; it does nothing on its own.
// main's stdout goes to the file $PEKOE_EVENT_HELPER names. $PEKOE_SOCK
// is testenv's, where nothing listens.
func TestEventHelper(t *testing.T) {
	path := os.Getenv("PEKOE_EVENT_HELPER")
	if path == "" {
		t.Skip("run by TestEventMessageDisplayNoSocket")
	}
	if _, err := os.Stat(os.Getenv("PEKOE_SOCK")); err == nil {
		t.Fatal("$PEKOE_SOCK exists")
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oldArgs, oldOut := os.Args, os.Stdout
	defer func() { os.Args, os.Stdout = oldArgs, oldOut }()
	os.Args, os.Stdout = []string{"pekoe", "event", "MessageDisplay"}, f
	main() // a non-zero exit would end the process here
}
