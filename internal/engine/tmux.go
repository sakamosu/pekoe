package engine

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tmux runs every agent in its own window of one tmux session ("boba") on
// a private server socket, and watches all panes through one control-mode
// client (`tmux -C`), which reports raw pane output as %output lines.
type Tmux struct {
	Socket  string // -L name
	Session string
	out     chan Output
	mu      sync.Mutex
	panes   map[string]paneRef // pane id (%3) -> ref
	ctrl    *exec.Cmd
	ctrlIn  io.WriteCloser
	closed  chan struct{}
	started bool
}

type paneRef struct{ slug, window string }

func NewTmux(socket string) *Tmux {
	if socket == "" {
		socket = "boba"
	}
	return &Tmux{Socket: socket, Session: "boba", out: make(chan Output, 1024), panes: map[string]paneRef{}, closed: make(chan struct{})}
}

func (t *Tmux) cmd(args ...string) *exec.Cmd {
	full := append([]string{"-L", t.Socket, "-f", "/dev/null"}, args...)
	c := exec.Command("tmux", full...)
	c.Env = os.Environ()
	return c
}

func (t *Tmux) run(args ...string) (string, error) {
	c := t.cmd(args...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func winName(slug, window string) string { return slug + "~" + window }

func (t *Tmux) target(slug, window string) string {
	return t.Session + ":=" + winName(slug, window)
}

func (t *Tmux) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started {
		return nil
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not found in PATH")
	}
	if _, err := t.run("has-session", "-t", "="+t.Session); err != nil {
		if _, err := t.run("new-session", "-d", "-s", t.Session, "-n", "_keeper", "-x", "200", "-y", "50",
			"sh -c 'while :; do sleep 3600; done'"); err != nil {
			return err
		}
	}
	for _, o := range [][]string{
		// note: a global window-size=manual crashes tmux 3.5a on new-window;
		// resize-window pins each window's size instead.
		{"set-option", "-g", "remain-on-exit", "on"},
		{"set-option", "-g", "history-limit", "50000"},
		{"set-option", "-g", "status", "off"},
		{"set-option", "-g", "allow-rename", "on"},
		{"set-option", "-g", "default-terminal", "tmux-256color"},
		{"set-option", "-s", "escape-time", "0"},
		{"set-option", "-s", "set-clipboard", "external"},
		{"set-option", "-g", "allow-passthrough", "on"},
		{"set-option", "-g", "focus-events", "on"},
		// Kitty keyboard passthrough: modified keys (S-Enter …) reach the
		// agent as CSI u when it asked for extended keys; legacy otherwise
		{"set-option", "-s", "extended-keys", "on"},
		{"set-option", "-s", "extended-keys-format", "csi-u"},
	} {
		_, _ = t.run(o...)
	}
	t.ensureTerminalFeature("xterm-256color:RGB:hyperlinks")
	t.started = true
	t.refreshPanesLocked()
	return t.startControl()
}

// ensureTerminalFeature keeps exactly one entry of f in the server-wide
// terminal-features array. The server outlives bobad, so a plain append
// (-sa) on every start piles up copies.
func (t *Tmux) ensureTerminalFeature(f string) {
	out, err := t.run("show-options", "-s", "terminal-features")
	if err != nil {
		return
	}
	idx := terminalFeatureIndexes(out, f)
	if len(idx) == 0 {
		_, _ = t.run("set-option", "-sa", "terminal-features", ","+f)
		return
	}
	for _, i := range idx[1:] {
		_, _ = t.run("set-option", "-su", fmt.Sprintf("terminal-features[%d]", i))
	}
}

// terminalFeatureIndexes returns the array indexes holding f in
// `show-options -s terminal-features` output ("terminal-features[3] value").
func terminalFeatureIndexes(out, f string) []int {
	var idx []int
	for _, l := range strings.Split(out, "\n") {
		name, val, ok := strings.Cut(strings.TrimSpace(l), " ")
		if !ok || strings.Trim(val, `"`) != f {
			continue
		}
		_, n, ok := strings.Cut(strings.TrimSuffix(name, "]"), "[")
		if i, err := strconv.Atoi(n); ok && err == nil {
			idx = append(idx, i)
		}
	}
	return idx
}

func (t *Tmux) startControl() error {
	c := t.cmd("-C", "attach-session", "-t", "="+t.Session)
	in, err := c.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	t.ctrl, t.ctrlIn = c, in
	// do not let the control client influence window sizes
	fmt.Fprintln(in, "refresh-client -f ignore-size")
	go t.readControl(stdout)
	go func() {
		_ = c.Wait()
		select {
		case <-t.closed:
			return
		default:
		}
		// tmux server died or control client detached: try to come back.
		time.Sleep(time.Second)
		t.mu.Lock()
		t.started = false
		t.mu.Unlock()
		_ = t.Start()
	}()
	return nil
}

func (t *Tmux) readControl(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			t.handleControl(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return
		}
	}
}

func (t *Tmux) handleControl(line string) {
	switch {
	case strings.HasPrefix(line, "%output "):
		rest := line[len("%output "):]
		pane, data, ok := strings.Cut(rest, " ")
		if !ok {
			return
		}
		t.emit(pane, UnescapeControl(data))
	case strings.HasPrefix(line, "%extended-output "):
		// %extended-output %pane age : data
		rest := line[len("%extended-output "):]
		pane, r2, _ := strings.Cut(rest, " ")
		if _, data, ok := strings.Cut(r2, " : "); ok {
			t.emit(pane, UnescapeControl(data))
		}
	case strings.HasPrefix(line, "%window-add"), strings.HasPrefix(line, "%window-renamed"),
		strings.HasPrefix(line, "%window-close"), strings.HasPrefix(line, "%unlinked-window"),
		strings.HasPrefix(line, "%layout-change"):
		t.mu.Lock()
		t.refreshPanesLocked()
		t.mu.Unlock()
	}
}

func (t *Tmux) emit(pane string, data []byte) {
	t.mu.Lock()
	ref, ok := t.panes[pane]
	if !ok {
		t.refreshPanesLocked()
		ref, ok = t.panes[pane]
	}
	t.mu.Unlock()
	if !ok {
		return
	}
	select {
	case t.out <- Output{Slug: ref.slug, Window: ref.window, Data: data, At: time.Now()}:
	default: // drop when the consumer is slow; output is only a refresh hint
	}
}

func (t *Tmux) refreshPanesLocked() {
	out, err := t.run("list-panes", "-s", "-t", "="+t.Session, "-F", "#{pane_id} #{window_name}")
	if err != nil {
		return
	}
	m := map[string]paneRef{}
	for _, ln := range strings.Split(out, "\n") {
		id, name, ok := strings.Cut(ln, " ")
		if !ok {
			continue
		}
		slug, win, ok := strings.Cut(name, "~")
		if !ok {
			continue
		}
		m[id] = paneRef{slug, win}
	}
	t.panes = m
}

// UnescapeControl decodes tmux control-mode octal escapes (\ooo).
func UnescapeControl(s string) []byte {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && isOct(s, i+1) {
			n, _ := strconv.ParseUint(s[i+1:i+4], 8, 8)
			b = append(b, byte(n))
			i += 3
			continue
		}
		b = append(b, s[i])
	}
	return b
}

func isOct(s string, i int) bool {
	if i+3 > len(s) {
		return false
	}
	for j := i; j < i+3; j++ {
		if s[j] < '0' || s[j] > '7' {
			return false
		}
	}
	return true
}

func (t *Tmux) Spawn(s Spawn) error {
	if err := t.Start(); err != nil {
		return err
	}
	if s.Window == "" {
		s.Window = WinAgent
	}
	_ = t.Kill(s.Slug, s.Window)
	args := []string{"new-window", "-d", "-t", "=" + t.Session + ":", "-n", winName(s.Slug, s.Window), "-c", s.Dir}
	for _, e := range s.Env {
		args = append(args, "-e", e)
	}
	args = append(args, "-P", "-F", "#{pane_id}", s.Command)
	pane, err := t.run(args...)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.panes[pane] = paneRef{s.Slug, s.Window}
	t.mu.Unlock()
	_, _ = t.run("set-option", "-w", "-t", t.target(s.Slug, s.Window), "automatic-rename", "off")
	_, _ = t.run("set-option", "-w", "-t", t.target(s.Slug, s.Window), "allow-rename", "off")
	if s.Width > 0 && s.Height > 0 {
		_ = t.Resize(s.Slug, s.Window, s.Width, s.Height)
	}
	return nil
}

// Alive reports whether the window exists and its process is running.
// (display-message falls back to another pane for a missing target, so
// list the windows instead.)
func (t *Tmux) Alive(slug, window string) bool {
	out, err := t.run("list-windows", "-t", "="+t.Session, "-F", "#{window_name} #{pane_dead}")
	if err != nil {
		return false
	}
	want := winName(slug, window) + " 0"
	for _, ln := range strings.Split(out, "\n") {
		if ln == want {
			return true
		}
	}
	return false
}

func (t *Tmux) exists(slug, window string) bool {
	out, err := t.run("list-windows", "-t", "="+t.Session, "-F", "#{window_name}")
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(out, "\n") {
		if ln == winName(slug, window) {
			return true
		}
	}
	return false
}

func (t *Tmux) SendText(slug, window, text string) error {
	if !strings.Contains(text, "\n") {
		return t.SendLiteral(slug, window, text)
	}
	buf := "boba-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	c := t.cmd("load-buffer", "-b", buf, "-")
	c.Stdin = strings.NewReader(text)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %s", out)
	}
	_, err := t.run("paste-buffer", "-p", "-d", "-r", "-b", buf, "-t", t.target(slug, window))
	return err
}

func (t *Tmux) SendLiteral(slug, window, text string) error {
	if text == "" {
		return nil
	}
	_, err := t.run("send-keys", "-t", t.target(slug, window), "-l", "--", text)
	return err
}

func (t *Tmux) SendKeys(slug, window string, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	args := append([]string{"send-keys", "-t", t.target(slug, window), "--"}, keys...)
	_, err := t.run(args...)
	return err
}

func (t *Tmux) SendHex(slug, window string, hex []string) error {
	if len(hex) == 0 {
		return nil
	}
	args := append([]string{"send-keys", "-t", t.target(slug, window), "-H", "--"}, hex...)
	_, err := t.run(args...)
	return err
}

func (t *Tmux) Capture(slug, window string) (*Screen, error) {
	if !t.exists(slug, window) {
		return nil, fmt.Errorf("no %s window for %s", window, slug)
	}
	tg := t.target(slug, window)
	// カーソル情報と画面の中身は 1 回の呼び出し(";" 区切りのコマンド列)で取る。
	// 別プロセスで順に取ると、その間に pane が書き換わり(Backspace 長押しなど)
	// カーソル位置と文字列が食い違って、描いたカーソルが揺れる。
	all, err := t.run("display-message", "-p", "-t", tg,
		"#{cursor_x} #{cursor_y} #{cursor_flag} #{pane_width} #{pane_height} #{alternate_on} #{mouse_any_flag} #{pane_dead} #{history_size}",
		";", "capture-pane", "-p", "-e", "-t", tg)
	if err != nil {
		return nil, err
	}
	info, out, _ := strings.Cut(all, "\n")
	f := strings.Fields(info)
	if len(f) < 9 {
		return nil, fmt.Errorf("tmux: unexpected display output %q", info)
	}
	sc := &Screen{}
	sc.Cursor.X, _ = strconv.Atoi(f[0])
	sc.Cursor.Y, _ = strconv.Atoi(f[1])
	sc.Cursor.Visible = f[2] == "1"
	sc.Width, _ = strconv.Atoi(f[3])
	sc.Height, _ = strconv.Atoi(f[4])
	sc.AltScreen = f[5] == "1"
	sc.MouseAny = f[6] == "1"
	sc.Dead = f[7] == "1"
	sc.History, _ = strconv.Atoi(f[8])
	sc.Lines = strings.Split(out, "\n")
	for len(sc.Lines) < sc.Height {
		sc.Lines = append(sc.Lines, "")
	}
	if len(sc.Lines) > sc.Height && sc.Height > 0 {
		sc.Lines = sc.Lines[:sc.Height]
	}
	return sc, nil
}

func (t *Tmux) Resize(slug, window string, w, h int) error {
	if w < 10 || h < 3 {
		return nil
	}
	_, err := t.run("resize-window", "-t", t.target(slug, window), "-x", strconv.Itoa(w), "-y", strconv.Itoa(h))
	return err
}

func (t *Tmux) Kill(slug, window string) error {
	_, err := t.run("kill-window", "-t", t.target(slug, window))
	return err
}

func (t *Tmux) KillSession(slug string) error {
	wins, _ := t.Sessions()
	for _, w := range wins[slug] {
		_ = t.Kill(slug, w)
	}
	return nil
}

func (t *Tmux) Sessions() (map[string][]string, error) {
	out, err := t.run("list-windows", "-t", "="+t.Session, "-F", "#{window_name}")
	if err != nil {
		return nil, err
	}
	m := map[string][]string{}
	for _, n := range strings.Split(out, "\n") {
		if slug, w, ok := strings.Cut(n, "~"); ok {
			m[slug] = append(m[slug], w)
		}
	}
	return m, nil
}

// PaneCommand returns the running command and pid of a window (for adopt).
func (t *Tmux) Output() <-chan Output { return t.out }

func (t *Tmux) Close() {
	select {
	case <-t.closed:
		return
	default:
		close(t.closed)
	}
	if t.ctrlIn != nil {
		t.ctrlIn.Close()
	}
	if t.ctrl != nil && t.ctrl.Process != nil {
		_ = t.ctrl.Process.Kill()
	}
}

// KillServer stops the private tmux server (used by tests).
func (t *Tmux) KillServer() { _, _ = t.run("kill-server") }
