// Package client talks to pekoed over the unix socket (JSON Lines).
package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pekoe/internal/paths"
)

type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type msg struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data"`
}

type Client struct {
	c       net.Conn
	mu      sync.Mutex
	next    int64
	pending map[int64]chan msg
	Events  chan Event
	done    chan struct{}
	err     atomic.Value
	human   bool   // Dial: send the human token
	token   string // the human token, sent with every request (empty: none)
}

// Dial connects as the person at the keyboard: it reads the human token
// (paths.HumanTokenFile), which the human-only methods (answering
// permission prompts, pressing keys, running commands) require. Without the
// file, those methods are refused and everything else works.
func Dial() (*Client, error) {
	cl, err := dial()
	if err != nil {
		return nil, err
	}
	cl.human = true // the token is read at the first call
	return cl, nil
}

// DialAgent connects without the human token, for code that serves an
// agent (the MCP server).
func DialAgent() (*Client, error) { return dial() }

// DialSocket opens the connection to pekoed's socket at path. Tests swap
// it for an in-memory one (internal/testenv.PipeListener): an agent's
// sandbox refuses binding a unix socket.
var DialSocket = func(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 2*time.Second)
}

func dial() (*Client, error) {
	c, err := DialSocket(paths.Socket())
	if err != nil {
		return nil, err
	}
	cl := &Client{c: c, pending: map[int64]chan msg{}, Events: make(chan Event, 512), done: make(chan struct{})}
	go cl.read()
	return cl, nil
}

// ReadHumanToken returns the human token, or "" when the file cannot be read.
func ReadHumanToken() string {
	b, err := os.ReadFile(paths.HumanTokenFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Connect dials the daemon, starting it in the background when absent.
func Connect() (*Client, error) {
	cl, dialErr := Dial()
	if dialErr == nil {
		return cl, nil
	}
	if err := StartDaemon(); err != nil {
		// e.g. inside an agent's sandbox, which may reach the socket but
		// not write pekoe's data directory: say what failed first
		return nil, fmt.Errorf("cannot reach pekoed (%v) and cannot start it: %w", dialErr, err)
	}
	// generous: the new pekoed may first wait for an old one to finish
	// shutting down (it holds the lock until it is gone)
	return WaitDial(10 * time.Second)
}

// WaitDial dials pekoed until it answers or timeout passes: for a pekoed
// that is being started (by StartDaemon, or by launchd).
func WaitDial(timeout time.Duration) (*Client, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cl, err := Dial(); err == nil {
			return cl, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("pekoed did not start; see %s", paths.DaemonLog())
}

// StopDaemon asks the running pekoed to stop and waits until its process has
// exited, so a following start (or a client's autostart) finds it gone.
// It reports running=false when no pekoed answered.
func StopDaemon(params any, timeout time.Duration) (running bool, err error) {
	cl, err := Dial()
	if err != nil {
		return false, nil
	}
	defer cl.Close()
	var st struct {
		Pid int `json:"pid"`
	}
	if err := cl.Call("daemon.status", nil, &st); err != nil {
		return true, err
	}
	if err := cl.Call("daemon.stop", params, nil); err != nil {
		return true, err
	}
	if st.Pid <= 0 {
		return true, nil
	}
	if !WaitExit(st.Pid, timeout) {
		return true, fmt.Errorf("pekoed (pid %d) did not exit within %s; see %s", st.Pid, timeout, paths.DaemonLog())
	}
	return true, nil
}

// WaitExit waits up to timeout for process pid to exit.
func WaitExit(pid int, timeout time.Duration) bool {
	for end := time.Now().Add(timeout); ; time.Sleep(50 * time.Millisecond) {
		if !alive(pid) {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

// StartDaemon launches `pekoe daemon run` detached, logging to pekoed.log.
func StartDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := paths.Ensure(paths.DataDir()); err != nil {
		return err
	}
	logf, err := os.OpenFile(paths.DaemonLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, paths.FilePerm)
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "daemon", "run")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Stdin = nil
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func (cl *Client) read() {
	defer close(cl.done)
	sc := bufio.NewScanner(cl.c)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Event != "" {
			select {
			case cl.Events <- Event{Event: m.Event, Data: m.Data}:
			default:
			}
			continue
		}
		if m.ID == nil {
			continue
		}
		cl.mu.Lock()
		ch := cl.pending[*m.ID]
		delete(cl.pending, *m.ID)
		cl.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	cl.err.Store(errors.New("connection to pekoed closed"))
	cl.mu.Lock()
	for id, ch := range cl.pending {
		ch <- msg{Error: "connection to pekoed closed"}
		delete(cl.pending, id)
	}
	cl.mu.Unlock()
	close(cl.Events)
}

func (cl *Client) Done() <-chan struct{} { return cl.done }

// Call sends a request and decodes the result into out (may be nil).
func (cl *Client) Call(method string, params any, out any) error {
	return cl.CallTimeout(method, params, out, timeoutFor(method))
}

// timeoutFor is how long Call waits for method. land runs [hooks] test
// for up to 30 minutes (internal/daemon/lifecycle.go) and then pushes and
// opens the PR; giving up before that would report a failure while pekoed
// carries on, and a retry would rebase and test the worktree twice.
func timeoutFor(method string) time.Duration {
	if method == "land" {
		return 35 * time.Minute
	}
	return 10 * time.Minute
}

func (cl *Client) CallTimeout(method string, params any, out any, timeout time.Duration) error {
	if e, ok := cl.err.Load().(error); ok && e != nil {
		return e
	}
	id := atomic.AddInt64(&cl.next, 1)
	ch := make(chan msg, 1)
	cl.mu.Lock()
	cl.pending[id] = ch
	cl.mu.Unlock()
	if params == nil {
		params = map[string]any{}
	}
	req := map[string]any{"id": id, "method": method, "params": params}
	cl.mu.Lock()
	if cl.human && cl.token == "" {
		// read once it exists (a pekoed from before the token may have
		// been restarted since this client connected)
		cl.token = ReadHumanToken()
	}
	tok := cl.token
	cl.mu.Unlock()
	if tok != "" {
		req["token"] = tok
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	cl.mu.Lock()
	_, err = cl.c.Write(append(b, '\n'))
	cl.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case m := <-ch:
		if m.Error != "" {
			return errors.New(m.Error)
		}
		if out != nil && len(m.Result) > 0 && string(m.Result) != "null" {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-time.After(timeout):
		cl.mu.Lock()
		delete(cl.pending, id)
		cl.mu.Unlock()
		// pekoed does not cancel a request whose caller went away
		return fmt.Errorf("%s: timed out waiting for pekoed (it may still be running it)", method)
	}
}

func (cl *Client) Subscribe(prefixes ...string) error {
	return cl.Call("subscribe", map[string]any{"events": prefixes}, nil)
}

func (cl *Client) Close() error { return cl.c.Close() }
