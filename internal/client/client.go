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
}

func Dial() (*Client, error) {
	c, err := net.DialTimeout("unix", paths.Socket(), 2*time.Second)
	if err != nil {
		return nil, err
	}
	cl := &Client{c: c, pending: map[int64]chan msg{}, Events: make(chan Event, 512), done: make(chan struct{})}
	go cl.read()
	return cl, nil
}

// Connect dials the daemon, starting it in the background when absent.
func Connect() (*Client, error) {
	if cl, err := Dial(); err == nil {
		return cl, nil
	}
	if err := StartDaemon(); err != nil {
		return nil, err
	}
	// generous: the new pekoed may first wait for an old one to finish
	// shutting down (it holds the lock until it is gone)
	deadline := time.Now().Add(10 * time.Second)
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
	return cl.CallTimeout(method, params, out, 10*time.Minute)
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
	b, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
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
		return fmt.Errorf("%s: timed out", method)
	}
}

func (cl *Client) Subscribe(prefixes ...string) error {
	return cl.Call("subscribe", map[string]any{"events": prefixes}, nil)
}

func (cl *Client) Close() error { return cl.c.Close() }
