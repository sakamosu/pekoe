package tui

import (
	"bufio"
	"encoding/json"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/client"
	"pekoe/internal/state"
	"pekoe/internal/testenv"
)

// ptyServer is a pekoed stand-in that, like the real one (server.go
// serve), handles every request in its own goroutine, and types what the
// key calls carry into a "pty" in the order they are handled.
type ptyServer struct {
	mu  sync.Mutex
	pty strings.Builder
}

func startPtyServer(t *testing.T) (*ptyServer, *client.Client) {
	t.Helper()
	// in memory: an agent's sandbox refuses binding a unix socket
	sock := filepath.Join(t.TempDir(), "s")
	l := testenv.Listen(sock)
	t.Cleanup(func() { l.Close() })
	oldDial := client.DialSocket
	client.DialSocket = l.Dial
	t.Cleanup(func() { client.DialSocket = oldDial })
	ps := &ptyServer{}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			var wmu sync.Mutex
			go func() {
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req struct {
						ID     int64          `json:"id"`
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					go func() {
						if strings.HasSuffix(req.Method, ".keys") || strings.HasSuffix(req.Method, ".send") {
							time.Sleep(time.Duration(rand.N(3000)) * time.Microsecond) // tmux takes its time
							ps.mu.Lock()
							if s, ok := req.Params["literal"].(string); ok {
								ps.pty.WriteString(s)
							}
							if ks, ok := req.Params["keys"].([]any); ok {
								for _, k := range ks {
									ps.pty.WriteString("<" + k.(string) + ">")
								}
							}
							ps.mu.Unlock()
						}
						b, _ := json.Marshal(map[string]any{"id": req.ID, "result": nil})
						wmu.Lock()
						c.Write(append(b, '\n'))
						wmu.Unlock()
					}()
				}
			}()
		}
	}()
	t.Setenv("PEKOE_SOCK", sock)
	cl, err := client.Dial()
	if err != nil {
		t.Fatal(err)
	}
	return ps, cl
}

// typeLikeTea feeds keys one Update at a time and runs each returned
// command in its own goroutine, as the Bubble Tea runtime does.
func typeLikeTea(m *model, keys []tea.KeyMsg) {
	var wg sync.WaitGroup
	for _, k := range keys {
		_, cmd := m.Update(k)
		if cmd == nil {
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); runAll(cmd) }()
	}
	wg.Wait()
}

// runAll runs a command tree without feeding results back.
func runAll(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var wg sync.WaitGroup
		for _, c := range msg {
			wg.Add(1)
			go func() { defer wg.Done(); runAll(c) }()
		}
		wg.Wait()
	}
}

// burst is what Bubble Tea makes of "reply with just: OK⏎" read at once:
// runs of letters, a separate message per space, then Enter.
func burst(s string) []tea.KeyMsg {
	var out []tea.KeyMsg
	for i, w := range strings.Split(s, " ") {
		if i > 0 {
			out = append(out, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		}
		if w != "" {
			out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(w)})
		}
	}
	return append(out, tea.KeyMsg{Type: tea.KeyEnter})
}

// Keys reach the pty in the order they were typed, into a session's agent
// and into tapi chat alike, even though
// every key's call runs on its own goroutine and pekoed handles calls
// concurrently.
func TestTypingKeepsOrder(t *testing.T) {
	const text = "reply with just: OK and keep the spaces where they are"
	want := text + "<Enter>"
	for _, target := range []string{"session", "tapi"} {
		for i := 0; i < 20; i++ {
			ps, cl := startPtyServer(t)
			t.Setenv("PEKOE_COLOR", "none")
			m := newModel(cl, "/src/api")
			m.tapi.splash = false
			m.width, m.height = 140, 40
			m.sessions = []*state.Session{sess("api-1", appA, state.Idle, time.Now())}
			m.lastSel = "api-1"
			m.focus, m.typing = pAgent, true
			if target == "tapi" { // typing into tapi chat (tapi.keys, chat.go)
				m.chat.open = true
			}
			typeLikeTea(m, burst(text))
			ps.mu.Lock()
			got := ps.pty.String()
			ps.mu.Unlock()
			if got != want {
				t.Fatalf("%s, try %d: the pty got %q", target, i, got)
			}
			cl.Close()
		}
	}
}
