package main

// send / event / undo / answer / attach: talk to a session's agent.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"pekoe/internal/client"
	"pekoe/internal/paths"
)

func cmdSend(args []string) error {
	f, pos, err := parseCmd("send", args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: pekoe send <slug> <text>   (text '-' reads stdin)")
	}
	text := strings.Join(pos[1:], " ")
	if text == "-" {
		b, _ := io.ReadAll(os.Stdin)
		text = strings.TrimRight(string(b), "\n")
	}
	cl := connect()
	defer cl.Close()
	return cl.Call("session.send", map[string]any{"slug": pos[0], "text": text, "submit": f["no-enter"] == ""}, nil)
}

// cmdEvent forwards a hook's stdin JSON to pekoed. It must be fast and must
// never break Claude Code: errors are swallowed, exit code is always 0.
func cmdEvent(args []string) {
	hook := ""
	if len(args) > 0 {
		hook = args[0]
	}
	b, _ := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
	if len(b) == 0 {
		b = []byte("{}")
	}
	if !json.Valid(b) {
		return
	}
	if hook == "MessageDisplay" {
		sendEventNoWait(hook, b)
		return
	}
	cl, err := client.Dial()
	if err != nil {
		return
	}
	defer cl.Close()
	_ = cl.CallTimeout("session.event", map[string]any{
		"hook": hook, "session": os.Getenv("PEKOE_SESSION"), "payload": json.RawMessage(b),
	}, nil, 8*time.Second)
}

// noWaitTimeout bounds how long `pekoe event MessageDisplay` may take to
// connect and write, all told; Claude Code gives the hook 2 seconds (docs/voice.md
// 3.1).
const noWaitTimeout = time.Second

// sendEventNoWait sends session.event as one line and closes the
// connection without waiting for the answer: Claude Code holds the
// message's text back from the screen until a MessageDisplay hook returns,
// and takes anything the hook writes to stdout as the text to show instead
// (docs/voice.md 3.1, 3.2). So it writes nothing to stdout, and a pekoed
// that is absent or stuck costs no more than noWaitTimeout. The human
// token is not sent: session.event does not need it.
func sendEventNoWait(hook string, payload []byte) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := client.DialSocket(paths.Socket())
		if err != nil {
			return
		}
		defer c.Close()
		req := map[string]any{"id": 1, "method": "session.event", "params": map[string]any{
			"hook": hook, "session": os.Getenv("PEKOE_SESSION"), "payload": json.RawMessage(payload),
		}}
		if who := os.Getenv("PEKOE_SESSION"); who != "" {
			req["session"] = who // as client.CallTimeout does
		}
		line, err := json.Marshal(req)
		if err != nil {
			return
		}
		_ = c.SetWriteDeadline(time.Now().Add(noWaitTimeout))
		_, _ = c.Write(append(line, '\n'))
	}()
	// the dial too: a pekoed that does not accept must not hold the screen
	select {
	case <-done:
	case <-time.After(noWaitTimeout):
	}
}

func cmdUndo(args []string) error {
	_, args, err := parseCmd("undo", args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: pekoe undo <slug> [n]")
	}
	n := 0
	if len(args) > 1 {
		// a typo must not fall back to the last prompt's snapshot
		if n, err = strconv.Atoi(args[1]); err != nil || n < 1 {
			return &usageError{use: specs["undo"].use, msg: "n must be a number from 1: " + args[1]}
		}
	}
	cl := connect()
	defer cl.Close()
	var res map[string]any
	if err := cl.Call("session.undo", map[string]any{"slug": args[0], "n": n}, &res); err != nil {
		return err
	}
	fmt.Printf("restored to snapshot #%v (%v)\n", res["restored"], res["prompt"])
	return nil
}

func cmdAnswer(args []string) error {
	_, args, err := parseCmd("answer", args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: pekoe answer <slug> allow|deny|always")
	}
	cl := connect()
	defer cl.Close()
	return cl.Call("session.answer", map[string]any{"slug": args[0], "answer": args[1]}, nil)
}

func cmdAttach(args []string) error {
	_, args, err := parseCmd("attach", args)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: pekoe attach <slug> [agent|review|dev]")
	}
	w := "agent"
	if len(args) > 1 {
		w = args[1]
	}
	sock := paths.TmuxSocketName()
	tm, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	return syscall.Exec(tm, []string{"tmux", "-L", sock, "attach-session", "-t", "pekoe:=" + args[0] + "~" + w}, os.Environ())
}
