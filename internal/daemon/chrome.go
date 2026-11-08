package daemon

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// chromeCapturer takes the preview screenshot with headless Chrome, driven
// over the DevTools protocol on --remote-debugging-pipe.
//
// Why CDP over a pipe, not `--screenshot` and not chromedp:
//   - `chrome --headless=new --screenshot --window-size=390,844` cannot do a
//     phone: the new headless mode keeps a 500 px minimum window width, so
//     mobile media queries never match. Emulation.setDeviceMetricsOverride
//     sets the real viewport (390 wide, DPR 2, mobile = viewport meta honored).
//     CDP also lets us wait for the load event instead of guessing.
//   - --remote-debugging-pipe speaks NUL-terminated JSON on fds 3/4, so no
//     websocket client is needed and go.mod gains nothing. chromedp would
//     bring a stack of dependencies for the five commands used here.
//
// One Chrome process per shot, with a throwaway profile (no stale cache, no
// clash with the user's running Chrome). Fine at the cadence of "the agent
// stopped" or "R".
type chromeCapturer struct{}

const defaultChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

const mobileUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"

// chromeBinary is $PEKOE_CHROME, else Google Chrome in /Applications.
func chromeBinary() (string, error) {
	bin := os.Getenv("PEKOE_CHROME")
	if bin == "" {
		bin = defaultChrome
	}
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return "", fmt.Errorf("Google Chrome not found at %s · install it or set PEKOE_CHROME to a Chrome binary", bin)
	}
	return bin, nil
}

func chromeArgs(profile string) []string {
	return []string{
		"--headless=new", "--remote-debugging-pipe",
		"--disable-gpu", "--hide-scrollbars", "--mute-audio",
		"--no-first-run", "--no-default-browser-check", "--disable-extensions",
		"--user-data-dir=" + profile,
		"about:blank",
	}
}

// deviceMetrics is the Emulation.setDeviceMetricsOverride for a request.
func deviceMetrics(r CaptureReq) map[string]any {
	scale := 1
	if r.Mobile {
		scale = 2
	}
	return map[string]any{"width": r.Width, "height": r.Height, "deviceScaleFactor": scale, "mobile": r.Mobile}
}

func (chromeCapturer) Capture(ctx context.Context, r CaptureReq) error {
	bin, err := chromeBinary()
	if err != nil {
		return err
	}
	profile, err := os.MkdirTemp("", "pekoe-chrome-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)

	toChrome, cmdIn, err := os.Pipe() // chrome reads fd 3
	if err != nil {
		return err
	}
	cmdOut, fromChrome, err := os.Pipe() // chrome writes fd 4
	if err != nil {
		toChrome.Close()
		cmdIn.Close()
		return err
	}
	cmd := exec.Command(bin, chromeArgs(profile)...)
	cmd.ExtraFiles = []*os.File{toChrome, fromChrome}
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 8 << 10}
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{toChrome, cmdIn, cmdOut, fromChrome} {
			f.Close()
		}
		return fmt.Errorf("start Chrome: %w", err)
	}
	toChrome.Close()
	fromChrome.Close()
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() {
		cmdIn.Close()
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		cmdOut.Close()
	}()

	c := newCDP(cmdIn, cmdOut)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
		case <-exited:
		}
		c.fail(errors.New("Chrome exited"))
	}()
	err = shoot(ctx, c, r)
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("headless Chrome timed out on %s", r.URL)
	}
	if err != nil && strings.Contains(err.Error(), "Chrome exited") && stderr.Len() > 0 {
		return fmt.Errorf("headless Chrome exited: %s", lastLine(stderr.String()))
	}
	_, _ = c.call("", "Browser.close", nil)
	return err
}

// shoot runs the CDP conversation for one screenshot.
func shoot(ctx context.Context, c *cdp, r CaptureReq) error {
	var tgt struct {
		TargetID string `json:"targetId"`
	}
	if err := c.callInto("", "Target.createTarget", map[string]any{"url": "about:blank"}, &tgt); err != nil {
		return err
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.callInto("", "Target.attachToTarget", map[string]any{"targetId": tgt.TargetID, "flatten": true}, &att); err != nil {
		return err
	}
	sid := att.SessionID
	type step struct {
		method string
		params any
	}
	steps := []step{
		{"Page.enable", nil},
		{"Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}},
		{"Emulation.setDeviceMetricsOverride", deviceMetrics(r)},
		{"Emulation.setScrollbarsHidden", map[string]any{"hidden": true}},
	}
	if r.Mobile {
		steps = append(steps,
			step{"Emulation.setUserAgentOverride", map[string]any{"userAgent": mobileUA}},
			step{"Emulation.setTouchEmulationEnabled", map[string]any{"enabled": true, "maxTouchPoints": 5}})
	}
	for _, s := range steps {
		if _, err := c.call(sid, s.method, s.params); err != nil {
			return err
		}
	}
	loaded := c.watch(sid, "Page.loadEventFired")
	idle := c.watchLifecycle(sid, "networkIdle")
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := c.callInto(sid, "Page.navigate", map[string]any{"url": r.URL}, &nav); err != nil {
		return err
	}
	if nav.ErrorText != "" {
		return fmt.Errorf("could not load %s: %s", r.URL, nav.ErrorText)
	}
	// the load event, then up to 3 s for the network to go quiet (an HMR
	// websocket can keep it busy forever, so this is only a best effort)
	select {
	case <-loaded:
	case <-time.After(20 * time.Second):
		return fmt.Errorf("%s did not finish loading in 20s", r.URL)
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-idle:
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := c.callInto(sid, "Page.captureScreenshot", map[string]any{"format": "png"}, &shot); err != nil {
		return err
	}
	png, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil || len(png) == 0 {
		return errors.New("Chrome returned no screenshot")
	}
	return os.WriteFile(r.Out, png, 0o600)
}

// ---- a minimal CDP client over --remote-debugging-pipe ----

type cdpMsg struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type cdpWatch struct {
	session, method, lifecycle string
	ch                         chan struct{}
}

type cdp struct {
	w       io.Writer
	wmu     sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[int]chan cdpMsg
	watches []*cdpWatch
	err     error
}

func newCDP(w io.Writer, r io.Reader) *cdp {
	c := &cdp{w: w, pending: map[int]chan cdpMsg{}}
	go c.read(r)
	return c
}

func (c *cdp) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		b, err := br.ReadBytes(0)
		if err != nil {
			c.fail(errors.New("Chrome exited"))
			return
		}
		var m cdpMsg
		if json.Unmarshal(b[:len(b)-1], &m) != nil {
			continue
		}
		c.dispatch(m)
	}
}

func (c *cdp) dispatch(m cdpMsg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m.ID != 0 {
		if ch := c.pending[m.ID]; ch != nil {
			delete(c.pending, m.ID)
			ch <- m
		}
		return
	}
	var lc struct {
		Name string `json:"name"`
	}
	if m.Method == "Page.lifecycleEvent" {
		_ = json.Unmarshal(m.Params, &lc)
	}
	keep := c.watches[:0]
	for _, wt := range c.watches {
		hit := wt.session == m.SessionID && (wt.method == m.Method || wt.lifecycle != "" && lc.Name == wt.lifecycle)
		if hit {
			close(wt.ch)
			continue
		}
		keep = append(keep, wt)
	}
	c.watches = keep
}

func (c *cdp) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
	}
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
}

// watch returns a channel closed when the session emits the event.
func (c *cdp) watch(session, method string) <-chan struct{} {
	wt := &cdpWatch{session: session, method: method, ch: make(chan struct{})}
	c.mu.Lock()
	c.watches = append(c.watches, wt)
	c.mu.Unlock()
	return wt.ch
}

func (c *cdp) watchLifecycle(session, name string) <-chan struct{} {
	wt := &cdpWatch{session: session, lifecycle: name, ch: make(chan struct{})}
	c.mu.Lock()
	c.watches = append(c.watches, wt)
	c.mu.Unlock()
	return wt.ch
}

func (c *cdp) call(session, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.next++
	id := c.next
	ch := make(chan cdpMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if session != "" {
		req["sessionId"] = session
	}
	b, _ := json.Marshal(req)
	c.wmu.Lock()
	_, err := c.w.Write(append(b, 0))
	c.wmu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	m, ok := <-ch
	if !ok {
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	if m.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
	}
	return m.Result, nil
}

func (c *cdp) callInto(session, method string, params, out any) error {
	res, err := c.call(session, method, params)
	if err != nil {
		return err
	}
	return json.Unmarshal(res, out)
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
